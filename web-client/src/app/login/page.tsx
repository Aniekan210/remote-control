import { redirect } from "next/navigation";
import { getSession, safeNext } from "@/lib/session";
import { Wordmark } from "@/components/top-bar";
import { GoogleButton } from "./google-button";

export const dynamic = "force-dynamic";

export default async function LoginPage({ searchParams }: { searchParams: Promise<{ next?: string }> }) {
  const next = safeNext((await searchParams).next);
  if (await getSession()) redirect(next);

  return (
    <div className="relative flex min-h-dvh flex-col overflow-hidden">
      {/* faint dot grid, fading out toward the bottom */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 opacity-60 [mask-image:linear-gradient(to_bottom,black,transparent_70%)]"
        style={{
          backgroundImage: "radial-gradient(circle, var(--color-line-strong) 1px, transparent 1px)",
          backgroundSize: "22px 22px",
        }}
      />

      <main className="pt-safe pb-safe relative mx-auto flex w-full max-w-xl flex-1 flex-col px-5">
        <div className="pt-6">
          <Wordmark />
        </div>

        <div className="mt-[18vh] animate-rise">
          <p className="label">v0.1 · Early access</p>
          <h1 className="mt-4 text-[2.75rem] leading-[0.98] font-medium tracking-[-0.035em] sm:text-6xl">
            Your computer,
            <br />
            <span className="text-dim">from anywhere.</span>
          </h1>
          <p className="mt-6 max-w-sm text-[15px] leading-relaxed text-dim">
            Say what you need done. It plans the steps and drives your mouse and keyboard while you
            watch it happen, live.
          </p>
        </div>

        <div className="mt-auto pt-12 pb-4">
          <GoogleButton callbackURL={next} />
          <p className="mt-4 text-center font-mono text-[11px] text-faint">
            One account · one computer · any phone
          </p>
        </div>
      </main>
    </div>
  );
}
