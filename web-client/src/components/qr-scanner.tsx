"use client";

import { useEffect, useRef, useState } from "react";
import { extractDeviceId } from "@/lib/device-id";

type Detector = { detect(src: CanvasImageSource): Promise<{ rawValue: string }[]> };
type DetectorCtor = new (opts: { formats: string[] }) => Detector;

/**
 * Full-screen camera QR scanner.
 * Uses the native BarcodeDetector where it exists (Chrome/Android) and falls
 * back to jsQR (iOS Safari has no BarcodeDetector). Camera needs HTTPS
 * (or localhost).
 */
export function QrScanner({ onResult, onClose }: { onResult: (deviceId: string) => void; onClose: () => void }) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const [error, setError] = useState<string | null>(null);
  const [hint, setHint] = useState<string | null>(null);
  const onResultRef = useRef(onResult);
  onResultRef.current = onResult;

  useEffect(() => {
    let stream: MediaStream | null = null;
    let raf = 0;
    let stopped = false;
    let lastScan = 0;
    let busy = false;

    const canvas = document.createElement("canvas");
    const ctx = canvas.getContext("2d", { willReadFrequently: true });

    async function start() {
      if (!window.isSecureContext || !navigator.mediaDevices?.getUserMedia) {
        setError("The camera needs a secure (https) connection. Enter the ID manually instead.");
        return;
      }
      try {
        stream = await navigator.mediaDevices.getUserMedia({
          video: { facingMode: { ideal: "environment" } },
          audio: false,
        });
      } catch (e) {
        const name = (e as DOMException).name;
        setError(
          name === "NotAllowedError"
            ? "Camera access was denied. Allow it in your browser settings, or enter the ID manually."
            : "Couldn't open the camera. Enter the ID manually instead.",
        );
        return;
      }
      if (stopped) return stream.getTracks().forEach((t) => t.stop());

      const video = videoRef.current!;
      video.srcObject = stream;
      await video.play().catch(() => {});

      const Native = (window as unknown as { BarcodeDetector?: DetectorCtor }).BarcodeDetector;
      const native = Native ? new Native({ formats: ["qr_code"] }) : null;
      const jsQR = native ? null : (await import("jsqr")).default;

      const tick = async (now: number) => {
        if (stopped) return;
        raf = requestAnimationFrame(tick);
        if (busy || now - lastScan < 150 || video.readyState < 2) return;
        lastScan = now;
        busy = true;
        try {
          let text: string | null = null;
          if (native) {
            const codes = await native.detect(video);
            text = codes[0]?.rawValue ?? null;
          } else if (jsQR && ctx) {
            // Downscale: faster decode, still plenty of pixels for a QR
            const scale = Math.min(1, 640 / Math.max(video.videoWidth, video.videoHeight));
            canvas.width = Math.round(video.videoWidth * scale);
            canvas.height = Math.round(video.videoHeight * scale);
            ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
            const img = ctx.getImageData(0, 0, canvas.width, canvas.height);
            text = jsQR(img.data, img.width, img.height, { inversionAttempts: "dontInvert" })?.data ?? null;
          }
          if (text) {
            const id = extractDeviceId(text);
            if (id) {
              stopped = true;
              navigator.vibrate?.(30);
              onResultRef.current(id);
              return;
            }
            setHint("That QR code isn't a Remote Control device code.");
          }
        } catch {
          /* transient decode error — keep scanning */
        } finally {
          busy = false;
        }
      };
      raf = requestAnimationFrame(tick);
    }

    start();
    return () => {
      stopped = true;
      cancelAnimationFrame(raf);
      stream?.getTracks().forEach((t) => t.stop());
    };
  }, []);

  // Close on Escape (desktop)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-ink" role="dialog" aria-label="Scan device QR code">
      <video ref={videoRef} playsInline muted className="absolute inset-0 size-full object-cover" />

      {/* Dimmed surround with a clear square window */}
      {!error && (
        <div className="pointer-events-none absolute inset-0 grid place-items-center">
          <div className="relative size-[min(70vw,300px)] rounded-3xl shadow-[0_0_0_100vmax_rgb(11_11_10/0.72)]">
            {["top-0 left-0 border-t-2 border-l-2 rounded-tl-3xl", "top-0 right-0 border-t-2 border-r-2 rounded-tr-3xl", "bottom-0 left-0 border-b-2 border-l-2 rounded-bl-3xl", "bottom-0 right-0 border-b-2 border-r-2 rounded-br-3xl"].map((c) => (
              <span key={c} className={`absolute size-10 border-signal ${c}`} />
            ))}
            <span className="absolute inset-x-6 top-1/2 h-px animate-blink bg-signal/70" />
          </div>
        </div>
      )}

      <div className="pt-safe relative flex items-center justify-between px-4 pt-4">
        <p className="label text-fg!">Scan device code</p>
        <button
          onClick={onClose}
          className="raised h-10 rounded-full px-4 text-[14px] font-medium active:scale-95"
        >
          Close
        </button>
      </div>

      <div className="pb-safe relative mt-auto px-6 pb-8 text-center">
        {error ? (
          <p className="mx-auto max-w-xs text-[15px] leading-relaxed text-dim">{error}</p>
        ) : (
          <>
            <p className="text-[15px] text-fg">Point at the QR code on your computer.</p>
            <p className={`mt-2 font-mono text-[11px] ${hint ? "text-hold" : "text-dim"}`}>
              {hint ?? "The Remote Control worker shows it on screen."}
            </p>
          </>
        )}
      </div>
    </div>
  );
}
