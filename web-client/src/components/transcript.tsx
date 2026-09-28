"use client";

import { Fragment, useState } from "react";
import { humanizeAction, type ActionKind } from "@/lib/actions";
import {
  activeStepIndex,
  fmtClock,
  fmtDuration,
  totalActions,
  type Mark,
  type StepRecord,
  type Timeline,
} from "@/lib/timeline";
import type { Execution } from "@/lib/task";
import {
  IconCheck,
  IconChevron,
  IconClock,
  IconCommand,
  IconCursor,
  IconDot,
  IconFrame,
  IconKeyboard,
  IconMove,
  IconPause,
  IconPlay,
  IconScroll,
  IconStop,
  IconWindow,
  IconX,
} from "./icons";

type RowState = "done" | "current" | "pending" | "stopped";

/**
 * Claude-style live transcript of a task: your request, then the agent's
 * plan → per-step action log → outcome, all timestamped.
 */
export function Transcript({ t, now, live }: { t: Timeline; now: number; live: boolean }) {
  const running = t.status === "RUNNING" && !t.outcome;
  const waiting = t.status === "NEEDS_INPUT" && !t.outcome;
  // Waiting on your answer looks and behaves like a pause in the transcript.
  const paused = t.status === "PAUSED" || waiting;
  const planning = t.steps.length === 0 && !t.outcome;
  const active = activeStepIndex(t);
  const done = t.outcome === "completed";
  const elapsed = (t.endedAt ?? now) - t.startedAt;

  const marksAfter = (i: number) => t.marks.filter((m) => m.afterStep === i);

  return (
    <div className="space-y-6">
      {/* ── Your request ───────────────────────────── */}
      <div className="flex flex-col items-end animate-rise">
        <div className="max-w-[85%] rounded-2xl rounded-tr-md border border-line bg-raised px-4 py-3 text-[15px] leading-relaxed whitespace-pre-wrap">
          {t.description || "Untitled task"}
        </div>
        <span className="mt-1.5 font-mono text-[11px] text-faint">You · {fmtClock(t.startedAt)}</span>
      </div>

      {/* ── Agent turn ─────────────────────────────── */}
      <div className="animate-rise">
        <div className="mb-4 flex items-center gap-2.5">
          <span className="grid h-6 w-4 place-items-center rounded-[6px] border border-line-strong bg-raised">
            <span className={`size-1.5 rounded-full bg-signal ${running ? "animate-blink" : ""}`} />
          </span>
          <span className="text-[14px] font-medium">Remote Control</span>
          <span className="font-mono text-[11px] text-faint">
            {done
              ? `finished in ${fmtDuration(elapsed)}`
              : t.outcome
                ? `stopped after ${fmtDuration(elapsed)}`
                : waiting
                  ? `waiting for you · ${fmtDuration(elapsed)}`
                  : paused
                    ? `paused · ${fmtDuration(elapsed)}`
                  : `working · ${fmtDuration(elapsed)}`}
          </span>
        </div>

        <div className="relative">
          {/* Planning */}
          <Row
            icon={planning ? <Spinner paused={paused} /> : t.steps.length === 0 ? <StoppedDot /> : <DoneDot />}
            last={planning && t.marks.length === 0}
            header={
              t.steps.length === 0 && t.outcome ? (
                <span className="text-dim">Stopped while planning</span>
              ) : planning ? (
                <span className="flex items-baseline justify-between gap-3">
                  <span className={paused ? "text-dim" : "shimmer"}>
                    {paused ? "Planning paused" : "Planning the steps"}
                  </span>
                  <Meta>{fmtDuration(now - t.startedAt)}</Meta>
                </span>
              ) : (
                <span className="flex items-baseline justify-between gap-3">
                  <span className="text-dim">
                    Made a plan · {t.steps.length} step{t.steps.length === 1 ? "" : "s"}
                  </span>
                  {!t.joinedLate && t.planEndedAt && <Meta>{fmtDuration(t.planEndedAt - t.startedAt)}</Meta>}
                </span>
              )
            }
          />
          {marksAfter(-1).map((m, i) => (
            <MarkRow key={`p${i}`} m={m} />
          ))}

          {t.steps.map((s, i) => {
            const state: RowState =
              done || i < active
                ? "done"
                : i === active
                  ? t.outcome
                    ? "stopped"
                    : "current"
                  : "pending";
            const isLast = i === t.steps.length - 1 && !t.outcome && marksAfter(i).length === 0;
            return (
              <Fragment key={i}>
                <StepRow
                  step={s}
                  index={i}
                  state={state}
                  paused={paused}
                  now={now}
                  joinedLate={t.joinedLate}
                  last={isLast}
                />
                {marksAfter(i).map((m, j) => (
                  <MarkRow key={`${i}-${j}`} m={m} />
                ))}
              </Fragment>
            );
          })}

          {t.outcome && <OutcomeRow t={t} />}
        </div>

        {/* Live status line */}
        {!t.outcome && (
          <p className="mt-4 flex items-center gap-2 pl-8 font-mono text-[12px]">
            {!live ? (
              <span className="text-hold">Connection lost — the task keeps running on your computer.</span>
            ) : waiting ? (
              <span className="text-hold">Waiting for your answer below.</span>
            ) : paused ? (
              <span className="text-hold">Paused. Nothing will happen until you resume.</span>
            ) : planning ? (
              <span className="shimmer">Reading your request…</span>
            ) : t.revising ? (
              <span className="shimmer">Revising the plan…</span>
            ) : (
              <span className="shimmer">
                Step {active + 1} of {t.steps.length} in progress
              </span>
            )}
          </p>
        )}
      </div>
    </div>
  );
}

