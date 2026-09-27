-- Remote Control — Neon schema
-- Run once in the Neon SQL editor (or: psql "$DATABASE_URL" -f schema.sql).
-- Safe to re-run: everything is IF NOT EXISTS.

-- ─────────────────────────────────────────────────────────────
-- better-auth core tables (column names are camelCase on purpose —
-- that's what better-auth's Postgres adapter reads/writes).
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS "user" (
  "id"            TEXT PRIMARY KEY,
  "name"          TEXT NOT NULL,
  "email"         TEXT NOT NULL UNIQUE,
  "emailVerified" BOOLEAN NOT NULL DEFAULT FALSE,
  "image"         TEXT,
  "createdAt"     TIMESTAMPTZ NOT NULL DEFAULT now(),
  "updatedAt"     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS "session" (
  "id"        TEXT PRIMARY KEY,
  "expiresAt" TIMESTAMPTZ NOT NULL,
  "token"     TEXT NOT NULL UNIQUE,
  "createdAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
  "updatedAt" TIMESTAMPTZ NOT NULL DEFAULT now(),
  "ipAddress" TEXT,
  "userAgent" TEXT,
  "userId"    TEXT NOT NULL REFERENCES "user"("id") ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS "session_userId_idx" ON "session"("userId");

CREATE TABLE IF NOT EXISTS "account" (
  "id"                    TEXT PRIMARY KEY,
  "accountId"             TEXT NOT NULL,
  "providerId"            TEXT NOT NULL,
  "userId"                TEXT NOT NULL REFERENCES "user"("id") ON DELETE CASCADE,
  "accessToken"           TEXT,
  "refreshToken"          TEXT,
  "idToken"               TEXT,
  "accessTokenExpiresAt"  TIMESTAMPTZ,
  "refreshTokenExpiresAt" TIMESTAMPTZ,
  "scope"                 TEXT,
  "password"              TEXT,
  "createdAt"             TIMESTAMPTZ NOT NULL DEFAULT now(),
  "updatedAt"             TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS "account_userId_idx" ON "account"("userId");

CREATE TABLE IF NOT EXISTS "verification" (
  "id"         TEXT PRIMARY KEY,
  "identifier" TEXT NOT NULL,
  "value"      TEXT NOT NULL,
  "expiresAt"  TIMESTAMPTZ NOT NULL,
  "createdAt"  TIMESTAMPTZ NOT NULL DEFAULT now(),
  "updatedAt"  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS "verification_identifier_idx" ON "verification"("identifier");

-- ─────────────────────────────────────────────────────────────
-- App table: one device per user.
--   PRIMARY KEY (user_id)  → a user can have at most one device
--   UNIQUE (device_id)     → a device can't be claimed by two accounts
-- Linking again replaces the row (upsert on user_id).
-- ─────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS device (
  user_id   TEXT PRIMARY KEY REFERENCES "user"("id") ON DELETE CASCADE,
  device_id TEXT NOT NULL UNIQUE CHECK (length(device_id) BETWEEN 4 AND 128),
  name      TEXT CHECK (name IS NULL OR length(name) <= 60),
  linked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
