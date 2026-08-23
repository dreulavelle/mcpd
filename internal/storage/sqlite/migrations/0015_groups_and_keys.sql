-- 0015_groups_and_keys: groups grant plugin access, and an API key is a
-- principal that draws its grants the same way a person does.
--
-- Two axes, kept apart on purpose. A role decides what a caller may *do* --
-- read, propose, approve, administer -- and the role-to-capability map in
-- internal/auth is the only thing that knows the difference. A group decides
-- what a caller may *reach*: which plugins, and nothing else. Capability-
-- carrying groups are a deliberate non-goal rather than an omission; with two
-- roles and four capabilities, a second bundle-of-rights mechanism would make
-- "why can this person approve" answerable only by reading both.
--
-- Default none, at every level. A new group grants nothing, a subject in no
-- group reaches nothing through one, and a new key reaches nothing at all.
-- That is the same reading `Principal.CanAccessPlugin` has always taken of an
-- empty list, and the same direction the SSO work took when it changed a
-- self-registration from ["*"] to [].
--
-- Nothing here touches `auth.static_tokens`. A file token is built at startup
-- from configuration, reaches no table, and keeps exactly the grants its
-- declaration lists.


-- A group is a name and a set of plugins.
--
-- `plugins_json` is the same shape as `users.plugins_json`: a JSON array of
-- plugin names, or the single element "*". Not a join table of one row per
-- plugin, because a grant may name a plugin that is not mounted -- an
-- integration configured later, or one temporarily removed -- and a foreign
-- key would either refuse that or silently drop the grant when the plugin
-- went. The account grant it unions with has always been a list, and two
-- shapes for one idea is one more thing to keep in step.
CREATE TABLE groups (
    id           TEXT    PRIMARY KEY,
    -- What an operator types and reads. Unique case-insensitively, because two
    -- groups called "Field" and "field" are one group as far as anybody
    -- looking at the list is concerned.
    name         TEXT    NOT NULL,
    description  TEXT    NOT NULL DEFAULT '',
    -- '[]' grants nothing. That is the zero value of the column and the zero
    -- value of the feature: a group created and not yet filled in must not
    -- widen anybody.
    plugins_json TEXT    NOT NULL DEFAULT '[]',
    created_by   TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    CHECK (name <> ''),
    CHECK (length(name) <= 64)
) STRICT;

CREATE UNIQUE INDEX ux_groups_name ON groups (lower(name));


-- An API key is a credential with an identity of its own.
--
-- Not a second authorization model. It carries a principal id, a role and a
-- set of grants -- which is what `auth.static_tokens` in the configuration
-- file has always carried -- so the only thing that differs is where the
-- declaration lives and, because it lives here, that it can be revoked or
-- re-scoped without a restart.
--
-- The secret is never stored. `secret_hash` is the SHA-256 of the presented
-- credential, which is what session tokens already do and is right for the
-- same reason: 256 bits from a CSPRNG has no dictionary to precompute, so
-- there is no work factor worth paying and a salt would only prevent the
-- lookup by digest that verification depends on.
CREATE TABLE api_keys (
    -- 'key_<32 hex>'. Generated, never chosen: an operator-chosen id could
    -- collide with a static token's, and the two must stay distinguishable in
    -- the audit trail. Config validation refuses a file token id beginning
    -- 'key_' for the other half of the same rule.
    id           TEXT    PRIMARY KEY,
    -- For reading. Never an identity: the trail names the id.
    name         TEXT    NOT NULL,
    secret_hash  TEXT    NOT NULL,
    role         TEXT    NOT NULL,
    -- Direct grants, unioned with every group this key belongs to. '[]'
    -- reaches nothing.
    plugins_json TEXT    NOT NULL DEFAULT '[]',
    created_by   TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    -- NULL never expires. An expiry is optional because a key issued for a
    -- long-lived connector has no honest date to carry, and inventing one
    -- would mean a connector that stops working on a day nobody chose.
    expires_at   INTEGER,
    -- Written at most once a minute per key, guarded in the WHERE clause, so a
    -- forgotten key is findable without a write on every request.
    last_used_at INTEGER,
    -- Revocation is a row, not a deletion. A deleted key would leave the audit
    -- trail naming an id that resolves to nothing, and "which agent did this"
    -- is the question the whole feature exists to answer.
    revoked_at   INTEGER,
    revoked_by   TEXT,
    CHECK (name <> ''),
    CHECK (length(name) <= 64),
    CHECK (role IN ('user', 'admin')),
    CHECK (secret_hash <> ''),
    CHECK (revoked_at IS NULL OR revoked_by IS NOT NULL)
) STRICT;

-- Verification is a lookup by digest, so the index is what makes it one
-- statement rather than a scan. Unique because two keys sharing a secret are
-- one credential with two names, which breaks revocation and audit the same
-- way two static tokens sharing a secret_ref does.
CREATE UNIQUE INDEX ux_api_keys_secret ON api_keys (secret_hash);


-- Membership, for accounts and for keys.
--
-- Two nullable columns with exactly one filled in, rather than two tables or
-- one polymorphic id. The CHECK is what makes it exactly one; the two foreign
-- keys are what make a deleted account or a revoked-and-removed key take its
-- memberships with it, which no polymorphic column could express. One table is
-- what lets the effective-grant query be a single statement.
CREATE TABLE group_members (
    group_id TEXT    NOT NULL REFERENCES groups(id)   ON DELETE CASCADE,
    user_id  TEXT             REFERENCES users(id)    ON DELETE CASCADE,
    key_id   TEXT             REFERENCES api_keys(id) ON DELETE CASCADE,
    added_by TEXT    NOT NULL,
    added_at INTEGER NOT NULL,
    CHECK ((user_id IS NULL) <> (key_id IS NULL))
) STRICT;

-- One membership per subject per group. Partial unique indexes rather than a
-- primary key, because a primary key cannot span columns that are null by
-- design.
CREATE UNIQUE INDEX ux_group_members_user ON group_members (group_id, user_id)
    WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX ux_group_members_key ON group_members (group_id, key_id)
    WHERE key_id IS NOT NULL;

-- Resolving a subject's grants reads by subject, which is the direction these
-- serve; the group_id lead in the uniques above serves listing a group's
-- members.
CREATE INDEX ix_group_members_user ON group_members (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX ix_group_members_key ON group_members (key_id) WHERE key_id IS NOT NULL;
