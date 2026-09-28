package main

import "time"

type StateUpdate struct {
	Type    string `json:"type"`
	Payload Task   `json:"payload"`
}

// RoomRequest represents the payload for creating a new room/item via HTTP
type RoomRequest struct {
	DeviceID     string `json:"device_id"`
	WorkerSecret string `json:"worker_secret"` // the worker's per-install secret; proves ownership of the room on /ws
}

type Task struct {
	DeviceID                string      `json:"device_id"`
	Description             string      `json:"description"` // the task the user said
	Status                  string      `json:"status"`      // RUNNING / NEEDS_INPUT / COMPLETED / PAUSED / CANCELLED / NONE
	CurrentInstructionIndex int         `json:"current_instruction_index"`
	InstructionList         []string    `json:"instruction_list"` // list of instructions from initial AI breakdown
	ExecutionList           []Execution `json:"execution_list"`   // list of executions after AI solves singular instruction
	Context                 bool        `json:"context"`          // boolean asking for context from the desktop worker
	Seq                     int         `json:"seq"`              // bumped on every broadcast the worker must act on; the worker echoes it in ADVANCE
	Reason                  string      `json:"reason"`           // why the next planner call is a revise rather than a fresh plan; "" on the first plan
	Question                string      `json:"question"`         // shown to the user while Status == NEEDS_INPUT
	QuestionKind            string      `json:"question_kind"`    // "blocked" / "confirm" / "budget"
	QuestionImage           string      `json:"question_image"`   // data:image/jpeg;base64,... of the screen when the question was asked; cleared once answered
	Answers                 []string    `json:"answers"`          // every "Q: … / A: …" pair so far, sent to every revise call
	AutoReplans             int         `json:"auto_replans"`     // automatic revises so far (capped)
	ConfirmedIndex          int         `json:"confirmed_index"`  // index of the last step the user approved; -1 = none
	NeedsConfirm            []bool      `json:"needs_confirm"`    // parallel to InstructionList: steps that must be approved first
	CostUSD                 float64     `json:"cost_usd"`         // total OpenRouter spend on this task so far
	LastError               string      `json:"last_error"`       // one-off error for the client (budget reached, ...); cleared on the next accepted action

	// Server-only bookkeeping for the per-task caps. json:"-" keeps these
	// out of the TASK_UPDATE broadcast; they live in taskHashTable only.
	AICalls      int       `json:"-"` // AI calls since the task started (or since the last "continue")
	PlannerCalls int       `json:"-"` // planning calls, same window
	CostBase     float64   `json:"-"` // CostUSD at the start of the current window
	StartedAt    time.Time `json:"-"` // start of the current window, for MAX_TASK_DURATION

	InstrAttempts int      `json:"-"` // executor calls on the current instruction (capped, then replan)
	StepActions   []string `json:"-"` // what earlier batches already did for the current instruction (shown to the executor)
	SkipStreak    int      `json:"-"` // steps skipped in a row with nothing done in between
	InFlight      bool     `json:"-"` // an AI call for the current Seq is running; further ADVANCEs for it are duplicates
}

type Execution struct {
	Type      string `json:"type"`        // RIGHT_CLICK / LEFT_CLICK / KEYBOARD_INPUT / MOUSE_MOVEMENT / WAIT
	KeyString string `json:"key_string"`  // the string that the keyboard is to input
	MousePosX int    `json:"mouse_pos_x"` // the x position the mouse is to move to
	MousePosY int    `json:"mouse_pos_y"` // the y position the mouse is to move to
	MouseHold bool   `json:"mouse_hold"`  // true to hold current click and true if you want to move the mouse with the click, false next execution to release
	Ms        int    `json:"ms"`          // WAIT only: how long to wait, in milliseconds
}

type Action struct {
	Type              string      `json:"type"`
	DeviceID          string      `json:"device_id"`           // for CREATE_TASK
	Description       string      `json:"description"`         // for CREATE_TASK
	FileSystemPayload []FileEntry `json:"file_system_payload"` // for ADVANCE
	ScreenshotPayload Screenshot  `json:"screenshot_payload"`
	Error             string      `json:"error"` // for ADVANCE: the worker couldn't carry out the last step (screenshot failed, bad coordinates, unknown action)
	Seq               int         `json:"seq"`   // for ADVANCE: the Task.Seq the worker acted on; stale ones are ignored

	// Set by the server, never by a client (json:"-"), so the state
	// machine stays pure: see onClientAction.
	ReceivedAt time.Time `json:"-"` // when the action arrived
	Refused    string    `json:"-"` // CREATE_TASK only: why the task can't start (shown as last_error)
}

type FileEntry struct {
	Path    string `json:"path"`
	Type    uint8  `json:"type"`
	Size    uint64 `json:"size"`
	ModTime int64  `json:"mod_time"`
}

type Screenshot struct {
	Format       string `json:"format"` // "jpeg" (downscaled by the worker) or "png" (native resolution)
	Width        uint32 `json:"width"`  // size of the image the AI sees — its coordinates are in this frame
	Height       uint32 `json:"height"`
	ScreenWidth  uint32 `json:"screen_width"` // the real screen's size; the worker scales coordinates back to it
	ScreenHeight uint32 `json:"screen_height"`
	Data         []byte `json:"data"`
}
