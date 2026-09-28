package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/joho/godotenv"
)

// srvLogPrefix tags every log line this file emits, distinct from the
// "[ai] " prefix in ai.go, so you can grep either the transport/state
// layer or the AI call layer in isolation.
const srvLogPrefix = "[server] "

func srvLogf(format string, args ...any) {
	log.Printf(srvLogPrefix+format, args...)
}

// Handles HTTP POST requests to create new rooms
func handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		srvLogf("POST /rooms rejected: method %s not allowed", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RoomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		srvLogf("POST /rooms rejected: invalid JSON payload: %v", err)
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	if req.DeviceID == "" {
		srvLogf("POST /rooms rejected: empty device_id")
		http.Error(w, "ID and Name fields are required", http.StatusBadRequest)
		return
	}

	mutex.Lock()

	if _, exists := taskHashTable[req.DeviceID]; exists {
		// The worker re-registers on every boot and after a 404, so an
		// existing room is normal — as long as it's the same worker. A
		// different secret means someone else is claiming this device ID.
		// A room registered without a secret (a pre-auth worker) is
		// upgraded to the first secret that shows up.
		existing := roomSecrets[req.DeviceID]
		switch {
		case existing == "" && req.WorkerSecret != "":
			roomSecrets[req.DeviceID] = req.WorkerSecret
		case existing != "" && !secretsEqual(existing, req.WorkerSecret):
			mutex.Unlock()
			srvLogf("POST /rooms rejected: room for device=%s is owned by a different worker secret", req.DeviceID)
			http.Error(w, "Room is owned by another worker", http.StatusForbidden)
			return
		}
		mutex.Unlock()
		srvLogf("POST /rooms: room already exists for device=%s", req.DeviceID)
		http.Error(w, "Room already exists", http.StatusConflict)
		return
	}

	taskHashTable[req.DeviceID] = resetTask(req.DeviceID)
	roomSecrets[req.DeviceID] = req.WorkerSecret

	mutex.Unlock()

	if req.WorkerSecret == "" {
		srvLogf("WARNING: device=%s registered without a worker secret (old worker build) — it can only connect while CONTROL_SHARED_SECRET is unset", req.DeviceID)
	}

	srvLogf("POST /rooms: created room for device=%s", req.DeviceID)

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "Room successfully created for device '%s'", req.DeviceID)
}

