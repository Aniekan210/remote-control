import Link from "next/link";
import { requireSession } from "@/lib/session";
import { getDevice } from "@/lib/device";
import { extractDeviceId } from "@/lib/device-id";
import { IconBack } from "@/components/icons";
import { getKeyInfo } from "@/lib/openrouter-key";
import { ApiKeyCard, DeviceCard, DeviceForm, SignOutButton } from "./client";

export const dynamic = "force-dynamic";

export default async function SettingsPage({
  searchParams,
}: {
  searchParams: Promise<{ device?: string }>;
}) {
  const { device: rawIncoming } = await searchParams;
  const session = await requireSession(
    rawIncoming ? `/settings?device=${encodeURIComponent(rawIncoming)}` : "/settings",
  );
  const device = await getDevice(session.user.id);
  const apiKey = await getKeyInfo(session.user.id).catch((err) => {
    console.error("getKeyInfo", err);
    return null;
  });
  const { user } = session;

  // Scanning the worker's QR with the phone's normal camera app can open
  // /settings?device=<id> directly — prefill the form from it.
  const incoming = rawIncoming ? extractDeviceId(rawIncoming) : null;
  const incomingDiffers = incoming && incoming !== device?.deviceId ? incoming : undefined;

  return (
    <div className="min-h-dvh">
      <header className="pt-safe sticky top-0 z-20 border-b border-line/60 bg-ink/85 backdrop-blur-md">
        <div className="mx-auto flex h-14 max-w-xl items-center gap-2 px-2">
          <Link
            href="/"
            aria-label="Back"
            className="grid size-10 place-items-center rounded-full text-dim transition hover:text-fg active:scale-95"
          >
            <IconBack />
          </Link>
          <h1 className="text-[15px] font-medium">Settings</h1>
        </div>
      </header>

      <main className="pb-safe mx-auto max-w-xl space-y-10 px-4 pt-8">
        <Section title="Device" hint="One computer per account. Linking a new one replaces the old.">
          {device ? (
            <DeviceCard
              deviceId={device.deviceId}
              name={device.name}
              linkedAt={device.linkedAt}
              incomingDeviceId={incomingDiffers}
            />
          ) : (
            <DeviceForm initialDeviceId={incoming ?? ""} />
          )}
        </Section>

        <Section
          title="OpenRouter API key"
          hint="Your tasks run on your own OpenRouter key. It's stored encrypted and never shown again."
        >
          <ApiKeyCard last4={apiKey?.last4 ?? null} updatedAt={apiKey?.updatedAt ?? null} />
        </Section>

        <Section title="Account">
          <div className="flex items-center gap-3 rounded-2xl border border-line bg-panel p-4">
            {user.image ? (
              // eslint-disable-next-line @next/next/no-img-element
              <img
                src={user.image}
                alt=""
                referrerPolicy="no-referrer"
                className="size-10 rounded-full border border-line"
              />
            ) : (
              <div className="grid size-10 place-items-center rounded-full bg-raised font-medium text-dim">
                {user.name.charAt(0).toUpperCase()}
              </div>
            )}
            <div className="min-w-0 flex-1">
              <p className="truncate text-[15px] font-medium">{user.name}</p>
              <p className="truncate font-mono text-[12px] text-dim">{user.email}</p>
            </div>
          </div>
          <SignOutButton />
        </Section>

        <p className="pb-6 text-center font-mono text-[11px] text-faint">Remote Control · v0.1</p>
      </main>
    </div>
  );
}

function Section({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return (
    <section>
      <h2 className="label">{title}</h2>
      {hint && <p className="mt-1.5 text-[13px] text-faint">{hint}</p>}
      <div className="mt-4 space-y-3">{children}</div>
    </section>
  );
}
