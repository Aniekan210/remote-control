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
	Status                  string      `json:"status"` // RUNNING / COMPLETED / PAUSED / CANCELLED / NONE
	CurrentInstructionIndex int         `json:"current_instruction_index"`
	InstructionList         []string    `json:"instruction_list"`
	ExecutionList           []Execution `json:"execution_list"`
	Context                 bool        `json:"context"`
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
