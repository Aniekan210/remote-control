"use client";

import { useEffect, useRef, useState } from "react";
import { Composer } from "./composer";
import { Controls } from "./controls";
import { ConnectionDot } from "./status";
import { Transcript } from "./transcript";
import { TaskSummary } from "./task-summary";
import { TopBar } from "./top-bar";
import { IconMonitor, IconArrowUp, IconChevron } from "./icons";
import { useControlSocket } from "./use-control-socket";
import type { ClientAction, TaskStatus } from "@/lib/task";
import {
  EMPTY_STORE,
  fmtClock,
  fmtDuration,
  loadTimeline,
  reduceTimeline,
  saveTimeline,
  type Intent,
  type Timeline,
  type TimelineStore,
} from "@/lib/timeline";

/**
 * Which actions are legal from which status. The server accepts anything,
 * so the UI is the gate: one task at a time, no resuming a task that isn't
 * paused, no creating over a running/paused/finished one.
 */
const ALLOWED: Record<TaskStatus, ClientAction["type"][]> = {
  NONE: ["CREATE_TASK"],
  RUNNING: ["PAUSE_TASK", "CANCEL_TASK"],
  PAUSED: ["RESUME_TASK", "CANCEL_TASK"],
  COMPLETED: ["CANCEL_TASK"], // clears the finished task so a new one can start
};

