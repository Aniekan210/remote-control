package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
		mutex.Unlock()
		srvLogf("POST /rooms: room already exists for device=%s", req.DeviceID)
		http.Error(w, "Room already exists", http.StatusConflict)
		return
	}

	taskHashTable[req.DeviceID] = resetTask(req.DeviceID)

	mutex.Unlock()

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

	// Register the client
	if rooms[taskID] == nil {
		rooms[taskID] = make(map[*websocket.Conn]bool)
	}

	// Accept the WebSocket connection
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})

	if err != nil {
		mutex.Unlock()
		srvLogf("WS connection FAILED to accept for device=%s: %v", taskID, err)
		return
	}

	rooms[taskID][conn] = true
	clientCount := len(rooms[taskID])

	mutex.Unlock()

	srvLogf("WS connected: device=%s remote=%s clientsInRoom=%d currentStatus=%s",
		taskID, r.RemoteAddr, clientCount, currentTask.Status)

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

		mutex.Lock()

		task := taskHashTable[taskID]
		prevStatus := task.Status

		switch action.Type {
		case "CREATE_TASK":
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
			}
			srvLogf("device=%s: CREATE_TASK description=%q (full state reset)", taskID, action.Description)

		case "ADVANCE":
			srvLogf("device=%s: ADVANCE received, context=%v currentInstrIdx=%d/%d screenshotBytes=%d fsEntries=%d",
				taskID, task.Context, task.CurrentInstructionIndex, len(task.InstructionList),
				len(action.ScreenshotPayload.Data), len(action.FileSystemPayload))

			// Release the lock before the network call so a slow request
			// (or our retries) don't block every other room's messages
			// from being processed while we wait on the AI.
			mutex.Unlock()

			var result any
			var sendErr error

			const maxRetries = 3
			for attempt := 1; attempt <= maxRetries; attempt++ {
				callStart := time.Now()
				result, sendErr = sendMessage(task, action)
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
				srvLogf("device=%s: plan set, %d instructions: %v", taskID, len(instructions), instructions)

			default:
				// Execution call: result is the execution list for the
				// current instruction.
				executions, ok := result.([]Execution)
				if !ok {
					srvLogf("device=%s: expected []Execution from execution call, got %T (value: %+v) — resetting task",
						taskID, result, result)
					task = resetTask(task.DeviceID)
					break
				}
				task.ExecutionList = executions
				task.CurrentInstructionIndex++
				srvLogf("device=%s: execution list set (%d actions) for instruction %d/%d: %+v",
					taskID, len(executions), task.CurrentInstructionIndex, len(task.InstructionList), executions)

				if task.CurrentInstructionIndex >= len(task.InstructionList) {
					task.Status = "COMPLETED"
					srvLogf("device=%s: all instructions done, task COMPLETED", taskID)
				}
			}

		case "PAUSE_TASK":
			task.Status = "PAUSED"
			srvLogf("device=%s: PAUSE_TASK", taskID)
		case "RESUME_TASK":
			task.Status = "RUNNING"
			srvLogf("device=%s: RESUME_TASK", taskID)
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

	// HTTP REST endpoint
	http.HandleFunc("/rooms", handleCreateRoom)

	// WebSocket endpoint
	http.HandleFunc("/ws", handleWebSocketConnections)

	fmt.Println(
		"Server running. API: POST http://localhost:8080/rooms | WS: ws://localhost:8080/ws?id=<id>",
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

	mutex sync.Mutex
)
