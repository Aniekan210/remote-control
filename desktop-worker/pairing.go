//go:build windows

package main

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"

	qrcode "github.com/skip2/go-qrcode"
	"golang.org/x/sys/windows"
)

// Defined locally rather than trusting it's exported by x/sys/windows —
// this is a stable, decades-old Win32 constant either way.
const swShowNormal = 1

// ShowPairingUI renders the device ID as a QR code, writes a small static
// HTML page showing it, and (by default) opens that page in the default
// browser. It does nothing else — no enrollment flow, no polling, just
// something to point a phone camera at, or to read the ID off manually.
//
// Called from main only on the very first-ever launch (when GetOrCreateDeviceID
// generates a fresh ID) — see the isNew check in main.go. On every later
// launch the cached ID is reused silently and this isn't called at all.
//
// Set REMOTE_WORKER_NO_PAIRING_UI=1 to suppress the browser auto-open even
// on that first launch (the HTML file is still written either way, and the
// device ID is always printed to console/log regardless of this function).
func ShowPairingUI(deviceID string) {
	appDir, err := AppDataDir()
	if err != nil {
		log.Printf("pairing: failed to resolve app data dir: %v", err)
		return
	}
	htmlPath := filepath.Join(appDir, "pairing.html")
	controlURL := "https://control.aniekan.dev?device=" + deviceID

	png, err := qrcode.Encode(controlURL, qrcode.Medium, 320)
	if err != nil {
		log.Printf("pairing: failed to generate QR code: %v", err)
		return
	}

	html := buildPairingHTML(deviceID, png)
	if err := os.WriteFile(htmlPath, []byte(html), 0o600); err != nil {
		log.Printf("pairing: failed to write pairing page: %v", err)
		return
	}
	log.Printf("pairing: page written to %s (device id: %s)", htmlPath, deviceID)

	if os.Getenv("REMOTE_WORKER_NO_PAIRING_UI") == "1" {
		log.Printf("pairing: browser auto-open skipped (REMOTE_WORKER_NO_PAIRING_UI=1)")
		return
	}

	pathPtr, err := syscall.UTF16PtrFromString(htmlPath)
	if err != nil {
		log.Printf("pairing: bad path: %v", err)
		return
	}
	if err := windows.ShellExecute(0, nil, pathPtr, nil, nil, swShowNormal); err != nil {
		log.Printf("pairing: failed to open browser: %v", err)
	}
}

func buildPairingHTML(deviceID string, qrPNG []byte) string {
	b64 := base64.StdEncoding.EncodeToString(qrPNG)
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Pair this device</title>
<style>
  body {
    background: #0a0a0a;
    color: #f2f2f2;
    font-family: -apple-system, Segoe UI, sans-serif;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    height: 100vh;
    margin: 0;
  }
  .card {
    background: #141414;
    border: 1px solid #2a2a2a;
    border-radius: 16px;
    padding: 32px 40px;
    text-align: center;
  }
  img { border-radius: 8px; margin-bottom: 20px; }
  .id {
    font-family: monospace;
    font-size: 14px;
    color: #aaa;
    letter-spacing: 1px;
  }
  h1 { font-size: 18px; font-weight: 600; margin: 0 0 20px; }
</style>
</head>
<body>
  <div class="card">
    <h1>Scan to pair this device</h1>
    <img src="data:image/png;base64,%s" width="320" height="320" alt="Device pairing QR code">
    <div class="id">%s</div>
  </div>
</body>
</html>`, b64, deviceID)
}
