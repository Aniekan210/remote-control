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
	"strconv"
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

// Planner output limits (B5). Reasoning tokens are billed as output tokens
// and can dominate a planning call's cost, so the planner runs with low
// reasoning effort and a hard output cap. The cap has to hold the whole
// JSON plan — a 20-step plan is ~600 tokens — plus the (low) reasoning,
// which on Gemini counts against the same output budget; 2000 leaves
// headroom so a long plan is never cut off mid-JSON (a truncated plan
// costs a full retry, which is worse than the few extra tokens).
// Both are overridable: PLANNER_MAX_TOKENS, PLANNER_REASONING_EFFORT
// ("none" disables the reasoning parameter entirely).
const (
	defaultPlannerMaxTokens       = 2000
	defaultPlannerReasoningEffort = "low"
)

// callOptions are the per-call knobs that differ between planner and
// executor. Zero values mean "don't send the parameter".
type callOptions struct {
	maxTokens       int
	reasoningEffort string
}

// plannerOptions returns the planner's output limits.
func plannerOptions() callOptions {
	opts := callOptions{maxTokens: defaultPlannerMaxTokens, reasoningEffort: defaultPlannerReasoningEffort}
	if v := os.Getenv("PLANNER_MAX_TOKENS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.maxTokens = n
		}
	}
	if v := os.Getenv("PLANNER_REASONING_EFFORT"); v != "" {
		opts.reasoningEffort = v
	}
	if opts.reasoningEffort == "none" {
		opts.reasoningEffort = ""
	}
	return opts
}

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
// the call never got a usable response). key is the OpenRouter key the call
// is billed to (the user's own, or the server's for an owner).
func sendMessage(task Task, action Action, key apiKey) (any, float64, error) {
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
		return callPlanner(callID, task, action, key)
	}
	return callExecutor(callID, task, action, key)
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

// executionActionSchema is one physical action: exactly the five
// Execution fields, coordinates as single integers.
var executionActionSchema = map[string]any{
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
}

// executionSchema is the strict structured-output schema for the executor:
// a verdict on the screen, plus the actions when the verdict is "act".
var executionSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"verdict": map[string]any{
			"type": "string",
			"enum": []string{"act", "skip", "replan", "blocked"},
		},
		"reason":           map[string]any{"type": "string"},
		"instruction_done": map[string]any{"type": "boolean"},
		"actions": map[string]any{
			"type":  "array",
			"items": executionActionSchema,
		},
	},
	"required":             []string{"verdict", "reason", "instruction_done", "actions"},
	"additionalProperties": false,
}

// Executor verdicts (see the executor prompt).
const (
	verdictAct     = "act"     // run Actions; advance only if InstructionDone
	verdictSkip    = "skip"    // the step is already done on screen
	verdictReplan  = "replan"  // off-plan but achievable: revise the plan
	verdictBlocked = "blocked" // can't be done as asked: ask the user
)

// ExecResult is the executor's judgement of the screen for one step.
type ExecResult struct {
	Verdict         string
	Reason          string
	InstructionDone bool
	Actions         []Execution
}

// callPlanner asks the planner to break the task into instructions.
func callPlanner(callID string, task Task, action Action, key apiKey) (any, float64, error) {
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
		plannerSystemPrompt, userContent, "plan", planSchema, plannerOptions(), key)
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
func callExecutor(callID string, task Task, action Action, key apiKey) (any, float64, error) {
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

	idx := task.CurrentInstructionIndex
	instruction := task.InstructionList[idx]

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
			"text": executorUserText(task, instruction, idx, w, h),
		},
		screenshotContent(action.ScreenshotPayload),
	}

	// No output cap for the executor: it's cheap, and a long, careful action
	// list is worth more than the tokens it saves to cut it short.
	response, cost, err := callOpenRouter(callID, "EXECUTION", modelForContext(false),
		executorSystemPrompt, userContent, "actions", executionSchema, callOptions{}, key)
	if err != nil {
		return nil, cost, err
	}

	res, err := parseExecResult(response)
	if err != nil {
		aiLogf("[%s] FAILED invalid execution result: %v, cleaned response: %s", callID, err, response)
		return nil, cost, fmt.Errorf("invalid execution result: %w (raw: %s)", err, response)
	}

	aiLogf("[%s] SUCCESS execution: verdict=%s done=%v reason=%q %d actions: %+v",
		callID, res.Verdict, res.InstructionDone, res.Reason, len(res.Actions), res.Actions)

	return res, cost, nil
}

// executorUserText is the dynamic part of an executor call: the step to
// carry out, plus the context needed to judge whether the screen is where
// the plan expects (E3): the overall task, the whole plan with the current
// step marked, the previous step, and the user's answers so far.
func executorUserText(task Task, instruction string, idx int, w, h uint32) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Instruction: %s\n\n", instruction)
	fmt.Fprintf(&b, "Overall task: %s\n\n", task.Description)
	b.WriteString("Plan:\n")
	for i, step := range task.InstructionList {
		marker := "   "
		if i == idx {
			marker = "-> "
		}
		fmt.Fprintf(&b, "%s%d. %s\n", marker, i+1, step)
	}
	if idx > 0 && idx-1 < len(task.InstructionList) {
		fmt.Fprintf(&b, "\nPrevious step: %s\n", task.InstructionList[idx-1])
	} else {
		b.WriteString("\nPrevious step: (none — this is the first step)\n")
	}
	if len(task.Answers) > 0 {
		b.WriteString("\nUser's answers so far:\n")
		for _, a := range task.Answers {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	}
	fmt.Fprintf(&b, "\nScreenshot size: %dx%d pixels (0 <= x < %d, 0 <= y < %d).", w, h, w, h)
	return b.String()
}

