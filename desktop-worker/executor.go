package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// interActionDelay is a short pause left after each physical action within
// one instruction's execution list. Actions in a single list run back to
// back with no screenshot between them, so the UI needs a beat to catch up.
// This is well below the per-step AI round-trip (seconds), so it doesn't
// make the agent feel slow; raise it if a machine is sluggish, lower it for
// snappier runs.
//
// After an action that usually opens something — {WIN}, {ENTER}, a click
// — a fixed beat isn't enough (the Start menu must actually be open before
// the next action types into it, or the keystrokes are dropped), so the
// executor waits for the screen to settle instead (see settlesAfter),
// bounded by these (env-overridable, milliseconds):
//
//	ACTION_SETTLE_INITIAL_MS, ACTION_SETTLE_POLL_MS, ACTION_SETTLE_MAX_MS
const interActionDelay = 120 * time.Millisecond

var (
	actionSettleInitial = envDuration("ACTION_SETTLE_INITIAL_MS", 150*time.Millisecond)
	actionSettlePoll    = envDuration("ACTION_SETTLE_POLL_MS", 150*time.Millisecond)
	actionSettleMax     = envDuration("ACTION_SETTLE_MAX_MS", 2500*time.Millisecond)
)

// maxWaitAction caps a WAIT the model asks for.
const maxWaitAction = 10 * time.Second

// Screen-settle timings for the ADVANCE screenshot. Every screenshot that
// drives the next step waits for the screen to stop changing first, so the
// AI never reasons about a half-loaded page. All three are overridable by
// env var (milliseconds) for slow or fast machines/networks:
//
//	SETTLE_INITIAL_MS  minimum wait before checking (lets a load begin)
//	SETTLE_POLL_MS     how often to re-check for stability
//	SETTLE_MAX_MS      hard cap so a constantly-animating screen can't hang
var (
	settleInitial = envDuration("SETTLE_INITIAL_MS", 400*time.Millisecond)
	settlePoll    = envDuration("SETTLE_POLL_MS", 250*time.Millisecond)
	settleMax     = envDuration("SETTLE_MAX_MS", 6000*time.Millisecond)
)

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return def
}

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
	// lastSeq is the Seq of the last state this executor acted on (ran its
	// actions / sent its ADVANCE). Seeing the same Seq again — after a
	// resume, or the state the server re-sends on reconnect — means "you
	// already did this; the ADVANCE may have been dropped": send the
	// ADVANCE again, but never replay the actions.
	lastSeq := 0
	resentCancel := -1

	for change := range changes {
		cur := change.Cur

		if cur.Status == "NONE" {
			// The server has reset the task (cancelled from anywhere, or a
			// server restart): a local hotkey cancel has done its job.
			clearLocalCancel()
		}

		overlay <- overlayStateFor(cur)

		if cur.Status != "RUNNING" {
			continue
		}

		// Cancelled from the laptop with the hotkey: do nothing more for
		// this task, even if the server hasn't heard yet (offline phone or
		// server). Once connected again, make sure the server knows.
		if cancelledLocally(cur.Seq) {
			if resentCancel != cur.Seq {
				resentCancel = cur.Seq
				log.Printf("executor: task seq=%d was cancelled with the hotkey — telling the server again", cur.Seq)
				go SendAction(state, Action{Type: "CANCEL_TASK", DeviceID: deviceID})
			}
			pushOverlay(overlay, OverlayState{Visible: false})
			continue
		}

		// A newer state has already arrived behind this one (e.g. a cancel
		// or a pause landed while the previous batch was running): acting
		// on this one would replay stale clicks. Skip it; the newer state
		// is already queued behind it.
		if latest := state.LastTask(); latest.Seq != cur.Seq || latest.Status != "RUNNING" {
			log.Printf("executor: skipping stale state seq=%d (newest is seq=%d, %s)", cur.Seq, latest.Seq, latest.Status)
			continue
		}

		if cur.Seq < lastSeq {
			// The server restarted and its sequence started over.
			lastSeq = 0
		}
		if cur.Seq != 0 && cur.Seq == lastSeq {
			log.Printf("executor: seq=%d already handled — re-sending ADVANCE without replaying actions", cur.Seq)
			sendAdvance(state, deviceID, cur.Context, fsQuery(cur), fsStore, "", cur.Seq, "")
			continue
		}
		lastSeq = cur.Seq

		if len(cur.ExecutionList) > 0 {
			// Refuse the whole list up front if any action can't be
			// carried out (bad coordinates, unknown type): half-running
			// a batch leaves the screen in a state nobody planned for.
			// The error goes back to the server, which revises the plan.
			execs, err := toScreenExecutions(cur.ExecutionList, state.Frame())
			if err != nil {
				log.Printf("executor: refusing execution list: %v", err)
				sendAdvance(state, deviceID, false, "", fsStore, err.Error(), cur.Seq, "")
				continue
			}
			if !executeList(execs, overlay, overlayStateFor(cur), cur.Seq) {
				continue // cancelled with the hotkey mid-batch
			}
			sendAdvance(state, deviceID, false, "", fsStore, "", cur.Seq, clipboardAfterCopy(cur, execs))
			continue
		}

		// A brand-new task: screenshots and clicks only cover the primary
		// monitor, so bring the window the user is working in onto it
		// before the first screenshot is taken.
		if cur.Context && len(cur.InstructionList) == 0 && cur.Reason == "" {
			ensureForegroundOnPrimary()
		}

		// Our turn to ask for the next step. The filesystem snapshot is
		// attached whenever Context is true (a plan or a revise is next).
		sendAdvance(state, deviceID, cur.Context, fsQuery(cur), fsStore, "", cur.Seq, "")
	}
}

