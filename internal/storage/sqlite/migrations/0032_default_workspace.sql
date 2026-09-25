-- The workspace an account's tunnels are made in unless somebody says
-- otherwise.
--
-- A Make listed a tunnel in every workspace saved on the account. Nearly
-- always one is meant, and OpenAI verifies each workspace named against the
-- organisation, so naming every one made a single refused workspace refuse
-- every tunnel. Empty means none was chosen, which keeps the old behaviour:
-- every saved workspace. It is always one of the account's workspaces; the
-- store clears it when that workspace is removed.
ALTER TABLE chatgpt_accounts ADD COLUMN default_workspace TEXT NOT NULL DEFAULT '';
