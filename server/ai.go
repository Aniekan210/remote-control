package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// aiLogPrefix tags every log line this file emits so they're easy to grep
// out of the rest of the server's output (e.g. `grep '\[ai\]' server.log`).
const aiLogPrefix = "[ai] "

func aiLogf(format string, args ...any) {
	log.Printf(aiLogPrefix+format, args...)
}

const (
	// PLANNING is a reasoning + light-vision job (break the task into
	// granular Windows steps, grounded in the screenshot). A top-tier
	// structured reasoner with strong vision. Overridable via PLANNING_MODEL.
	defaultPlanningModel = "google/gemini-3.1-pro-preview"

	// EXECUTION is a visual-grounding job (turn one instruction + the
	// screenshot into exact click pixels). Qwen2.5-VL is grounding-first —
	// coordinate/point output is a trained capability, not an afterthought
	// — which is exactly what pixel-accurate clicking needs.
	defaultExecutionModel = "qwen/qwen2.5-vl-72b-instruct"
)

// modelForContext returns the model for the current call. planning==true
// selects the planner, false selects the executor. Both can be overridden
// at runtime via the PLANNING_MODEL / EXECUTION_MODEL environment
// variables (paste any OpenRouter model slug) so models can be swapped
// without a rebuild.
func modelForContext(planning bool) string {
	if planning {
		if m := os.Getenv("PLANNING_MODEL"); m != "" {
			return m
		}
		return defaultPlanningModel
	}
	if m := os.Getenv("EXECUTION_MODEL"); m != "" {
		return m
	}
	return defaultExecutionModel
}

// sendMessage makes one AI call for the task's current phase and returns
// the parsed result plus what OpenRouter says the call cost in USD (0 when
// the call never got a usable response).
func sendMessage(task Task, action Action) (any, float64, error) {
	// callID ties every log line for this one call together, so concurrent
	// tasks' logs don't get interleaved into something unreadable — grep
	// for the same callID to follow one call start-to-finish.
	callID := fmt.Sprintf("%s-%d", task.DeviceID, time.Now().UnixNano())

	mode := "EXECUTION"
	if task.Context {
		mode = "PLANNING"
	}
	aiLogf("[%s] start mode=%s device=%s status=%s instrIdx=%d/%d",
		callID, mode, task.DeviceID, task.Status,
		task.CurrentInstructionIndex, len(task.InstructionList))

	if task.Context {
		return callPlanner(callID, task, action)
	}
	return callExecutor(callID, task, action)
}

// planSchema is the strict structured-output schema for the planner.
var planSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"instructions": map[string]any{
			"type":  "array",
			"items": map[string]any{"type": "string"},
		},
	},
	"required":             []string{"instructions"},
	"additionalProperties": false,
}

// executionSchema is the strict structured-output schema for the executor:
// exactly the five Execution fields, coordinates as single integers.
var executionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"response": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type": map[string]any{
						"type": "string",
						"enum": []string{"MOUSE_MOVEMENT", "LEFT_CLICK", "RIGHT_CLICK", "KEYBOARD_INPUT"},
					},
					"mouse_pos_x": map[string]any{"type": "integer"},
					"mouse_pos_y": map[string]any{"type": "integer"},
					"key_string":  map[string]any{"type": "string"},
					"mouse_hold":  map[string]any{"type": "boolean"},
				},
				"required":             []string{"type", "mouse_pos_x", "mouse_pos_y", "key_string", "mouse_hold"},
				"additionalProperties": false,
			},
		},
	},
	"required":             []string{"response"},
	"additionalProperties": false,
}

// callPlanner asks the planner to break the task into instructions.
func callPlanner(callID string, task Task, action Action) (any, float64, error) {
	fs := formatFileList(action.FileSystemPayload, time.Now())

	// The task description lives on the Task (set at CREATE_TASK), NOT on
	// this ADVANCE action — an ADVANCE carries no Description, so reading
	// action.Description here sends the planner an empty task and the
	// model correctly reports "No task was provided."
	aiLogf("[%s] planning request: task=%q filesystemEntries=%d",
		callID, task.Description, len(action.FileSystemPayload))

	userContent := []any{
		map[string]any{
			"type": "text",
			"text": fmt.Sprintf(
				"Task:\n%s\n\nFilesystem (filtered, newest first):\n%s",
				task.Description, fs,
			),
		},
		screenshotContent(action.ScreenshotPayload),
	}

	response, cost, err := callOpenRouter(callID, "PLANNING", modelForContext(true),
		plannerSystemPrompt, userContent, "plan", planSchema)
	if err != nil {
		return nil, cost, err
	}

	instructions, err := parseInstructionList(response)
	if err != nil {
		aiLogf("[%s] FAILED invalid instruction list: %v, cleaned response: %s", callID, err, response)
		return nil, cost, fmt.Errorf("invalid instruction list: %w (raw: %s)", err, response)
	}

	aiLogf("[%s] SUCCESS planning: %d instructions: %v", callID, len(instructions), instructions)
	return instructions, cost, nil
}

