"use client";

import { useCallback, useEffect, useRef, useState } from "react";

// Minimal typings — the Web Speech API isn't in lib.dom for all TS versions.
type SpeechRecognitionResultLike = { 0: { transcript: string }; isFinal: boolean };
type SpeechRecognitionEventLike = {
  resultIndex: number;
  results: ArrayLike<SpeechRecognitionResultLike>;
};
type Recognition = {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  start(): void;
  stop(): void;
  abort(): void;
  onresult: ((e: SpeechRecognitionEventLike) => void) | null;
  onend: (() => void) | null;
  onerror: ((e: { error: string }) => void) | null;
};
type RecognitionCtor = new () => Recognition;

function getCtor(): RecognitionCtor | null {
  if (typeof window === "undefined") return null;
  const w = window as unknown as {
    SpeechRecognition?: RecognitionCtor;
    webkitSpeechRecognition?: RecognitionCtor;
  };
  return w.SpeechRecognition ?? w.webkitSpeechRecognition ?? null;
}

/**
 * Browser speech-to-text. `onText` receives the full transcript so far
 * (final + interim), so the caller can mirror it into the input live.
 * Supported: Chrome (desktop + Android), Safari 14.5+ (iOS/macOS), Edge.
 * Not supported: Firefox — the mic button just doesn't render there.
 */
export function useSpeech(onText: (text: string) => void) {
  const [supported, setSupported] = useState(false);
  const [listening, setListening] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const recRef = useRef<Recognition | null>(null);
  const onTextRef = useRef(onText);
  onTextRef.current = onText;

  useEffect(() => setSupported(getCtor() !== null), []);

  const stop = useCallback(() => recRef.current?.stop(), []);

  const start = useCallback(() => {
    const Ctor = getCtor();
    if (!Ctor || recRef.current) return;
    setError(null);

    const rec = new Ctor();
    rec.lang = navigator.language || "en-US";
    rec.continuous = false; // stops on a pause — right for one-sentence tasks
    rec.interimResults = true;

    let finalText = "";
    rec.onresult = (e) => {
      let interim = "";
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const r = e.results[i];
        if (r.isFinal) finalText += r[0].transcript;
        else interim += r[0].transcript;
      }
      onTextRef.current((finalText + interim).trim());
    };
    rec.onerror = (e) => {
      if (e.error === "not-allowed" || e.error === "service-not-allowed") {
        setError("Microphone access is blocked for this site.");
      } else if (e.error !== "aborted" && e.error !== "no-speech") {
        setError("Couldn't hear that. Try again.");
      }
    };
    rec.onend = () => {
      recRef.current = null;
      setListening(false);
    };

    recRef.current = rec;
    setListening(true);
    try {
      rec.start();
    } catch {
      recRef.current = null;
      setListening(false);
    }
  }, []);

  useEffect(() => () => recRef.current?.abort(), []);

  return { supported, listening, error, start, stop };
}
