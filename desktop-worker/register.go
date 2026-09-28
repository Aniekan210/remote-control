package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

type roomRequest struct {
	DeviceID     string `json:"device_id"`
	WorkerSecret string `json:"worker_secret"`
}

// RegisterRoom calls the server's POST /rooms itself, so you never have to
// do it by hand. The server keeps rooms in memory only, so this runs on
// every boot (not just the first) — a 409 (room already exists) is treated
// as success since it just means the server already knows about us,
// whether from an earlier run or an earlier connection this session.
//
// secret is this install's worker secret (see GetOrCreateWorkerSecret): the
// server stores it with the room and only accepts ADVANCE from a
// connection that presents it. A 403 means the room is owned by a
// different secret — it won't fix itself by retrying.
func RegisterRoom(httpAddr, deviceID, secret string) error {
	body, err := json.Marshal(roomRequest{DeviceID: deviceID, WorkerSecret: secret})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("https://%s/rooms", httpAddr)
	client := &http.Client{Timeout: 10 * time.Second}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		resp, err := client.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			lastErr = err
			log.Printf("register: attempt %d failed: %v", attempt, err)
			time.Sleep(time.Duration(attempt) * time.Second)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		switch resp.StatusCode {
		case http.StatusCreated:
			log.Printf("register: room created for device %s", deviceID)
			return nil
		case http.StatusConflict:
			log.Printf("register: room already exists for device %s, continuing", deviceID)
			return nil
		case http.StatusForbidden:
			log.Printf("register: server says room for device %s belongs to a different worker secret — was worker.secret deleted? It clears when the server restarts", deviceID)
			return fmt.Errorf("room owned by another worker secret: %s", string(respBody))
		default:
			lastErr = fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
			log.Printf("register: attempt %d: %v", attempt, lastErr)
			time.Sleep(time.Duration(attempt) * time.Second)
		}
	}

	return lastErr
}