/* ── Rows ─────────────────────────────────────────────────── */

function Row({
  icon,
  header,
  children,
  last,
  onToggle,
  expanded,
}: {
  icon: React.ReactNode;
  header: React.ReactNode;
  children?: React.ReactNode;
  last: boolean;
  onToggle?: () => void;
  expanded?: boolean;
}) {
  const Head = onToggle ? "button" : "div";
  return (
    <div className="relative pb-1 pl-8">
      {/* rail */}
      {!last && <span className="absolute top-6 bottom-0 left-[9px] w-px bg-line" aria-hidden />}
      <span className="absolute top-[3px] left-0 grid size-[19px] place-items-center">{icon}</span>
      <Head
        {...(onToggle ? { type: "button" as const, onClick: onToggle, "aria-expanded": expanded } : {})}
        className={`block w-full py-0.5 text-left text-[15px] leading-snug ${onToggle ? "active:opacity-70" : ""}`}
      >
        {header}
      </Head>
      {children && <div className="pt-2 pb-3">{children}</div>}
      {!children && <div className="h-3" />}
    </div>
  );
}

function StepRow({
  step,
  index,
  state,
  paused,
  now,
  joinedLate,
  last,
}: {
  step: StepRecord;
  index: number;
  state: RowState;
  paused: boolean;
  now: number;
  joinedLate: boolean;
  last: boolean;
}) {
  // null = follow the default (current step open, others closed)
  const [open, setOpen] = useState<boolean | null>(null);
  const expanded = open ?? state === "current";
  const [raw, setRaw] = useState(false);

  const duration =
    step.startedAt === null
      ? null
      : state === "current"
        ? now - step.startedAt
        : step.endedAt !== null
          ? step.endedAt - step.startedAt
          : null;

  const n = step.actions.length;
  const canExpand = state !== "pending";

  const icon =
    state === "done" ? (
      <DoneDot />
    ) : state === "current" ? (
      <Spinner paused={paused} />
    ) : state === "stopped" ? (
      <StoppedDot />
    ) : (
      <PendingDot />
    );

  return (
    <Row
      icon={icon}
      last={last}
      expanded={expanded}
      onToggle={canExpand ? () => setOpen(!expanded) : undefined}
      header={
        <span className="flex items-start gap-3">
          <span className="mt-[3px] w-5 shrink-0 font-mono text-[11px] tabular-nums text-faint">
            {String(index + 1).padStart(2, "0")}
          </span>
          <span
            className={`flex-1 ${
              state === "current" ? "text-fg" : state === "done" || state === "stopped" ? "text-dim" : "text-faint"
            }`}
          >
            {step.text}
          </span>
          {canExpand && (
            <span className="mt-[3px] flex shrink-0 items-center gap-1.5">
              {(n > 0 || duration !== null) && (
                <Meta>
                  {[n > 0 ? `${n} action${n === 1 ? "" : "s"}` : null, duration !== null ? fmtDuration(duration) : null]
                    .filter(Boolean)
                    .join(" · ")}
                </Meta>
              )}
              <IconChevron
                width={14}
                height={14}
                className={`text-faint transition-transform ${expanded ? "rotate-90" : ""}`}
              />
            </span>
          )}
        </span>
      }
    >
      {expanded && canExpand && (
        <div className="ml-8">
          {n === 0 ? (
            <p className="font-mono text-[12px]">
              {state === "current" ? (
                <span className={paused ? "text-hold" : "shimmer"}>
                  {paused ? "Paused before acting" : "Working out what to do…"}
                </span>
              ) : (
                <span className="text-faint">
                  {state === "stopped"
                    ? "Stopped before any actions ran."
                    : joinedLate
                      ? "Finished before this screen was opened."
                      : "No actions were needed."}
                </span>
              )}
            </p>
          ) : (
            <>
              <ul className="space-y-0.5 rounded-xl border border-line bg-panel/70 p-1.5">
                {step.actions.map((a, i) => (
                  <ActionItem key={i} e={a} latest={state === "current" && i === n - 1} />
                ))}
              </ul>
              <div className="mt-1.5 flex items-center justify-between px-1">
                {step.actionsAt && (
                  <span className="font-mono text-[10px] text-faint">received {fmtClock(step.actionsAt)}</span>
                )}
                <button
                  type="button"
                  onClick={() => setRaw(!raw)}
                  className="font-mono text-[10px] text-faint uppercase tracking-[0.08em] hover:text-dim"
                >
                  {raw ? "Hide raw" : "Raw"}
                </button>
              </div>
              {raw && (
                <pre className="mt-1.5 max-h-60 overflow-auto rounded-lg border border-line bg-ink p-3 font-mono text-[11px] leading-relaxed text-dim">
                  {JSON.stringify(step.actions, null, 2)}
                </pre>
              )}
            </>
          )}
        </div>
      )}
    </Row>
  );
}

