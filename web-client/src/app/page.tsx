import Link from "next/link";
import { headers } from "next/headers";
import { requireSession } from "@/lib/session";
import { getDevice } from "@/lib/device";
import { Console } from "@/components/console";
import { TopBar } from "@/components/top-bar";
import { IconDownload, IconMonitor } from "@/components/icons";
import { CopyText } from "@/components/copy-text";
import {
  isMobileUserAgent,
  WORKER_DOWNLOAD_ADDRESS,
  WORKER_DOWNLOAD_URL,
  WORKER_RELEASES_URL,
} from "@/lib/worker-download";

export const dynamic = "force-dynamic";

export default async function Home() {
  const session = await requireSession();
  const device = await getDevice(session.user.id);

  if (!device) {
    const mobile = isMobileUserAgent((await headers()).get("user-agent"));
    return <NoDevice firstName={session.user.name.split(" ")[0]} mobile={mobile} />;
  }

  return <Console deviceId={device.deviceId} deviceLabel={device.name || device.deviceId} />;
}

function NoDevice({ firstName, mobile }: { firstName: string; mobile: boolean }) {
  return (
    <div className="flex min-h-dvh flex-col">
      <TopBar />
      <main className="mx-auto flex w-full max-w-xl flex-1 flex-col px-4 pt-[10vh] pb-10">
        <div className="animate-rise">
          <div className="mb-6 grid size-12 place-items-center rounded-2xl border border-line bg-panel text-dim">
            <IconMonitor />
          </div>
          <p className="label">Hi {firstName}</p>
          <h1 className="mt-3 text-[2.25rem] leading-[1.05] font-medium tracking-[-0.03em] text-balance">
            Link a computer to get started.
          </h1>
          <p className="mt-4 max-w-sm text-[15px] leading-relaxed text-dim">
            Remote Control needs a small app — the worker — running on the Windows PC you want to control. You
            only set this up once.
          </p>

          <ol className="mt-8 space-y-6">
            {mobile ? (
              <Step n={1} title="On your PC, open this address">
                The worker is a Windows app, so download it on the computer, not this phone. Type this into the
                PC&apos;s browser — it downloads the worker straight away:
                <span className="mt-3 block">
                  <CopyText text={WORKER_DOWNLOAD_ADDRESS} copyValue={`https://${WORKER_DOWNLOAD_ADDRESS}`} />
                </span>
              </Step>
            ) : (
              <Step n={1} title="Download the worker">
                Do this on the PC you want to control.
                <span className="mt-3 block">
                  <a
                    href={WORKER_DOWNLOAD_URL}
                    className="flex h-14 items-center justify-center gap-2.5 rounded-2xl bg-fg font-medium text-ink transition active:scale-[0.98]"
                  >
                    <IconDownload width={18} height={18} /> Download for Windows (.exe)
                  </a>
                </span>
                <span className="mt-2 block text-[13px] text-faint">
                  Not this computer? Open <span className="font-mono">{WORKER_DOWNLOAD_ADDRESS}</span> on the right
                  one.
                </span>
              </Step>
            )}
            <Step n={2} title="Run it on the PC">
              Open the downloaded <span className="font-mono text-fg">.exe</span>. If Windows says it protected your
              PC, choose <span className="text-fg">More info → Run anyway</span>. The worker then shows a QR code
              and a device ID.
            </Step>
            <Step n={3} title="Scan the QR code with your phone">
              Use your phone&apos;s camera, or tap <span className="text-fg">Link device</span> below to scan it here
              or type the device ID.
            </Step>
          </ol>

          <p className="mt-6 text-[13px] text-faint">
            Release notes and older versions are on{" "}
            <a href={WORKER_RELEASES_URL} target="_blank" rel="noopener noreferrer" className="text-dim underline">
              GitHub
            </a>
            .
          </p>
        </div>

        <Link
          href="/settings"
          className={`mt-auto flex h-14 items-center justify-center rounded-2xl font-medium transition active:scale-[0.98] ${
            mobile ? "bg-fg text-ink" : "border border-line bg-panel text-fg"
          }`}
        >
          Link device
        </Link>
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
      <div className="min-w-0 flex-1 pt-0.5">
        <p className="text-[15px] font-medium">{title}</p>
        <div className="mt-1 text-[14px] leading-relaxed text-dim">{children}</div>
      </div>
    </li>
  );
}
