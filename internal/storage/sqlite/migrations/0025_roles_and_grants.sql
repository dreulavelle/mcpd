-- 0025_roles_and_grants: an editable permission model.
--
-- Until now a subject held one of two fixed roles, a role expanded to four
-- capabilities of which one -- admin -- gated seventy of the ninety dashboard
-- routes, and a group could subtract capabilities from its members through a
-- ceiling. Two mechanisms answered "what may this person do" in opposite
-- directions, and "settings but not users" had no word for it.
--
-- After this migration there are three objects, each answering one question.
-- A ROLE is a named set of host permissions, one level per area (approvals,
-- policies, plugins, tunnels, settings, access, history, system); three are
-- built in and any number can be composed beside them. A GRANT is a plugin,
-- or every plugin, at read or write, and a subject holds a list of them. A
-- GROUP carries a role and grants and hands them to every member. Effective
-- access is the union, and nothing subtracts: the ceiling goes, and the rule
-- that a subject's own grant beat its groups' goes with it. That is the shape
-- every product people trust with credentials converged on, and it is the one
-- that can be explained in one direction.
--
-- Nobody is locked out and nobody gains anything silently. Every subject's
-- role becomes the built-in that means what its old role meant; every plugin
-- list becomes write grants, because an ordinary user could propose. Where
-- the change in composition could widen somebody -- a subject with a grant of
-- its own inside a group that grants more, or a member of a group that
-- imposed a ceiling -- a row is written to access_notes, which the host reads
-- at startup, names in a warning, and clears.


-- A role is a name and a set of permissions.
--
-- permissions_json is an object of area to level: {"settings":"write",
-- "approvals":"decide"}. An area absent from it is held at nothing. The three
-- built-in rows are re-applied from the binary at every startup, so an area
-- added in a later version reaches every administrator without anybody
-- editing anything; the rows here are what a database read before that first
-- startup would see.
CREATE TABLE roles (
    id               TEXT    PRIMARY KEY,
    name             TEXT    NOT NULL,
    description      TEXT    NOT NULL DEFAULT '',
    builtin          INTEGER NOT NULL DEFAULT 0,
    permissions_json TEXT    NOT NULL DEFAULT '{}',
    created_by       TEXT    NOT NULL,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    CHECK (name <> ''),
    CHECK (length(name) <= 64),
    CHECK (builtin IN (0, 1)),
    CHECK (json_valid(permissions_json))
) STRICT;

CREATE UNIQUE INDEX ux_roles_name ON roles (lower(name));

INSERT INTO roles (id, name, description, builtin, permissions_json, created_by, created_at, updated_at) VALUES
    ('role_reader', 'Reader',
     'Reads everything except who has access, and changes nothing.', 1,
     '{"approvals":"read","policies":"read","plugins":"read","tunnels":"read","settings":"read","history":"read","system":"read"}',
     'system', 0, 0),
    ('role_operator', 'Operator',
     'Reads everything, decides on proposed changes, and administers nothing.', 1,
     '{"approvals":"decide","policies":"read","plugins":"read","tunnels":"read","settings":"read","history":"read","system":"read"}',
     'system', 0, 0),
    ('role_administrator', 'Administrator',
     'Everything, including who has access to this host.', 1,
     '{"approvals":"decide","policies":"write","plugins":"write","tunnels":"write","settings":"write","access":"write","history":"write","system":"write"}',
     'system', 0, 0);


-- What this migration could not carry over unchanged, for the startup
-- warning. Read once, logged, and deleted by the host.
CREATE TABLE access_notes (
    id      INTEGER PRIMARY KEY,
    kind    TEXT NOT NULL,
    subject TEXT NOT NULL,
    detail  TEXT NOT NULL DEFAULT ''
) STRICT;

-- A group that took capabilities away no longer can. Its members keep the
-- rights of their own role, which the ceiling had narrowed.
INSERT INTO access_notes (kind, subject, detail)
SELECT 'ceiling_dropped', g.name,
       'permitted ' || g.capabilities_json || ' to ' ||
       (SELECT COUNT(*) FROM group_members m WHERE m.group_id = g.id) || ' member(s)'
  FROM groups g
 WHERE g.capabilities_json IS NOT NULL;

