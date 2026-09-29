import "server-only";
import { pool } from "./db";

/**
 * The user's standing instructions for the AI ("my taskbar is hidden",
 * "never close Spotify", "my work files are in D:\Work"…), like custom
 * instructions in a chat app. The Go server reads them from the same table
 * when a task starts and sends them with every AI call.
 */

/** Keep in sync with maxUserNotes in server/keys.go. */
export const MAX_INSTRUCTIONS = 4000;

let tableReady: Promise<unknown> | null = null;

/** Creates the table on first use, so no manual migration is needed. */
function ensureTable() {
  tableReady ??= pool
    .query(
      `CREATE TABLE IF NOT EXISTS user_instructions (
         user_id      TEXT PRIMARY KEY,
         instructions TEXT NOT NULL,
         updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
       )`,
    )
    .catch((err) => {
      tableReady = null; // retry next time
      throw err;
    });
  return tableReady;
}

export async function getInstructions(userId: string): Promise<string> {
  await ensureTable();
  const { rows } = await pool.query<{ instructions: string }>(
    "SELECT instructions FROM user_instructions WHERE user_id = $1",
    [userId],
  );
  return rows[0]?.instructions ?? "";
}

/** Saves the instructions; empty text removes them. */
export async function saveInstructions(userId: string, text: string) {
  await ensureTable();
  const clean = text.trim().slice(0, MAX_INSTRUCTIONS);
  if (!clean) {
    await pool.query("DELETE FROM user_instructions WHERE user_id = $1", [userId]);
    return;
  }
  await pool.query(
    `INSERT INTO user_instructions (user_id, instructions)
     VALUES ($1, $2)
     ON CONFLICT (user_id)
     DO UPDATE SET instructions = EXCLUDED.instructions, updated_at = now()`,
    [userId, clean],
  );
}
