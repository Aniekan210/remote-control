"use server";

import { revalidatePath } from "next/cache";
import { requireSession } from "@/lib/session";
import { DeviceTakenError, removeDevice, upsertDevice } from "@/lib/device";
import { DEVICE_ID_PATTERN } from "@/lib/device-id";
import { checkKey, removeKey, saveKey } from "@/lib/openrouter-key";

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

export type KeyState = { ok: boolean; error?: string; warning?: string } | null;

export async function saveOpenRouterKey(_prev: KeyState, form: FormData): Promise<KeyState> {
  const session = await requireSession();
  const key = String(form.get("key") ?? "").trim();

  if (!/^sk-or-[A-Za-z0-9_-]{8,}$/.test(key)) {
    return { ok: false, error: "That doesn't look like an OpenRouter key (they start with sk-or-)." };
  }

  const check = await checkKey(key);
  if (!check.ok) return { ok: false, error: check.error };

  try {
    await saveKey(session.user.id, key);
  } catch (err) {
    console.error("saveOpenRouterKey", err);
    return { ok: false, error: "Couldn't save the key. Try again." };
  }

  revalidatePath("/settings");
  return check.hasLimit
    ? { ok: true }
    : { ok: true, warning: "Saved. This key has no credit limit — set one in OpenRouter so a runaway task can't overspend." };
}

export async function removeOpenRouterKey(): Promise<void> {
  const session = await requireSession();
  await removeKey(session.user.id);
  revalidatePath("/settings");
}

export async function unlinkDevice(): Promise<void> {
  const session = await requireSession();
  await removeDevice(session.user.id);
  revalidatePath("/");
  revalidatePath("/settings");
}
