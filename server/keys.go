package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Bring-your-own-key: every user runs tasks on their own OpenRouter key,
// saved (encrypted) from the web app's Settings page into the shared Neon
// database. Only the users listed in OWNER_USER_IDS may fall back to the
// server's own OPENROUTER_API_KEY — otherwise strangers would be spending
// the owner's budget.
//
// The resolved key is kept in taskKeys (keyed by device), never in Task:
// Task is broadcast in full to every client in the room.

// apiKey is the OpenRouter key a task's calls are billed to.
type apiKey struct {
	value     string
	serverKey bool // the server's own OPENROUTER_API_KEY (counts toward MONTHLY_BUDGET_USD)
}

var db *pgxpool.Pool

// dbErr is set when DATABASE_URL is set but unusable. Then no task can
// start (see resolveAPIKey): quietly falling back to "everyone runs on the
// server's key" would let anyone spend it.
var dbErr error

// initDB connects to DATABASE_URL (the same Neon database as the web app).
// Without it, per-user keys can't be looked up and every task runs on the
// server's key, as before BYO keys existed.
func initDB() {
	raw, set := os.LookupEnv("DATABASE_URL")
	url := cleanDatabaseURL(raw)
	if !set || (url == "" && strings.TrimSpace(raw) == "") {
		if set {
			dbErr = errors.New("DATABASE_URL is set but empty")
			srvLogf("ERROR: DATABASE_URL is set but empty — no task can start until it holds the Neon connection string (postgresql://user:password@ep-....neon.tech/neondb?sslmode=require)")
			return
		}
		srvLogf("WARNING: DATABASE_URL is not set — per-user OpenRouter keys are disabled and EVERY task runs on the server's key")
		return
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		dbErr = fmt.Errorf("DATABASE_URL can't be parsed: %w", err)
		srvLogf("ERROR: DATABASE_URL can't be parsed (%v) — no task can start. It should be the bare Neon connection string: postgresql://user:password@ep-....neon.tech/neondb?sslmode=require", err)
		return
	}
	host, database := cfg.ConnConfig.Host, cfg.ConnConfig.Database
	if host == "" || strings.HasPrefix(host, "/") || database == "" {
		// What an empty or mangled value parses to: the driver's defaults,
		// a local Unix socket and no database — never what's meant here.
		dbErr = fmt.Errorf("DATABASE_URL has no host or database (host=%q database=%q)", host, database)
		srvLogf("ERROR: DATABASE_URL has no host or database (parsed host=%q database=%q) — no task can start. It should be the bare Neon connection string: postgresql://user:password@ep-....neon.tech/neondb?sslmode=require (no quotes, no \"psql\")", host, database)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		dbErr = fmt.Errorf("can't set up the database pool: %w", err)
		srvLogf("ERROR: could not set up DATABASE_URL (host=%s database=%s): %v — no task can start", host, database, err)
		return
	}
	db = pool
	if err := pool.Ping(ctx); err != nil {
		// Could be a blip (Neon waking up): keep the pool, it retries per query.
		srvLogf("WARNING: database at host=%s database=%s isn't answering yet: %v — will keep trying", host, database, err)
		return
	}
	srvLogf("database connected: host=%s database=%s (per-user OpenRouter keys enabled)", host, database)
}

// cleanDatabaseURL forgives the usual paste mistakes: surrounding spaces or
// newlines, quotes, and Neon's "psql '...'" copy button format.
func cleanDatabaseURL(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.HasPrefix(s, "psql ") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "psql "))
	}
	return strings.TrimSpace(strings.Trim(s, `"'`))
}

// ownerUserIDs parses OWNER_USER_IDS (comma-separated web-app user IDs).
func ownerUserIDs() map[string]bool {
	out := map[string]bool{}
	for _, id := range strings.Split(os.Getenv("OWNER_USER_IDS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

// errNoKey means the device's user has no key of their own and isn't an
// owner; its message is shown to the user as last_error.
var errNoKey = errors.New("Add your OpenRouter key in Settings")

// errDBMisconfigured means DATABASE_URL is set but unusable (see initDB).
var errDBMisconfigured = errors.New("the server's database isn't configured correctly")

// resolveAPIKey finds the key to bill a new task on this device to:
// device -> user -> that user's saved key; owners without a key of their
// own use the server's key.
func resolveAPIKey(ctx context.Context, deviceID string) (apiKey, error) {
	serverKey := apiKey{value: os.Getenv("OPENROUTER_API_KEY"), serverKey: true}
	if dbErr != nil {
		return apiKey{}, fmt.Errorf("%w: %v", errDBMisconfigured, dbErr)
	}
	if db == nil {
		return serverKey, nil
	}

	var userID string
	var keyEnc *string
	err := db.QueryRow(ctx,
		`SELECT d.user_id, k.key_enc
		   FROM device d
		   LEFT JOIN openrouter_key k ON k.user_id = d.user_id
		  WHERE d.device_id = $1`, deviceID).Scan(&userID, &keyEnc)
	if err != nil && isUndefinedTable(err) {
		// The web app creates openrouter_key the first time anyone opens
		// Settings; until then nobody has a key of their own.
		err = db.QueryRow(ctx, `SELECT user_id FROM device WHERE device_id = $1`, deviceID).Scan(&userID)
		keyEnc = nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// A worker that isn't linked to any account yet.
		return apiKey{}, errNoKey
	}
	if err != nil {
		return apiKey{}, fmt.Errorf("look up key for device: %w", err)
	}

	if keyEnc != nil && *keyEnc != "" {
		plain, err := decryptAPIKey(*keyEnc)
		if err != nil {
			return apiKey{}, fmt.Errorf("decrypt key for user %s: %w", userID, err)
		}
		return apiKey{value: plain}, nil
	}
	if ownerUserIDs()[userID] {
		return serverKey, nil
	}
	return apiKey{}, errNoKey
}

// isUndefinedTable reports a Postgres "relation does not exist" (42P01).
func isUndefinedTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "42P01")
}

// decryptAPIKey reverses web-client/src/lib/openrouter-key.ts encryptKey:
// base64(nonce[12] | ciphertext | tag[16]), AES-256-GCM with
// key = SHA-256(KEY_ENCRYPTION_SECRET).
func decryptAPIKey(enc string) (string, error) {
	secret := os.Getenv("KEY_ENCRYPTION_SECRET")
	if secret == "" {
		return "", errors.New("KEY_ENCRYPTION_SECRET is not set")
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	k := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
