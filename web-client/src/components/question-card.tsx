"use client";

import { useEffect, useState } from "react";
import type { Task } from "@/lib/task";
import { Composer } from "./composer";
import { IconCheck, IconPlay, IconStop, IconX } from "./icons";

/**
 * Shown in the bottom dock while the task is NEEDS_INPUT: what the screen
 * looked like when it got stuck, the question, and a way to answer.
 *  - confirm: Approve (sends exactly "approve"), or say what to do instead
 *  - budget:  Continue (a fresh allowance of calls/spend/time)
 *  - blocked: free-text answer, typed or dictated
 * Cancel works as everywhere else (two taps).
 */
export function QuestionCard({
  task,
  busy,
  onAnswer,
  onCancel,
}: {
  task: Task;
  busy: boolean;
  /** return false to keep the text (e.g. socket not open) */
  onAnswer: (text: string) => boolean;
  onCancel: () => void;
}) {
  const [zoomed, setZoomed] = useState(false);
  const [armed, setArmed] = useState(false);
  const [instead, setInstead] = useState(false);
  useEffect(() => {
    if (!armed) return;
    const t = setTimeout(() => setArmed(false), 3000);
    return () => clearTimeout(t);
  }, [armed]);

  const kind = task.questionKind || "blocked";
  const label = kind === "confirm" ? "Approve this step?" : kind === "budget" ? "Keep going?" : "Needs your input";

  const cancel = (
    <button
      onClick={() => {
        if (!armed) return setArmed(true);
        setArmed(false);
        onCancel();
      }}
      disabled={busy}
      className={`flex h-14 items-center justify-center gap-2 rounded-2xl border font-medium transition active:scale-[0.98] disabled:opacity-60 ${
        armed ? "border-danger bg-danger text-ink" : "border-line text-danger hover:border-danger/50"
      }`}
    >
      <IconStop width={16} height={16} />
      {armed ? "Tap to confirm" : "Cancel task"}
    </button>
  );

  return (
    <div className="space-y-3">
      <div className="rounded-2xl border border-hold/40 bg-panel p-3">
        <div className="flex gap-3">
          {task.questionImage && (
            <button
              type="button"
              onClick={() => setZoomed(true)}
              aria-label="View the screen full size"
              className="shrink-0 overflow-hidden rounded-lg border border-line active:opacity-80"
            >
              {/* eslint-disable-next-line @next/next/no-img-element */}
              <img src={task.questionImage} alt="The computer's screen when it stopped" className="h-20 w-28 object-cover" />
            </button>
          )}
          <div className="min-w-0 flex-1">
            <p className="label text-hold!">{label}</p>
            <p className="mt-1.5 max-h-28 overflow-y-auto text-[15px] leading-snug whitespace-pre-wrap">
              {task.question || "The task needs your input to continue."}
            </p>
          </div>
        </div>
      </div>

      {kind === "confirm" && !instead && (
        <>
          <div className="grid grid-cols-2 gap-2">
            <button
              onClick={() => onAnswer("approve")}
              disabled={busy}
              className="flex h-14 items-center justify-center gap-2 rounded-2xl bg-signal font-medium text-ink shadow-[inset_0_1px_0_rgb(255_255_255/0.25)] transition active:scale-[0.98] disabled:opacity-60"
            >
              <IconCheck width={18} height={18} /> Approve
            </button>
            {cancel}
          </div>
          <button
            type="button"
            onClick={() => setInstead(true)}
            disabled={busy}
            className="w-full py-1 text-[13px] text-dim hover:text-fg disabled:opacity-60"
          >
            Do something else instead…
          </button>
        </>
      )}

      {kind === "budget" && (
        <div className="grid grid-cols-2 gap-2">
          <button
            onClick={() => onAnswer("continue")}
            disabled={busy}
            className="flex h-14 items-center justify-center gap-2 rounded-2xl bg-signal font-medium text-ink shadow-[inset_0_1px_0_rgb(255_255_255/0.25)] transition active:scale-[0.98] disabled:opacity-60"
          >
            <IconPlay width={18} height={18} /> Continue
          </button>
          {cancel}
        </div>
      )}

      {(kind === "blocked" || (kind === "confirm" && instead)) && (
        <>
          <Composer
            disabled={busy}
            placeholder={kind === "confirm" ? "What should it do instead?" : "Tell it what to do…"}
            onSubmit={onAnswer}
          />
          <div className={kind === "confirm" ? "grid grid-cols-2 gap-2" : "grid"}>
            {kind === "confirm" && (
              <button
                type="button"
                onClick={() => setInstead(false)}
                disabled={busy}
                className="raised flex h-14 items-center justify-center rounded-2xl font-medium transition active:scale-[0.98] disabled:opacity-60"
              >
                Back
              </button>
            )}
            {cancel}
          </div>
        </>
      )}

      {zoomed && task.questionImage && (
        <div
          role="dialog"
          aria-label="Screen"
          onClick={() => setZoomed(false)}
          className="fixed inset-0 z-50 grid place-items-center bg-black/90 p-2"
        >
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img src={task.questionImage} alt="The computer's screen when it stopped" className="max-h-full max-w-full object-contain" />
          <button
            type="button"
            aria-label="Close"
            className="pt-safe absolute top-3 right-3 grid size-11 place-items-center rounded-full bg-ink/70 text-fg"
          >
            <IconX width={18} height={18} />
          </button>
        </div>
      )}
    </div>
  );
}
