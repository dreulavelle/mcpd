-- The workspaces a ChatGPT account owns.
--
-- An account is one OpenAI organisation: its runtime key, the admin key that
-- makes tunnels in it, and the identity its connectors act as. The workspaces
-- its connectors sit in were inferred from its tunnels, which meant an account
-- with no tunnels yet could not say where its first one should be listed, and
-- a workspace nobody had used yet could not be named at all. They are the
-- account's own now, a JSON list of ids, and what the listings report is
-- merged in when it is shown.
ALTER TABLE chatgpt_accounts ADD COLUMN workspaces TEXT NOT NULL DEFAULT '[]';
