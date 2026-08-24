-- 0016_own_provider: the schema learns about a provider the operator runs.
--
-- 0014 wrote the set of providers into two CHECK constraints. That was right --
-- a provider is a primary-key column and decides which flow runs, so a value
-- nobody configured has no business being storable -- and it means adding one
-- is a schema change rather than a Go constant. This is that change.
--
-- The alternative was to drop the list and check only that the column is not
-- empty, leaving the set to the Go enum. It is not taken: the enum already
-- refuses a bad value on the way in, and the point of the constraint is to
-- hold when something bypasses that. A migration per provider is a small
-- price, and it keeps the set readable at a sqlite3 prompt.
--
-- SQLite cannot alter a CHECK, so both tables are rebuilt. Neither is
-- referenced by anything, which is what makes this safe to do plainly: the
-- hazard 0011 met -- DROP TABLE performing an implicit delete that cascades
-- into a table pointing at the one being rebuilt -- does not arise, because
-- these two are the children. They point at users; nothing points at them.

CREATE TABLE user_identities_new (
    -- 'google', 'github', 'entra', or 'oidc' for the provider the operator
    -- runs themselves. The configured provider, not the issuer URL: a
    -- deployment moving an Entra tenant should not orphan every link.
    --
    -- 'oidc' names one issuer, the one in auth.oidc.issuer. Pointing that
    -- setting at a different provider does not migrate these rows and is not
    -- meant to: a subject is only unique within the issuer that minted it, so
    -- the same subject at a new issuer is a different person.
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

INSERT INTO user_identities_new
    (provider, subject, user_id, email, linked_by, created_at)
SELECT provider, subject, user_id, email, linked_by, created_at
  FROM user_identities;

DROP TABLE user_identities;
ALTER TABLE user_identities_new RENAME TO user_identities;

CREATE INDEX ix_user_identities_user ON user_identities (user_id);


CREATE TABLE sso_states_new (
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

-- Copied rather than discarded, though every row here expires within minutes.
-- A state is one person part-way through signing in, and dropping the table
-- would fail their callback with "that sign-in link is not one this host is
-- waiting for" -- a message about tampering, shown to somebody who did
-- nothing but restart into a new version at the wrong moment.
INSERT INTO sso_states_new
    (state_hash, provider, purpose, binding_hash, user_id, code_verifier,
     nonce, redirect_uri, return_to, created_at, expires_at, consumed_at)
SELECT state_hash, provider, purpose, binding_hash, user_id, code_verifier,
       nonce, redirect_uri, return_to, created_at, expires_at, consumed_at
  FROM sso_states;

DROP TABLE sso_states;
ALTER TABLE sso_states_new RENAME TO sso_states;

CREATE INDEX ix_sso_states_expiry ON sso_states (expires_at);