// Handles WebSocket connections for a specific room
func handleWebSocketConnections(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("id")

	if taskID == "" {
		srvLogf("WS connection rejected: missing 'id' query parameter (remote=%s)", r.RemoteAddr)
		http.Error(w, "Missing 'id' query parameter", http.StatusBadRequest)
		return
	}

	mutex.Lock()

	// Make sure the room exists before accepting the connection
	currentTask, exists := taskHashTable[taskID]

	if !exists {
		mutex.Unlock()
		srvLogf("WS connection rejected: room does not exist for device=%s (remote=%s)", taskID, r.RemoteAddr)
		http.Error(
			w,
			"Room does not exist. Create it via POST /rooms first.",
			http.StatusNotFound,
		)
		return
	}

	// Who is this? The worker proves the room's secret; a web client
	// proves a fresh token minted by the Next.js app. Anyone else is
	// turned away before the upgrade.
	role, ok := authenticateConnection(r, taskID, roomSecrets[taskID])
	if !ok {
		mutex.Unlock()
		srvLogf("WS connection rejected: unauthorized for device=%s (remote=%s)", taskID, r.RemoteAddr)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Register the client
	if rooms[taskID] == nil {
		rooms[taskID] = make(map[*websocket.Conn]bool)
	}

	// Accept the WebSocket connection. Browsers always send an Origin
	// header, so only the web app (and local dev) may open a socket from a
	// page; this stops some other site from driving the computer through a
	// visitor's browser. The Go worker sends no Origin header at all, which
	// the library accepts without checking.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"control.aniekan.dev", "localhost:*"},
	})

	if err != nil {
		mutex.Unlock()
		srvLogf("WS connection FAILED to accept for device=%s: %v", taskID, err)
		return
	}

	// The default coder/websocket read limit is 32 KiB, but an ADVANCE
	// carries a full-screen PNG screenshot (base64-inflated inside JSON),
	// which is routinely hundreds of KiB to a few MiB. Without raising
	// this, the server closes the connection with StatusMessageTooBig the
	// instant the worker sends its first real ADVANCE — which looked like
	// a mysterious reconnect loop on the worker side. 256 MiB is a safety
	// ceiling, not an allocation.
	conn.SetReadLimit(256 << 20)

	rooms[taskID][conn] = true
	clientCount := len(rooms[taskID])

	mutex.Unlock()

	srvLogf("WS connected: device=%s remote=%s role=%s clientsInRoom=%d currentStatus=%s",
		taskID, r.RemoteAddr, role, clientCount, currentTask.Status)

	defer conn.Close(websocket.StatusInternalError, "connection closing")

	// Send current state to the newly connected client
	sendTaskState(r.Context(), conn, currentTask)

	// Listen for incoming actions
	for {
		_, msg, err := conn.Read(r.Context())

		if err != nil {
			mutex.Lock()

			delete(rooms[taskID], conn)
			remaining := len(rooms[taskID])

			if remaining == 0 {
				delete(rooms, taskID)
			}

			mutex.Unlock()
			srvLogf("WS disconnected: device=%s remote=%s reason=%v remainingClientsInRoom=%d",
				taskID, r.RemoteAddr, err, remaining)
			break
		}

		var action Action

		if err := json.Unmarshal(msg, &action); err != nil {
			srvLogf("device=%s: invalid JSON payload from client: %v (raw: %s)", taskID, err, string(msg))
			continue
		}

		srvLogf("device=%s: received action type=%s", taskID, action.Type)

		if !roleMayAct(role, action.Type) {
			srvLogf("device=%s: ignoring %s from a %s connection (not allowed for that role)", taskID, action.Type, role)
			continue
		}

		// Resolve which OpenRouter key a new task is billed to before
		// taking the lock: it's a database round trip.
		var newKey apiKey
		var newKeyErr error
		if action.Type == "CREATE_TASK" {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			newKey, newKeyErr = resolveAPIKey(ctx, taskID)
			cancel()
		}

		mutex.Lock()

		task := taskHashTable[taskID]
		prevStatus := task.Status

		// A one-off error is shown until the next accepted client action.
		switch action.Type {
		case "CREATE_TASK", "PAUSE_TASK", "RESUME_TASK", "CANCEL_TASK", "ANSWER":
			task.LastError = ""
		}

		switch action.Type {
		case "CREATE_TASK":
			// Every task needs a key to bill: the user's own, or the
			// server's for an owner (OWNER_USER_IDS). Without one the task
			// is refused and the client is told what to do.
			if newKeyErr != nil {
				if errors.Is(newKeyErr, errNoKey) {
					task.LastError = errNoKey.Error()
				} else {
					task.LastError = "Couldn't look up your OpenRouter key. Try again."
				}
				srvLogf("device=%s: CREATE_TASK refused, no usable OpenRouter key: %v", taskID, newKeyErr)
				break
			}

			// The monthly budget protects the server's own OpenRouter key:
			// once it's spent, new tasks on it are refused (the running
			// one, if any, is left alone) and the client is told why.
			// Users' own keys are theirs to limit.
			if spent := costs.serverMonthTotal(); newKey.serverKey && spent >= limits.MonthlyBudgetUSD {
				task.LastError = "Monthly AI budget reached"
				srvLogf("device=%s: CREATE_TASK refused, monthly budget reached ($%.4f of $%.2f)",
					taskID, spent, limits.MonthlyBudgetUSD)
				break
			}
			taskKeys[taskID] = newKey

			// Full reset, not just overwrite: a device that ran a previous
			// task (completed, paused, or otherwise not explicitly
			// CANCEL_TASK'd) still has that task's leftover
			// CurrentInstructionIndex/InstructionList/ExecutionList sitting
			// in taskHashTable — CREATE_TASK used to only touch
			// Description/Context/Status, so a stale non-empty
			// ExecutionList would survive into the new task. The worker's
			// executor treats "ExecutionList non-empty" as "re-run these
			// actions" unconditionally, so it would replay the OLD task's
			// last batch of clicks/keystrokes instead of sending the
			// ADVANCE that starts planning the NEW task — which looks
			// exactly like "ADVANCE is never sent" from the server's side,
			// since the worker is busy doing something else instead.
			task = Task{
				DeviceID:                taskID,
				Description:             action.Description,
				Status:                  "RUNNING",
				CurrentInstructionIndex: 0,
				InstructionList:         make([]string, 0),
				ExecutionList:           make([]Execution, 0),
				Context:                 true,
				Answers:                 make([]string, 0),
				ConfirmedIndex:          -1,
				NeedsConfirm:            make([]bool, 0),
				StartedAt:               time.Now(),
			}
			srvLogf("device=%s: CREATE_TASK description=%q (full state reset)", taskID, action.Description)

		case "ADVANCE":
			srvLogf("device=%s: ADVANCE received, context=%v currentInstrIdx=%d/%d screenshotBytes=%d fsEntries=%d",
				taskID, task.Context, task.CurrentInstructionIndex, len(task.InstructionList),
				len(action.ScreenshotPayload.Data), len(action.FileSystemPayload))

			// The worker couldn't do what it was told (the screenshot
			// failed twice, coordinates off the screen, an action type it
			// doesn't know). That's the screen not matching the plan, as
			// far as recovery goes: revise it, no executor call.
			if action.Error != "" {
				srvLogf("device=%s: worker reported an error, replanning: %s", taskID, action.Error)
				task = requestReplan(task, "The computer couldn't carry out the last step: "+action.Error,
					screenshotDataURL(action.ScreenshotPayload))
				break
			}

			// If the worker has just finished the final execution,
			// this ADVANCE means the task is now actually complete.
			// Do not make another AI call.
			if !task.Context && task.CurrentInstructionIndex >= len(task.InstructionList) {
				task.Status = "COMPLETED"
				task.ExecutionList = make([]Execution, 0)

				srvLogf("device=%s: all instructions executed, task COMPLETED", taskID)

				taskHashTable[taskID] = task

				clients := make([]*websocket.Conn, 0, len(rooms[taskID]))
				for client := range rooms[taskID] {
					clients = append(clients, client)
				}

				mutex.Unlock()

				broadcastToTaskRoom(r.Context(), taskID, task, clients)
				continue
			}

			// Per-task caps: a runaway task (looping, or just long) stops
			// spending before the next call. Hitting a cap doesn't fail the
			// task — it becomes a budget question, and answering it grants
			// a fresh window of calls/spend/time.
			if reason := taskCapReached(task, time.Now()); reason != "" {
				srvLogf("device=%s: per-task cap reached, asking the user: %s", taskID, reason)
				task = askUser(task, "budget", reason, screenshotDataURL(action.ScreenshotPayload))
				break
			}

			// A step that still isn't done after maxExecutorCallsPerStep
			// executor calls won't be fixed by a fourth: the plan is off.
			if !task.Context && task.InstrAttempts >= maxExecutorCallsPerStep {
				srvLogf("device=%s: instruction %d not done after %d executor calls, replanning",
					taskID, task.CurrentInstructionIndex, task.InstrAttempts)
				task = requestReplan(task, fmt.Sprintf("Step %d (%q) still wasn't done after %d attempts.",
					task.CurrentInstructionIndex+1, task.InstructionList[task.CurrentInstructionIndex], task.InstrAttempts),
					screenshotDataURL(action.ScreenshotPayload))
				break
			}

			key, haveKey := taskKeys[taskID]

			// Release the lock before the network call so a slow request
			// (or our retries) don't block every other room's messages
			// from being processed while we wait on the AI.
			mutex.Unlock()

			if !haveKey {
				// Only possible for a task created before the server knew
				// about keys; look it up now rather than failing the task.
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
				key, _ = resolveAPIKey(ctx, taskID)
				cancel()
			}

			var result any
			var sendErr error
			var callCost float64
			wasPlanning := task.Context

			const maxRetries = 3
			for attempt := 1; attempt <= maxRetries; attempt++ {
				callStart := time.Now()
				var attemptCost float64
				result, attemptCost, sendErr = sendMessage(task, action, key)
				callCost += attemptCost
				callElapsed := time.Since(callStart)

				if sendErr == nil {
					srvLogf("device=%s: sendMessage attempt %d/%d succeeded in %s",
						taskID, attempt, maxRetries, callElapsed)
					break
				}
				srvLogf("device=%s: sendMessage attempt %d/%d FAILED after %s: %v",
					taskID, attempt, maxRetries, callElapsed, sendErr)
				if attempt < maxRetries {
					backoff := time.Duration(attempt) * time.Second
					srvLogf("device=%s: retrying in %s", taskID, backoff)
					time.Sleep(backoff) // 1s, 2s backoff
				}
			}

			mutex.Lock()

			// Re-read current state: another message (PAUSE/CANCEL/another
			// ADVANCE from a different client in the room) may have landed
			// while we were waiting on the network call.
			task = taskHashTable[taskID]

			// Charge the call to the task, unless it was cancelled while the
			// call was in flight (a reset task starts again from $0).
			if task.Status != "NONE" {
				task.CostUSD += callCost
				task.AICalls++
				if wasPlanning {
					task.PlannerCalls++
				}
			}

			switch {
			case sendErr != nil:
				// Never remaking a room, so give up by resetting the task
				// to a clean slate instead of deleting it from the table.
				srvLogf("device=%s: giving up after %d attempts, resetting task to NONE: %v",
					taskID, maxRetries, sendErr)
				task = resetTask(task.DeviceID)

			case task.Status != "RUNNING":
				// Task was paused/cancelled/reset while this request was
				// in flight — drop the now-stale result.
				srvLogf("device=%s: dropping stale ADVANCE result, status changed to %q while AI call was in flight",
					taskID, task.Status)

			case task.Context:
				// Planning call: result is the instruction list.
				instructions, ok := result.([]string)
				if !ok {
					srvLogf("device=%s: expected []string from planning call, got %T (value: %+v) — resetting task",
						taskID, result, result)
					task = resetTask(task.DeviceID)
					break
				}
				task.InstructionList = instructions
				task.CurrentInstructionIndex = 0
				task.Context = false // next ADVANCE generates executions, not a new plan
				task.Reason = ""
				task.InstrAttempts = 0
				srvLogf("device=%s: plan set, %d instructions: %v", taskID, len(instructions), instructions)

				// An empty plan means there's nothing to execute. Mark the
				// task COMPLETED instead of leaving it RUNNING with 0
				// instructions — otherwise the next ADVANCE asks for
				// instruction 0 of a 0-length list and fails ("index out of
				// range") on a loop until the retries give up.
				if len(instructions) == 0 {
					task.Status = "COMPLETED"
					srvLogf("device=%s: planner returned an empty plan, marking COMPLETED", taskID)
				}

			default:
				// Execution call: result is the executor's verdict on the
				// screen for the current instruction.
				res, ok := result.(ExecResult)
				if !ok {
					srvLogf("device=%s: expected ExecResult from execution call, got %T (value: %+v) — resetting task",
						taskID, result, result)
					task = resetTask(task.DeviceID)
					break
				}
				task = applyExecResult(task, res, screenshotDataURL(action.ScreenshotPayload))
			}

		case "PAUSE_TASK":
			task.Status = "PAUSED"
			srvLogf("device=%s: PAUSE_TASK", taskID)
		case "RESUME_TASK":
			task.Status = "RUNNING"
			srvLogf("device=%s: RESUME_TASK", taskID)
		case "ANSWER":
			if task.Status != "NEEDS_INPUT" {
				srvLogf("device=%s: ignoring ANSWER, task is %s (not waiting on a question)", taskID, task.Status)
				mutex.Unlock()
				continue
			}
			answer := strings.TrimSpace(action.Description)
			srvLogf("device=%s: ANSWER to %s question %q: %q", taskID, task.QuestionKind, task.Question, answer)
			task = applyAnswer(task, answer, time.Now())
		case "CANCEL_TASK":
			task = resetTask(task.DeviceID)
			srvLogf("device=%s: CANCEL_TASK, task reset", taskID)
		default:
			srvLogf("device=%s: unknown action type: %q", taskID, action.Type)
			mutex.Unlock()
			continue
		}

		if task.Status != prevStatus {
			srvLogf("device=%s: status transition %s -> %s", taskID, prevStatus, task.Status)
		}

		taskHashTable[taskID] = task

		// Copy the clients while we have the lock.
		clients := make([]*websocket.Conn, 0, len(rooms[taskID]))

		for client := range rooms[taskID] {
			clients = append(clients, client)
		}

		mutex.Unlock()

		// Network I/O happens AFTER releasing the mutex.
		broadcastToTaskRoom(r.Context(), taskID, task, clients)
	}
}