// clipboardAfterCopy reports what the clipboard holds after a batch that
// copied something (Ctrl+C / Ctrl+X, or a step about copying — e.g. a
// right-click "Copy"), so the executor can check the copy really worked:
// the clipboard isn't visible on screen. Only then — the clipboard isn't
// sent anywhere otherwise. Truncated; "" when nothing was copied.
func clipboardAfterCopy(t Task, execs []Execution) string {
	copied := false
	for _, e := range execs {
		k := strings.ToUpper(e.KeyString)
		if e.Type == "KEYBOARD_INPUT" && (strings.Contains(k, "{CTRL+C}") || strings.Contains(k, "{CTRL+X}") ||
			strings.Contains(k, "{CTRL+INSERT}")) {
			copied = true
		}
	}
	for _, i := range []int{t.CurrentInstructionIndex - 1, t.CurrentInstructionIndex} {
		if i >= 0 && i < len(t.InstructionList) {
			step := strings.ToLower(t.InstructionList[i])
			if strings.Contains(step, "copy") || strings.Contains(step, "cut ") {
				copied = true
			}
		}
	}
	if !copied {
		return ""
	}
	time.Sleep(150 * time.Millisecond) // let the app finish writing it
	text, ok, err := readClipboardText()
	switch {
	case err != nil:
		return "(couldn't read the clipboard)"
	case !ok || strings.TrimSpace(text) == "":
		return "(empty, or not text)"
	}
	const maxLen = 300
	if r := []rune(text); len(r) > maxLen {
		text = string(r[:maxLen]) + "…"
	}
	log.Printf("executor: after a copy the clipboard holds %d characters", len([]rune(text)))
	return text
}

// fsQuery is the text the filesystem snapshot is filtered against: the
// task, plus why the plan is being revised and what the user answered.
func fsQuery(t Task) string {
	return strings.Join(append([]string{t.Description, t.Reason}, t.Answers...), " ")
}

// toScreenExecutions checks every action can actually be performed here —
// a known type, and mouse coordinates on the screenshot the AI saw — and
// returns a copy with the coordinates scaled from that screenshot to real
// screen pixels (the screenshot is downscaled; see scaling.go).
func toScreenExecutions(execs []Execution, frame shotFrame) ([]Execution, error) {
	out := make([]Execution, len(execs))
	for i, e := range execs {
		switch e.Type {
		case "MOUSE_MOVEMENT":
			x, y, err := frame.toScreen(e.MousePosX, e.MousePosY)
			if err != nil {
				return nil, fmt.Errorf("action %d: %w", i+1, err)
			}
			e.MousePosX, e.MousePosY = x, y
		case "LEFT_CLICK", "RIGHT_CLICK", "KEYBOARD_INPUT", "WAIT":
		default:
			return nil, fmt.Errorf("action %d: unknown action type %q", i+1, e.Type)
		}
		out[i] = e
	}
	return out, nil
}

