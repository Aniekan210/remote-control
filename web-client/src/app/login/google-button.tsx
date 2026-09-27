"use client";

import { useState } from "react";
import { authClient } from "@/lib/auth-client";
import { IconGoogle } from "@/components/icons";

export function GoogleButton({ callbackURL = "/" }: { callbackURL?: string }) {
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  return (
    <>
      <button
        onClick={async () => {
          setPending(true);
          setError(null);
          const { error } = await authClient.signIn.social({ provider: "google", callbackURL });
          // On success the browser is already navigating to Google.
          if (error) {
            setError(error.message ?? "Sign-in failed. Try again.");
            setPending(false);
          }
        }}
        disabled={pending}
        className="flex h-14 w-full items-center justify-center gap-3 rounded-2xl bg-fg font-medium text-ink shadow-[inset_0_-2px_0_rgb(0_0_0/0.12)] transition active:scale-[0.98] disabled:opacity-70"
      >
        <IconGoogle />
        {pending ? "Opening Google…" : "Continue with Google"}
      </button>
      {error && <p className="mt-3 text-center font-mono text-[11px] text-danger">{error}</p>}
    </>
  );
}