// callExecutor asks the executor for the physical actions that carry out
// the current instruction.
func callExecutor(callID string, task Task, action Action) (any, float64, error) {
	// Guard against an out-of-range index instead of panicking.
	if task.CurrentInstructionIndex < 0 ||
		task.CurrentInstructionIndex >= len(task.InstructionList) {
		aiLogf("[%s] FAILED instruction index %d out of range (list has %d items): %v",
			callID, task.CurrentInstructionIndex, len(task.InstructionList), task.InstructionList)
		return nil, 0, fmt.Errorf(
			"instruction index %d out of range (list has %d items)",
			task.CurrentInstructionIndex, len(task.InstructionList),
		)
	}

	// Describe the *actual* screenshot dimensions rather than assuming a
	// fixed resolution, since Screenshot carries its own Width/Height.
	w := action.ScreenshotPayload.Width
	h := action.ScreenshotPayload.Height

	instruction := task.InstructionList[task.CurrentInstructionIndex]

	aiLogf("[%s] execution request: instruction=%q screenshot=%dx%d imageBytes=%d",
		callID, instruction, w, h, len(action.ScreenshotPayload.Data))

	if w == 0 || h == 0 {
		aiLogf("[%s] WARNING: screenshot dimensions are 0x0 — the worker may not have sent a valid screenshot, and the model has no coordinate frame to work with", callID)
	}
	if len(action.ScreenshotPayload.Data) == 0 {
		aiLogf("[%s] WARNING: screenshot payload is empty (0 bytes) — the model is being asked to click on a blank image", callID)
	}

	userContent := []any{
		map[string]any{
			"type": "text",
			"text": fmt.Sprintf(
				"Instruction: %s\n\nScreenshot size: %dx%d pixels (0 <= x < %d, 0 <= y < %d).",
				instruction, w, h, w, h,
			),
		},
		screenshotContent(action.ScreenshotPayload),
	}

	response, cost, err := callOpenRouter(callID, "EXECUTION", modelForContext(false),
		executorSystemPrompt, userContent, "actions", executionSchema)
	if err != nil {
		return nil, cost, err
	}

	var executions struct {
		Response []Execution `json:"response"`
	}

	if err := json.Unmarshal([]byte(response), &executions); err != nil {
		aiLogf("[%s] FAILED invalid execution list: %v, cleaned response: %s", callID, err, response)
		return nil, cost, fmt.Errorf("invalid execution list: %w (raw: %s)", err, response)
	}

	// Validate the decoded actions. This is the safety net for the failure
	// that silently produced six empty actions before: json.Unmarshal does
	// NOT error when the model uses the wrong field names (e.g. "action"/
	// "x"/"y"/"text" instead of "type"/"mouse_pos_x"/...) — it just leaves
	// every field at its zero value. An action with an empty/unknown Type is
	// that exact symptom, so we reject it here and let the caller retry
	// (with the corrected, schema-explicit prompt) instead of sending the
	// worker a list of no-op clicks at (0,0).
	if len(executions.Response) == 0 {
		aiLogf("[%s] FAILED model returned an empty execution list, cleaned response: %s", callID, response)
		return nil, cost, fmt.Errorf("empty execution list (raw: %s)", response)
	}
	for i, e := range executions.Response {
		switch e.Type {
		case "MOUSE_MOVEMENT", "LEFT_CLICK", "RIGHT_CLICK", "KEYBOARD_INPUT":
			// valid
		default:
			aiLogf("[%s] FAILED execution %d has invalid/empty type %q — the model almost certainly used the wrong JSON field names (expected type/mouse_pos_x/mouse_pos_y/key_string/mouse_hold); cleaned response: %s",
				callID, i, e.Type, response)
			return nil, cost, fmt.Errorf("execution %d has invalid/empty type %q (raw: %s)", i, e.Type, response)
		}
	}

	aiLogf("[%s] SUCCESS execution: %d actions: %+v", callID, len(executions.Response), executions.Response)

	return executions.Response, cost, nil
}

// screenshotContent wraps a screenshot as an OpenAI-style image part.
func screenshotContent(shot Screenshot) map[string]any {
	return map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": "data:image/" + shot.Format + ";base64," +
				base64.StdEncoding.EncodeToString(shot.Data),
		},
	}
}

// schemaUnsupported remembers models whose provider rejected a
// json_schema response_format, so later calls go straight to the
// json_object fallback instead of failing once per call.
var schemaUnsupported = struct {
	sync.Mutex
	models map[string]bool
}{models: map[string]bool{}}

