import { NextResponse } from "next/server";
import { getSession } from "@/lib/session";
import { getDevice } from "@/lib/device";
import { buildWsUrl } from "@/lib/control-server";

export const dynamic = "force-dynamic";

/**
 * Called by the browser before every (re)connect: checks the session and
 * resolves the signed-in user's linked device into a WS URL. Keeping this
 * server-side means the device ID always comes from the account, so a phone
 * that signs in later controls the same computer with no re-linking.
 */
export async function GET() {
  const session = await getSession();
  if (!session) return NextResponse.json({ error: "unauthorized" }, { status: 401 });

  const device = await getDevice(session.user.id);
  if (!device) return NextResponse.json({ error: "no_device" }, { status: 404 });

  const url = buildWsUrl(device.deviceId);
  if (!url) return NextResponse.json({ error: "CONTROL_WS_URL not set" }, { status: 500 });

  return NextResponse.json({ url }, { headers: { "Cache-Control": "no-store" } });
}
