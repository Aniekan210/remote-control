package main

import (
	"reflect"
	"sync"

	"github.com/coder/websocket"
)

// State is shared across goroutines. All access goes through the methods
// below, which take the mutex — nothing else in the codebase should touch
// these fields directly.
type State struct {
	mu       sync.Mutex
	conn     *websocket.Conn
	lastTask Task
	frame    shotFrame // the last screenshot sent: the frame the next actions' coordinates are in
}

// SetFrame records the size of the screenshot just sent (and of the real
// screen), so the actions that come back can be scaled onto the screen.
func (s *State) SetFrame(f shotFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frame = f
}

// Frame returns the last screenshot's frame.
func (s *State) Frame() shotFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frame
}

func NewState() *State {
	return &State{lastTask: Task{Status: "NONE"}}
}

func (s *State) SetConn(c *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conn = c
}

func (s *State) GetConn() *websocket.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn
}

// CurrentStatus returns the status of the most recently seen task
// ("RUNNING", "PAUSED", "NONE", ...). Used by the takeover monitor so it
// only pauses when a task is actually running.
func (s *State) CurrentStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTask.Status
}

// LastTask returns the most recently received task state.
func (s *State) LastTask() Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTask
}

// UpdateTask stores the freshly received task and reports whether it
// actually differs from what we last saw. Duplicate/no-op broadcasts
// (which the server can legitimately send) are filtered out here so
// callers never have to worry about re-processing the same state twice.
func (s *State) UpdateTask(newTask Task) (prev Task, changed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev = s.lastTask
	s.lastTask = newTask
	return prev, !reflect.DeepEqual(prev, newTask)
}

// TaskChange is pushed from the WebSocket reader to the executor whenever
// a genuinely new task state arrives.
type TaskChange struct {
	Prev Task
	Cur  Task
}

// OverlayState is pushed from the executor to the overlay window. It
// carries granular step progress (not just a RUNNING/PAUSED label) so the
// overlay can show what's actually happening — "Step 3 of 7: Open Google
// Chrome." — instead of just a static status word.
type OverlayState struct {
	Visible bool
	// Status is "RUNNING", "PAUSED", "NEEDS_INPUT" or "COMPLETED" — drives
	// the border color (NEEDS_INPUT shows amber, like PAUSED).
	Status string
	// TaskDescription is the overall goal the user typed ("open chrome and
	// search for cats") — shown small/muted for context above the current
	// step.
	TaskDescription string
	// StepText is the current instruction being worked on, or a
	// placeholder like "Planning..." before the instruction list exists
	// yet (right after CREATE_TASK, before the planning call returns).
	StepText string
	// ActionText is the live low-level action being performed right now
	// within the current step — e.g. `Typing "chrome"`, `Click (452, 310)`,
	// `Move cursor`. Empty when nothing physical is executing (planning,
	// paused, between steps). This is the most granular line: it's what
	// the machine is literally doing at this instant.
	ActionText string
	// StepIndex is the 1-based current step number for display ("Step 3
	// of 7"). 0 means no step data yet (still planning).
	StepIndex int
	// StepTotal is the total number of steps once known; 0 if not yet
	// known.
	StepTotal int
}
