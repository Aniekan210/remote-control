import { activeStepIndex, fmtCost, fmtDuration, type Timeline } from "@/lib/timeline";
import { StatusBadge } from "./status";

const pad = (n: number) => String(n).padStart(2, "0");

/** Compact, sticky progress readout that stays visible while the transcript scrolls. */
export function TaskSummary({ t, now }: { t: Timeline; now: number }) {
  const total = t.steps.length;
  const done = t.outcome === "completed";
  const paused = t.status === "PAUSED" || t.status === "NEEDS_INPUT";
  const planning = total === 0 && !t.outcome;
  const active = activeStepIndex(t);
  const elapsed = (t.endedAt ?? now) - t.startedAt;

  return (
    <div className="border-b border-line/60 bg-ink/90 backdrop-blur-md">
      <div className="mx-auto max-w-xl px-4 py-3">
        <div className="flex items-center justify-between gap-3">
          <StatusBadge status={t.status} planning={planning} />
          <span className="flex items-center gap-3 font-mono text-[11px] tabular-nums text-dim">
            {total > 0 && <span>{done ? `${pad(total)}/${pad(total)}` : `${pad(active + 1)}/${pad(total)}`}</span>}
            {(t.costUsd ?? 0) > 0 && <span className="text-faint">{fmtCost(t.costUsd ?? 0)}</span>}
            <span className="text-faint">{fmtDuration(elapsed)}</span>
          </span>
        </div>

        <div className="mt-2.5" aria-hidden>
          {planning || total === 0 ? (
            <div className="relative h-1 overflow-hidden rounded-full bg-line">
              {!paused && !t.outcome && (
                <div className="absolute inset-y-0 w-2/5 animate-scan rounded-full bg-signal" />
              )}
            </div>
          ) : (
            <div className="flex gap-1">
              {t.steps.map((_, i) => {
                const state = done || i < active ? "done" : i === active ? "now" : "todo";
                return (
                  <div
                    key={i}
                    className={`h-1 flex-1 rounded-full transition-colors duration-500 ${
                      state === "done"
                        ? done
                          ? "bg-done"
                          : "bg-fg"
                        : state === "now"
                          ? t.outcome
                            ? "bg-line-strong"
                            : paused
                              ? "bg-hold"
                              : "animate-blink bg-signal"
                          : "bg-line"
                    }`}
                  />
                );
              })}
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
