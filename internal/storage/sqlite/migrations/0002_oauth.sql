-- 0002_oauth: identities, OAuth clients, and credentials.
--
-- mcpd is both the authorization server and the resource server. That is a
-- deliberate choice for this deployment shape: the alternative is requiring an
-- operator to stand up Keycloak or buy Auth0 before ChatGPT can connect to
-- their own network gear.
--
-- Being both is also what lets tokens be opaque rather than JWTs. There is no
-- third party that needs to validate a token offline, so a random string
-- looked up in this database beats a signed assertion on every axis that
-- matters here: revocation is immediate, there is no key rotation to operate,
-- no signing library in the dependency tree, and no algorithm-confusion class
-- of bug. Credentials are stored only as SHA-256 digests, so a database leak
-- yields no usable token.

CREATE TABLE users (
    id            TEXT    PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE,
    -- bcrypt. Passwords are low-entropy and human-chosen, so they need a slow
    -- KDF; tokens elsewhere in this schema are high-entropy and use SHA-256.
    password_hash TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    role          TEXT    NOT NULL,
    -- JSON array of plugin names, or ["*"]. An empty array denies everything,
    -- which is the safe reading of an incomplete grant.
    plugins_json  TEXT    NOT NULL DEFAULT '[]',
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    last_login_at INTEGER,
    CHECK (role IN ('viewer','operator','approver','admin')),
    CHECK (json_valid(plugins_json))
) STRICT;

CREATE INDEX ix_users_enabled ON users (username) WHERE disabled = 0;


CREATE TABLE oauth_clients (
    client_id            TEXT    PRIMARY KEY,
    -- NULL for public clients, which authenticate with PKCE alone. ChatGPT
    -- registers as a public client.
    client_secret_hash   TEXT,
    client_name          TEXT    NOT NULL DEFAULT '',
    redirect_uris_json   TEXT    NOT NULL,
    -- 'dcr' for RFC 7591 dynamic registration, 'cimd' for a Client ID Metadata
    -- Document, 'static' for one provisioned by an administrator.
    registration_type    TEXT    NOT NULL DEFAULT 'dcr',
    -- Lets a client update its own registration (RFC 7592). Hashed like every
    -- other credential.
    registration_token_hash TEXT,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    disabled             INTEGER NOT NULL DEFAULT 0,
    CHECK (registration_type IN ('dcr','cimd','static')),
    CHECK (json_valid(redirect_uris_json))
) STRICT;


CREATE TABLE oauth_auth_codes (
    code_hash             TEXT    PRIMARY KEY,
    client_id             TEXT    NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
    user_id               TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- The code is bound to the exact redirect_uri used at authorization. The
    -- token request must present the same one, which is what stops a code
    -- being redeemed against an attacker-controlled callback.
    redirect_uri          TEXT    NOT NULL,
    scope                 TEXT    NOT NULL DEFAULT '',
    -- PKCE is mandatory: there is no code path that issues a code without a
    -- challenge, and only S256 is accepted.
    code_challenge        TEXT    NOT NULL,
    code_challenge_method TEXT    NOT NULL,
    created_at            INTEGER NOT NULL,
    expires_at            INTEGER NOT NULL,
    -- Set on first redemption. A second attempt is a replay.
    consumed_at           INTEGER,
    CHECK (code_challenge_method = 'S256')
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_auth_codes_expiry ON oauth_auth_codes (expires_at);


CREATE TABLE oauth_tokens (
    token_hash   TEXT    PRIMARY KEY,
    kind         TEXT    NOT NULL,
    client_id    TEXT    NOT NULL REFERENCES oauth_clients(client_id) ON DELETE CASCADE,
    user_id      TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope        TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    revoked_at   INTEGER,
    -- Links a rotated refresh token to the one it replaced, so that replaying
    -- a superseded token can be detected and the whole chain revoked.
    parent_hash  TEXT,
    -- Groups every token descended from one authorization, so revoking a
    -- session revokes the lineage rather than a single credential.
    lineage_id   TEXT    NOT NULL,
    CHECK (kind IN ('access','refresh'))
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_tokens_lineage ON oauth_tokens (lineage_id);
CREATE INDEX ix_tokens_user ON oauth_tokens (user_id, kind);
CREATE INDEX ix_tokens_expiry ON oauth_tokens (expires_at) WHERE revoked_at IS NULL;


-- Short-lived login sessions backing the authorization endpoint's consent
-- screen. These are browser sessions, not API credentials.
CREATE TABLE oauth_sessions (
    session_hash TEXT    PRIMARY KEY,
    user_id      TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_sessions_expiry ON oauth_sessions (expires_at);