// maxExecutorCallsPerStep caps executor calls on one instruction (an "act"
// with instruction_done=false asks for another look); past it, replan.
const maxExecutorCallsPerStep = 3

// applyExecResult applies the executor's verdict for the current step:
//   - act:     hand the worker the actions; move to the next step only if
//     the executor says they finish this one (otherwise the next ADVANCE
//     gets another executor call on the same step)
//   - skip:    the step is already done — next step, nothing to run (the
//     worker sees an empty list and just sends a plain ADVANCE)
//   - replan:  off-plan but achievable — revise the plan
//   - blocked: can't be done as asked — ask the user
//
// image is the screenshot the executor judged, as a data URL, shown with
// any question that results.
func applyExecResult(t Task, res ExecResult, image string) Task {
	switch res.Verdict {
	case verdictAct:
		t.ExecutionList = res.Actions
		if res.InstructionDone {
			t.CurrentInstructionIndex++
			t.InstrAttempts = 0
		} else {
			t.InstrAttempts++
		}
		srvLogf("device=%s: executor act: %d actions for instruction %d/%d (done=%v): %+v",
			t.DeviceID, len(res.Actions), t.CurrentInstructionIndex, len(t.InstructionList), res.InstructionDone, res.Actions)

	case verdictSkip:
		t.ExecutionList = make([]Execution, 0)
		t.CurrentInstructionIndex++
		t.InstrAttempts = 0
		srvLogf("device=%s: executor skip: instruction already done on screen, now %d/%d",
			t.DeviceID, t.CurrentInstructionIndex, len(t.InstructionList))

	case verdictReplan:
		srvLogf("device=%s: executor replan: %s", t.DeviceID, res.Reason)
		t = requestReplan(t, res.Reason, image)

	case verdictBlocked:
		srvLogf("device=%s: executor blocked: %s", t.DeviceID, res.Reason)
		t = askUser(t, "blocked", res.Reason, image)
	}
	return t
}