const KIND_ICON: Record<ActionKind, (p: React.SVGProps<SVGSVGElement>) => React.ReactNode> = {
  click: IconCursor,
  type: IconKeyboard,
  key: IconCommand,
  scroll: IconScroll,
  move: IconMove,
  drag: IconMove,
  wait: IconClock,
  screenshot: IconFrame,
  open: IconWindow,
  other: IconDot,
};

function ActionItem({ e, latest }: { e: Execution; latest: boolean }) {
  const a = humanizeAction(e);
  const Icon = KIND_ICON[a.kind];
  return (
    <li className={`flex gap-3 rounded-lg px-2.5 py-2 ${latest ? "bg-raised" : ""}`}>
      <Icon width={15} height={15} className={`mt-0.5 shrink-0 ${latest ? "text-signal" : "text-faint"}`} />
      <div className="min-w-0 flex-1">
        <p className="text-[14px] leading-snug break-words text-fg">{a.title}</p>
        {a.detail && <p className="mt-0.5 font-mono text-[11px] break-all text-faint">{a.detail}</p>}
        {a.reason && <p className="mt-1 text-[13px] leading-snug text-dim">{a.reason}</p>}
      </div>
    </li>
  );
}

function MarkRow({ m }: { m: Mark }) {
  if (m.kind === "question" || m.kind === "answer") return <QuestionMarkRow m={m} />;
  if (m.kind === "replan") {
    return (
      <Row
        icon={<IconDot width={12} height={12} className="text-signal" />}
        last={false}
        header={
          <span className="flex items-start justify-between gap-3 text-[13px]">
            <span className="leading-snug text-dim">Plan updated{m.text ? `: ${m.text}` : ""}</span>
            <Meta>{fmtClock(m.at)}</Meta>
          </span>
        }
      />
    );
  }
  const Icon = m.kind === "paused" ? IconPause : IconPlay;
  return (
    <Row
      icon={<Icon width={12} height={12} className={m.kind === "paused" ? "text-hold" : "text-dim"} />}
      last={false}
      header={
        <span className="flex items-baseline justify-between gap-3 text-[13px]">
          <span className={m.kind === "paused" ? "text-hold" : "text-dim"}>
            {m.kind === "paused" ? "Paused" : "Resumed"} {m.byYou ? "by you" : "from another device"}
          </span>
          <Meta>{fmtClock(m.at)}</Meta>
        </span>
      }
    />
  );
}