export function Console({ deviceId, deviceLabel }: { deviceId: string; deviceLabel: string }) {
  const { connection, task, send } = useControlSocket();
  const status: TaskStatus | null = task?.status ?? null;
  const live = connection === "live";

  // ── Action gate ───────────────────────────────────────────────
  const statusRef = useRef(status);
  statusRef.current = status;
  const intentRef = useRef<Intent>(null);

  // "busy" = we sent something and haven't heard back yet. Blocks double
  // taps and, crucially, a second CREATE_TASK before the first lands.
  const [busy, setBusy] = useState(false);
  useEffect(() => setBusy(false), [task]);
  useEffect(() => {
    if (!busy) return;
    const t = setTimeout(() => setBusy(false), 5000);
    return () => clearTimeout(t);
  }, [busy]);

  const act = (action: ClientAction): boolean => {
    const s = statusRef.current;
    if (busy || s === null || !ALLOWED[s].includes(action.type)) return false;
    const ok = send(action);
    if (ok) {
      intentRef.current = { type: action.type, at: Date.now() };
      setBusy(true);
    }
    return ok;
  };

  // ── Timeline: history built from successive snapshots ─────────
  const [store, setStore] = useState<TimelineStore>(() =>
    typeof window === "undefined" ? EMPTY_STORE : loadTimeline(deviceId),
  );
  useEffect(() => {
    if (task) setStore((s) => reduceTimeline(s, task, Date.now(), intentRef.current));
  }, [task]);
  useEffect(() => saveTimeline(deviceId, store), [deviceId, store]);

  const current = store.current;
  const ticking = !!current && !current.outcome;
  const now = useNow(ticking);

  // ── Follow the live step, unless the user scrolled up to read ──
  const pinnedRef = useRef(true);
  useEffect(() => {
    const onScroll = () => {
      const bottom = window.innerHeight + window.scrollY;
      pinnedRef.current = bottom >= document.documentElement.scrollHeight - 200;
    };
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);
  useEffect(() => {
    if (!pinnedRef.current || !current) return;
    requestAnimationFrame(() =>
      window.scrollTo({ top: document.documentElement.scrollHeight, behavior: "smooth" }),
    );
  }, [current]);

  const showTranscript = status !== null && status !== "NONE" && current;

  return (
    <div className="flex min-h-dvh flex-col">
      <TopBar
        right={<ConnectionDot connection={connection} />}
        below={showTranscript ? <TaskSummary t={current} now={now} /> : undefined}
      />

      <main className="mx-auto w-full max-w-xl flex-1 px-4 pt-6 pb-48">
        <div className="mb-8 flex items-center gap-2 text-dim">
          <IconMonitor width={14} height={14} />
          <span className="truncate font-mono text-[11px] uppercase tracking-[0.08em]">{deviceLabel}</span>
        </div>

        {!task ? (
          connection === "offline" ? <WaitingForComputer /> : <Skeleton />
        ) : showTranscript ? (
          <Transcript t={current} now={now} live={live} />
        ) : task.status === "NONE" ? (
          <Empty last={store.last} now={now} />
        ) : (
          <Skeleton />
        )}
      </main>

      {/* Bottom dock — its contents depend entirely on the task's status */}
      {status && (
        <div className="pb-safe fixed inset-x-0 bottom-0 z-20 bg-gradient-to-t from-ink via-ink to-ink/0 pt-8">
          <div className="mx-auto max-w-xl px-4">
            {task?.lastError && (
              <p role="alert" className="mb-2 px-1 font-mono text-[12px] leading-snug text-danger">
                {task.lastError}
              </p>
            )}

            {status === "NONE" && (
              <Composer
                disabled={!live || busy}
                placeholder={!live ? "Waiting for connection…" : "What should your computer do?"}
                onSubmit={(description) => act({ type: "CREATE_TASK", description })}
              />
            )}

            {(status === "RUNNING" || status === "PAUSED") && (
              <Controls
                status={status}
                busy={busy || !live}
                onPause={() => act({ type: "PAUSE_TASK" })}
                onResume={() => act({ type: "RESUME_TASK" })}
                onCancel={() => act({ type: "CANCEL_TASK" })}
              />
            )}

            {status === "COMPLETED" && (
              <button
                onClick={() => act({ type: "CANCEL_TASK" })}
                disabled={busy || !live}
                className="flex h-14 w-full items-center justify-center gap-2 rounded-2xl bg-fg font-medium text-ink transition active:scale-[0.98] disabled:opacity-60"
              >
                New task <IconArrowUp width={16} height={16} className="rotate-45" />
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

/** Re-renders every second while `active`, so durations tick live. */
function useNow(active: boolean) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [active]);
  return now;
}

function Empty({ last, now }: { last: Timeline | null; now: number }) {
  return (
    <div className="animate-rise pt-[8vh]">
      <p className="label">Ready</p>
      <h1 className="mt-3 text-[2.25rem] leading-[1.05] font-medium tracking-[-0.03em] text-balance">
        Tell your computer what to&nbsp;do.
      </h1>
      <p className="mt-4 max-w-sm text-[15px] leading-relaxed text-dim">
        One task at a time. Type it or use the mic — it plans the steps, then drives the mouse and
        keyboard while you watch every action here.
      </p>

      {last ? (
        <LastTask t={last} now={now} />
      ) : (
        <ul className="mt-8 space-y-2 font-mono text-[12px] text-faint">
          <li>› open spotify and play my liked songs</li>
          <li>› email the q3 report on my desktop to sam</li>
          <li>› close every chrome tab except gmail</li>
        </ul>
      )}
    </div>
  );
}

const OUTCOME = {
  completed: { label: "Done", cls: "text-done" },
  cancelled: { label: "Cancelled", cls: "text-danger" },
  ended: { label: "Stopped", cls: "text-danger" },
} as const;

function LastTask({ t, now }: { t: Timeline; now: number }) {
  const [open, setOpen] = useState(false);
  const o = OUTCOME[t.outcome ?? "ended"];
  return (
    <section className="mt-10">
      <p className="label">Last task</p>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="mt-3 flex w-full items-start gap-3 rounded-2xl border border-line bg-panel p-4 text-left active:bg-raised"
      >
        <span className="min-w-0 flex-1">
          <span className="line-clamp-2 text-[15px] leading-snug">{t.description}</span>
          <span className="mt-1.5 flex gap-2 font-mono text-[11px]">
            <span className={o.cls}>{o.label}</span>
            <span className="text-faint">
              {fmtClock(t.startedAt)} · {fmtDuration((t.endedAt ?? t.startedAt) - t.startedAt)}
            </span>
          </span>
        </span>
        <IconChevron
          width={16}
          height={16}
          className={`mt-1 shrink-0 text-faint transition-transform ${open ? "rotate-90" : ""}`}
        />
      </button>
      {open && (
        <div className="mt-6">
          <Transcript t={t} now={now} live />
        </div>
      )}
    </section>
  );
}

function WaitingForComputer() {
  return (
    <div className="animate-rise pt-[12vh]">
      <p className="label text-hold!">Waiting</p>
      <h1 className="mt-3 text-[1.75rem] leading-[1.1] font-medium tracking-[-0.02em] text-balance">
        Your computer isn&apos;t connected.
      </h1>
      <p className="mt-3 max-w-sm text-[15px] leading-relaxed text-dim">
        Start the Remote Control worker on it. This screen connects on its own as soon as it&apos;s
        up.
      </p>
    </div>
  );
}

function Skeleton() {
  return (
    <div className="space-y-4 pt-2" aria-hidden>
      <div className="h-3 w-20 rounded bg-panel" />
      <div className="h-8 w-4/5 rounded-lg bg-panel" />
      <div className="h-1 w-full rounded-full bg-panel" />
    </div>
  );
}
