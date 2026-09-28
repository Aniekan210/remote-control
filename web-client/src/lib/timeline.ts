import type { ClientAction, Execution, Task, TaskStatus } from "./task";

/**
 * The Go server only ever broadcasts the CURRENT task snapshot, and it
 * overwrites ExecutionList on every step. To show a real history (which
 * actions ran for which step, how long things took, who paused), we diff
 * each snapshot against the last one and record what changed, with
 * timestamps, on the client.
 *
 * How snapshots map to progress (from main.go):
 *  - CREATE_TASK            → RUNNING, Context=true, no instructions  → planning
 *  - planning ADVANCE       → InstructionList set, index=0, Context=false
 *  - execution ADVANCE      → ExecutionList = actions for step[index], then index++
 *                             so after it lands, step[index-1] owns ExecutionList
 *  - index >= len(steps)    → COMPLETED
 *  - CANCEL / give-up reset → NONE
 */

export type StepRecord = {
  text: string;
  /** when work on this step began (previous step's actions landed, or plan finished) */
  startedAt: number | null;
  /** when the next step's actions landed, or the task ended */
  endedAt: number | null;
  /** when this step's actions arrived from the AI */
  actionsAt: number | null;
  actions: Execution[];
};

export type Mark = { kind: "paused" | "resumed"; at: number; byYou: boolean; afterStep: number };

/** completed = all steps done · cancelled = you cancelled · ended = reset by someone/something else */
export type Outcome = "completed" | "cancelled" | "ended";

export type Timeline = {
  id: string;
  description: string;
  status: TaskStatus;
  startedAt: number;
  endedAt: number | null;
  outcome: Outcome | null;
  planEndedAt: number | null;
  steps: StepRecord[];
  lastIndex: number;
  marks: Mark[];
  /** true when this phone opened mid-task, so earlier history wasn't observed */
  joinedLate: boolean;
  /** OpenRouter spend on this task so far, in USD (from the server) */
  costUsd?: number;
};

export type TimelineStore = { current: Timeline | null; last: Timeline | null };

/** The last action THIS client sent — used to tell "you paused" from "paused elsewhere". */
export type Intent = { type: ClientAction["type"]; at: number } | null;

export const EMPTY_STORE: TimelineStore = { current: null, last: null };

export function reduceTimeline(store: TimelineStore, snap: Task, now: number, intent: Intent): TimelineStore {
  const byYou = (type: ClientAction["type"]) => !!intent && intent.type === type && now - intent.at < 10_000;
  let cur = store.current;

  // ── Task cleared ──────────────────────────────────────────────
  if (snap.status === "NONE") {
    if (!cur) return store;
    const outcome: Outcome = cur.outcome ?? (byYou("CANCEL_TASK") ? "cancelled" : "ended");
    return {
      current: null,
      last: { ...cur, status: "NONE", outcome, endedAt: cur.endedAt ?? now },
    };
  }

  // ── New task (or first sight of one) ─────────────────────────
  if (!cur || cur.description !== snap.description) {
    const sawStart = snap.planning && snap.instructionList.length === 0;
    cur = {
      id: `${now}-${Math.random().toString(36).slice(2, 7)}`,
      description: snap.description,
      status: snap.status,
      startedAt: now,
      endedAt: null,
      outcome: null,
      planEndedAt: null,
      steps: [],
      lastIndex: sawStart ? -1 : snap.currentInstructionIndex,
      marks: [],
      joinedLate: !sawStart,
    };
  }

  let steps = cur.steps.map((s) => ({ ...s }));
  let planEndedAt = cur.planEndedAt;
  const marks = [...cur.marks];
  const idx = snap.currentInstructionIndex;

  // ── Plan arrived ──────────────────────────────────────────────
  if (snap.instructionList.length > 0 && steps.length !== snap.instructionList.length) {
    steps = snap.instructionList.map(
      (text, i) => steps[i] ?? { text, startedAt: null, endedAt: null, actionsAt: null, actions: [] },
    );
    if (planEndedAt === null) {
      planEndedAt = now;
      if (!cur.joinedLate && steps[0]) steps[0].startedAt = now;
    }
  }

  // ── A batch of actions landed (index moved) ───────────────────
  const joiningNow = cur.steps.length === 0 && cur.joinedLate;
  if (steps.length > 0 && (idx !== cur.lastIndex || joiningNow)) {
    const owner = idx - 1; // step that ExecutionList belongs to
    if (owner >= 0 && owner < steps.length && snap.executionList.length > 0) {
      steps[owner].actions = snap.executionList;
      if (!joiningNow || steps[owner].actionsAt === null) steps[owner].actionsAt = now;
      for (let i = 0; i < owner; i++) {
        if (!joiningNow) steps[i].endedAt ??= now;
      }
      if (steps[owner + 1]) steps[owner + 1].startedAt ??= now;
    }
  }

  // ── Pause / resume ────────────────────────────────────────────
  const active = activeStepIndex({ ...cur, steps });
  if (cur.status === "RUNNING" && snap.status === "PAUSED") {
    marks.push({ kind: "paused", at: now, byYou: byYou("PAUSE_TASK"), afterStep: active });
  } else if (cur.status === "PAUSED" && snap.status === "RUNNING") {
    marks.push({ kind: "resumed", at: now, byYou: byYou("RESUME_TASK"), afterStep: active });
  }

  // ── Finished ──────────────────────────────────────────────────
  let { outcome, endedAt } = cur;
  if (snap.status === "COMPLETED" && !outcome) {
    outcome = "completed";
    endedAt = now;
    for (const s of steps) if (!cur.joinedLate || s.startedAt !== null) s.endedAt ??= now;
  }

  return {
    ...store,
    current: {
      ...cur,
      status: snap.status,
      steps,
      planEndedAt,
      marks,
      outcome,
      endedAt,
      lastIndex: idx,
      costUsd: Math.max(cur.costUsd ?? 0, snap.costUsd),
    },
  };
}

/**
 * The step currently in play: the latest step that has received actions,
 * or step 0 while the AI works out the first batch. -1 = still planning.
 */
export function activeStepIndex(t: Pick<Timeline, "steps">): number {
  if (t.steps.length === 0) return -1;
  for (let i = t.steps.length - 1; i >= 0; i--) if (t.steps[i].actionsAt !== null || t.steps[i].actions.length) return i;
  return 0;
}

export function totalActions(t: Timeline): number {
  return t.steps.reduce((n, s) => n + s.actions.length, 0);
}

// ── Persistence (per device, so a reload keeps the history) ─────
const key = (deviceId: string) => `rc:timeline:v1:${deviceId}`;

export function loadTimeline(deviceId: string): TimelineStore {
  try {
    const raw = localStorage.getItem(key(deviceId));
    return raw ? (JSON.parse(raw) as TimelineStore) : EMPTY_STORE;
  } catch {
    return EMPTY_STORE;
  }
}

export function saveTimeline(deviceId: string, store: TimelineStore) {
  try {
    localStorage.setItem(key(deviceId), JSON.stringify(store));
  } catch {
    /* storage full / private mode — history just won't survive reloads */
  }
}

// ── Formatting ──────────────────────────────────────────────────
export function fmtDuration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
}

/** "$0.012" — enough precision to see per-task spend in cents. */
export function fmtCost(usd: number): string {
  if (usd <= 0) return "$0";
  return usd < 0.01 ? `$${usd.toFixed(4)}` : `$${usd.toFixed(3)}`;
}

export function fmtClock(ts: number): string {
  return new Date(ts).toLocaleTimeString(undefined, { hour: "numeric", minute: "2-digit" });
}
