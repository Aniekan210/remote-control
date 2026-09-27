import type { Execution } from "./task";

/**
 * Turns an Execution (shape unknown — no struct was shared) into a readable
 * line like "Clicked at 412, 88" or "Typed “hello”". Matches common key
 * names case-insensitively and falls back to a generic line, so it degrades
 * gracefully whatever the worker's schema turns out to be.
 */

export type ActionKind =
  | "click"
  | "type"
  | "key"
  | "scroll"
  | "move"
  | "drag"
  | "wait"
  | "screenshot"
  | "open"
  | "other";

export type HumanAction = {
  kind: ActionKind;
  title: string;
  /** mono detail line (coords, amounts…) */
  detail?: string;
  /** model-supplied rationale, if the execution carries one */
  reason?: string;
};

function lowerMap(e: Execution): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(e)) out[k.toLowerCase().replace(/[_\s-]/g, "")] = v;
  return out;
}

function first(m: Record<string, unknown>, ...keys: string[]): unknown {
  for (const k of keys) if (m[k] !== undefined && m[k] !== null && m[k] !== "") return m[k];
  return undefined;
}

function num(v: unknown): number | undefined {
  const n = typeof v === "string" ? Number(v) : v;
  return typeof n === "number" && Number.isFinite(n) ? Math.round(n * 100) / 100 : undefined;
}

function coords(m: Record<string, unknown>): string | undefined {
  let x = num(first(m, "x", "posx", "left"));
  let y = num(first(m, "y", "posy", "top"));
  const pair = first(m, "coordinate", "coordinates", "position", "pos", "point", "to", "target");
  if ((x === undefined || y === undefined) && pair) {
    if (Array.isArray(pair)) [x, y] = [num(pair[0]), num(pair[1])];
    else if (typeof pair === "object") {
      const p = lowerMap(pair as Execution);
      [x, y] = [num(p.x), num(p.y)];
    }
  }
  return x !== undefined && y !== undefined ? `${x}, ${y}` : undefined;
}

const clip = (s: string, n = 64) => (s.length > n ? `${s.slice(0, n - 1)}…` : s);

const KEY_LABELS: Record<string, string> = {
  control: "Ctrl", ctrl: "Ctrl", cmd: "Cmd", command: "Cmd", meta: "Win", win: "Win", super: "Win",
  alt: "Alt", option: "Alt", shift: "Shift", enter: "Enter", return: "Enter", esc: "Esc",
  escape: "Esc", tab: "Tab", space: "Space", backspace: "Backspace", delete: "Delete",
};

function keysLabel(v: unknown): string | undefined {
  const parts = Array.isArray(v)
    ? v.map(String)
    : typeof v === "string"
      ? v.split(/\s*[+\s]\s*/)
      : [];
  const clean = parts.filter(Boolean).map((k) => KEY_LABELS[k.toLowerCase()] ?? (k.length === 1 ? k.toUpperCase() : k));
  return clean.length ? clean.join(" + ") : undefined;
}

export function humanizeAction(e: Execution): HumanAction {
  const m = lowerMap(e);
  const rawKind = String(first(m, "type", "action", "kind", "name", "command", "op", "event") ?? "")
    .toLowerCase()
    .replace(/[\s-]/g, "_");
  const reasonRaw = first(m, "reason", "reasoning", "thought", "description", "explanation", "why", "note");
  const reason = typeof reasonRaw === "string" ? clip(reasonRaw, 160) : undefined;
  const at = coords(m);
  const text = first(m, "text", "value", "content", "input", "string");
  const keys = keysLabel(first(m, "keys", "key", "hotkey", "combo", "shortcut"));
  const has = (...w: string[]) => w.some((x) => rawKind.includes(x));

  if (has("screenshot", "capture", "snapshot")) return { kind: "screenshot", title: "Took a screenshot", reason };

  if (has("double")) return { kind: "click", title: "Double-clicked", detail: at && `at ${at}`, reason };
  if (has("right") && has("click")) return { kind: "click", title: "Right-clicked", detail: at && `at ${at}`, reason };
  if (has("click", "tap", "mouse_down", "mousedown")) {
    const button = first(m, "button");
    const title = button && String(button).toLowerCase() === "right" ? "Right-clicked" : "Clicked";
    return { kind: "click", title, detail: at && `at ${at}`, reason };
  }

  if (has("type", "write", "input", "text") && typeof text === "string") {
    return { kind: "type", title: `Typed “${clip(text, 48)}”`, reason };
  }

  if (has("key", "hotkey", "press", "shortcut", "combo")) {
    return { kind: "key", title: keys ? `Pressed ${keys}` : "Pressed a key", reason };
  }

  if (has("scroll", "wheel")) {
    const dir = first(m, "direction", "dir");
    const amt = num(first(m, "amount", "delta", "dy", "clicks", "distance", "lines"));
    const title = `Scrolled${dir ? ` ${String(dir).toLowerCase()}` : amt !== undefined ? (amt < 0 ? " up" : " down") : ""}`;
    return { kind: "scroll", title, detail: [amt !== undefined ? `by ${Math.abs(amt)}` : null, at && `at ${at}`].filter(Boolean).join(" ") || undefined, reason };
  }

  if (has("drag")) return { kind: "drag", title: "Dragged", detail: at && `to ${at}`, reason };
  if (has("move", "hover")) return { kind: "move", title: "Moved the pointer", detail: at && `to ${at}`, reason };

  if (has("wait", "sleep", "delay", "pause")) {
    const d = num(first(m, "duration", "seconds", "secs", "ms", "milliseconds", "time", "amount"));
    const secs = d === undefined ? undefined : "ms" in m || "milliseconds" in m || d > 60 ? d / 1000 : d;
    return { kind: "wait", title: secs !== undefined ? `Waited ${secs}s` : "Waited", reason };
  }

  if (has("open", "launch", "run", "start", "exec", "navigate", "url")) {
    const target = first(m, "app", "application", "program", "url", "path", "target", "name", "value");
    return { kind: "open", title: target ? `Opened ${clip(String(target), 40)}` : "Opened an app", reason };
  }

  // Fallback: "Some action" + whatever fields are left
  const skip = new Set(["type", "action", "kind", "name", "command", "op", "event", "reason", "reasoning", "thought", "description", "explanation"]);
  const rest = Object.entries(e)
    .filter(([k]) => !skip.has(k.toLowerCase()))
    .map(([k, v]) => `${k.toLowerCase()}=${typeof v === "object" ? JSON.stringify(v) : String(v)}`)
    .join("  ");
  const title = rawKind ? rawKind.replace(/_/g, " ").replace(/^\w/, (c) => c.toUpperCase()) : "Action";
  return { kind: "other", title, detail: rest ? clip(rest, 90) : undefined, reason };
}