// parseExecResult decodes and validates the executor's output. It is
// tolerant of the pre-verdict shape ({"response": [...]}), read as "act,
// step done", so a model that ignores the schema still works.
//
// Validation is the safety net for the failure that once silently produced
// six empty actions: json.Unmarshal does NOT error when the model uses the
// wrong field names (e.g. "action"/"x"/"y"/"text" instead of
// "type"/"mouse_pos_x"/...) — it just leaves every field at its zero value.
// An action with an empty/unknown Type is that exact symptom, so it's
// rejected here and the caller retries, instead of sending the worker a
// list of no-op clicks at (0,0).
func parseExecResult(response string) (ExecResult, error) {
	var raw struct {
		Verdict         string      `json:"verdict"`
		Reason          string      `json:"reason"`
		InstructionDone *bool       `json:"instruction_done"`
		Actions         []Execution `json:"actions"`
		Response        []Execution `json:"response"` // legacy shape
	}
	if err := json.Unmarshal([]byte(response), &raw); err != nil {
		return ExecResult{}, err
	}

	res := ExecResult{
		Verdict: strings.ToLower(strings.TrimSpace(raw.Verdict)),
		Reason:  strings.TrimSpace(raw.Reason),
		Actions: raw.Actions,
	}
	if len(res.Actions) == 0 && len(raw.Response) > 0 {
		res.Actions = raw.Response
	}
	if res.Verdict == "" && len(res.Actions) > 0 {
		res.Verdict = verdictAct
	}
	// A missing instruction_done means the old one-call-per-step contract.
	res.InstructionDone = raw.InstructionDone == nil || *raw.InstructionDone

	switch res.Verdict {
	case verdictAct:
		if len(res.Actions) == 0 {
			if res.InstructionDone {
				// Nothing to do and the step is done: that's a skip.
				res.Verdict = verdictSkip
				return res, nil
			}
			return ExecResult{}, fmt.Errorf("verdict act with no actions")
		}
		for i, e := range res.Actions {
			switch e.Type {
			case "MOUSE_MOVEMENT", "LEFT_CLICK", "RIGHT_CLICK", "KEYBOARD_INPUT":
				// valid
			default:
				return ExecResult{}, fmt.Errorf("execution %d has invalid/empty type %q — the model almost certainly used the wrong JSON field names (expected type/mouse_pos_x/mouse_pos_y/key_string/mouse_hold)", i, e.Type)
			}
		}
	case verdictSkip:
		res.Actions = nil
	case verdictReplan, verdictBlocked:
		res.Actions = nil
		if res.Reason == "" {
			res.Reason = "The screen isn't what the plan expected."
		}
	default:
		return ExecResult{}, fmt.Errorf("unknown verdict %q", raw.Verdict)
	}
	return res, nil
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
func callOpenRouter(callID, mode, model, systemPrompt string, userContent []any, schemaName string, schema map[string]any, opts callOptions, key apiKey) (string, float64, error) {
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
		if opts.maxTokens > 0 {
			requestBody["max_tokens"] = opts.maxTokens
		}
		if opts.reasoningEffort != "" {
			requestBody["reasoning"] = map[string]any{"effort": opts.reasoningEffort}
		}

		content, cost, status, err := postChatCompletion(callID, mode, model, requestBody, key)
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
func postChatCompletion(callID, mode, model string, requestBody map[string]any, key apiKey) (string, float64, int, error) {
	body, err := json.Marshal(requestBody)
	if err != nil {
		aiLogf("[%s] FAILED marshal request body: %v", callID, err)
		return "", 0, 0, fmt.Errorf("marshal request body: %w", err)
	}

	if key.value == "" {
		// This is a very common silent-failure cause: every request will
		// come back 401 and, without this line, that 401 looks identical
		// to a real auth/billing problem with a *valid* key.
		if key.serverKey {
			aiLogf("[%s] WARNING: OPENROUTER_API_KEY is empty — every request will fail auth", callID)
		} else {
			aiLogf("[%s] WARNING: the user's OpenRouter key is empty — every request will fail auth", callID)
		}
	}

	keyOwner := "user"
	if key.serverKey {
		keyOwner = "server"
	}
	aiLogf("[%s] calling OpenRouter: model=%s key=%s requestBytes=%d", callID, model, keyOwner, len(body))

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

	req.Header.Set("Authorization", "Bearer "+key.value)
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
	day, month := costs.add(cost, key.serverKey)
	aiLogf("[%s] usage: mode=%s model=%s key=%s promptTokens=%d cachedTokens=%d completionTokens=%d reasoningTokens=%d totalTokens=%d cost=$%.5f | today=$%.4f month=$%.4f",
		callID, mode, model, keyOwner, result.Usage.PromptTokens, result.Usage.PromptTokensDetails.CachedTokens,
		result.Usage.CompletionTokens, result.Usage.CompletionTokensDetails.ReasoningTokens,
		result.Usage.TotalTokens, cost, day, month)

	if mode == "EXECUTION" && result.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
		aiLogf("[%s] WARNING: executor model=%s spent %d reasoning tokens — the executor should not be a reasoning model (slow and billed as output); pick a non-reasoning EXECUTION_MODEL",
			callID, model, result.Usage.CompletionTokensDetails.ReasoningTokens)
	}

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
