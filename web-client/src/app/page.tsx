import Link from "next/link";
import { requireSession } from "@/lib/session";
import { getDevice } from "@/lib/device";
import { Console } from "@/components/console";
import { TopBar } from "@/components/top-bar";
import { IconDownload, IconMonitor } from "@/components/icons";
import { WORKER_DOWNLOAD_PATH, WORKER_DOWNLOAD_URL } from "@/lib/worker-download";

export const dynamic = "force-dynamic";

export default async function Home() {
  const session = await requireSession();
  const device = await getDevice(session.user.id);

  if (!device) return <NoDevice firstName={session.user.name.split(" ")[0]} />;

  return <Console deviceId={device.deviceId} deviceLabel={device.name || device.deviceId} />;
}

function NoDevice({ firstName }: { firstName: string }) {
  return (
    <div className="flex min-h-dvh flex-col">
      <TopBar />
      <main className="mx-auto flex w-full max-w-xl flex-1 flex-col px-4 pt-[14vh] pb-10">
        <div className="animate-rise">
          <div className="mb-6 grid size-12 place-items-center rounded-2xl border border-line bg-panel text-dim">
            <IconMonitor />
          </div>
          <p className="label">Hi {firstName}</p>
          <h1 className="mt-3 text-[2.25rem] leading-[1.05] font-medium tracking-[-0.03em] text-balance">
            Link a computer to get started.
          </h1>
          <p className="mt-4 max-w-sm text-[15px] leading-relaxed text-dim">
            You only do this once — the device follows your account to any phone you sign in on.
          </p>

          <ol className="mt-8 space-y-5">
            <Step n={1} title="Download the worker on your PC">
              It&apos;s a Windows app, free on GitHub. On the PC, go to{" "}
              <span className="font-mono text-fg">control.aniekan.dev{WORKER_DOWNLOAD_PATH}</span> or use the button
              below.
            </Step>
            <Step n={2} title="Run it">
              If Windows warns about an unrecognised app, choose <span className="text-fg">More info → Run anyway</span>.
              It shows a QR code.
            </Step>
            <Step n={3} title="Scan the QR code">
              With your phone&apos;s camera, or tap <span className="text-fg">Link device</span> below.
            </Step>
          </ol>
        </div>

        <div className="mt-auto space-y-3 pt-10">
          <a
            href={WORKER_DOWNLOAD_URL}
            target="_blank"
            rel="noopener noreferrer"
            className="flex h-14 items-center justify-center gap-2.5 rounded-2xl border border-line bg-panel font-medium text-fg transition active:scale-[0.98]"
          >
            <IconDownload width={18} height={18} /> Download for Windows
          </a>
          <Link
            href="/settings"
            className="flex h-14 items-center justify-center rounded-2xl bg-fg font-medium text-ink transition active:scale-[0.98]"
          >
            Link device
          </Link>
        </div>
      </main>
    </div>
  );
}

function Step({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <li className="flex gap-3.5">
      <span className="grid size-7 shrink-0 place-items-center rounded-full border border-line bg-panel font-mono text-[12px] text-dim">
        {n}
      </span>
      <div className="pt-0.5">
        <p className="text-[15px] font-medium">{title}</p>
        <p className="mt-1 text-[14px] leading-relaxed text-dim">{children}</p>
      </div>
    </li>
  );
}
