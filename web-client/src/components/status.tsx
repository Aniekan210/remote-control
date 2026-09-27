import type { TaskStatus } from "@/lib/task";
import type { Connection } from "./use-control-socket";

const STATUS: Record<TaskStatus, { label: string; dot: string; text: string; blink?: boolean }> = {
  NONE: { label: "Idle", dot: "bg-faint", text: "text-dim" },
  RUNNING: { label: "Running", dot: "bg-signal", text: "text-signal", blink: true },
  PAUSED: { label: "Paused", dot: "bg-hold", text: "text-hold" },
  COMPLETED: { label: "Done", dot: "bg-done", text: "text-done" },
};

export function StatusBadge({ status, planning }: { status: TaskStatus; planning?: boolean }) {
  const s = STATUS[status];
  const label = status === "RUNNING" && planning ? "Planning" : s.label;
  return (
    <span className={`inline-flex items-center gap-2 font-mono text-[11px] uppercase tracking-[0.08em] ${s.text}`}>
      <span className={`size-1.5 rounded-full ${s.dot} ${s.blink ? "animate-blink" : ""}`} />
      {label}
    </span>
  );
}

const CONN: Record<Connection, { label: string; dot: string }> = {
  live: { label: "Live", dot: "bg-done" },
  connecting: { label: "Connecting", dot: "bg-hold animate-blink" },
  offline: { label: "Reconnecting", dot: "bg-danger animate-blink" },
};

export function ConnectionDot({ connection }: { connection: Connection }) {
  const c = CONN[connection];
  return (
    <span className="inline-flex items-center gap-1.5 font-mono text-[11px] uppercase tracking-[0.08em] text-dim">
      <span className={`size-1.5 rounded-full ${c.dot}`} />
      {c.label}
    </span>
  );
}
