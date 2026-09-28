package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// RunWSClient owns the connection lifecycle: connect, read TASK_UPDATE
// broadcasts, push genuinely-new ones onto changes, and reconnect with
// backoff on any drop. On reconnect the server re-sends the current state
// (see handleWebSocketConnections/sendTaskState server-side), so the
// worker naturally resyncs — no extra recovery logic needed here.
//
// Rooms live only in the server's memory, so after a server restart every
// dial comes back 404 ("room does not exist") forever. serverAddr and
// deviceID are here so a 404 can re-register the room (POST /rooms) and
// redial straight away instead of backing off into that dead end.
//
// secret is sent as the X-Worker-Secret header on every dial, proving to
// the server that this connection is the room's worker.
func RunWSClient(ctx context.Context, serverAddr, deviceID, secret, wsURL string, state *State, changes chan<- TaskChange) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	// justReregistered stops a 404 -> register -> 404 loop from spinning
	// with no delay if the server keeps losing the room (e.g. it's
	// crash-looping): only the first redial after a re-register is instant.
	justReregistered := false

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"X-Worker-Secret": []string{secret}},
		})
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound && !justReregistered {
				log.Printf("wsclient: dial got 404 (server lost our room, probably restarted) — re-registering and redialing")
				if rerr := RegisterRoom(serverAddr, deviceID, secret); rerr != nil {
					log.Printf("wsclient: re-register failed: %v (retrying in %s)", rerr, backoff)
				} else {
					backoff = time.Second
					justReregistered = true
					continue
				}
			}
			justReregistered = false
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
		justReregistered = false

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
	// The first TASK_UPDATE after (re)connecting is the server's current
	// state. It's passed on even if it equals what we last saw: if our
	// ADVANCE was lost in the drop, the executor sees the same Seq again
	// and re-sends it, instead of both sides waiting on each other forever.
	first := true
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
		resync := first
		first = false
		if !changed && !resync {
			continue
		}
		// A genuinely user-driven update (resume, cancel, or a new task) is
		// the explicit signal that clears a human-takeover HOLD. Ordinary
		// task-progression updates (a fresh execution list, same RUNNING
		// status) are NOT treated as resume, so an in-flight AI result can't
		// silently cancel a takeover pause. This runs on the wsclient
		// goroutine, so it works even while the executor is blocked in Gate().
		if changed && isUserResumeSignal(prev, update.Payload) {
			takeover.ServerResume()
		}
		changes <- TaskChange{Prev: prev, Cur: update.Payload}
	}
}

// isUserResumeSignal reports whether a task update reflects an explicit
// person-driven command (rather than automatic task progression):
//   - resuming a paused task, or answering a question
//     (PAUSED or NEEDS_INPUT -> RUNNING),
//   - cancelling / resetting a task (-> NONE or CANCELLED),
//   - starting a brand-new task (Context with no plan yet and no Reason).
//
// Context alone is NOT enough: the server also sets it for an automatic
// revise (Reason set, plan non-empty), and treating that as a resume
// would clear a takeover hold behind the user's back on every revise.
func isUserResumeSignal(prev, cur Task) bool {
	switch cur.Status {
	case "NONE", "CANCELLED":
		return true
	case "RUNNING":
		if prev.Status == "PAUSED" || prev.Status == "NEEDS_INPUT" {
			return true
		}
		if cur.Context && len(cur.InstructionList) == 0 && cur.Reason == "" {
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
