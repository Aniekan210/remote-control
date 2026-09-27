import Link from "next/link";
import { IconSliders } from "./icons";

export function TopBar({ right, below }: { right?: React.ReactNode; below?: React.ReactNode }) {
  return (
    <header className="pt-safe sticky top-0 z-20 border-b border-line/60 bg-ink/85 backdrop-blur-md">
      <div className="mx-auto flex h-14 max-w-xl items-center justify-between px-4">
        <Link href="/" className="flex items-center gap-2.5">
          <Wordmark />
        </Link>
        <div className="flex items-center gap-3">
          {right}
          <Link
            href="/settings"
            aria-label="Settings"
            className="grid size-10 place-items-center rounded-full text-dim transition hover:text-fg active:scale-95"
          >
            <IconSliders />
          </Link>
        </div>
      </div>
      {below}
    </header>
  );
}

export function Wordmark({ size = "sm" }: { size?: "sm" | "lg" }) {
  return (
    <span className={`flex items-center gap-2.5 ${size === "lg" ? "text-xl" : "text-[15px]"}`}>
      <span
        className={`grid place-items-center rounded-[7px] border border-line-strong bg-raised ${
          size === "lg" ? "h-9 w-6" : "h-6 w-4"
        }`}
      >
        <span className={`rounded-full bg-signal ${size === "lg" ? "size-2" : "size-1.5"}`} />
      </span>
      <span className="font-medium tracking-[-0.01em]">Remote Control</span>
    </span>
  );
}
