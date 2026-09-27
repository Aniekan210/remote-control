import { Pool } from "pg";

// One pool per server process. In dev, Next's HMR re-evaluates modules,
// so stash it on globalThis to avoid leaking connections to Neon.
const globalForDb = globalThis as unknown as { __rcPool?: Pool };

export const pool =
  globalForDb.__rcPool ??
  new Pool({
    connectionString: process.env.DATABASE_URL,
    max: 5,
    idleTimeoutMillis: 20_000,
  });

if (process.env.NODE_ENV !== "production") globalForDb.__rcPool = pool;