// requestReplan asks the worker for fresh context so the planner can
// revise the plan: RUNNING with an empty list and Context=true makes the
// worker send a context ADVANCE (screenshot + filesystem), and the planner
// then revises. Automatic revises are capped (MAX_AUTO_REPLANS): past the
// cap the system is going in circles, so it asks the user instead.
func requestReplan(t Task, reason, image string) Task {
	if t.AutoReplans+1 > limits.MaxAutoReplans {
		srvLogf("device=%s: %d automatic revises already, asking the user instead", t.DeviceID, t.AutoReplans)
		return askUser(t, "blocked", fmt.Sprintf(
			"I've tried to recover %d times and I'm still stuck: %s What should I do?",
			t.AutoReplans, sentence(reason)), image)
	}
	t.AutoReplans++
	t.Context = true
	t.ExecutionList = make([]Execution, 0)
	t.Reason = reason
	t.InstrAttempts = 0
	t.Status = "RUNNING"
	return t
}

// sentence trims s and makes sure it ends like a sentence.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s != "" && !strings.ContainsAny(s[len(s)-1:], ".!?") {
		s += "."
	}
	return s
}

// askUser parks the task on a question for the user. The worker idles
// (Status != RUNNING) until an answer or a cancel arrives. image is the
// screen the question is about, as a data URL ("" if there's none).
func askUser(t Task, kind, question, image string) Task {
	t.Status = "NEEDS_INPUT"
	t.Question = question
	t.QuestionKind = kind
	t.QuestionImage = image
	t.ExecutionList = make([]Execution, 0)
	return t
}

