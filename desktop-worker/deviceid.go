package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// AppDataDir returns (and creates) the local config folder used for
// everything the worker caches on disk: %LOCALAPPDATA%\RemoteWorker.
//
// NOTE: this deliberately reads the LOCALAPPDATA environment variable
// directly rather than calling os.UserConfigDir(). On Windows,
// os.UserConfigDir() actually returns %APPDATA% (Roaming AppData), not
// %LOCALAPPDATA% — a mismatch with what every comment/doc in this project
// (including an earlier version of this function) claimed. A cached
// device-identity token belongs in Local AppData (machine-local, not
// synced/roamed), so this fixes that instead of just fixing the comment.
func AppDataDir() (string, error) {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		// Fallback for the rare case LOCALAPPDATA isn't set (some minimal
		// service contexts). os.UserConfigDir() is Roaming, but it's a
		// working fallback rather than a hard failure.
		var err error
		dir, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
	}
	appDir := filepath.Join(dir, "RemoteWorker")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		return "", err
	}
	return appDir, nil
}

// GetOrCreateDeviceID returns a stable random ID for this machine, cached
// under the user's local config directory so the worker re-identifies
// itself the same way on every subsequent boot. This is a random
// identifier, not a hardware fingerprint — it exists so the server can key
// a task/room to "this install", nothing more.
//
// isNew reports whether this call just generated the ID (i.e. this is the
// very first launch), which is what gates the one-time QR pairing screen.
func GetOrCreateDeviceID() (id string, isNew bool, err error) {
	appDir, err := AppDataDir()
	if err != nil {
		return "", false, err
	}
	idPath := filepath.Join(appDir, "device.id")

	if data, err := os.ReadFile(idPath); err == nil {
		existing := strings.TrimSpace(string(data))
		if existing != "" {
			return existing, false, nil
		}
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	id = hex.EncodeToString(buf)

	if err := os.WriteFile(idPath, []byte(id), 0o600); err != nil {
		return "", false, err
	}
	return id, true, nil
}

// GetOrCreateWorkerSecret returns this install's random worker secret,
// stored next to device.id as worker.secret and generated on first use.
// The worker sends it when registering the room (POST /rooms) and on every
// /ws connection; the server only lets the connection that knows it act as
// the worker (send ADVANCE). Unlike the device ID — which is shown in the
// QR code and stored in the web app's database — the secret never leaves
// this machine except to the server.
func GetOrCreateWorkerSecret() (string, error) {
	appDir, err := AppDataDir()
	if err != nil {
		return "", err
	}
	secretPath := filepath.Join(appDir, "worker.secret")

	if data, err := os.ReadFile(secretPath); err == nil {
		existing := strings.TrimSpace(string(data))
		if existing != "" {
			return existing, nil
		}
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(buf)

	if err := os.WriteFile(secretPath, []byte(secret), 0o600); err != nil {
		return "", err
	}
	return secret, nil
}
