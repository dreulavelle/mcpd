-- 0006_accounts: local accounts sign in with an email address, and the
-- authorization server is gone.
--
-- mcpd stopped being an OAuth authorization server. It reaches ChatGPT through
-- the tunnel, which carries the connection and the credential both, so the
-- authorize/token/consent machinery served no client that exists: signing
-- someone in through a tunnel needs mcpd reachable from the public internet,
-- which is the one thing a tunnel exists to avoid. The tables backing it go
-- with it.
--
-- What survives is the part that was never about OAuth: people, their
-- passwords, and their browser sessions. Those now stand on their own.
--
-- Identity moves from a username to an email address. Two names for one person
-- is two things to keep in step, and the address is already the one an operator
-- recognises in an audit record.

-- Dependants first: each of these carries a foreign key into users, which is
-- rebuilt below.
DROP TABLE oauth_auth_codes;
DROP TABLE oauth_tokens;
DROP TABLE oauth_sessions;
DROP TABLE oauth_clients;


-- SQLite cannot rename a column and change its constraints in place, so the
-- table is rebuilt.
--
-- Rows whose username is already an address carry over untouched. Rows whose
-- username is not an address are dropped rather than mangled into one: they
-- could only ever have signed in through the consent screen this migration
-- removes, so there is no flow left for them, and inventing an address like
-- "admin@local" would leave an account nobody can prove they own. A deployment
-- that loses its only account this way is bootstrapped again from
-- MCPD_BOOTSTRAP_EMAIL on the next start, which is the same path a fresh
-- install takes.
CREATE TABLE users_rebuilt (
    id            TEXT    PRIMARY KEY,
    email         TEXT    NOT NULL UNIQUE,
    -- bcrypt. Passwords are low-entropy and human-chosen, so they need a slow
    -- KDF; the session tokens below are high-entropy and use SHA-256.
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
    CHECK (json_valid(plugins_json)),
    -- Not a validator -- the application parses the address properly. This
    -- only stops a row that could never be signed in as from reaching the
    -- table at all.
    CHECK (email LIKE '%_@_%')
) STRICT;

INSERT INTO users_rebuilt (id, email, password_hash, display_name, role,
                           plugins_json, disabled, created_at, updated_at, last_login_at)
SELECT id, lower(username), password_hash, display_name, role,
       plugins_json, disabled, created_at, updated_at, last_login_at
FROM users
WHERE username LIKE '%_@_%';

DROP TABLE users;
ALTER TABLE users_rebuilt RENAME TO users;

CREATE INDEX ix_users_enabled ON users (email) WHERE disabled = 0;


-- Browser sessions for the dashboard.
--
-- The token is stored only as a SHA-256 digest, so a database leak yields
-- nothing that can be presented as a session. It carries 256 bits from a
-- CSPRNG, which is what makes a plain digest right here and a slow KDF
-- pointless: there is no dictionary to precompute.
--
-- csrf_token is stored in the clear on purpose. It is not a credential -- it
-- is only ever compared against a header echoed back by a page that already
-- holds the cookie, and it defends against a cross-site request that cannot
-- read this row in the first place.
CREATE TABLE user_sessions (
    session_hash TEXT    PRIMARY KEY,
    -- A stable public identifier for the session, so an audit record can name
    -- which sign-in performed an act without naming the token that did it.
    id           TEXT    NOT NULL UNIQUE,
    user_id      TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token   TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_user_sessions_expiry ON user_sessions (expires_at);
CREATE INDEX ix_user_sessions_user ON user_sessions (user_id);