// applyAnswer resumes a NEEDS_INPUT task with the user's answer:
//   - budget: any answer means "continue" — the per-task counters start a
//     fresh window and the task carries on where it was, no planner call
//     (Context keeps its value, so a plan that was about to be made still
//     gets made)
//   - blocked (and anything else): the answer is recorded and the planner
//     revises the plan with it — Reason set, Context=true, so the worker
//     sends a context ADVANCE. This doesn't count as an automatic revise.
func applyAnswer(t Task, answer string, now time.Time) Task {
	kind := t.QuestionKind
	question := t.Question

	t.Question = ""
	t.QuestionKind = ""
	t.QuestionImage = ""
	t.ExecutionList = make([]Execution, 0)
	t.Status = "RUNNING"

	switch kind {
	case "budget":
		t.AICalls = 0
		t.PlannerCalls = 0
		t.CostBase = t.CostUSD
		t.StartedAt = now
		srvLogf("device=%s: per-task caps reset after the user chose to continue", t.DeviceID)

	default:
		t.Answers = append(t.Answers, fmt.Sprintf("Q: %s / A: %s", question, answer))
		t.Reason = "User answered: " + answer
		t.Context = true
		t.InstrAttempts = 0
	}
	return t
}

// screenshotDataURL renders a screenshot as a data: URL for question_image.
func screenshotDataURL(shot Screenshot) string {
	if len(shot.Data) == 0 {
		return ""
	}
	return "data:image/" + shot.Format + ";base64," + base64.StdEncoding.EncodeToString(shot.Data)
}

