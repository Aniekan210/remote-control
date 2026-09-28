import "server-only";
import { createCipheriv, createDecipheriv, createHash, randomBytes } from "node:crypto";
import { pool } from "./db";

/**
 * Each user's own OpenRouter API key ("bring your own key"). Only the
 * server's owner runs on the server's key; everyone else's tasks are billed
 * to the key they save here.
 *
 * Stored encrypted (AES-256-GCM) in the openrouter_key table. The Go control
 * server reads the same table and decrypts with the same
 * KEY_ENCRYPTION_SECRET, so the format below must match server/keys.go:
 *   base64( nonce[12] | ciphertext | tag[16] ), key = SHA-256(KEY_ENCRYPTION_SECRET)
 *
 * The plaintext key never goes back to the browser — only its last 4 chars.
 */

export type KeyInfo = { last4: string; updatedAt: string };

let tableReady: Promise<unknown> | null = null;

/** Creates the table on first use, so no manual migration is needed. */
function ensureTable() {
  tableReady ??= pool
    .query(
      `CREATE TABLE IF NOT EXISTS openrouter_key (
         user_id    TEXT PRIMARY KEY,
         key_enc    TEXT NOT NULL,
         key_last4  TEXT NOT NULL,
         updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
       )`,
    )
    .catch((err) => {
      tableReady = null; // retry next time
      throw err;
    });
  return tableReady;
}

function encryptionKey(): Buffer {
  const secret = process.env.KEY_ENCRYPTION_SECRET;
  if (!secret) throw new Error("KEY_ENCRYPTION_SECRET is not set");
  return createHash("sha256").update(secret).digest();
}

export function encryptKey(plain: string): string {
  const nonce = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", encryptionKey(), nonce);
  const ct = Buffer.concat([cipher.update(plain, "utf8"), cipher.final()]);
  return Buffer.concat([nonce, ct, cipher.getAuthTag()]).toString("base64");
}

export function decryptKey(enc: string): string {
  const raw = Buffer.from(enc, "base64");
  const nonce = raw.subarray(0, 12);
  const tag = raw.subarray(raw.length - 16);
  const ct = raw.subarray(12, raw.length - 16);
  const decipher = createDecipheriv("aes-256-gcm", encryptionKey(), nonce);
  decipher.setAuthTag(tag);
  return Buffer.concat([decipher.update(ct), decipher.final()]).toString("utf8");
}

export async function getKeyInfo(userId: string): Promise<KeyInfo | null> {
  await ensureTable();
  const { rows } = await pool.query<{ key_last4: string; updated_at: Date }>(
    "SELECT key_last4, updated_at FROM openrouter_key WHERE user_id = $1",
    [userId],
  );
  const row = rows[0];
  return row ? { last4: row.key_last4, updatedAt: row.updated_at.toISOString() } : null;
}

export async function saveKey(userId: string, key: string) {
  await ensureTable();
  await pool.query(
    `INSERT INTO openrouter_key (user_id, key_enc, key_last4)
     VALUES ($1, $2, $3)
     ON CONFLICT (user_id)
     DO UPDATE SET key_enc = EXCLUDED.key_enc, key_last4 = EXCLUDED.key_last4, updated_at = now()`,
    [userId, encryptKey(key), key.slice(-4)],
  );
}

export async function removeKey(userId: string) {
  await ensureTable();
  await pool.query("DELETE FROM openrouter_key WHERE user_id = $1", [userId]);
}

export type KeyCheck =
  | { ok: true; hasLimit: boolean }
  | { ok: false; error: string };

/** One request to OpenRouter's key-info endpoint: is this a real, active key? */
export async function checkKey(key: string): Promise<KeyCheck> {
  let res: Response;
  try {
    res = await fetch("https://openrouter.ai/api/v1/key", {
      headers: { Authorization: `Bearer ${key}` },
      cache: "no-store",
      signal: AbortSignal.timeout(10_000),
    });
  } catch {
    return { ok: false, error: "Couldn't reach OpenRouter to check the key. Try again." };
  }
  if (res.status === 401 || res.status === 403) return { ok: false, error: "OpenRouter rejected that key." };
  if (!res.ok) return { ok: false, error: `OpenRouter couldn't check the key (${res.status}). Try again.` };
  const body = (await res.json().catch(() => null)) as { data?: { limit?: number | null } } | null;
  return { ok: true, hasLimit: body?.data?.limit !== null && body?.data?.limit !== undefined };
}
