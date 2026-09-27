"use server";

import { revalidatePath } from "next/cache";
import { requireSession } from "@/lib/session";
import { DeviceTakenError, removeDevice, upsertDevice } from "@/lib/device";
import { DEVICE_ID_PATTERN } from "@/lib/device-id";

export type LinkState = { ok: boolean; error?: string } | null;

export async function linkDevice(_prev: LinkState, form: FormData): Promise<LinkState> {
  const session = await requireSession();

  const deviceId = String(form.get("deviceId") ?? "").trim();
  const rawName = String(form.get("name") ?? "").trim();
  const name = rawName ? rawName.slice(0, 60) : null;

  if (!DEVICE_ID_PATTERN.test(deviceId)) {
    return { ok: false, error: "That doesn't look like a device ID. Scan it again or copy it exactly from the worker." };
  }

  try {
    await upsertDevice(session.user.id, deviceId, name);
  } catch (err) {
    if (err instanceof DeviceTakenError) {
      return { ok: false, error: "That device is already linked to another account." };
    }
    console.error("linkDevice", err);
    return { ok: false, error: "Couldn't save the device. Try again." };
  }

  revalidatePath("/");
  revalidatePath("/settings");
  return { ok: true };
}

export async function unlinkDevice(): Promise<void> {
  const session = await requireSession();
  await removeDevice(session.user.id);
  revalidatePath("/");
  revalidatePath("/settings");
}
