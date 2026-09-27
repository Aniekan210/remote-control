import "server-only";

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
  return url.toString();
}
