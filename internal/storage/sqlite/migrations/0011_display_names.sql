-- 0011_display_names: the database enforces the bound the application now puts
-- on a display name, and rows written before there was one are brought into
-- line.
--
-- The column has existed since accounts did, and until now it accepted
-- anything: any length, any character, including the ones that decide how the
-- text around them renders. That was tolerable while only an administrator
-- could write it. It stops being tolerable now that an account may name
-- itself, because the value is written by whoever holds the account and read
-- by everyone else on the Users page, in a session heading, and beside every
-- operation someone requested.
--
-- The application refuses control characters, invisible formatting characters,
-- and a name that is another account's address. SQLite can express one of
-- those four rules honestly -- the length -- so that is the one that goes in
-- the schema, and it is here rather than only in Go because a value typed at a
-- sqlite3 prompt is a value this host will later render.
--
-- length() counts characters of text in SQLite, and Go counts runes, so the
-- two agree on what sixty-four means.

-- Sessions are held aside first, and this is the whole reason the migration is
-- ordered the way it is. user_sessions carries ON DELETE CASCADE into users,
-- and DROP TABLE performs an implicit delete when foreign keys are on, so
-- rebuilding users underneath the sessions would sign every operator out. An
-- earlier migration did exactly that. Adding a length check is not a reason to
-- end everybody's day, so the rows go into a holding table and come back.
CREATE TABLE user_sessions_hold AS SELECT * FROM user_sessions;
DROP TABLE user_sessions;

-- Bring existing rows inside the rule before the rule exists, or the rebuild
-- below fails on a row nobody can now edit.
--
-- Conditioned rather than applied to every row: a name already inside the rule
-- has nothing to correct, and a migration that rewrites every account's name
-- is one that has to be read carefully to see that it changed nothing.
-- updated_at is deliberately left alone -- this is not an edit anyone made.
UPDATE users
   SET display_name = trim(substr(
           trim(replace(replace(replace(display_name, char(13), ' '),
                                char(10), ' '), char(9), ' ')), 1, 64))
 WHERE length(display_name) > 64
    OR display_name <> trim(replace(replace(replace(display_name, char(13), ' '),
                                            char(10), ' '), char(9), ' '));

-- SQLite cannot add a constraint in place, so the table is rebuilt. Everything
-- else about it is unchanged from 0007.
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
    CHECK (email LIKE '%_@_%'),
    -- A name is for reading. Sixty-four characters is more than any real one
    -- and short enough that a row is a row rather than a paragraph.
    CHECK (length(display_name) <= 64)
) STRICT;

INSERT INTO users_rebuilt (id, email, password_hash, display_name, role,
                           plugins_json, disabled, created_at, updated_at, last_login_at)
SELECT id, email, password_hash, display_name, role,
       plugins_json, disabled, created_at, updated_at, last_login_at
FROM users;

DROP TABLE users;
ALTER TABLE users_rebuilt RENAME TO users;

CREATE INDEX ix_users_enabled ON users (email) WHERE disabled = 0;

-- The sessions come back, against the rebuilt table.
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
