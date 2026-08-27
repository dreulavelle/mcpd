-- 0018_chatgpt_accounts: the ChatGPT accounts this host connects to, and what
-- each one is allowed to reach.
--
-- Until now there was one set of OpenAI credentials in `settings`, and every
-- tunnel used it. That is the right shape for one account and the wrong shape
-- for several: two people connecting from two ChatGPT workspaces shared a
-- single key, a single grant, and a single identity in the audit trail, so
-- nothing this host recorded could say which of them made a call.
--
-- A table rather than more settings keys. Accounts are a collection whose size
-- nobody knows in advance, and `settings` is a flat key/value store -- holding
-- several there means synthesising keys like `tunnel.account.3.api_key`, which
-- is a table with the constraints left out. Here a name can be unique, a
-- credential can be NOT NULL, and removing an account is one guarded DELETE
-- rather than a sweep for a prefix.
--
-- What is deliberately *not* here: which tunnels an account owns. That stays
-- in `settings` beside `tunnel.tunnel_id`, because a tunnel assignment is a
-- setting an operator edits and it already lives there. Moving half of it into
-- this table would put one decision in two places, which is the thing the
-- configuration rules exist to prevent.
CREATE TABLE chatgpt_accounts (
    id            TEXT    PRIMARY KEY,
    -- What an operator types on the ChatGPT page and reads on the Tunnels
    -- page. Unique case-insensitively, for the reason groups.name and
    -- ca_certificates.name are: "Work" and "work" are one account to anybody
    -- reading the list, and two rows that look identical are two rows nobody
    -- can tell apart when assigning a tunnel.
    name          TEXT    NOT NULL,

    -- The runtime key, encrypted with the same cipher every other stored
    -- credential uses. NOT NULL because an account without one cannot run a
    -- tunnel, and an account that cannot run a tunnel is a row that looks like
    -- a working connector and is not.
    api_key       TEXT    NOT NULL,
    -- The admin key and organisation, for creating and deleting tunnels in
    -- this account's organisation. Both optional and both useless alone: the
    -- API scopes every tunnel request to exactly one organisation, so a key
    -- with no org cannot list anything. An account with neither can still run
    -- tunnels whose ids were pasted in, which is why they are not required.
    admin_key     TEXT,
    org_id        TEXT,

    -- The identity every call arriving through this account's tunnels acts as,
    -- and what the audit trail records. Per account rather than per host: one
    -- shared `svc:chatgpt` across several accounts cannot say which workspace
    -- a call came from, which is most of the point of having accounts at all.
    principal     TEXT    NOT NULL,
    -- What this account may do, and which systems it may reach. Per account so
    -- one workspace can be read-only over a single integration while another
    -- is not. The grant is intersected with the tunnel's own -- a per-plugin
    -- tunnel is already narrowed to its plugin -- and the narrower of the two
    -- wins, so assigning a tunnel to an account can only ever reduce what that
    -- tunnel reaches.
    role          TEXT    NOT NULL,
    -- A JSON array of plugin names, or ["*"]. An empty array is not written:
    -- it would mean an account granted nothing, which is never what leaving a
    -- field blank meant.
    plugins       TEXT    NOT NULL,

    -- Calls per second this account may make, across every tunnel it owns.
    -- Zero is unlimited and is the default, because the traffic runs inward --
    -- ChatGPT calls mcpd, not the reverse -- so this bounds what one account
    -- can ask of this host and the systems behind it, and is not a quota
    -- anybody owes OpenAI. It exists for the case where one workspace's
    -- retry loop must not become every other workspace's outage.
    rate_per_sec  REAL    NOT NULL DEFAULT 0,

    enabled       INTEGER NOT NULL DEFAULT 1,
    created_by    TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (name <> ''),
    CHECK (length(name) <= 64),
    CHECK (api_key <> ''),
    CHECK (principal <> ''),
    CHECK (role IN ('user', 'admin')),
    CHECK (rate_per_sec >= 0),
    CHECK (enabled IN (0, 1))
) STRICT;

CREATE UNIQUE INDEX ux_chatgpt_accounts_name ON chatgpt_accounts (lower(name));
-- Two accounts sharing an identity would put two workspaces' calls under one
-- name in the audit trail, which is the confusion this table exists to remove.
CREATE UNIQUE INDEX ux_chatgpt_accounts_principal ON chatgpt_accounts (principal);
