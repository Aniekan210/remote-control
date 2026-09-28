import "server-only";
import { createHmac } from "node:crypto";

/** How long a connect token is valid. /api/connect mints a fresh one before every reconnect. */
const TOKEN_TTL_SECONDS = 60;

/**
 * Short-lived proof, checked by the Go server on /ws, that this connection
 * was set up by the app for a signed-in user who owns `deviceId`:
 * base64url(deviceId|exp) "." base64url(HMAC-SHA256(CONTROL_SHARED_SECRET, first part)).
 * Without it, anyone who learned a device ID could connect and send tasks.
 */
function connectToken(deviceId: string, secret: string): string {
  const exp = Math.floor(Date.now() / 1000) + TOKEN_TTL_SECONDS;
  const payload = Buffer.from(`${deviceId}|${exp}`).toString("base64url");
  const sig = createHmac("sha256", secret).update(payload).digest("base64url");
  return `${payload}.${sig}`;
}

/**
 * WebSocket URL the browser connects to. Rooms are created by the worker,
 * not the web app — if the worker isn't running, the Go server rejects the
 * upgrade and the console shows "waiting for your computer" and keeps retrying.
 */
export function buildWsUrl(deviceId: string): string | null {
  const base = process.env.CONTROL_WS_URL;
  if (!base) return null;
  const url = new URL("/ws", base);
  url.searchParams.set("id", deviceId);
  const secret = process.env.CONTROL_SHARED_SECRET;
  if (secret) url.searchParams.set("token", connectToken(deviceId, secret));
  return url.toString();
}