// taskCapReached returns a user-facing explanation when the task has hit
// one of the per-task caps (calls, planner calls, spend, running time), or
// "" when it may make another AI call. The planner cap only applies when
// the next call is a planner call (Context == true).
func taskCapReached(t Task, now time.Time) string {
	spent := t.CostUSD - t.CostBase
	used := fmt.Sprintf("This task has used %d AI calls / $%.3f", t.AICalls, t.CostUSD)
	switch {
	case t.AICalls >= limits.MaxAICallsPerTask:
		return used + fmt.Sprintf(" (limit %d calls). Continue?", limits.MaxAICallsPerTask)
	case t.Context && t.PlannerCalls >= limits.MaxPlannerCallsPerTask:
		return used + fmt.Sprintf(" and planned %d times (limit %d). Continue?", t.PlannerCalls, limits.MaxPlannerCallsPerTask)
	case spent >= limits.MaxTaskCostUSD:
		return used + fmt.Sprintf(" (limit $%.2f). Continue?", limits.MaxTaskCostUSD)
	case !t.StartedAt.IsZero() && now.Sub(t.StartedAt) >= limits.MaxTaskDuration:
		return used + fmt.Sprintf(" and has run for %s (limit %s). Continue?",
			now.Sub(t.StartedAt).Round(time.Second), limits.MaxTaskDuration)
	}
	return ""
}

// resetTask returns a fresh, empty task for the given device — the same
// shape CANCEL_TASK produces. Used both for explicit cancellation and for
// giving up on a task after repeated ADVANCE failures: the room's entry in
// taskHashTable stays put (rooms are never recreated), it just goes back
// to a clean NONE state.
func resetTask(deviceID string) Task {
	return Task{
		DeviceID:                deviceID,
		Description:             "",
		Status:                  "NONE",
		CurrentInstructionIndex: 0,
		InstructionList:         make([]string, 0),
		ExecutionList:           make([]Execution, 0),
		Context:                 false,
		Answers:                 make([]string, 0),
		ConfirmedIndex:          -1,
		NeedsConfirm:            make([]bool, 0),
	}
}