function QuestionMarkRow({ m }: { m: Mark }) {
  const asking = m.kind === "question";
  return (
    <Row
      icon={
        asking ? (
          <span className="grid size-[19px] place-items-center rounded-full border border-hold/60 font-mono text-[11px] text-hold">
            ?
          </span>
        ) : (
          <IconCheck width={12} height={12} className="text-dim" />
        )
      }
      last={false}
      header={
        <span className="flex items-start justify-between gap-3 text-[13px]">
          <span className={`leading-snug ${asking ? "text-hold" : "text-dim"}`}>
            {asking ? (
              <>Needs your input: {m.text || "a question"}</>
            ) : (
              <>
                {m.byYou ? "You" : "Answered from another device"}: {m.text}
              </>
            )}
          </span>
          <Meta>{fmtClock(m.at)}</Meta>
        </span>
      }
    />
  );
}

function OutcomeRow({ t }: { t: Timeline }) {
  const elapsed = (t.endedAt ?? t.startedAt) - t.startedAt;
  const actions = totalActions(t);
  if (t.outcome === "completed") {
    return (
      <Row
        icon={
          <span className="grid size-[19px] place-items-center rounded-full bg-done text-ink">
            <IconCheck width={12} height={12} strokeWidth={2.5} />
          </span>
        }
        last
        header={
          <span>
            <span className="font-medium text-done">Done</span>
            <span className="mt-1 block font-mono text-[11px] text-faint">
              {t.steps.length} steps · {actions} actions · {fmtDuration(elapsed)}
            </span>
          </span>
        }
      />
    );
  }
  return (
    <Row
      icon={
        <span className="grid size-[19px] place-items-center rounded-full border border-danger/60 text-danger">
          <IconStop width={9} height={9} />
        </span>
      }
      last
      header={
        <span>
          <span className="font-medium text-danger">{t.outcome === "cancelled" ? "Cancelled by you" : "Stopped"}</span>
          <span className="mt-1 block text-[13px] leading-snug text-dim">
            {t.outcome === "cancelled"
              ? `After ${fmtDuration(elapsed)}.`
              : "It was cancelled from another device, or the server gave up after repeated errors."}
          </span>
        </span>
      }
    />
  );
}

/* ── Small parts ──────────────────────────────────────────── */

function Meta({ children }: { children: React.ReactNode }) {
  return <span className="shrink-0 font-mono text-[11px] tabular-nums text-faint">{children}</span>;
}

function Spinner({ paused }: { paused: boolean }) {
  if (paused) {
    return (
      <span className="grid size-[19px] place-items-center rounded-full border border-hold/60 text-hold">
        <IconPause width={10} height={10} strokeWidth={2.5} />
      </span>
    );
  }
  return (
    <svg viewBox="0 0 20 20" className="size-[19px] animate-spin text-signal" aria-hidden>
      <circle cx="10" cy="10" r="8" fill="none" stroke="currentColor" strokeOpacity="0.2" strokeWidth="2" />
      <path d="M10 2a8 8 0 0 1 8 8" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

function DoneDot() {
  return (
    <span className="grid size-[19px] place-items-center rounded-full border border-line-strong text-dim">
      <IconCheck width={11} height={11} strokeWidth={2.25} />
    </span>
  );
}

function StoppedDot() {
  return (
    <span className="grid size-[19px] place-items-center rounded-full border border-line-strong text-faint">
      <IconX width={10} height={10} strokeWidth={2.25} />
    </span>
  );
}

function PendingDot() {
  return <span className="size-[9px] rounded-full border border-line-strong bg-ink" />;
}