// callOpenRouter sends one chat completion and returns the model's content
// (markdown fences stripped) and the call's cost in USD.
//
// The static system prompt goes first and the per-call content last so
// prompt-prefix caching can reuse the system prompt; Anthropic models get
// an explicit cache_control breakpoint on it (Gemini caches implicitly).
//
// Output is constrained with a strict json_schema. If the provider rejects
// that (HTTP 400 mentioning the response format), the call is retried once
// with plain json_object, and the model is remembered so it isn't tried
// again. The tolerant parsers downstream stay in place either way.
func callOpenRouter(callID, mode, model, systemPrompt string, userContent []any, schemaName string, schema map[string]any) (string, float64, error) {
	var system any = systemPrompt
	if strings.HasPrefix(model, "anthropic/") {
		system = []any{map[string]any{
			"type":          "text",
			"text":          systemPrompt,
			"cache_control": map[string]any{"type": "ephemeral"},
		}}
	}

	schemaUnsupported.Lock()
	useSchema := schema != nil && !schemaUnsupported.models[model]
	schemaUnsupported.Unlock()

	for {
		responseFormat := map[string]any{"type": "json_object"}
		if useSchema {
			responseFormat = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   schemaName,
					"strict": true,
					"schema": schema,
				},
			}
		}

		requestBody := map[string]any{
			"model": model,
			"messages": []any{
				map[string]any{
					"role":    "system",
					"content": system,
				},
				map[string]any{
					"role":    "user",
					"content": userContent,
				},
			},
			// Ask the provider to enforce valid JSON output instead of
			// relying solely on prompt instructions, which some models
			// ignore.
			"response_format": responseFormat,
			// Makes OpenRouter return the call's real cost (usage.cost, in
			// USD) alongside the token counts, so every call can be priced.
			"usage": map[string]any{"include": true},
		}

		content, cost, status, err := postChatCompletion(callID, mode, model, requestBody)
		if err != nil && useSchema && status == http.StatusBadRequest && mentionsResponseFormat(err.Error()) {
			aiLogf("[%s] model=%s rejected json_schema output, falling back to json_object for this model from now on", callID, model)
			schemaUnsupported.Lock()
			schemaUnsupported.models[model] = true
			schemaUnsupported.Unlock()
			useSchema = false
			continue
		}
		return content, cost, err
	}
}

// mentionsResponseFormat reports whether an OpenRouter error is about the
// structured-output request (rather than, say, a bad image).
func mentionsResponseFormat(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "response_format") || strings.Contains(m, "json_schema") ||
		strings.Contains(m, "structured output") || strings.Contains(m, "response format")
}

