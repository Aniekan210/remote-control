package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Connection roles. The worker (who proved the room's secret) owns the
// room and is the only one that may send ADVANCE; web clients (who proved
// a short-lived token minted by the Next.js app) may only drive the task.
const (
	roleWorker = "worker"
	roleWeb    = "web"
	// roleAny is only used while CONTROL_SHARED_SECRET is unset (auth
	// disabled): an unauthenticated connection may do anything, exactly
	// like before auth existed, so an un-upgraded deployment keeps working.
	roleAny = "any"
)

// webAllowed are the only actions a web-client connection may send.
var webAllowed = map[string]bool{
	"CREATE_TASK": true,
	"PAUSE_TASK":  true,
	"RESUME_TASK": true,
	"CANCEL_TASK": true,
	"ANSWER":      true,
}

// roleMayAct reports whether a connection with this role may send action.
func roleMayAct(role, actionType string) bool {
	switch role {
	case roleWorker, roleAny:
		return true
	case roleWeb:
		return webAllowed[actionType]
	}
	return false
}

// sharedSecret is CONTROL_SHARED_SECRET, shared with the Next.js app,
// which signs the web client's connect tokens with it.
func sharedSecret() string {
	return os.Getenv("CONTROL_SHARED_SECRET")
}

// workerSecretHeader carries the worker's per-install secret on /ws.
const workerSecretHeader = "X-Worker-Secret"

// authenticateConnection decides what an incoming /ws connection may do.
// roomSecret is the secret the worker registered the room with. ok=false
// means reject with 401.
func authenticateConnection(r *http.Request, deviceID, roomSecret string) (role string, ok bool) {
	if ws := r.Header.Get(workerSecretHeader); ws != "" {
		if roomSecret != "" && secretsEqual(ws, roomSecret) {
			return roleWorker, true
		}
		return "", false
	}
	secret := sharedSecret()
	if secret == "" {
		return roleAny, true
	}
	if verifyConnectToken(r.URL.Query().Get("token"), deviceID, secret, time.Now()) {
		return roleWeb, true
	}
	return "", false
}

// verifyConnectToken checks a web connect token:
//
//	base64url(deviceId|exp) "." base64url(HMAC-SHA256(secret, <first part>))
//
// minted by web-client/src/lib/control-server.ts. It must be for this
// device and not expired (exp is Unix seconds; tokens live ~60s — the app
// mints a fresh one before every reconnect).
func verifyConnectToken(token, deviceID, secret string, now time.Time) bool {
	payloadB64, sigB64, found := strings.Cut(token, ".")
	if !found || payloadB64 == "" || sigB64 == "" {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadB64))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return false
	}
	id, expStr, found := strings.Cut(string(payload), "|")
	if !found || id != deviceID {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil {
		return false
	}
	return now.Unix() <= exp
}

// secretsEqual compares two secrets in constant time.
func secretsEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
