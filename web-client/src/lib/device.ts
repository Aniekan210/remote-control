import "server-only";
import { pool } from "./db";

export type Device = {
  deviceId: string;
  name: string | null;
  linkedAt: string;
};

export async function getDevice(userId: string): Promise<Device | null> {
  const { rows } = await pool.query<{ device_id: string; name: string | null; linked_at: Date }>(
    "SELECT device_id, name, linked_at FROM device WHERE user_id = $1",
    [userId],
  );
  const row = rows[0];
  if (!row) return null;
  return { deviceId: row.device_id, name: row.name, linkedAt: row.linked_at.toISOString() };
}

export class DeviceTakenError extends Error {}

/** Link (or replace) the user's single device. */
export async function upsertDevice(userId: string, deviceId: string, name: string | null) {
  try {
    await pool.query(
      `INSERT INTO device (user_id, device_id, name)
       VALUES ($1, $2, $3)
       ON CONFLICT (user_id)
       DO UPDATE SET device_id = EXCLUDED.device_id, name = EXCLUDED.name, linked_at = now()`,
      [userId, deviceId, name],
    );
  } catch (err) {
    // 23505 = unique_violation → device_id already belongs to someone else
    if ((err as { code?: string }).code === "23505") throw new DeviceTakenError();
    throw err;
  }
}

export async function removeDevice(userId: string) {
  await pool.query("DELETE FROM device WHERE user_id = $1", [userId]);
}
