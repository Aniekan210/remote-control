"use client";

import { useState } from "react";
import { IconCheck, IconCopy } from "./icons";

/** A value shown in mono with a copy button — for addresses people will type on another device. */
export function CopyText({ text, copyValue = text }: { text: string; copyValue?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(copyValue);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          // clipboard blocked — the text is still on screen to read
        }
      }}
      className="flex h-14 w-full items-center justify-between gap-3 rounded-2xl border border-line bg-panel px-4 text-left transition active:scale-[0.98]"
    >
      <span className="truncate font-mono text-[15px] text-fg">{text}</span>
      <span className="flex shrink-0 items-center gap-1.5 font-mono text-[11px] text-dim">
        {copied ? <IconCheck width={14} height={14} /> : <IconCopy width={14} height={14} />}
        {copied ? "Copied" : "Copy"}
      </span>
    </button>
  );
}
