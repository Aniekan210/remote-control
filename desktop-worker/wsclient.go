package main

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/coder/websocket"
)

// RunWSClient owns the connection lifecycle: connect, read TASK_UPDATE
// broadcasts, push genuinely-new ones onto changes, and reconnect with
// backoff on any drop. On reconnect the server re-sends the current state
// (see handleWebSocketConnections/sendTaskState server-side), so the
// worker naturally resyncs — no extra recovery logic needed here.
func RunWSClient(ctx context.Context, wsURL string, state *State, changes chan<- TaskChange) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		if err != nil {
			log.Printf("wsclient: dial failed: %v (retrying in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = minDuration(backoff*2, maxBackoff)
			continue
		}

		log.Printf("wsclient: connected")
		backoff = time.Second

		// The default coder/websocket read limit is 32 KiB. TASK_UPDATE
		// broadcasts are small, but raise it far past that so a large
		// payload never trips a StatusMessageTooBig close — the server
		// sets the matching limit for the ADVANCE screenshots we send it.
		// 256 MiB is vastly more than any single message needs; it's a
		// safety ceiling, not an allocation.
		conn.SetReadLimit(256 << 20)

		state.SetConn(conn)

		readLoop(ctx, conn, state, changes)

		state.SetConn(nil)
		conn.Close(websocket.StatusNormalClosure, "reconnecting")
	}
}

func readLoop(ctx context.Context, conn *websocket.Conn, state *State, changes chan<- TaskChange) {
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			log.Printf("wsclient: read error, will reconnect: %v", err)
			return
		}

		var update StateUpdate
		if err := json.Unmarshal(msg, &update); err != nil {
			log.Printf("wsclient: bad payload: %v", err)
			continue
		}
		if update.Type != "TASK_UPDATE" {
			continue
		}

		prev, changed := state.UpdateTask(update.Payload)
		if !changed {
			continue
		}
		// A genuinely user-driven update (resume, cancel, or a new task) is
		// the explicit signal that clears a human-takeover HOLD. Ordinary
		// task-progression updates (a fresh execution list, same RUNNING
		// status) are NOT treated as resume, so an in-flight AI result can't
		// silently cancel a takeover pause. This runs on the wsclient
		// goroutine, so it works even while the executor is blocked in Gate().
		if isUserResumeSignal(prev, update.Payload) {
			takeover.ServerResume()
		}
		changes <- TaskChange{Prev: prev, Cur: update.Payload}
	}
}

// isUserResumeSignal reports whether a task update reflects an explicit
// person-driven command (rather than automatic task progression):
//   - resuming a paused task (PAUSED -> RUNNING),
//   - cancelling / resetting a task (-> NONE or CANCELLED),
//   - starting a fresh task (a new plan: RUNNING with Context still true).
func isUserResumeSignal(prev, cur Task) bool {
	switch cur.Status {
	case "NONE", "CANCELLED":
		return true
	case "RUNNING":
		if prev.Status == "PAUSED" || cur.Context {
			return true
		}
	}
	return false
}

// SendAction writes an Action to whatever connection is currently active.
// If the socket is down (mid-reconnect), the send is dropped after a
// couple of short retries — a fresh TASK_UPDATE on reconnect will put us
// back in sync, so losing one in-flight ADVANCE isn't fatal.
func SendAction(state *State, action Action) {
	payload, err := json.Marshal(action)
	if err != nil {
		log.Printf("wsclient: failed to marshal action: %v", err)
		return
	}

	for attempt := 0; attempt < 3; attempt++ {
		conn := state.GetConn()
		if conn == nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := conn.Write(ctx, websocket.MessageText, payload)
		cancel()
		if err == nil {
			return
		}
		log.Printf("wsclient: send attempt %d failed: %v", attempt+1, err)
		time.Sleep(500 * time.Millisecond)
	}
	log.Printf("wsclient: dropped action %q, no live connection", action.Type)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
