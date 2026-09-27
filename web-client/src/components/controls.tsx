"use client";

import { useEffect, useState } from "react";
import type { TaskStatus } from "@/lib/task";
import { IconPause, IconPlay, IconStop } from "./icons";

export function Controls({
  status,
  busy,
  onPause,
  onResume,
  onCancel,
}: {
  status: TaskStatus;
  busy: boolean;
  onPause: () => void;
  onResume: () => void;
  onCancel: () => void;
}) {
  // Two-tap cancel: a stray thumb shouldn't kill a 10-step task.
  const [armed, setArmed] = useState(false);
  useEffect(() => {
    if (!armed) return;
    const t = setTimeout(() => setArmed(false), 3000);
    return () => clearTimeout(t);
  }, [armed]);

  const paused = status === "PAUSED";

  return (
    <div className="grid grid-cols-2 gap-2">
      {paused ? (
        <button
          onClick={onResume}
          disabled={busy}
          className="flex h-14 items-center justify-center gap-2 rounded-2xl bg-signal font-medium text-ink shadow-[inset_0_1px_0_rgb(255_255_255/0.25)] transition active:scale-[0.98] disabled:opacity-60"
        >
          <IconPlay width={18} height={18} /> Resume
        </button>
      ) : (
        <button
          onClick={onPause}
          disabled={busy}
          className="raised flex h-14 items-center justify-center gap-2 rounded-2xl font-medium transition active:scale-[0.98] disabled:opacity-60"
        >
          <IconPause width={18} height={18} /> Pause
        </button>
      )}

      <button
        onClick={() => {
          if (!armed) return setArmed(true);
          setArmed(false);
          onCancel();
        }}
        disabled={busy}
        className={`flex h-14 items-center justify-center gap-2 rounded-2xl border font-medium transition active:scale-[0.98] disabled:opacity-60 ${
          armed
            ? "border-danger bg-danger text-ink"
            : "border-line text-danger hover:border-danger/50"
        }`}
      >
        <IconStop width={16} height={16} />
        {armed ? "Tap to confirm" : "Cancel"}
      </button>
    </div>
  );
}
