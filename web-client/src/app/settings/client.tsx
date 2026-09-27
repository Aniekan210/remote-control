"use client";

import { useActionState, useEffect, useRef, useState, useTransition } from "react";
import { useRouter } from "next/navigation";
import { authClient } from "@/lib/auth-client";
import { IconCheck, IconCopy, IconMonitor } from "@/components/icons";
import { QrScanner } from "@/components/qr-scanner";
import { linkDevice, unlinkDevice, type LinkState } from "./actions";

const inputCls =
  "h-12 w-full rounded-xl border border-line bg-ink px-3.5 text-base text-fg outline-none transition placeholder:text-faint focus:border-line-strong";

export function DeviceForm({ initialDeviceId = "", onDone }: { initialDeviceId?: string; onDone?: () => void }) {
  const [state, action, pending] = useActionState<LinkState, FormData>(linkDevice, null);
  const [deviceId, setDeviceId] = useState(initialDeviceId);
  const [scanning, setScanning] = useState(false);
  const [scanned, setScanned] = useState(Boolean(initialDeviceId));
  const nameRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (state?.ok) onDone?.();
  }, [state, onDone]);

  return (
    <>
      {scanning && (
        <QrScanner
          onClose={() => setScanning(false)}
          onResult={(id) => {
            setDeviceId(id);
            setScanned(true);
            setScanning(false);
            nameRef.current?.focus();
          }}
        />
      )}

      <form action={action} className="space-y-4 rounded-2xl border border-line bg-panel p-4">
        {!scanned && (
          <>
            <button
              type="button"
              onClick={() => setScanning(true)}
              className="flex h-14 w-full items-center justify-center gap-2.5 rounded-xl bg-fg font-medium text-ink transition active:scale-[0.98]"
            >
              <IconQr /> Scan QR code
            </button>
            <div className="flex items-center gap-3" aria-hidden>
              <span className="h-px flex-1 bg-line" />
              <span className="label text-faint!">or enter it</span>
              <span className="h-px flex-1 bg-line" />
            </div>
          </>
        )}

        <label className="block">
          <span className="flex items-center justify-between">
            <span className="label">Device ID</span>
            {scanned && (
              <span className="inline-flex items-center gap-1 font-mono text-[11px] text-done">
                <IconCheck width={12} height={12} /> Scanned
              </span>
            )}
          </span>
          <input
            name="deviceId"
            required
            value={deviceId}
            onChange={(e) => {
              setDeviceId(e.target.value);
              setScanned(false);
            }}
            autoComplete="off"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            placeholder="Shown by the worker on your PC"
            className={`${inputCls} mt-2 font-mono`}
          />
        </label>
        <label className="block">
          <span className="label">Name · optional</span>
          <input ref={nameRef} name="name" maxLength={60} placeholder="Desk PC" className={`${inputCls} mt-2`} />
        </label>

        {state?.error && <p className="font-mono text-[12px] text-danger">{state.error}</p>}

        <button
          disabled={pending || !deviceId.trim()}
          className={`h-12 w-full rounded-xl font-medium transition active:scale-[0.98] disabled:opacity-50 ${
            scanned ? "bg-fg text-ink" : "raised text-fg"
          }`}
        >
          {pending ? "Linking…" : "Link device"}
        </button>
      </form>
    </>
  );
}

function IconQr() {
  return (
    <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden>
      <rect x="3.5" y="3.5" width="6" height="6" rx="1" />
      <rect x="14.5" y="3.5" width="6" height="6" rx="1" />
      <rect x="3.5" y="14.5" width="6" height="6" rx="1" />
      <path d="M14.5 14.5h2.5v2.5M20.5 14.5v.01M14.5 20.5h.01M17.5 20.5h3v-3" strokeLinecap="round" />
    </svg>
  );
}

export function DeviceCard({
  deviceId,
  name,
  linkedAt,
  incomingDeviceId,
}: {
  deviceId: string;
  name: string | null;
  linkedAt: string;
  /** From a scanned deep link (?device=…) that differs from the linked one */
  incomingDeviceId?: string;
}) {
  const [replacing, setReplacing] = useState(Boolean(incomingDeviceId));
  const [confirming, setConfirming] = useState(false);
  const [copied, setCopied] = useState(false);
  const [pending, startTransition] = useTransition();

  if (replacing) {
    return (
      <>
        <DeviceForm initialDeviceId={incomingDeviceId} onDone={() => setReplacing(false)} />
        <button onClick={() => setReplacing(false)} className="w-full py-2 text-[13px] text-dim">
          Keep current device
        </button>
      </>
    );
  }

  const date = new Date(linkedAt).toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });

  return (
    <div className="rounded-2xl border border-line bg-panel">
      <div className="flex items-start gap-3 p-4">
        <div className="grid size-10 shrink-0 place-items-center rounded-xl border border-line bg-raised text-dim">
          <IconMonitor width={18} height={18} />
        </div>
        <div className="min-w-0 flex-1">
          <p className="truncate text-[15px] font-medium">{name || "Unnamed computer"}</p>
          <button
            onClick={async () => {
              await navigator.clipboard.writeText(deviceId);
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            }}
            className="mt-0.5 flex max-w-full items-center gap-1.5 font-mono text-[12px] text-dim"
          >
            <span className="truncate">{deviceId}</span>
            {copied ? <IconCheck width={13} height={13} /> : <IconCopy width={13} height={13} />}
          </button>
          <p className="mt-2 font-mono text-[11px] text-faint">Linked {date}</p>
        </div>
      </div>
      <div className="grid grid-cols-2 border-t border-line text-[14px]">
        <button onClick={() => setReplacing(true)} className="h-12 border-r border-line text-fg active:bg-raised">
          Replace
        </button>
        <button
          disabled={pending}
          onClick={() => {
            if (!confirming) {
              setConfirming(true);
              setTimeout(() => setConfirming(false), 3000);
              return;
            }
            startTransition(() => unlinkDevice());
          }}
          className="h-12 text-danger active:bg-raised disabled:opacity-60"
        >
          {pending ? "Unlinking…" : confirming ? "Tap to confirm" : "Unlink"}
        </button>
      </div>
    </div>
  );
}

export function SignOutButton() {
  const router = useRouter();
  const [pending, setPending] = useState(false);
  return (
    <button
      disabled={pending}
      onClick={async () => {
        setPending(true);
        await authClient.signOut();
        router.replace("/login");
        router.refresh();
      }}
      className="h-12 w-full rounded-xl border border-line text-[14px] text-dim transition hover:text-fg active:scale-[0.99] disabled:opacity-60"
    >
      {pending ? "Signing out…" : "Sign out"}
    </button>
  );
}
