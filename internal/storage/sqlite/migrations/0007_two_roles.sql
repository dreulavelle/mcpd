-- 0007_two_roles: viewer, operator and approver become user.
--
-- The ladder was viewer -> operator -> approver -> admin, and the finer steps
-- were never asked for. Worse, the ladder invited a reading it did not support:
-- separating proposing from approving only means something when the two are
-- different people, and the second-approver rule that would have made that so
-- was dropped in cd42732.
--
-- What is actually enforced is one line, and it is about administering the host
-- rather than operating it. A user reads, proposes, and approves -- everything
-- the integrations exist to do. An administrator additionally changes settings,
-- makes and assigns tunnels, manages accounts, and clears history.
--
-- Every non-admin collapses to user, including viewer. That widens a viewer,
-- which is the honest trade: the alternative is inventing a read-only role the
-- new model does not have and leaving those accounts unable to do the thing the
-- dashboard is for. There are no viewer accounts in any deployment this ships
-- to, and an administrator can see and change every account on the Users page.

CREATE TABLE users_rebuilt (
    id            TEXT    PRIMARY KEY,
    email         TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    role          TEXT    NOT NULL,
    plugins_json  TEXT    NOT NULL DEFAULT '[]',
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    last_login_at INTEGER,
    CHECK (role IN ('admin', 'user')),
    CHECK (json_valid(plugins_json)),
    CHECK (email LIKE '%_@_%')
) STRICT;

INSERT INTO users_rebuilt (id, email, password_hash, display_name, role,
                           plugins_json, disabled, created_at, updated_at, last_login_at)
SELECT id, email, password_hash, display_name,
       CASE WHEN role = 'admin' THEN 'admin' ELSE 'user' END,
       plugins_json, disabled, created_at, updated_at, last_login_at
FROM users;

DROP TABLE users;
ALTER TABLE users_rebuilt RENAME TO users;

CREATE INDEX ix_users_enabled ON users (email) WHERE disabled = 0;

-- Sessions reference users(id) and the rebuild replaced the table underneath
-- them. The rows survive the rename -- the ids did not change -- but the
-- foreign key was declared against the old table, so it is redeclared here
-- rather than left pointing at something that no longer exists.
CREATE TABLE user_sessions_rebuilt (
    session_hash TEXT    PRIMARY KEY,
    id           TEXT    NOT NULL UNIQUE,
    user_id      TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token   TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

INSERT INTO user_sessions_rebuilt
SELECT session_hash, id, user_id, csrf_token, created_at, expires_at
FROM user_sessions;

DROP TABLE user_sessions;
ALTER TABLE user_sessions_rebuilt RENAME TO user_sessions;

CREATE INDEX ix_user_sessions_expiry ON user_sessions (expires_at);
CREATE INDEX ix_user_sessions_user ON user_sessions (user_id);