// sendAdvance captures the settled screen and sends ADVANCE. When includeFS
// is set it attaches the filtered filesystem snapshot, using query (the
// task text) to pick which entries are relevant. workerErr, if set, tells
// the server the last step couldn't be carried out here. seq is the
// Task.Seq this ADVANCE answers; the server ignores it once it has moved on.
func sendAdvance(state *State, deviceID string, includeFS bool, query string, fsStore *SnapshotStore, workerErr string, seq int, clipboard string) {
	// Don't capture or advance while the human is driving the mouse.
	takeover.Gate()

	// Wait for the screen to settle before capturing, so the next step is
	// planned against a fully-rendered screen rather than a mid-load one.
	// A failed capture is retried once; if it fails again the server is
	// told (instead of the task silently stalling here forever).
	// Planner calls (includeFS) always get the downscaled image; executor
	// calls too, unless EXECUTOR_FULL_RES is set.
	fullRes := executorFullRes && !includeFS
	shot, err := CaptureStableScreen(settleInitial, settlePoll, settleMax, fullRes)
	if err != nil {
		log.Printf("executor: screenshot failed: %v — retrying once", err)
		time.Sleep(500 * time.Millisecond)
		shot, err = CaptureStableScreen(settleInitial, settlePoll, settleMax, fullRes)
	}
	if err != nil {
		log.Printf("executor: screenshot failed twice: %v — reporting it to the server", err)
		if workerErr == "" {
			workerErr = "taking a screenshot failed twice: " + err.Error()
		}
	} else {
		// The actions that come back are in this image's coordinates.
		state.SetFrame(shotFrame{
			imgW: int(shot.Width), imgH: int(shot.Height),
			screenW: int(shot.ScreenWidth), screenH: int(shot.ScreenHeight),
		})
		log.Printf("executor: screenshot %dx%d %s (%d KB) of a %dx%d screen",
			shot.Width, shot.Height, shot.Format, len(shot.Data)/1024, shot.ScreenWidth, shot.ScreenHeight)
	}

	action := Action{
		Type:              "ADVANCE",
		DeviceID:          deviceID,
		ScreenshotPayload: shot,
		Error:             workerErr,
		Seq:               seq,
		Clipboard:         clipboard,
	}
	if includeFS {
		snap := fsStore.Snapshot(query)
		action.FileSystemPayload = snap
		// Log the newest few entries so you can confirm, from the worker
		// log, that recently-created files (a just-taken screenshot, a fresh
		// download) are actually in the snapshot. If the file you asked
		// about isn't near the top here, it's a capture problem; if it IS
		// here but the AI still picks the wrong one, it's a model/prompt
		// problem — this line tells you which.
		log.Printf("executor: filesystem snapshot: %d entries (newest first)", len(snap))
		for i := 0; i < len(snap) && i < 8; i++ {
			log.Printf("executor:   [%d] mtime=%d %s", i, snap[i].ModTime, snap[i].Path)
		}
	}

	SendAction(state, action)
}