// postChatCompletion performs the HTTP call. status is the HTTP status (0
// when no response arrived).
func postChatCompletion(callID, mode, model string, requestBody map[string]any) (string, float64, int, error) {
	body, err := json.Marshal(requestBody)
	if err != nil {
		aiLogf("[%s] FAILED marshal request body: %v", callID, err)
		return "", 0, 0, fmt.Errorf("marshal request body: %w", err)
	}

	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		// This is a very common silent-failure cause: every request will
		// come back 401 and, without this line, that 401 looks identical
		// to a real auth/billing problem with a *valid* key.
		aiLogf("[%s] WARNING: OPENROUTER_API_KEY is empty — every request will fail auth", callID)
	}

	aiLogf("[%s] calling OpenRouter: model=%s requestBytes=%d", callID, model, len(body))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		"https://openrouter.ai/api/v1/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		aiLogf("[%s] FAILED build request: %v", callID, err)
		return "", 0, 0, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	elapsed := time.Since(start)

	if err != nil {
		// This branch fires for network-level failures (DNS, connection
		// refused, TLS, context deadline exceeded from the 30s timeout
		// above) — NOT for HTTP error status codes, which are handled
		// below once we have a response to read.
		aiLogf("[%s] FAILED request after %s: %v", callID, elapsed, err)
		return "", 0, 0, fmt.Errorf("openrouter request failed: %w", err)
	}
	defer resp.Body.Close()

	aiLogf("[%s] response received after %s: status=%d", callID, elapsed, resp.StatusCode)

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		aiLogf("[%s] FAILED read response body: %v", callID, err)
		return "", 0, resp.StatusCode, fmt.Errorf("read response body: %w", err)
	}

	// Check status BEFORE trying to decode into the success shape, so
	// error responses surface their real message instead of a generic
	// JSON parse error.
	if resp.StatusCode != http.StatusOK {
		aiLogf("[%s] FAILED OpenRouter status %d, body: %s", callID, resp.StatusCode, string(rawBody))
		return "", 0, resp.StatusCode, fmt.Errorf(
			"OpenRouter returned status %d: %s",
			resp.StatusCode, string(rawBody),
		)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		// Usage is logged on every call (the fastest way to notice "the
		// model saw 0 image tokens" type problems) and, since the request
		// sets usage.include, carries the call's cost in USD.
		Usage struct {
			PromptTokens            int     `json:"prompt_tokens"`
			CompletionTokens        int     `json:"completion_tokens"`
			TotalTokens             int     `json:"total_tokens"`
			Cost                    float64 `json:"cost"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(rawBody, &result); err != nil {
		aiLogf("[%s] FAILED decode openrouter response: %v, raw body: %s", callID, err, string(rawBody))
		return "", 0, resp.StatusCode, fmt.Errorf("decode openrouter response: %w (body: %s)", err, string(rawBody))
	}

	cost := result.Usage.Cost
	day, month := costs.add(cost)
	aiLogf("[%s] usage: mode=%s model=%s promptTokens=%d cachedTokens=%d completionTokens=%d reasoningTokens=%d totalTokens=%d cost=$%.5f | today=$%.4f month=$%.4f",
		callID, mode, model, result.Usage.PromptTokens, result.Usage.PromptTokensDetails.CachedTokens,
		result.Usage.CompletionTokens, result.Usage.CompletionTokensDetails.ReasoningTokens,
		result.Usage.TotalTokens, cost, day, month)

	if len(result.Choices) == 0 {
		aiLogf("[%s] FAILED no choices in response, raw body: %s", callID, string(rawBody))
		return "", cost, resp.StatusCode, fmt.Errorf("no model response (body: %s)", string(rawBody))
	}

	if fr := result.Choices[0].FinishReason; fr == "length" {
		aiLogf("[%s] WARNING: model output was cut off by max_tokens (finish_reason=length) — the JSON is probably incomplete", callID)
	}

	rawContent := result.Choices[0].Message.Content
	aiLogf("[%s] raw model content: %s", callID, rawContent)

	response := cleanJSONResponse(rawContent)
	if response != rawContent {
		aiLogf("[%s] stripped markdown fences from response, cleaned: %s", callID, response)
	}

	return response, cost, resp.StatusCode, nil
}

// parseInstructionList extracts the planner's list of instructions from
// the model's JSON, tolerant of the two shapes it realistically returns:
//
//  1. a bare array — ["Open Chrome.", "Search."]
//  2. an object wrapping the array — {"instructions": [...]} (which is what
//     response_format:json_object nudges the model toward), or the same
//     under a differently-named key.
//
// Being tolerant here matters because the model is not perfectly
// consistent about which shape it emits; keying the whole pipeline on one
// exact shape is what made planning fail intermittently before.
func parseInstructionList(response string) ([]string, error) {
	// 1) Bare array.
	var arr []string
	if err := json.Unmarshal([]byte(response), &arr); err == nil {
		return arr, nil
	}

	// 2) Object wrapping the array. Try the expected/likely keys first, then
	// fall back to the first value that is itself an array of strings.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(response), &obj); err != nil {
		return nil, fmt.Errorf("response is neither a JSON string array nor an object: %w", err)
	}
	for _, key := range []string{"instructions", "response", "steps", "plan", "tasks", "actions"} {
		if raw, ok := obj[key]; ok {
			var out []string
			if err := json.Unmarshal(raw, &out); err == nil {
				return out, nil
			}
		}
	}
	for _, raw := range obj {
		var out []string
		if err := json.Unmarshal(raw, &out); err == nil {
			return out, nil
		}
	}
	return nil, fmt.Errorf("no array of instruction strings found in response object")
}

// formatFileList renders the worker's filesystem snapshot as one compact
// line per entry — "D  2h  ~\Desktop\Hackathon" — instead of a JSON object
// per file. Same information the planner uses (kind, recency, path) at a
// fraction of the tokens; size is dropped. Order is preserved (the worker
// sends it newest-first).
func formatFileList(entries []FileEntry, now time.Time) string {
	if len(entries) == 0 {
		return "(no entries)"
	}
	var b strings.Builder
	for _, e := range entries {
		kind := "F"
		if e.Type == 1 {
			kind = "D"
		}
		fmt.Fprintf(&b, "%s  %s  %s\n", kind, relativeAge(now.Unix()-e.ModTime), e.Path)
	}
	return strings.TrimRight(b.String(), "\n")
}

// relativeAge turns an age in seconds into "5m", "2h" or "3d".
func relativeAge(secs int64) string {
	switch {
	case secs < 60:
		return "0m"
	case secs < 3600:
		return fmt.Sprintf("%dm", secs/60)
	case secs < 86400:
		return fmt.Sprintf("%dh", secs/3600)
	default:
		return fmt.Sprintf("%dd", secs/86400)
	}
}

// cleanJSONResponse strips markdown code fences that some models add
// around JSON output despite instructions not to, so json.Unmarshal
// doesn't choke on a leading/trailing ```json ... ``` wrapper.
func cleanJSONResponse(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