func sendTaskState(ctx context.Context, conn *websocket.Conn, task Task) {
	update := StateUpdate{
		Type:    "TASK_UPDATE",
		Payload: task,
	}

	msg, err := json.Marshal(update)

	if err != nil {
		srvLogf("FAILED to marshal state for device=%s: %v", task.DeviceID, err)
		return
	}

	if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
		srvLogf("FAILED to send initial state to device=%s: %v", task.DeviceID, err)
	}
}

func broadcastToTaskRoom(
	ctx context.Context,
	taskID string,
	task Task,
	clients []*websocket.Conn,
) {
	update := StateUpdate{
		Type:    "TASK_UPDATE",
		Payload: task,
	}

	msg, err := json.Marshal(update)

	if err != nil {
		srvLogf("FAILED to marshal broadcast for device=%s: %v", taskID, err)
		return
	}

	srvLogf("device=%s: broadcasting status=%s to %d client(s)", taskID, task.Status, len(clients))

	for _, client := range clients {
		if err := client.Write(ctx, websocket.MessageText, msg); err != nil {
			srvLogf("device=%s: FAILED to send update to a client, dropping it: %v", taskID, err)

			mutex.Lock()

			delete(rooms[taskID], client)

			if len(rooms[taskID]) == 0 {
				delete(rooms, taskID)
			}

			mutex.Unlock()

			client.Close(
				websocket.StatusAbnormalClosure,
				"write failed",
			)
		}
	}
}

func main() {
	err := godotenv.Load()
	if err != nil {
		// Downgraded from log.Fatal: a missing .env file shouldn't be a
		// hard crash if OPENROUTER_API_KEY etc. are already set some other
		// way (real environment variables, a deployment platform's secret
		// injection, ...). It's still logged loudly, because "the AI calls
		// are all failing with 401" is a much more confusing symptom than
		// this one line would have been.
		srvLogf("WARNING: could not load .env file: %v (continuing — this is only fatal if OPENROUTER_API_KEY etc. also aren't set some other way)", err)
	} else {
		srvLogf(".env file loaded")
	}

	limits = loadLimits()
	srvLogf("limits: %d AI calls, %d planner calls, $%.2f and %s per task; $%.2f/month on the server key",
		limits.MaxAICallsPerTask, limits.MaxPlannerCallsPerTask, limits.MaxTaskCostUSD,
		limits.MaxTaskDuration, limits.MonthlyBudgetUSD)

	costStateFile := os.Getenv("COST_STATE_FILE")
	if costStateFile == "" {
		costStateFile = "cost-state.json"
	}
	costs = loadCostTracker(costStateFile)

	initDB()

	if sharedSecret() == "" {
		srvLogf("WARNING: CONTROL_SHARED_SECRET is not set — /ws connections are NOT authenticated. Anyone who knows a device ID can drive that computer. Set it (same value in the Next.js app) to turn auth on.")
	}

	// HTTP REST endpoint
	http.HandleFunc("/rooms", handleCreateRoom)

	// WebSocket endpoint
	http.HandleFunc("/ws", handleWebSocketConnections)

	fmt.Println(
		"Server running",
	)
	srvLogf("listening on :8080")

	log.Fatal(http.ListenAndServe(":8080", nil))
}

// IN-MEMORY DATA STORAGE
var (
	// in memory hash table
	taskHashTable = map[string]Task{}

	// Tracks connected clients grouped by Task ID
	rooms = make(map[string]map[*websocket.Conn]bool)

	// The secret each room's worker registered with (POST /rooms). Kept out
	// of Task, which is broadcast to every client in full.
	roomSecrets = map[string]string{}

	// The OpenRouter key each device's current task is billed to (see
	// keys.go). Kept out of Task for the same reason.
	taskKeys = map[string]apiKey{}

	mutex sync.Mutex
)
