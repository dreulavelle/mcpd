-- When each remote MCP server was last asked what it offers.
--
-- Discovery used to happen only when an administrator pressed a button, so
-- "what this server offers" was a snapshot of the last time somebody thought
-- to look. A server that quietly added, withdrew or rewrote a tool was not
-- noticed until the next person went looking.
--
-- A scheduled re-discovery closes that, and it needs somewhere to record when
-- it last ran. In memory would not survive a restart: a host that restarts
-- often would re-probe every remote server on every start, which is load on
-- somebody else's service that nobody asked for.
--
-- Three columns rather than one, because they answer three different questions
-- and collapsing them loses the one that matters during an incident.

ALTER TABLE mcp_servers ADD COLUMN last_attempt_at INTEGER;
ALTER TABLE mcp_servers ADD COLUMN last_discovered_at INTEGER;
ALTER TABLE mcp_servers ADD COLUMN last_discovery_error TEXT;

-- last_attempt_at is when discovery last ran, successfully or not. The
-- scheduler reads this one, so a server that is refusing connections is
-- retried on the same interval as everything else rather than in a tight loop.
--
-- last_discovered_at is when it last ran *and worked*. This is what the
-- dashboard shows beside the tool list, and it is the honest age of what is on
-- screen. It deliberately stops advancing while a server is failing: "these
-- tools were confirmed four days ago" is the true statement then, and showing
-- the failed attempt's timestamp instead would present stale data as fresh.
--
-- last_discovery_error is what the most recent attempt said when it failed,
-- and NULL when it succeeded. NULL is "the last attempt was fine", not "no
-- attempt has been made" -- that case is last_attempt_at being NULL, which is
-- what every existing row is until the schedule first reaches it.

-- Every row starts null: nothing has been discovered *by the scheduler* yet,
-- and backfilling from the tool snapshot would be inventing an attempt that
-- never happened. A server imported before this migration reads as "not
-- checked", which is true, and the first scheduled pass fills it in.
