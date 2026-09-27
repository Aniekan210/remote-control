"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { parseServerMessage, type ClientAction, type Task } from "@/lib/task";

export type Connection = "connecting" | "live" | "offline";

const MAX_BACKOFF_MS = 8_000; // short cap: picks the worker up quickly once it starts

/**
 * Owns the WebSocket to the Go control server.
 * - fetches the WS URL for the user's linked device from /api/connect
 *   before every connect
 * - reconnects with exponential backoff (the Go server refuses the upgrade
 *   until the worker has created the room, so "offline" often just means
 *   the worker isn't running yet)
 * - reconnects immediately when the tab comes back to the foreground or the
 *   network returns (mobile browsers kill sockets in the background)
 */
export function useControlSocket() {
  const [connection, setConnection] = useState<Connection>("connecting");
  const [task, setTask] = useState<Task | null>(null);

  const wsRef = useRef<WebSocket | null>(null);
  const retryRef = useRef(0);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const disposedRef = useRef(false);
  const connectingRef = useRef(false);

  const connect = useCallback(async () => {
    if (disposedRef.current || connectingRef.current) return;
    const state = wsRef.current?.readyState;
    if (state === WebSocket.OPEN || state === WebSocket.CONNECTING) return;

    connectingRef.current = true;
    if (timerRef.current) clearTimeout(timerRef.current);
    setConnection("connecting");

    const scheduleRetry = () => {
      if (disposedRef.current) return;
      setConnection("offline");
      const delay = Math.min(1000 * 2 ** retryRef.current, MAX_BACKOFF_MS);
      retryRef.current += 1;
      timerRef.current = setTimeout(connect, delay);
    };

    try {
      const res = await fetch("/api/connect", { cache: "no-store" });
      if (res.status === 401) {
        window.location.href = "/login";
        return;
      }
      if (res.status === 404) {
        // Device was unlinked (maybe from another phone) — re-render the page.
        window.location.reload();
        return;
      }
      if (!res.ok) throw new Error(`connect: ${res.status}`);
      const { url } = (await res.json()) as { url: string };
      if (disposedRef.current) return;

      const ws = new WebSocket(url);
      wsRef.current = ws;

      ws.onopen = () => {
        retryRef.current = 0;
        setConnection("live");
      };
      ws.onmessage = (ev) => {
        if (typeof ev.data !== "string") return;
        const next = parseServerMessage(ev.data);
        if (next) setTask(next);
      };
      ws.onclose = () => {
        if (wsRef.current === ws) wsRef.current = null;
        scheduleRetry();
      };
      // onerror is always followed by onclose; nothing extra to do.
    } catch {
      scheduleRetry();
    } finally {
      connectingRef.current = false;
    }
  }, []);

  useEffect(() => {
    disposedRef.current = false;
    connect();

    const wake = () => {
      if (document.visibilityState !== "visible") return;
      const s = wsRef.current?.readyState;
      if (s !== WebSocket.OPEN && s !== WebSocket.CONNECTING) {
        retryRef.current = 0;
        connect();
      }
    };
    document.addEventListener("visibilitychange", wake);
    window.addEventListener("online", wake);

    return () => {
      disposedRef.current = true;
      document.removeEventListener("visibilitychange", wake);
      window.removeEventListener("online", wake);
      if (timerRef.current) clearTimeout(timerRef.current);
      wsRef.current?.close(1000, "unmount");
      wsRef.current = null;
    };
  }, [connect]);

  /** Returns false if the socket isn't open (caller should keep the input). */
  const send = useCallback((...actions: ClientAction[]) => {
    const ws = wsRef.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return false;
    // Go's json.Unmarshal matches keys case-insensitively, so lowercase keys
    // land on Action.Type / Action.Description regardless of struct tags.
    for (const a of actions) ws.send(JSON.stringify(a));
    return true;
  }, []);

  return { connection, task, send };
}