-- A subject whose own grant used to be the whole answer, in a group that
-- grants something. Under union it now reaches both.
INSERT INTO access_notes (kind, subject, detail)
SELECT 'reach_widens', 'user:' || u.email, g.name
  FROM users u
  JOIN group_members m ON m.user_id = u.id
  JOIN groups g ON g.id = m.group_id
 WHERE u.plugins_json <> '[]' AND u.plugins_json <> '["*"]' AND g.plugins_json <> '[]';

INSERT INTO access_notes (kind, subject, detail)
SELECT 'reach_widens', 'key:' || k.id, g.name
  FROM api_keys k
  JOIN group_members m ON m.key_id = k.id
  JOIN groups g ON g.id = m.group_id
 WHERE k.revoked_at IS NULL
   AND k.plugins_json <> '[]' AND k.plugins_json <> '["*"]' AND g.plugins_json <> '[]';


-- The tables carrying a role and a plugin list are rebuilt, because each
-- holds a table-level CHECK naming the old column and SQLite cannot drop a
-- column such a constraint names. Every table that references users or
-- api_keys cascades on delete, and DROP TABLE performs an implicit delete
-- when foreign keys are on, so their rows go into holding tables first and
-- come back afterwards -- the same dance 0011 did for sessions, for the same
-- reason: a permission model is not a reason to end everybody's day.
CREATE TABLE user_sessions_hold   AS SELECT * FROM user_sessions;
CREATE TABLE user_identities_hold AS SELECT * FROM user_identities;
CREATE TABLE sso_states_hold      AS SELECT * FROM sso_states;
CREATE TABLE group_members_hold   AS SELECT * FROM group_members;
DROP TABLE user_sessions;
DROP TABLE user_identities;
DROP TABLE sso_states;
DROP TABLE group_members;


-- users: role -> role_id, plugins_json -> grants_json.
CREATE TABLE users_rebuilt (
    id            TEXT    PRIMARY KEY,
    email         TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    role_id       TEXT    NOT NULL,
    grants_json   TEXT    NOT NULL DEFAULT '[]',
    disabled      INTEGER NOT NULL DEFAULT 0,
    status        TEXT    NOT NULL DEFAULT 'active',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    last_login_at INTEGER,
    CHECK (role_id <> ''),
    CHECK (json_valid(grants_json)),
    CHECK (email LIKE '%_@_%'),
    CHECK (length(display_name) <= 64),
    CHECK (status IN ('active', 'pending'))
) STRICT;

INSERT INTO users_rebuilt (id, email, password_hash, display_name, role_id,
                           grants_json, disabled, status, created_at, updated_at, last_login_at)
SELECT id, email, password_hash, display_name,
       CASE role WHEN 'admin' THEN 'role_administrator' ELSE 'role_operator' END,
       (SELECT json_group_array(json_object('plugin', j.value, 'level', 'write'))
          FROM json_each(users.plugins_json) j),
       disabled, status, created_at, updated_at, last_login_at
  FROM users;

DROP TABLE users;
ALTER TABLE users_rebuilt RENAME TO users;

CREATE INDEX ix_users_enabled ON users (email) WHERE disabled = 0;
CREATE INDEX ix_users_pending ON users (created_at) WHERE status = 'pending';


-- api_keys: the same two columns. previous_secret_hash and
-- previous_until are for rotation: a new secret is issued and the old one
-- keeps working until a moment the administrator chose, so a deployment can
-- swap without a gap. NULL is a key that has not been rotated, or whose
-- grace has been cleared.
CREATE TABLE api_keys_rebuilt (
    id                   TEXT    PRIMARY KEY,
    name                 TEXT    NOT NULL,
    secret_hash          TEXT    NOT NULL,
    previous_secret_hash TEXT,
    previous_until       INTEGER,
    role_id              TEXT    NOT NULL,
    grants_json          TEXT    NOT NULL DEFAULT '[]',
    created_by           TEXT    NOT NULL,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    expires_at           INTEGER,
    last_used_at         INTEGER,
    revoked_at           INTEGER,
    revoked_by           TEXT,
    CHECK (name <> ''),
    CHECK (length(name) <= 64),
    CHECK (role_id <> ''),
    CHECK (secret_hash <> ''),
    CHECK (json_valid(grants_json)),
    CHECK ((previous_secret_hash IS NULL) = (previous_until IS NULL)),
    CHECK (revoked_at IS NULL OR revoked_by IS NOT NULL)
) STRICT;

