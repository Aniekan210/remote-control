package main

type StateUpdate struct {
	Type    string `json:"type"`
	Payload Task   `json:"payload"`
}

// RoomRequest represents the payload for creating a new room/item via HTTP
type RoomRequest struct {
	DeviceID string `json:"device_id"`
}

// Item represents an entity in our master hash table
type Task struct {
	DeviceID                string        `json:"device_id"`
	Description             string        `json:"description"` // the task the user said
	Status                  string        `json:"status"`      // RUNNING / COMPLETED / PAUSED / CANCELLED / NONE
	CurrentInstructionIndex int           `json:"current_instruction_index"`
	InstructionList         []Instruction `json:"instruction_list"` // list of instructions from initial AI breakdown
	ExecutionList           []Execution   `json:"execution_list"`   // list of executions after AI solves singular instruction
	Context                 bool          `json:"context"`          // boolean asking for context from the desktop worker
}

type Instruction struct {

}

type Execution struct {
	Type      string `json:"type"`        // RIGHT_CLICK / LEFT_CLICK / KEYBOARD_INPUT / MOUSE_MOVEMENT
	KeyString string `json:"key_string"`  // the string that the keyboard is to input
	MousePosX int    `json:"mouse_pos_x"` // the x position the mouse is to move to
	MousePosY int    `json:"mouse_pos_y"` // the y position the mouse is to move to
}

type Action struct {
	Type              string      `json:"type"`
	DeviceID          string      `json:"device_id"`           // for CREATE_TASK
	Description       string      `json:"description"`         // for CREATE_TASK
	FileSystemPayload []FileEntry `json:"file_system_payload"` // for ADVANCE
	ScreenshotPayload Screenshot  `json:"screenshot_payload"`  
}

type FileEntry struct {
	Path    string `json:"path"`
	Type    uint8  `json:"type"`
	Size    uint64 `json:"size"`
	ModTime int64  `json:"mod_time"`
}

type Screenshot struct {
	Format string `json:"format"`
	Width  uint32 `json:"width"`
	Height uint32 `json:"height"`
	Data   []byte `json:"data"`
}
