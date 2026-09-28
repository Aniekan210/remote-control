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

	var systemPrompt string
	var userContent []any

	if task.Context {
		systemPrompt = `
You are the PLANNING agent for a computer-control system that operates a
MICROSOFT WINDOWS computer on the user's behalf. You decide WHAT should
happen, step by step. A separate execution agent decides HOW (the physical
clicks and keystrokes) — so you never describe mouse or keyboard actions.

Your job: turn the user's task into an ordered list of small, single-purpose
instructions, each describing exactly one meaningful change to the computer's
state, grounded in what is actually on screen right now.

────────────────────────────────────────
TARGET ENVIRONMENT — WINDOWS
────────────────────────────────────────
- The machine runs Microsoft Windows. Plan the way things are really done on
  Windows: the Start menu, the taskbar, the desktop, and Windows apps.
- Applications are opened by name via the Start menu (Windows key → type the
  name → Enter). You do not need file paths or shell commands to launch apps.
- Prefer the simplest reliable Windows path to each outcome.

────────────────────────────────────────
GROUND EVERY STEP IN THE CURRENT SCREENSHOT
────────────────────────────────────────
Before writing the plan, read the screenshot and note the real starting
state: which app is focused, whether a window is maximized and covering the
screen, and whether the taskbar and desktop are visible.

- Start from the state actually shown — never from an imagined clean desktop.
- CLEAR THE WAY FIRST. If a window is currently maximized or covering the
  screen and the task's next move is to launch or switch to a DIFFERENT app,
  make your FIRST instruction "Minimize the current window to reveal the
  desktop and taskbar." (or "Show the desktop." to clear everything). This
  gives a clean, predictable surface before opening anything new.
- ENSURE THE TASKBAR IS AVAILABLE. If the taskbar is hidden or not visible in
  the screenshot, include "Reveal the Windows taskbar." before any step that
  relies on the Start button or taskbar.
- Do NOT include steps for state that is already true (e.g. don't say "Open
  the Start menu." if it is already open, or "Show the desktop." if the
  desktop is already clear).

────────────────────────────────────────
BE GRANULAR — RELIABILITY OVER BREVITY
────────────────────────────────────────
Your single most important job is RELIABILITY, not efficiency. Do NOT try to
keep the plan short. A LONG list of small, simple, individually-verifiable
steps is BETTER than a short list of big ones, because the executor gets a
fresh screenshot before every step — so more, smaller steps means more
chances to observe the real screen and stay on track. When in doubt, SPLIT.
Err on the side of MORE steps. A genuinely multi-part task should usually be
around 8–14 steps, not 3–5.

THE SPLITTING RULE: give a separate instruction to each distinct change in
what is on screen. Split on ALL of these — each is its own step:
  - an app/window opening, closing, or coming to the front
  - a page navigating or loading new content
  - a new browser tab opening
  - a menu, panel, dialog, sidebar, popup, or overlay appearing or closing
  - switching to a different window or app
  - a file, folder, or document opening
  - FOCUSING an input (search box, address bar, text field) — its own step
  - ENTERING text into a focused field — its own step, SEPARATE from…
  - SUBMITTING / confirming that entry (pressing Enter, clicking a button)
  - a list of results, suggestions, or autocomplete appearing
  - selecting or opening one item from such a list
  - dismissing a cookie/consent banner or other interruption

Split "enter text" and "submit it" into TWO steps — after typing, the screen
usually changes (suggestions, validation) and the executor should see that
before submitting.

Every instruction is ONE short, plain sentence describing the RESULT — never
the physical action (no "move the mouse", "click", "type", "press", "scroll").
"Focus the search box." is a result; "Click at 800,400." is not.

────────────────────────────────────────
GRANULARITY — WORKED EXAMPLES (note how many steps)
────────────────────────────────────────
Task: "Search for cats on Google." — Chrome already open on another page.
{"instructions": [
  "Open a new browser tab.",
  "Go to google.com.",
  "Focus the Google search box.",
  "Enter 'cats' in the search box.",
  "Submit the search.",
  "Open the top search result."
]}

Task: "Play lo-fi music on YouTube." — a maximized window currently covers the
screen.
{"instructions": [
  "Minimize the current window to reveal the desktop and taskbar.",
  "Open Google Chrome.",
  "Open a new browser tab.",
  "Go to youtube.com.",
  "Focus the YouTube search box.",
  "Enter 'lofi hip hop radio' in the search box.",
  "Submit the search.",
  "Open the first video in the results.",
  "Make sure the video is playing."
]}

Task: "Reply 'thanks!' to the newest email in Gmail." — Chrome already open.
{"instructions": [
  "Open a new browser tab.",
  "Go to gmail.com.",
  "Open the most recent email in the inbox.",
  "Open the reply editor for that email.",
  "Focus the reply body.",
  "Enter 'thanks!' in the reply body.",
  "Send the reply."
]}

Do NOT collapse these into coarse steps like ["Reply to the newest email."],
and do NOT drop to physical actions like ["Click the Compose button."]. Aim
for the granular middle: many small, plain, result-level steps.

────────────────────────────────────────
USE ALL THE CONTEXT
────────────────────────────────────────
- Use the screenshot for the current application and desktop state.
- Use the filesystem information to know which files and folders exist; do
  not invent files, folders, apps, or UI elements that aren't there.
- Do not include steps that are already completed.

────────────────────────────────────────
USING THE FILESYSTEM — FILES & RECENCY
────────────────────────────────────────
- The filesystem is a JSON array of entries, each with: path (full Windows
  path), type (0=file, 1=directory), size, and mod_time (Unix seconds since
  1970). It is provided SORTED NEWEST-FIRST — the most recently modified
  files are at the TOP of the array.
- "Current time (Unix seconds)" is given alongside it; use it to judge how
  recent a file is (e.g. "earlier this month" vs "today").
- For "most recent" / "last" / "latest" requests (e.g. "open the last
  screenshot I took"), choose the entry with the LARGEST mod_time that
  matches — screenshots are typically PNGs whose path contains
  "Screenshot" and usually live under Pictures\Screenshots. Do NOT just pick
  any file whose name contains the keyword; pick the newest matching one by
  mod_time.
- When the task refers to a specific file, resolve it against the filesystem
  and put the EXACT file name (and its folder) into the instruction, so the
  executor knows precisely what to open — e.g.
  "Open the file 'Screenshot 2026-09-27 143022.png' from the
  Pictures\Screenshots folder." Prefer opening a file directly from its
  folder in File Explorer over guessing a path from memory.

────────────────────────────────────────
OUTPUT
────────────────────────────────────────
Return ONLY a JSON object of this exact form, with no surrounding prose and
no markdown code fences:

{"instructions": ["...", "..."]}
`

		fs, err := json.Marshal(action.FileSystemPayload)
		if err != nil {
			aiLogf("[%s] FAILED marshal filesystem payload: %v", callID, err)
			return nil, 0, fmt.Errorf("marshal filesystem payload: %w", err)
		}

		// The task description lives on the Task (set at CREATE_TASK), NOT on
		// this ADVANCE action — an ADVANCE carries no Description, so reading
		// action.Description here sends the planner an empty task and the
		// model correctly reports "No task was provided."
		aiLogf("[%s] planning request: task=%q filesystemEntries=%d",
			callID, task.Description, len(action.FileSystemPayload))

		userContent = []any{
			map[string]any{
				"type": "text",
				"text": fmt.Sprintf(
					"Task:\n%s\n\nCurrent time (Unix seconds): %d\n\nFilesystem (sorted newest-first by mod_time):\n%s",
					task.Description, time.Now().Unix(), string(fs),
				),
			},
		}
	} else {
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
		// fixed 1920x1200 resolution, since Screenshot carries its own
		// Width/Height.
		w := action.ScreenshotPayload.Width
		h := action.ScreenshotPayload.Height

		systemPrompt = fmt.Sprintf(`
You are the EXECUTION agent for a computer-control system operating a
MICROSOFT WINDOWS computer. You are given ONE instruction and the current
screenshot, and you output the exact sequence of physical mouse and keyboard
actions that carries out that one instruction. Do not explain anything;
return only the JSON.

────────────────────────────────────────
TARGET ENVIRONMENT — WINDOWS
────────────────────────────────────────
- The machine runs Microsoft Windows. Act like a Windows user: the Start
  button / Start menu, the taskbar, window title-bar buttons (minimize,
  maximize, close), and normal Windows apps.
- To OPEN an app: press the Windows key to open the Start menu, type the
  app's name, then press Enter — e.g. one KEYBOARD_INPUT "{WIN}", one
  KEYBOARD_INPUT "chrome", one KEYBOARD_INPUT "{ENTER}". (You may also click
  the Start button if it is clearly visible, then type the name.)
- To SHOW THE DESKTOP / clear windows: KEYBOARD_INPUT "{WIN+D}".
- To MINIMIZE the current window: KEYBOARD_INPUT "{WIN+DOWN}" (or click the
  window's minimize button if visible).
- To SWITCH windows: KEYBOARD_INPUT "{ALT+TAB}". To CLOSE: "{ALT+F4}".
- NEVER type shell commands (e.g. "google-chrome &"). This is not a terminal.

────────────────────────────────────────
CLEAR OBSTACLES FIRST (HANDLE THE UNEXPECTED)
────────────────────────────────────────
Before doing the instruction, LOOK at the screenshot for anything that is
blocking or overlaying your target and would make the instruction fail. Common
interruptions on Windows and the web:
  - cookie / consent banners ("Accept all", "Reject all", "I agree")
  - modal dialogs, popups, and overlays with a close (X) button
  - "Sign in", "Continue as", or account-chooser walls over the content
  - notification / location permission prompts ("Allow", "Block", "Not now")
  - autoplay or promo overlays, newsletter popups, "Open in app" prompts
  - tooltips or coach-marks covering the element

If such an obstacle is present AND it blocks progress toward this instruction,
DISMISS IT FIRST (click Accept/Reject/Close/X/Not now/Dismiss — whichever
safely gets it out of the way, preferring the least-committal option like
"Reject all" or "Not now" unless accepting is clearly required), and THEN do
the instruction — all in the SAME action list. Adding these extra actions is
correct and expected; it is NOT a violation of "do only the instruction."

If the target of the instruction is simply not where you expected but IS
visible elsewhere on screen, act on where it actually is. Do not blindly click
a remembered location.

────────────────────────────────────────
RELIABILITY OVER BREVITY
────────────────────────────────────────
Do NOT try to minimize the number of actions. Output as MANY actions as are
needed to complete this instruction reliably — including obstacle-clearing
above, a MOUSE_MOVEMENT before every click, and separate keystrokes. A longer,
safe action list that succeeds is always better than a short one that misses.

────────────────────────────────────────
COORDINATES — BE MAXIMALLY PRECISE
────────────────────────────────────────
- The screenshot is EXACTLY %dx%d pixels, and it is the machine's true
  native resolution. Your coordinates are in that same pixel space, 1:1 —
  there is no scaling, DPI adjustment, or offset to apply. A coordinate you
  output is the exact on-screen pixel the cursor moves to.
- (0,0) is the top-left pixel. x increases rightward to width-1; y increases
  downward to height-1. Every coordinate MUST fall inside the screen:
  0 <= x < %d and 0 <= y < %d.
- PRECISION IS THE #1 PRIORITY. Being off by even a few pixels can miss the
  target and break the whole task. Take the coordinate seriously.
- Use this exact method for every mouse target:
    1. Find the target element's full bounding box in the image — its left
       edge (x1), right edge (x2), top edge (y1), bottom edge (y2).
    2. Compute the center: x = (x1 + x2) / 2, y = (y1 + y2) / 2.
    3. Output that center, rounded to the nearest whole pixel.
  Aim at the CENTER of the element's clickable body, never a corner, never an
  edge, never the surrounding padding or label.
- For a text field, the center of the input box itself (not its placeholder
  text, not its label). For a button, the center of the button's filled area.
  For an icon, the center of the icon glyph. For a list/search result, the
  center of that row.
- Do NOT round to convenient numbers (like 100, 500, 960). Use the real
  measured center even if it's an odd value like 743, 391.
- Every coordinate must fall inside the screen (the bounds given above) AND
  inside the target element's own bounding box.
- Measure each target independently from what is actually drawn in THIS
  screenshot. Never reuse coordinates from memory or assume a fixed layout.
- Only act on elements actually visible in the screenshot. Never invent
  icons, buttons, or windows that are not shown — locate the real element and
  click its true center.

────────────────────────────────────────
OUTPUT SCHEMA — STRICT
────────────────────────────────────────
Return ONLY a JSON object of exactly this shape:

{"response": [ <action>, <action>, ... ]}

Each <action> uses EXACTLY these five field names — no others:

  "type"         one of: "MOUSE_MOVEMENT" | "LEFT_CLICK" | "RIGHT_CLICK" | "KEYBOARD_INPUT"
  "mouse_pos_x"  INTEGER ONLY — exactly one number, never an array, never a list, never [x,y]
  "mouse_pos_y"  INTEGER ONLY — exactly one number, never an array, never a list, never [x,y]
  "key_string"   string   — text/keys to send (use "" unless type is KEYBOARD_INPUT)
  "mouse_hold"   boolean  — true ONLY to hold the button down during a drag

	CRITICAL:
	mouse_pos_x and mouse_pos_y are SEPARATE INTEGER FIELDS.
	NEVER write coordinates as [x,y].
	NEVER put an array inside mouse_pos_x.
	NEVER put an array inside mouse_pos_y.

	CORRECT:
	{"type":"MOUSE_MOVEMENT","mouse_pos_x":817,"mouse_pos_y":306,"key_string":"","mouse_hold":false}

	WRONG:
	{"type":"MOUSE_MOVEMENT","mouse_pos_x":[817,306],"mouse_pos_y":318,"key_string":"","mouse_hold":false}

	WRONG:
	{"type":"MOUSE_MOVEMENT","mouse_pos_x":[817,306],"mouse_pos_y":[817,306],"key_string":"","mouse_hold":false}
	
	Do NOT use "action", "x", "y", or "text". The keys are exactly "type",
	"mouse_pos_x", "mouse_pos_y", "key_string", "mouse_hold".

────────────────────────────────────────
MOUSE RULES
────────────────────────────────────────
- Each physical operation is its own action, in execution order.
- A normal click is TWO actions: MOUSE_MOVEMENT to the target, then
  LEFT_CLICK (or RIGHT_CLICK). mouse_hold is false for both.
- ALWAYS MOUSE_MOVEMENT before a click — never assume the cursor is already
  in place.
- A drag is FOUR actions: MOUSE_MOVEMENT to start; LEFT_CLICK mouse_hold=true;
  MOUSE_MOVEMENT to destination mouse_hold=true; LEFT_CLICK mouse_hold=false.

────────────────────────────────────────
KEYBOARD RULES
────────────────────────────────────────
- KEYBOARD_INPUT sends the contents of key_string. Plain text is typed
  literally: {"type":"KEYBOARD_INPUT","key_string":"hello world","mouse_pos_x":0,"mouse_pos_y":0,"mouse_hold":false}
- Special keys and chords go in {curly braces} inside key_string:
    {WIN}         Windows key (opens the Start menu)
    {ENTER} {TAB} {ESC} {BACKSPACE} {DELETE} {SPACE}
    {UP} {DOWN} {LEFT} {RIGHT} {HOME} {END} {PAGEUP} {PAGEDOWN}
    {F1}..{F12}
    Chords with +: {WIN+D} (show desktop), {WIN+DOWN} (minimize),
    {ALT+TAB} (switch window), {ALT+F4} (close), {CTRL+A}, {CTRL+C},
    {CTRL+V}, {CTRL+S}, {CTRL+SHIFT+ESC}, etc.
- You may mix literal text and keys in one key_string, e.g.
  "chrome{ENTER}" types "chrome" then presses Enter.
- Prefer keyboard for launching/switching/closing apps and for text entry;
  prefer mouse for clicking specific on-screen targets (links, buttons,
  fields).

────────────────────────────────────────
EXAMPLES
────────────────────────────────────────
Instruction: "Open Google Chrome."
{"response": [
  {"type": "KEYBOARD_INPUT", "mouse_pos_x": 0, "mouse_pos_y": 0, "key_string": "{WIN}", "mouse_hold": false},
  {"type": "KEYBOARD_INPUT", "mouse_pos_x": 0, "mouse_pos_y": 0, "key_string": "chrome", "mouse_hold": false},
  {"type": "KEYBOARD_INPUT", "mouse_pos_x": 0, "mouse_pos_y": 0, "key_string": "{ENTER}", "mouse_hold": false}
]}

Instruction: "Minimize the current window to reveal the desktop and taskbar."
{"response": [
  {"type": "KEYBOARD_INPUT", "mouse_pos_x": 0, "mouse_pos_y": 0, "key_string": "{WIN+D}", "mouse_hold": false}
]}

Instruction: "Click the Sign in button." (button visible at ~1650,240)
{"response": [
  {"type": "MOUSE_MOVEMENT", "mouse_pos_x": 1650, "mouse_pos_y": 240, "key_string": "", "mouse_hold": false},
  {"type": "LEFT_CLICK", "mouse_pos_x": 0, "mouse_pos_y": 0, "key_string": "", "mouse_hold": false}
]}

Return ONLY the JSON object, with no surrounding prose and no markdown code fences.
`, w, h, w, h)

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

	// Two models, one per job: a strong reasoner for PLANNING (decomposing
	// the task into granular Windows steps) and a grounding-first vision
	// model for EXECUTION (turning one instruction + the screenshot into
	// precise click coordinates). Both are overridable by env var so you
	// can A/B different models without recompiling.
	model := modelForContext(task.Context)

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
		// Makes OpenRouter return the call's real cost (usage.cost, in
		// USD) alongside the token counts, so every call can be priced.
		"usage": map[string]any{"include": true},
	}

	body, err := json.Marshal(requestBody)
	if err != nil {
		aiLogf("[%s] FAILED marshal request body: %v", callID, err)
		return nil, 0, fmt.Errorf("marshal request body: %w", err)
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
		return nil, 0, fmt.Errorf("build request: %w", err)
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
		return nil, 0, fmt.Errorf("openrouter request failed: %w", err)
	}
	defer resp.Body.Close()

	aiLogf("[%s] response received after %s: status=%d", callID, elapsed, resp.StatusCode)

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		aiLogf("[%s] FAILED read response body: %v", callID, err)
		return nil, 0, fmt.Errorf("read response body: %w", err)
	}

	// Check status BEFORE trying to decode into the success shape, so
	// error responses surface their real message instead of a generic
	// JSON parse error.
	if resp.StatusCode != http.StatusOK {
		aiLogf("[%s] FAILED OpenRouter status %d, body: %s", callID, resp.StatusCode, string(rawBody))
		return nil, 0, fmt.Errorf(
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
		} `json:"usage"`
	}

	if err := json.Unmarshal(rawBody, &result); err != nil {
		aiLogf("[%s] FAILED decode openrouter response: %v, raw body: %s", callID, err, string(rawBody))
		return nil, 0, fmt.Errorf("decode openrouter response: %w (body: %s)", err, string(rawBody))
	}

	cost := result.Usage.Cost
	day, month := costs.add(cost)
	aiLogf("[%s] usage: mode=%s model=%s promptTokens=%d completionTokens=%d reasoningTokens=%d totalTokens=%d cost=$%.5f | today=$%.4f month=$%.4f",
		callID, mode, model, result.Usage.PromptTokens, result.Usage.CompletionTokens,
		result.Usage.CompletionTokensDetails.ReasoningTokens, result.Usage.TotalTokens, cost, day, month)

	if len(result.Choices) == 0 {
		aiLogf("[%s] FAILED no choices in response, raw body: %s", callID, string(rawBody))
		return nil, cost, fmt.Errorf("no model response (body: %s)", string(rawBody))
	}

	rawContent := result.Choices[0].Message.Content
	aiLogf("[%s] raw model content: %s", callID, rawContent)

	response := cleanJSONResponse(rawContent)
	if response != rawContent {
		aiLogf("[%s] stripped markdown fences from response, cleaned: %s", callID, response)
	}

	if task.Context {
		instructions, err := parseInstructionList(response)
		if err != nil {
			aiLogf("[%s] FAILED invalid instruction list: %v, cleaned response: %s", callID, err, response)
			return nil, cost, fmt.Errorf("invalid instruction list: %w (raw: %s)", err, response)
		}

		aiLogf("[%s] SUCCESS planning: %d instructions: %v", callID, len(instructions), instructions)
		return instructions, cost, nil
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
