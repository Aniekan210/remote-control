package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// IN-MEMORY DATA STORAGE
var (
	// in memory hash table
	taskHashTable = map[string]Task{}

	// Tracks connected clients grouped by Task ID
	rooms = make(map[string]map[*websocket.Conn]bool)

	mutex sync.Mutex
)

// Handles HTTP POST requests to create new rooms
func handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RoomRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	if req.DeviceID == "" {
		http.Error(w, "ID and Name fields are required", http.StatusBadRequest)
		return
	}

	mutex.Lock()

	if _, exists := taskHashTable[req.DeviceID]; exists {
		mutex.Unlock()
		http.Error(w, "Room already exists", http.StatusConflict)
		return
	}

	taskHashTable[req.DeviceID] = Task{
		DeviceID:                req.DeviceID,
		Description:             "",
		Status:                  "NONE",
		CurrentInstructionIndex: 0,
		InstructionList:         make([]Instruction, 0),
		ExecutionList:           make([]Execution, 0),
		Context:                 false,
	}

	mutex.Unlock()

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "Room successfully created for device '%s'", req.DeviceID)
}

// Handles WebSocket connections for a specific room
func handleWebSocketConnections(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("id")

	if taskID == "" {
		http.Error(w, "Missing 'id' query parameter", http.StatusBadRequest)
		return
	}

	mutex.Lock()

	// Make sure the room exists before accepting the connection
	currentTask, exists := taskHashTable[taskID]

	if !exists {
		mutex.Unlock()
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
		log.Printf("Failed to accept connection: %v", err)
		return
	}

	rooms[taskID][conn] = true

	mutex.Unlock()

	defer conn.Close(websocket.StatusInternalError, "connection closing")

	// Send current state to the newly connected client
	sendTaskState(r.Context(), conn, currentTask)

	// Listen for incoming actions
	for {
		_, msg, err := conn.Read(r.Context())

		if err != nil {
			mutex.Lock()

			delete(rooms[taskID], conn)

			if len(rooms[taskID]) == 0 {
				delete(rooms, taskID)
			}

			mutex.Unlock()
			break
		}

		var action Action

		if err := json.Unmarshal(msg, &action); err != nil {
			log.Printf("Invalid JSON payload: %v", err)
			continue
		}

		mutex.Lock()

		task := taskHashTable[taskID]

		switch action.Type {
		case "CREATE_TASK":
			// what to happen when creating tasks
			task.Description = action.Description
			task.Context = true
			task.Status = "RUNNING"

		case "ADVANCE":
			// get context
			// user preferences, file system payload, screenshot payload

			// if context give all to ai and get instructions
			// set context to false, then ask the ai again for execution list of the current instruction

			// if not initial feed screenshot and preferences to the ai to get execution list
			// then increment instruction index
		case "PAUSE_TASK":
			task.Status = "PAUSED"
		case "RESUME_TASK":
			task.Status = "RUNNING"
		case "CANCEL_TASK":
			task = Task{
				DeviceID:                task.DeviceID,
				Description:             "",
				Status:                  "NONE",
				CurrentInstructionIndex: 0,
				InstructionList:         make([]Instruction, 0),
				ExecutionList:           make([]Execution, 0),
				Context:                 false,
			}
		default:
			log.Printf("Unknown action type: %s", action.Type)
			mutex.Unlock()
			continue
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

func sendTaskState(ctx context.Context, conn *websocket.Conn, task Task) {
	update := StateUpdate{
		Type:    "TASK_UPDATE",
		Payload: task,
	}

	msg, err := json.Marshal(update)

	if err != nil {
		log.Printf("Failed to marshal state: %v", err)
		return
	}

	if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
		log.Printf("Failed to send state: %v", err)
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
		log.Printf("Failed to marshal broadcast: %v", err)
		return
	}

	for _, client := range clients {
		if err := client.Write(ctx, websocket.MessageText, msg); err != nil {
			log.Printf("Failed to send update to client: %v", err)

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
	// HTTP REST endpoint
	http.HandleFunc("/rooms", handleCreateRoom)

	// WebSocket endpoint
	http.HandleFunc("/ws", handleWebSocketConnections)

	fmt.Println(
		"Server running. API: POST http://localhost:8080/rooms | WS: ws://localhost:8080/ws?id=<id>",
	)

	log.Fatal(http.ListenAndServe(":8080", nil))
}
