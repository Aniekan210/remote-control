package main

import (
	"log"
)

// RunExecutor is the state machine that drives the CREATE_TASK -> ADVANCE
// protocol described by the server. It reacts to each distinct Task state
// as follows:
//
//   - ExecutionList non-empty  -> run those actions, then ADVANCE (screenshot
//     only; Context is always false by the time an ExecutionList exists).
//   - ExecutionList empty      -> nothing to run, it's just our turn to ask
//     for the next step. ADVANCE with the filesystem snapshot attached only
//     when Context is true (i.e. right after CREATE_TASK); otherwise a plain
//     ADVANCE (e.g. right after the planning call, before the first
//     execution list exists).
//   - Status != RUNNING        -> don't touch input devices at all, just
//     reflect status on the overlay.
//
// Because state.UpdateTask() already filters out no-op broadcasts, every
// TaskChange delivered here represents a genuinely new state, so there's
// no risk of re-executing the same ExecutionList twice.
func RunExecutor(state *State, deviceID string, changes <-chan TaskChange, overlay chan<- OverlayState, fsStore *SnapshotStore) {
	for change := range changes {
		cur := change.Cur

		overlay <- overlayStateFor(cur)

		if cur.Status != "RUNNING" {
			continue
		}

		if len(cur.ExecutionList) > 0 {
			executeList(cur.ExecutionList)
			sendAdvance(state, deviceID, false, fsStore)
			continue
		}

		// Our turn to ask for the next step. Only attach the filesystem
		// snapshot on the very first ADVANCE of a task (Context == true).
		sendAdvance(state, deviceID, cur.Context, fsStore)
	}
}

func sendAdvance(state *State, deviceID string, includeFS bool, fsStore *SnapshotStore) {
	shot, err := CaptureScreen()
	if err != nil {
		log.Printf("executor: screenshot failed: %v", err)
		return
	}

	action := Action{
		Type:              "ADVANCE",
		DeviceID:          deviceID,
		ScreenshotPayload: shot,
	}
	if includeFS {
		action.FileSystemPayload = fsStore.Snapshot()
	}

	SendAction(state, action)
}

// executeList runs one instruction's worth of physical actions in order.
// Hold-state is local to a single list: per the planner's system prompt, a
// drag's press/move/release steps all arrive together in one ExecutionList,
// so there's no need to persist hold-state across network round trips.
func executeList(execs []Execution) {
	leftHeld := false
	rightHeld := false

	for _, e := range execs {
		switch e.Type {
		case "MOUSE_MOVEMENT":
			MoveMouse(e.MousePosX, e.MousePosY)

		case "LEFT_CLICK":
			switch {
			case e.MouseHold && !leftHeld:
				LeftDown()
				leftHeld = true
			case !e.MouseHold && leftHeld:
				LeftUp()
				leftHeld = false
			case !e.MouseHold && !leftHeld:
				LeftClick()
			}

		case "RIGHT_CLICK":
			switch {
			case e.MouseHold && !rightHeld:
				RightDown()
				rightHeld = true
			case !e.MouseHold && rightHeld:
				RightUp()
				rightHeld = false
			case !e.MouseHold && !rightHeld:
				RightClick()
			}

		case "KEYBOARD_INPUT":
			TypeText(e.KeyString)

		default:
			log.Printf("executor: unknown execution type %q", e.Type)
		}
	}

	// Safety net: never leave a button physically stuck down if a list
	// ends mid-hold unexpectedly.
	if leftHeld {
		LeftUp()
	}
	if rightHeld {
		RightUp()
	}
}

func overlayStateFor(t Task) OverlayState {
	switch t.Status {
	case "RUNNING", "PAUSED":
		total := len(t.InstructionList)
		idx := t.CurrentInstructionIndex

		var stepText string
		var stepIndex int
		switch {
		case total == 0:
			// Right after CREATE_TASK, before the planning call has
			// returned an instruction list yet.
			stepText = "Planning your task..."
		case idx >= total:
			// Last instruction's execution list just got sent; the next
			// ADVANCE result will flip this to COMPLETED.
			stepText = "Finishing up..."
			stepIndex = total
		default:
			stepText = t.InstructionList[idx]
			stepIndex = idx + 1 // 1-based for "Step X of Y" display
		}

		return OverlayState{
			Visible:   true,
			Status:    t.Status,
			StepText:  stepText,
			StepIndex: stepIndex,
			StepTotal: total,
		}

	case "COMPLETED":
		total := len(t.InstructionList)
		return OverlayState{
			Visible:   true,
			Status:    "COMPLETED",
			StepText:  "Task completed",
			StepIndex: total,
			StepTotal: total,
		}

	default: // CANCELLED, NONE
		return OverlayState{Visible: false}
	}
}
