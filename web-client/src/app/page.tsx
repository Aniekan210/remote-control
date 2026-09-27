import Link from "next/link";
import { requireSession } from "@/lib/session";
import { getDevice } from "@/lib/device";
import { Console } from "@/components/console";
import { TopBar } from "@/components/top-bar";
import { IconMonitor } from "@/components/icons";

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
            Start the Remote Control worker on your PC and scan the QR code it shows. You only do
            this once — the device follows your account to any phone you sign in on.
          </p>
        </div>
        <Link
          href="/settings"
          className="mt-auto flex h-14 items-center justify-center rounded-2xl bg-fg font-medium text-ink transition active:scale-[0.98]"
        >
          Link device
        </Link>
      </main>
    </div>
  );
}