INSERT INTO api_keys_rebuilt (id, name, secret_hash, role_id, grants_json, created_by,
                              created_at, updated_at, expires_at, last_used_at, revoked_at, revoked_by)
SELECT id, name, secret_hash,
       CASE role WHEN 'admin' THEN 'role_administrator' ELSE 'role_operator' END,
       (SELECT json_group_array(json_object('plugin', j.value, 'level', 'write'))
          FROM json_each(api_keys.plugins_json) j),
       created_by, created_at, updated_at, expires_at, last_used_at, revoked_at, revoked_by
  FROM api_keys;

DROP TABLE api_keys;
ALTER TABLE api_keys_rebuilt RENAME TO api_keys;

CREATE UNIQUE INDEX ux_api_keys_secret ON api_keys (secret_hash);
CREATE UNIQUE INDEX ux_api_keys_previous_secret ON api_keys (previous_secret_hash)
    WHERE previous_secret_hash IS NOT NULL;


-- chatgpt_accounts: role -> role_id, plugins -> grants_json. Nothing
-- references this table, so no holding is needed.
CREATE TABLE chatgpt_accounts_rebuilt (
    id            TEXT    PRIMARY KEY,
    name          TEXT    NOT NULL,
    api_key       TEXT    NOT NULL,
    admin_key     TEXT,
    org_id        TEXT,
    workspaces    TEXT    NOT NULL DEFAULT '[]',
    principal     TEXT    NOT NULL,
    role_id       TEXT    NOT NULL,
    grants_json   TEXT    NOT NULL DEFAULT '[]',
    rate_per_sec  REAL    NOT NULL DEFAULT 0,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_by    TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (name <> ''),
    CHECK (length(name) <= 64),
    CHECK (api_key <> ''),
    CHECK (principal <> ''),
    CHECK (role_id <> ''),
    CHECK (json_valid(grants_json)),
    CHECK (rate_per_sec >= 0),
    CHECK (enabled IN (0, 1))
) STRICT;

INSERT INTO chatgpt_accounts_rebuilt (id, name, api_key, admin_key, org_id, workspaces, principal,
                                      role_id, grants_json, rate_per_sec, enabled,
                                      created_by, created_at, updated_at)
SELECT id, name, api_key, admin_key, org_id, workspaces, principal,
       CASE role WHEN 'admin' THEN 'role_administrator' ELSE 'role_operator' END,
       (SELECT json_group_array(json_object('plugin', j.value, 'level', 'write'))
          FROM json_each(chatgpt_accounts.plugins) j),
       rate_per_sec, enabled, created_by, created_at, updated_at
  FROM chatgpt_accounts;

DROP TABLE chatgpt_accounts;
ALTER TABLE chatgpt_accounts_rebuilt RENAME TO chatgpt_accounts;

CREATE UNIQUE INDEX ux_chatgpt_accounts_name ON chatgpt_accounts (lower(name));
CREATE UNIQUE INDEX ux_chatgpt_accounts_principal ON chatgpt_accounts (principal);


-- groups: a role (or none -- '' -- for a group that only hands out reach),
-- grants in place of a plugin list, and no ceiling. Nothing here names the
-- old columns in a table-level CHECK, so they can be dropped in place.
ALTER TABLE groups ADD COLUMN role_id TEXT NOT NULL DEFAULT '';
ALTER TABLE groups ADD COLUMN grants_json TEXT NOT NULL DEFAULT '[]';
UPDATE groups
   SET grants_json = (SELECT json_group_array(json_object('plugin', j.value, 'level', 'write'))
                        FROM json_each(groups.plugins_json) j);
