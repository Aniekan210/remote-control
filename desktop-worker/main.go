package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
)

// bestEffortWriter fans out to every writer independently, ignoring
// errors from any individual one. Plain io.MultiWriter stops at the FIRST
// writer that errors and never calls the rest — under a -H=windowsgui
// build there's no real console attached, so os.Stdout is an invalid
// handle and writing to it fails immediately. With os.Stdout listed
// first, that meant every log.Printf call failed right there and NEVER
// reached the log file, which log.Printf then silently swallowed (it
// doesn't check the error Output() returns). The file still got created
// (os.OpenFile succeeded), so it looked like logging was wired up, but it
// permanently received zero bytes. This is almost certainly why
// worker.log has been coming back empty. A broken stdout must never be
// able to block the one sink (the file) that actually matters.
type bestEffortWriter struct {
	writers []io.Writer
}

func (m bestEffortWriter) Write(p []byte) (int, error) {
	for _, w := range m.writers {
		w.Write(p)
	}
	return len(p), nil
}

// setupLogging routes log output to a file under the same app-data folder
// as the device ID, IN ADDITION TO stdout/stderr (which may not exist at
// all under -H=windowsgui — see bestEffortWriter above for why that must
// not be allowed to block the file). Returns the opened file (caller
// should defer Close), or nil if the file couldn't be opened — logging
// then falls back to stdout/stderr only rather than crashing the whole
// program over a logging failure.
func setupLogging() *os.File {
	appDir, err := AppDataDir()
	if err != nil {
		fmt.Println("warning: could not resolve app data dir for logging:", err)
		return nil
	}
	logPath := filepath.Join(appDir, "worker.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Println("warning: could not open log file:", err)
		return nil
	}
	log.SetOutput(bestEffortWriter{writers: []io.Writer{os.Stdout, f}})
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	fmt.Println("Logging to:", logPath)
	return f
}

func main() {
	// Set up file logging FIRST, before anything else can fail silently.
	// This matters because the recommended release build uses
	// -H=windowsgui, which hides the console entirely — without a log
	// file, a crash or early error leaves literally no trace anywhere.
	// The very first line written below is proof the process actually
	// started running main() at all, which is the thing to check first
	// if the worker seems to "do nothing": if this line never appears in
	// the log file, the process isn't reaching Go code — that points at
	// something stopping the .exe before it runs (most commonly, on a
	// fresh Windows machine, antivirus/Defender quarantining or blocking
	// it outright, since SendInput + screen capture + a hidden
	// click-through window is a textbook heuristic match for a RAT).
	//
	// This is pure file I/O — no window or device context involved — so
	// it's safe to do before setDPIAware() below, and doing it first means
	// setDPIAware()'s own log line actually lands in the file.
	logFile := setupLogging()
	if logFile != nil {
		defer logFile.Close()
	}
	log.Printf("=== RemoteWorker process started, pid=%d ===", os.Getpid())

	// Must run before any window, DC, or screen capture happens — see
	// dpi.go for why. Skipping this, or calling it too late, is what
	// causes "executor: screenshot failed: GetDIBits failed" on scaled
	// displays (the default on most modern Windows laptops).
	setDPIAware()

	serverAddr := os.Getenv("REMOTE_SERVER_ADDR")
	if serverAddr == "" {
		serverAddr = "localhost:8080"
	}

	deviceID, isNew, err := GetOrCreateDeviceID()
	if err != nil {
		log.Fatalf("failed to get/create device id: %v", err)
	}

	fmt.Println("=====================================")
	fmt.Println(" RemoteWorker starting")
	fmt.Println(" Device ID:", deviceID)
	fmt.Println("=====================================")

	// Register ourselves with the server. The server's room table is
	// in-memory only, so this runs on every boot, not just the first —
	// a 409 (already registered) is treated as success.
	if err := RegisterRoom(serverAddr, deviceID); err != nil {
		log.Printf("warning: failed to register room after retries: %v", err)
		log.Printf("will still try to connect — the room may already exist on the server")
	}

	// First launch only: show the QR/device-ID pairing page. On every
	// later launch, the cached device ID from device.id is reused as-is
	// (it never changes/regenerates) and this popup is skipped — the ID
	// is still printed above and in the log on every run either way.
	if isNew {
		log.Println("this is a freshly generated device ID (first-ever launch) — opening pairing page")
		ShowPairingUI(deviceID)
	} else {
		log.Println("reusing cached device ID from a previous launch — skipping pairing page")
	}

	wsURL := (&url.URL{Scheme: "ws", Host: serverAddr, Path: "/ws", RawQuery: "id=" + deviceID}).String()
	fmt.Println(" Connecting to:", wsURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	state := NewState()
	changes := make(chan TaskChange, 8)
	overlayUpdates := make(chan OverlayState, 8)
	overlayDone := make(chan struct{})

	fsStore := NewSnapshotStore()

	go RunFSWatcher(ctx, defaultWatchRoots(), fsStore)
	go RunWSClient(ctx, wsURL, state, changes)
	go RunExecutor(state, deviceID, changes, overlayUpdates, fsStore)

	// The overlay is the riskiest code in this project — raw Win32 window
	// creation via hand-built struct layouts, which I have not been able
	// to run on real Windows hardware to confirm. If the worker seems to
	// die/vanish with no explanation, set REMOTE_WORKER_NO_OVERLAY=1 and
	// try again: if it now runs fine, the overlay code is the culprit and
	// you at least have a working worker (no on-screen indicator) while
	// that gets debugged separately, instead of nothing at all.
	if os.Getenv("REMOTE_WORKER_NO_OVERLAY") != "1" {
		go RunOverlay(overlayUpdates, overlayDone)
	} else {
		log.Println("overlay disabled via REMOTE_WORKER_NO_OVERLAY=1")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	<-sigCh

	log.Println("shutting down")
	cancel()
	close(overlayDone)
}
