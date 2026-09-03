-- 0026_plugin_rows: the rows of a plugin's collection settings.
--
-- A plugin may declare a setting that is a table -- the customers one
-- instance serves, each with an address and a credential -- and `settings` is
-- a flat key/value store. Holding several rows there means synthesising keys
-- like `plugins.pbx.customers.3.password`, which is a table with the
-- constraints left out; holding them as one encrypted blob means a form that
-- cannot replace one row's credential without sending every other row's
-- again. Here a row has an id that survives renaming, a name that is unique
-- per instance and field, and secret columns encrypted as a unit with the
-- same cipher every other stored credential uses.
--
-- The columns themselves are the plugin's business, declared in code and
-- validated by the host, so the shape is one JSON object per row rather than
-- a column per field. What the database enforces is what is the same for
-- every collection: identity, uniqueness and ownership.
CREATE TABLE plugin_rows (
    id          TEXT    PRIMARY KEY,
    -- The instance and the bare field key the rows belong to. Removing an
    -- instance from the dashboard removes its rows along with its settings.
    instance    TEXT    NOT NULL,
    field       TEXT    NOT NULL,
    -- The identity column's value, copied out of `data` so it can be unique.
    identity    TEXT    NOT NULL,
    -- Display order. Rows are listed in the order they were added.
    position    INTEGER NOT NULL,
    -- The non-secret columns, as a JSON object keyed by column.
    data        TEXT    NOT NULL,
    -- The secret columns, as a JSON object keyed by column, encrypted. Empty
    -- when the collection has none set.
    secrets     TEXT    NOT NULL DEFAULT '',
    created_by  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_by  TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    CHECK (instance <> ''),
    CHECK (field <> ''),
    CHECK (identity <> ''),
    CHECK (length(identity) <= 128)
) STRICT;

-- "Acme" and "acme" are one customer to anybody reading the list.
CREATE UNIQUE INDEX ux_plugin_rows_identity ON plugin_rows (instance, field, lower(identity));
CREATE INDEX ix_plugin_rows_order ON plugin_rows (instance, field, position);
