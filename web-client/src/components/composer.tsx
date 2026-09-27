"use client";

import { useEffect, useRef, useState } from "react";
import { IconArrowUp, IconMic } from "./icons";
import { useSpeech } from "./use-speech";

export function Composer({
  disabled,
  placeholder,
  onSubmit,
}: {
  disabled: boolean;
  placeholder: string;
  /** return false to keep the text (e.g. socket not open) */
  onSubmit: (text: string) => boolean;
}) {
  const [text, setText] = useState("");
  const areaRef = useRef<HTMLTextAreaElement>(null);
  const baseRef = useRef("");

  const speech = useSpeech((spoken) => {
    const base = baseRef.current;
    setText(base ? `${base} ${spoken}` : spoken);
  });

  // Auto-grow up to ~6 lines
  useEffect(() => {
    const el = areaRef.current;
    if (!el) return;
    el.style.height = "0px";
    el.style.height = `${Math.min(el.scrollHeight, 160)}px`;
  }, [text]);

  const trimmed = text.trim();
  const canSend = !disabled && trimmed.length > 0;

  function submit() {
    if (!canSend) return;
    if (speech.listening) speech.stop();
    if (onSubmit(trimmed)) setText("");
  }

  function toggleMic() {
    if (speech.listening) return speech.stop();
    baseRef.current = text.trim();
    speech.start();
  }

  return (
    <div>
      {speech.error && <p className="mb-2 px-1 font-mono text-[11px] text-danger">{speech.error}</p>}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
        className={`raised flex items-end gap-2 rounded-[22px] p-2 transition-colors ${
          speech.listening ? "border-signal/60" : "focus-within:border-line-strong"
        }`}
      >
        <textarea
          ref={areaRef}
          rows={1}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            // Enter sends on hardware keyboards; Shift+Enter for a newline.
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              submit();
            }
          }}
          disabled={disabled}
          placeholder={speech.listening ? "Listening…" : placeholder}
          enterKeyHint="send"
          aria-label="Task"
          // 16px minimum: stops iOS Safari from zooming on focus
          className="min-h-11 flex-1 resize-none bg-transparent px-3 py-2.5 text-base leading-6 text-fg outline-none placeholder:text-faint disabled:opacity-50"
        />

        {speech.supported && (
          <button
            type="button"
            onClick={toggleMic}
            disabled={disabled}
            aria-label={speech.listening ? "Stop dictation" : "Dictate task"}
            aria-pressed={speech.listening}
            className={`relative grid size-11 shrink-0 place-items-center rounded-full transition active:scale-95 disabled:opacity-40 ${
              speech.listening ? "bg-signal text-ink" : "text-dim hover:text-fg"
            }`}
          >
            {speech.listening && (
              <span className="absolute inset-0 animate-ping rounded-full bg-signal/40" />
            )}
            <IconMic className="relative" />
          </button>
        )}

        <button
          type="submit"
          disabled={!canSend}
          aria-label="Start task"
          className="grid size-11 shrink-0 place-items-center rounded-full bg-fg text-ink transition active:scale-95 disabled:bg-line disabled:text-faint"
        >
          <IconArrowUp />
        </button>
      </form>
    </div>
  );
}