ALTER TABLE groups DROP COLUMN plugins_json;
ALTER TABLE groups DROP COLUMN capabilities_json;


-- The held tables come back, against the rebuilt parents. Their DDL is
-- exactly what 0011, 0015 and 0016 left, and the indexes with it.
CREATE TABLE user_sessions (
    session_hash TEXT    PRIMARY KEY,
    id           TEXT    NOT NULL UNIQUE,
    user_id      TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token   TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

INSERT INTO user_sessions (session_hash, id, user_id, csrf_token, created_at, expires_at)
SELECT session_hash, id, user_id, csrf_token, created_at, expires_at
  FROM user_sessions_hold;
DROP TABLE user_sessions_hold;

CREATE INDEX ix_user_sessions_expiry ON user_sessions (expires_at);
CREATE INDEX ix_user_sessions_user ON user_sessions (user_id);

CREATE TABLE user_identities (
    provider   TEXT    NOT NULL,
    subject    TEXT    NOT NULL,
    user_id    TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email      TEXT    NOT NULL DEFAULT '',
    linked_by  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (provider, subject),
    UNIQUE (provider, user_id),
    CHECK (provider IN ('google', 'github', 'entra', 'oidc')),
    CHECK (subject <> '')
) STRICT, WITHOUT ROWID;

INSERT INTO user_identities (provider, subject, user_id, email, linked_by, created_at)
SELECT provider, subject, user_id, email, linked_by, created_at
  FROM user_identities_hold;
DROP TABLE user_identities_hold;

CREATE INDEX ix_user_identities_user ON user_identities (user_id);

CREATE TABLE sso_states (
    state_hash    TEXT    PRIMARY KEY,
    provider      TEXT    NOT NULL,
    purpose       TEXT    NOT NULL,
    binding_hash  TEXT    NOT NULL,
    user_id       TEXT    REFERENCES users(id) ON DELETE CASCADE,
    code_verifier TEXT    NOT NULL DEFAULT '',
    nonce         TEXT    NOT NULL DEFAULT '',
    redirect_uri  TEXT    NOT NULL,
    return_to     TEXT    NOT NULL DEFAULT '/',
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    consumed_at   INTEGER,
    CHECK (purpose IN ('signin', 'link')),
    CHECK (provider IN ('google', 'github', 'entra', 'oidc')),
    CHECK (purpose <> 'link' OR user_id IS NOT NULL)
) STRICT, WITHOUT ROWID;

INSERT INTO sso_states (state_hash, provider, purpose, binding_hash, user_id, code_verifier,
                        nonce, redirect_uri, return_to, created_at, expires_at, consumed_at)
SELECT state_hash, provider, purpose, binding_hash, user_id, code_verifier,
       nonce, redirect_uri, return_to, created_at, expires_at, consumed_at
  FROM sso_states_hold;
DROP TABLE sso_states_hold;

CREATE INDEX ix_sso_states_expiry ON sso_states (expires_at);

CREATE TABLE group_members (
    group_id TEXT    NOT NULL REFERENCES groups(id)   ON DELETE CASCADE,
    user_id  TEXT             REFERENCES users(id)    ON DELETE CASCADE,
    key_id   TEXT             REFERENCES api_keys(id) ON DELETE CASCADE,
    added_by TEXT    NOT NULL,
    added_at INTEGER NOT NULL,
    CHECK ((user_id IS NULL) <> (key_id IS NULL))
) STRICT;

INSERT INTO group_members (group_id, user_id, key_id, added_by, added_at)
SELECT group_id, user_id, key_id, added_by, added_at
  FROM group_members_hold;
DROP TABLE group_members_hold;

CREATE UNIQUE INDEX ux_group_members_user ON group_members (group_id, user_id)
    WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX ux_group_members_key ON group_members (group_id, key_id)
    WHERE key_id IS NOT NULL;
CREATE INDEX ix_group_members_user ON group_members (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX ix_group_members_key ON group_members (key_id) WHERE key_id IS NOT NULL;
