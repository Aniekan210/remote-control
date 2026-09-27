/** Shared between the server action and the QR scanner (client). */
export const DEVICE_ID_PATTERN = /^[A-Za-z0-9._:-]{4,128}$/;

/**
 * Pulls a device ID out of whatever a QR code contains. Accepts:
 *  - the raw ID                                  "a1b2c3d4-..."
 *  - a link with ?device= / ?deviceId= / ?id=    "https://app/settings?device=a1b2..."
 *  - JSON with an id field                       {"deviceId":"a1b2..."}
 */
export function extractDeviceId(raw: string): string | null {
  const text = raw.trim();
  if (DEVICE_ID_PATTERN.test(text)) return text;

  try {
    const url = new URL(text);
    for (const key of ["device", "deviceId", "device_id", "id"]) {
      const v = url.searchParams.get(key)?.trim();
      if (v && DEVICE_ID_PATTERN.test(v)) return v;
    }
  } catch {
    /* not a URL */
  }

  try {
    const obj = JSON.parse(text) as Record<string, unknown>;
    for (const key of ["DeviceID", "deviceId", "device_id", "id"]) {
      const v = obj[key];
      if (typeof v === "string" && DEVICE_ID_PATTERN.test(v.trim())) return v.trim();
    }
  } catch {
    /* not JSON */
  }

  return null;
}
