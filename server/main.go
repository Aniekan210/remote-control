package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
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

		action.ReceivedAt = time.Now()

		// Anything needing I/O is worked out before taking the lock, and
		// handed to the (pure) state machine inside the action.
		var newKey apiKey
		if action.Type == "CREATE_TASK" {
			newKey, action.Refused = checkCanStart(r.Context(), taskID)
		}

		mutex.Lock()

		prev := taskHashTable[taskID]
		task := onClientAction(prev, action)
		startCall := task.InFlight && !prev.InFlight
		changed := visibleChange(prev, task)

		if action.Type == "CREATE_TASK" && action.Refused == "" {
			taskKeys[taskID] = newKey
		}
		if task.Status != prev.Status {
			srvLogf("device=%s: status transition %s -> %s", taskID, prev.Status, task.Status)
		}

		taskHashTable[taskID] = task
		clients := roomClients(taskID)

		mutex.Unlock()

		// Network I/O happens AFTER releasing the mutex.
		if changed {
			broadcastToTaskRoom(r.Context(), taskID, task, clients)
		}

		// The AI call runs on its own goroutine, so this connection keeps
		// reading while it's in flight — a PAUSE_TASK from the worker (the
		// person grabbed the mouse) lands immediately instead of queueing
		// behind a call that can take a minute with retries.
		if startCall {
			go runAICall(taskID, task, action)
		}
	}
}

// checkCanStart decides whether a new task may start on this device: it
// returns the key the task will be billed to, or why it can't start (shown
// to the user as last_error). Every task needs a key — the user's own, or
// the server's for an owner (OWNER_USER_IDS) — and the monthly budget
// protects the server's own key: once it's spent, new tasks on it are
// refused (a running one is left alone). Users' own keys are theirs to
// limit.
func checkCanStart(ctx context.Context, deviceID string) (apiKey, string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	key, err := resolveAPIKey(ctx, deviceID)
	if err != nil {
		srvLogf("device=%s: no usable OpenRouter key: %v", deviceID, err)
		if errors.Is(err, errNoKey) {
			return apiKey{}, errNoKey.Error()
		}
		return apiKey{}, "Couldn't look up your OpenRouter key. Try again."
	}
	if spent := costs.serverMonthTotal(); key.serverKey && spent >= limits.MonthlyBudgetUSD {
		srvLogf("device=%s: monthly budget reached ($%.4f of $%.2f)", deviceID, spent, limits.MonthlyBudgetUSD)
		return apiKey{}, "Monthly AI budget reached"
	}
	return key, ""
}

// runAICall makes the AI call an ADVANCE asked for (with retries), then
// feeds the outcome back through the state machine and broadcasts.
func runAICall(taskID string, task Task, action Action) {
	mutex.Lock()
	key, haveKey := taskKeys[taskID]
	mutex.Unlock()

	if !haveKey {
		// Only possible for a task created before the server knew about
		// keys; look it up now rather than failing the task.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		key, _ = resolveAPIKey(ctx, taskID)
		cancel()
	}

	res := AIResult{
		Seq:      task.Seq,
		Planning: task.Context,
		Image:    screenshotDataURL(action.ScreenshotPayload),
	}

	var result any
	start := time.Now()
	const maxRetries = 3
	for attempt := 1; attempt <= maxRetries; attempt++ {
		callStart := time.Now()
		var attemptCost float64
		result, attemptCost, res.Err = sendMessage(task, action, key)
		res.Cost += attemptCost
		res.Attempts = attempt
		callElapsed := time.Since(callStart)

		if res.Err == nil {
			srvLogf("device=%s: sendMessage attempt %d/%d succeeded in %s",
				taskID, attempt, maxRetries, callElapsed)
			break
		}
		srvLogf("device=%s: sendMessage attempt %d/%d FAILED after %s: %v",
			taskID, attempt, maxRetries, callElapsed, res.Err)
		if aiErrorMessage(res.Err) != "" {
			break // a bad key or no credits won't fix itself on a retry
		}
		if attempt < maxRetries {
			backoff := time.Duration(attempt) * time.Second
			srvLogf("device=%s: retrying in %s", taskID, backoff)
			time.Sleep(backoff) // 1s, 2s backoff
		}
	}
	res.Took = time.Since(start)

	if res.Err == nil {
		switch v := result.(type) {
		case PlanResult:
			res.Plan = v
		case ExecResult:
			res.Exec = v
		default:
			res.Err = fmt.Errorf("unexpected AI result type %T", result)
		}
	}

	mutex.Lock()
	prev := taskHashTable[taskID]
	next := onAIResult(prev, res)
	if next.Status != prev.Status {
		srvLogf("device=%s: status transition %s -> %s", taskID, prev.Status, next.Status)
	}
	taskHashTable[taskID] = next
	clients := roomClients(taskID)
	mutex.Unlock()

	if visibleChange(prev, next) {
		// Not tied to any one connection's context: the connection that
		// sent the ADVANCE may be gone by now.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		broadcastToTaskRoom(ctx, taskID, next, clients)
	}
}

// roomClients copies a room's connections. Caller holds mutex.
func roomClients(taskID string) []*websocket.Conn {
	clients := make([]*websocket.Conn, 0, len(rooms[taskID]))
	for client := range rooms[taskID] {
		clients = append(clients, client)
	}
	return clients
}

// visibleChange reports whether a transition changed anything clients can
// see (the broadcast Task); server-only bookkeeping (json:"-") doesn't
// warrant a broadcast.
func visibleChange(a, b Task) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA != nil || errB != nil || !bytes.Equal(ja, jb)
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
