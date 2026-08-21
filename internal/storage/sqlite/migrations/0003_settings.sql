-- 0003_settings: runtime configuration, managed from the dashboard.
--
-- Configuration splits in two, and the split is forced rather than chosen.
--
-- A handful of settings are needed *before* the database exists: where the
-- database is, what address to listen on, how to log. Those cannot live in the
-- database, so they stay in a small bootstrap file.
--
-- Everything else -- which integrations are on, how they are configured, the
-- approval policy, the tunnel -- is stored here and editable at runtime.
--
-- Secret values are encrypted at rest with a key that lives outside the
-- database. That is what makes dashboard management safe: an operator can type
-- an API key into a form without it landing in plaintext beside the data it
-- protects, and a stolen database file yields nothing without the key.

CREATE TABLE settings (
    key         TEXT    PRIMARY KEY,
    -- Canonical JSON for ordinary values; base64 ciphertext when encrypted.
    value       TEXT    NOT NULL,
    -- 1 when value holds ciphertext rather than JSON.
    encrypted   INTEGER NOT NULL DEFAULT 0,
    updated_at  INTEGER NOT NULL,
    -- The principal who last changed it. Configuration changes are as
    -- consequential as the mutations this system exists to gate, so they are
    -- attributed the same way.
    updated_by  TEXT    NOT NULL,
    CHECK (encrypted IN (0, 1))
) STRICT, WITHOUT ROWID;

-- Every change is recorded, including what it was before.
--
-- Secret values are never written here, in either column: a history table
-- would otherwise become the plaintext copy the encryption was meant to
-- prevent.
CREATE TABLE settings_history (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    key        TEXT    NOT NULL,
    old_value  TEXT,
    new_value  TEXT,
    -- 1 when the value was secret and therefore not recorded.
    redacted   INTEGER NOT NULL DEFAULT 0,
    changed_at INTEGER NOT NULL,
    changed_by TEXT    NOT NULL,
    CHECK (redacted IN (0, 1))
) STRICT;

CREATE INDEX ix_settings_history_key ON settings_history (key, seq DESC);

CREATE TRIGGER trg_settings_history_no_update
BEFORE UPDATE ON settings_history
BEGIN
    SELECT RAISE(ABORT, 'settings_history is append-only');
END;

CREATE TRIGGER trg_settings_history_no_delete
BEFORE DELETE ON settings_history
BEGIN
    SELECT RAISE(ABORT, 'settings_history is append-only');
END;
