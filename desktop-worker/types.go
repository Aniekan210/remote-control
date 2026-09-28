package main

// These types must stay byte-for-byte compatible with the server's
// JSON schema. Copied intentionally rather than shared as a module,
// since the worker and server are separate binaries/repos.

type StateUpdate struct {
	Type    string `json:"type"`
	Payload Task   `json:"payload"`
}

type Task struct {
	DeviceID                string      `json:"device_id"`
	Description             string      `json:"description"`
	Status                  string      `json:"status"` // RUNNING / NEEDS_INPUT / COMPLETED / PAUSED / CANCELLED / NONE
	CurrentInstructionIndex int         `json:"current_instruction_index"`
	InstructionList         []string    `json:"instruction_list"`
	ExecutionList           []Execution `json:"execution_list"`
	Context                 bool        `json:"context"`
	// Seq is the only one of the fields below the worker acts on (it is
	// echoed in every ADVANCE); the rest are here so the JSON round-trips
	// cleanly and so state changes are seen in full.
	Seq            int      `json:"seq"`
	Reason         string   `json:"reason"` // non-empty while the server is revising the plan (see isUserResumeSignal)
	Question       string   `json:"question"`
	QuestionKind   string   `json:"question_kind"`
	QuestionImage  string   `json:"question_image"`
	Answers        []string `json:"answers"`
	AutoReplans    int      `json:"auto_replans"`
	ConfirmedIndex int      `json:"confirmed_index"`
	NeedsConfirm   []bool   `json:"needs_confirm"`
	CostUSD        float64  `json:"cost_usd"`
	LastError      string   `json:"last_error"`
}

type Execution struct {
	Type      string `json:"type"` // RIGHT_CLICK / LEFT_CLICK / KEYBOARD_INPUT / MOUSE_MOVEMENT
	KeyString string `json:"key_string"`
	MousePosX int    `json:"mouse_pos_x"`
	MousePosY int    `json:"mouse_pos_y"`
	MouseHold bool   `json:"mouse_hold"`
}

type Action struct {
	Type              string      `json:"type"`
	DeviceID          string      `json:"device_id,omitempty"`
	Description       string      `json:"description,omitempty"`
	FileSystemPayload []FileEntry `json:"file_system_payload,omitempty"`
	ScreenshotPayload Screenshot  `json:"screenshot_payload"`
	// Error, on ADVANCE, reports that the last step couldn't be carried
	// out here (screenshot failed twice, coordinates off the screen, an
	// unknown action type). The server treats it as a replan.
	Error string `json:"error,omitempty"`
	// Seq, on ADVANCE, is the Task.Seq this ADVANCE answers. The server
	// ignores ADVANCEs for a state it has already moved past.
	Seq int `json:"seq,omitempty"`
}

type FileEntry struct {
	Path    string `json:"path"`
	Type    uint8  `json:"type"` // 0 = file, 1 = directory
	Size    uint64 `json:"size"`
	ModTime int64  `json:"mod_time"`
}

const (
	FileTypeFile uint8 = 0
	FileTypeDir  uint8 = 1
)

type Screenshot struct {
	Format string `json:"format"`
	Width  uint32 `json:"width"`
	Height uint32 `json:"height"`
	Data   []byte `json:"data"` // encoding/json base64-encodes []byte automatically
}
