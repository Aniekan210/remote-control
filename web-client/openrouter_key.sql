-- Optional: the web app creates this table itself the first time anyone
-- opens Settings. Run it by hand if you'd rather create it up front.
CREATE TABLE IF NOT EXISTS openrouter_key (
  user_id    TEXT PRIMARY KEY,
  key_enc    TEXT NOT NULL,
  key_last4  TEXT NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Standing instructions for the AI (Settings → Instructions for the AI).
-- Also created automatically by the web app on first use.
CREATE TABLE IF NOT EXISTS user_instructions (
  user_id      TEXT PRIMARY KEY,
  instructions TEXT NOT NULL,
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
