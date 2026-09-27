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
	"time"
)

// aiLogPrefix tags every log line this file emits so they're easy to grep
// out of the rest of the server's output (e.g. `grep '\[ai\]' server.log`).
const aiLogPrefix = "[ai] "

func aiLogf(format string, args ...any) {
	log.Printf(aiLogPrefix+format, args...)
}

func sendMessage(task Task, action Action) (any, error) {
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

	var systemPrompt string
	var userContent []any

	if task.Context {
		systemPrompt = `
You are a task planning agent for a computer-control system.

Break the user's task into a sequence of simple, granular instructions
that another AI will execute one at a time.

Use:
- The task description
- The filesystem information
- The current screenshot

Each instruction MUST:
- Be one simple sentence.
- Have exactly one clear meaning.
- Cause only one meaningful visual or application-state change.
- Be independently understandable.
- Describe what should happen, not how to physically perform it.

Do NOT combine multiple actions or state changes into one instruction.

GRANULARITY

Each instruction should represent one meaningful change to the computer.

For example, for:

"Open Chrome and go to Google."

Return:

["Open Google Chrome.", "Navigate to Google."]

NOT:

["Open Google Chrome and navigate to Google."]

Another example:

"Create a file called hello.txt in the Documents folder."

Return:

["Open the file manager.", "Open the Documents folder.", "Create a new text file.", "Name the file hello.txt."]

Do NOT return:

["Open the file manager and navigate to Documents.", "Create and name a new text file hello.txt."]

Another example:

"Open VS Code and open main.go."

Return:

["Open Visual Studio Code.", "Open main.go."]

Another example:

"Connect with Elon Musk on LinkedIn."

Return:

["Open Google Chrome.", "Navigate to LinkedIn.", "Search for Elon Musk.", "Open Elon Musk's profile.", "Click the Connect button."]

Do NOT return:

["Open Google Chrome and navigate to LinkedIn.", "Search for Elon Musk and open his profile.", "Connect with Elon Musk."]

The planner should break the task down to the smallest meaningful application
or visual state changes required to complete it.

The instruction should NOT describe individual physical operations.

For example, do NOT create instructions such as:

"Move the mouse to the Chrome icon."
"Click the Chrome icon."
"Move the mouse to the search bar."
"Type Elon Musk."

Those are physical execution details. The execution agent handles them.

The instruction should instead describe the meaningful result:

"Open Google Chrome."
"Search for Elon Musk."

The same applies to typing, clicking, dragging, scrolling, and keyboard
shortcuts. Only describe the meaningful state change they are intended to
produce.

Use the screenshot to determine the current application and desktop state.

Use the filesystem information to determine available files and directories.

Do not include steps that have already been completed.

Do not invent application state, files, or UI elements.

Return the instructions in the exact order they must be completed.

Each instruction must depend only on the state produced by the previous
instructions or the state already visible in the screenshot.

Return ONLY a JSON array of strings, with no surrounding prose and no
markdown code fences.

Example:

["Open the file manager.", "Open the Downloads folder.", "Open the project folder.", "Open the terminal."]
`

		fs, err := json.Marshal(action.FileSystemPayload)
		if err != nil {
			aiLogf("[%s] FAILED marshal filesystem payload: %v", callID, err)
			return nil, fmt.Errorf("marshal filesystem payload: %w", err)
		}

		aiLogf("[%s] planning request: task=%q filesystemEntries=%d",
			callID, action.Description, len(action.FileSystemPayload))

		userContent = []any{
			map[string]any{
				"type": "text",
				"text": "Task:\n" + action.Description +
					"\n\nFilesystem:\n" + string(fs),
			},
		}
	} else {
		// Guard against an out-of-range index instead of panicking.
		if task.CurrentInstructionIndex < 0 ||
			task.CurrentInstructionIndex >= len(task.InstructionList) {
			aiLogf("[%s] FAILED instruction index %d out of range (list has %d items): %v",
				callID, task.CurrentInstructionIndex, len(task.InstructionList), task.InstructionList)
			return nil, fmt.Errorf(
				"instruction index %d out of range (list has %d items)",
				task.CurrentInstructionIndex, len(task.InstructionList),
			)
		}

		// Describe the *actual* screenshot dimensions rather than assuming a
		// fixed 1920x1200 resolution, since Screenshot carries its own
		// Width/Height.
		w := action.ScreenshotPayload.Width
		h := action.ScreenshotPayload.Height

		systemPrompt = fmt.Sprintf(`
You are a computer-control action generator.

Convert the given instruction into the precise sequence of physical
mouse and keyboard actions required to complete it using the screenshot.

Prioritize speed, visual accuracy, and correct execution.
Do not explain your reasoning. Return only the structured execution list.

The screenshot resolution is %dx%d.
Coordinates are absolute pixel coordinates within this screenshot.
(0,0) is the top-left corner.
X increases rightward.
Y increases downward.

Determine coordinates from the full screenshot as given, with no manual
offset or correction applied.

For mouse interactions, click the center of the visible clickable area.

Every physical mouse operation MUST be a separate execution.

A normal click requires:
1. MOUSE_MOVEMENT
2. LEFT_CLICK or RIGHT_CLICK

A drag requires:
1. MOUSE_MOVEMENT to the start
2. LEFT_CLICK with mouse_hold=true
3. MOUSE_MOVEMENT to the destination with mouse_hold=true
4. LEFT_CLICK with mouse_hold=false

Every keyboard operation MUST be a separate KEYBOARD_INPUT.

Return actions in exact physical execution order.
Do not combine physical operations.
Do not assume the mouse is already at the target.
Do not invent UI elements or application state.

Return ONLY: {"response": [...]}
With no surrounding prose and no markdown code fences.
`, w, h)

		instruction := task.InstructionList[task.CurrentInstructionIndex]

		aiLogf("[%s] execution request: instruction=%q screenshot=%dx%d imageBytes=%d",
			callID, instruction, w, h, len(action.ScreenshotPayload.Data))

		if w == 0 || h == 0 {
			aiLogf("[%s] WARNING: screenshot dimensions are 0x0 — the worker may not have sent a valid screenshot, and the model has no coordinate frame to work with", callID)
		}
		if len(action.ScreenshotPayload.Data) == 0 {
			aiLogf("[%s] WARNING: screenshot payload is empty (0 bytes) — the model is being asked to click on a blank image", callID)
		}

		userContent = []any{
			map[string]any{
				"type": "text",
				"text": instruction,
			},
		}
	}

	// Add screenshot to both modes.
	imageData := "data:image/" + action.ScreenshotPayload.Format +
		";base64," +
		base64.StdEncoding.EncodeToString(action.ScreenshotPayload.Data)

	userContent = append(userContent, map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": imageData,
		},
	})

	const model = "deepseek/deepseek-v4.1-flash"

	requestBody := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{
				"role":    "system",
				"content": systemPrompt,
			},
			map[string]any{
				"role":    "user",
				"content": userContent,
			},
		},
		// Ask the provider to enforce valid JSON output instead of relying
		// solely on prompt instructions, which some models ignore.
		"response_format": map[string]any{
			"type": "json_object",
		},
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		aiLogf("[%s] FAILED marshal request body: %v", callID, err)
		return nil, fmt.Errorf("marshal request body: %w", err)
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
		return nil, fmt.Errorf("build request: %w", err)
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
		return nil, fmt.Errorf("openrouter request failed: %w", err)
	}
	defer resp.Body.Close()

	aiLogf("[%s] response received after %s: status=%d", callID, elapsed, resp.StatusCode)

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		aiLogf("[%s] FAILED read response body: %v", callID, err)
		return nil, fmt.Errorf("read response body: %w", err)
	}

	// Check status BEFORE trying to decode into the success shape, so
	// error responses surface their real message instead of a generic
	// JSON parse error.
	if resp.StatusCode != http.StatusOK {
		aiLogf("[%s] FAILED OpenRouter status %d, body: %s", callID, resp.StatusCode, string(rawBody))
		return nil, fmt.Errorf(
			"OpenRouter returned status %d: %s",
			resp.StatusCode, string(rawBody),
		)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		// Usage isn't used for any logic, but logging it costs nothing and
		// is the fastest way to notice "the model saw 0 image tokens" type
		// problems (e.g. a malformed image URL the provider silently drops).
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(rawBody, &result); err != nil {
		aiLogf("[%s] FAILED decode openrouter response: %v, raw body: %s", callID, err, string(rawBody))
		return nil, fmt.Errorf("decode openrouter response: %w (body: %s)", err, string(rawBody))
	}

	aiLogf("[%s] usage: promptTokens=%d completionTokens=%d totalTokens=%d",
		callID, result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens)

	if len(result.Choices) == 0 {
		aiLogf("[%s] FAILED no choices in response, raw body: %s", callID, string(rawBody))
		return nil, fmt.Errorf("no model response (body: %s)", string(rawBody))
	}

	rawContent := result.Choices[0].Message.Content
	aiLogf("[%s] raw model content: %s", callID, rawContent)

	response := cleanJSONResponse(rawContent)
	if response != rawContent {
		aiLogf("[%s] stripped markdown fences from response, cleaned: %s", callID, response)
	}

	if task.Context {
		var instructions []string

		if err := json.Unmarshal([]byte(response), &instructions); err != nil {
			aiLogf("[%s] FAILED invalid instruction list: %v, cleaned response: %s", callID, err, response)
			return nil, fmt.Errorf("invalid instruction list: %w (raw: %s)", err, response)
		}

		aiLogf("[%s] SUCCESS planning: %d instructions: %v", callID, len(instructions), instructions)
		return instructions, nil
	}

	var executions struct {
		Response []Execution `json:"response"`
	}

	if err := json.Unmarshal([]byte(response), &executions); err != nil {
		aiLogf("[%s] FAILED invalid execution list: %v, cleaned response: %s", callID, err, response)
		return nil, fmt.Errorf("invalid execution list: %w (raw: %s)", err, response)
	}

	if len(executions.Response) == 0 {
		aiLogf("[%s] WARNING: model returned an empty execution list — the worker will have nothing to do and this instruction will silently never advance", callID)
	}

	aiLogf("[%s] SUCCESS execution: %d actions: %+v", callID, len(executions.Response), executions.Response)

	return executions.Response, nil
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