// executeList runs one instruction's worth of physical actions in order.
// Hold-state is local to a single list: per the planner's system prompt, a
// drag's press/move/release steps all arrive together in one ExecutionList,
// so there's no need to persist hold-state across network round trips.
//
// Before each physical action it pushes an overlay update carrying a live
// ActionText ("Typing ...", "Click (x,y)", ...) built on top of `base`
// (the step-level state for this instruction), so the panel shows the
// most granular thing possible: what the machine is doing at this instant.
//
// It returns false if the task was cancelled with the hotkey part-way
// (seq is the task state the list belongs to); the rest is not run.
func executeList(execs []Execution, overlay chan<- OverlayState, base OverlayState, seq int) bool {
	leftHeld := false
	rightHeld := false

	for i, e := range execs {
		// If the human has taken over the mouse, block here until it's clear
		// to proceed — so the worker never fights the person for the cursor.
		takeover.Gate()

		if cancelledLocally(seq) {
			log.Printf("executor: cancelled with the hotkey — stopping before action %d of %d", i+1, len(execs))
			if leftHeld {
				LeftUp()
			}
			if rightHeld {
				RightUp()
			}
			return false
		}

		if txt := actionText(e); txt != "" {
			st := base
			st.ActionText = txt
			pushOverlay(overlay, st)
		}

		switch e.Type {
		case "MOUSE_MOVEMENT":
			// Already scaled to screen pixels by toScreenExecutions.
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

		case "WAIT":
			d := time.Duration(e.Ms) * time.Millisecond
			d = min(max(d, 0), maxWaitAction)
			time.Sleep(d)

		default:
			// toScreenExecutions rejects these before anything runs.
			log.Printf("executor: unknown execution type %q", e.Type)
		}

		// Let the UI settle before the next action in this list. The last
		// action needs nothing here: the ADVANCE screenshot waits for the
		// screen to settle anyway.
		switch {
		case i == len(execs)-1:
		case settlesAfter(e):
			WaitForStableScreen(actionSettleInitial, actionSettlePoll, actionSettleMax)
		default:
			time.Sleep(interActionDelay)
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
	return true
}

// Local cancel (G1): the Ctrl+Alt+Shift+X hotkey stops the current task on
// this machine at once, without waiting for the server — which may be
// unreachable, or the phone offline. localCancelSeq holds the cancelled
// task state's Seq+1 (0 = nothing cancelled); every state up to it is
// treated as dead until the server resets the task.
var localCancelSeq atomic.Int64

// CancelLocally marks the task state with this Seq (and older) cancelled.
func CancelLocally(seq int) { localCancelSeq.Store(int64(seq) + 1) }

func cancelledLocally(seq int) bool {
	c := localCancelSeq.Load()
	return c > 0 && int64(seq) < c
}

func clearLocalCancel() { localCancelSeq.Store(0) }

// settlesAfter reports whether an action usually opens or changes
// something on screen ({WIN}, {ENTER}, a click or a drag's release), so the
// next action in the list should wait for the screen to settle rather
// than a fixed beat.
func settlesAfter(e Execution) bool {
	switch e.Type {
	case "LEFT_CLICK", "RIGHT_CLICK":
		return !e.MouseHold
	case "KEYBOARD_INPUT":
		k := strings.ToUpper(e.KeyString)
		return strings.Contains(k, "{WIN}") || strings.Contains(k, "{ENTER}") || strings.Contains(k, "{RETURN}") ||
			strings.HasSuffix(e.KeyString, "\n")
	}
	return false
}

// actionText renders one physical action as a short human-readable line for
// the overlay. Returns "" for actions with nothing worth announcing.
func actionText(e Execution) string {
	switch e.Type {
	case "MOUSE_MOVEMENT":
		return fmt.Sprintf("Move cursor → (%d, %d)", e.MousePosX, e.MousePosY)
	case "LEFT_CLICK":
		switch {
		case e.MouseHold:
			return "Left button down"
		default:
			return "Left click"
		}
	case "RIGHT_CLICK":
		switch {
		case e.MouseHold:
			return "Right button down"
		default:
			return "Right click"
		}
	case "KEYBOARD_INPUT":
		s := e.KeyString
		if len(s) > 24 {
			s = s[:24] + "…"
		}
		return fmt.Sprintf("Typing “%s”", s)
	case "WAIT":
		return fmt.Sprintf("Waiting %d ms", e.Ms)
	default:
		return ""
	}
}

// pushOverlay sends without ever blocking the executor: if the overlay
// channel is momentarily full, the intermediate action update is dropped
// (the next one, or the step-level update, will catch the panel up).
func pushOverlay(overlay chan<- OverlayState, st OverlayState) {
	select {
	case overlay <- st:
	default:
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
			Visible:         true,
			Status:          t.Status,
			TaskDescription: t.Description,
			StepText:        stepText,
			StepIndex:       stepIndex,
			StepTotal:       total,
		}

	case "NEEDS_INPUT":
		// The server is waiting on the user's answer (a blocker, a step to
		// approve, or a budget question). The worker idles until it comes.
		total := len(t.InstructionList)
		idx := t.CurrentInstructionIndex
		stepText := "Waiting for you on your phone"
		stepIndex := 0
		if idx >= 0 && idx < total {
			stepText = "Waiting for you on your phone — " + t.InstructionList[idx]
			stepIndex = idx + 1
		}
		return OverlayState{
			Visible:         true,
			Status:          "NEEDS_INPUT",
			TaskDescription: t.Description,
			StepText:        stepText,
			StepIndex:       stepIndex,
			StepTotal:       total,
		}

	case "COMPLETED":
		total := len(t.InstructionList)
		return OverlayState{
			Visible:         true,
			Status:          "COMPLETED",
			TaskDescription: t.Description,
			StepText:        "Task completed",
			StepIndex:       total,
			StepTotal:       total,
		}

	default: // CANCELLED, NONE
		return OverlayState{Visible: false}
	}
}
