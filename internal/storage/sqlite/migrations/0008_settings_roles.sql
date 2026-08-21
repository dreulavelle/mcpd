-- 0008_settings_roles: the role collapse reached the users table and stopped.
--
-- 0007 mapped every account to admin or user and left tunnel.role alone, which
-- is stored here as canonical JSON. A host that had ever set it -- the default
-- was "approver" -- came back up with a tunnel that refused to start:
--
--   tunnel failed  error="auth: principal svc:chatgpt has unknown role \"approver\""
--
-- The failure is at least loud and total rather than quietly granting
-- something. Still, the migration that removed the roles is the one that owed
-- this, so it is done here in the same terms: everything that is not admin
-- becomes user.
--
-- Values are canonical JSON, so the comparison and the replacement both carry
-- their quotes.

UPDATE settings
   SET value = '"user"'
 WHERE key = 'tunnel.role'
   AND value <> '"admin"';

-- Per-plugin tunnels store their own role under tunnel.<plugin>.role.
UPDATE settings
   SET value = '"user"'
 WHERE key LIKE 'tunnel.%.role'
   AND value <> '"admin"';

-- History is an append-only record of what was set at the time, and rewriting
-- it would make it a record of something that never happened. The rows stay as
-- they are; they are never read back as configuration.
