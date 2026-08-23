-- 0013_plugin_overrides: let the dashboard override what the configuration
-- file declares about a plugin.
--
-- A plugin named in config.yaml could not be removed or switched off from the
-- dashboard. The refusal was honest as far as it went -- a delete that the
-- next start undoes reads as a delete that failed -- but "go and edit the
-- file" is not a thing an operator can always do. The file is mounted
-- read-only in the container image, the root filesystem is read-only, the
-- systemd unit is ProtectSystem=strict, and in any deployment provisioned by
-- configuration management the file is regenerated on the next deploy. mcpd
-- cannot write it, and should not: rewriting hand-authored YAML destroys
-- comments, ordering and anchors, and loses the argument with whatever
-- produced the file anyway.
--
-- So the removal is recorded here instead, and the file's declaration for that
-- name is ignored from the next read onward. The file is unchanged and stays
-- unchanged. Redeploying from it does not resurrect the plugin, because what
-- decides is this table rather than the file.
--
-- Keyed on the name, deliberately. Pinning the override to the declaration it
-- was written against -- a hash of the file entry, the way descriptor_hash
-- pins a tool approval -- would mean an edit to that entry silently
-- resurrecting the plugin, which is exactly the "it came back" failure this
-- exists to prevent. A tool approval is a statement about a descriptor; a
-- removal is a statement about a name.
--
-- The same row carries the enable/disable override, because it is the same
-- problem one step smaller: `enabled: false` in a file nobody can edit is as
-- unreachable as the entry itself.
CREATE TABLE plugin_overrides (
    -- The instance name as the configuration file spells it. Same namespace as
    -- mcp_servers.name and the instances. settings keys, so the three cannot
    -- disagree about what a plugin is called.
    name          TEXT    PRIMARY KEY,
    -- 1 means the file's declaration for this name is ignored: the plugin is
    -- not listed as configured, not mounted, and does not come back on a
    -- restart. Reversible from the same page that set it.
    removed       INTEGER NOT NULL DEFAULT 0,
    -- The store's answer to `enabled`, or NULL to follow the file. Three
    -- states rather than two: "off", "on", and "whatever the file says" are
    -- different, and collapsing the last into a boolean would freeze the
    -- file's current value into the database the first time anyone touched
    -- the toggle.
    enabled       INTEGER,
    -- What the file said this was an instance of when the override was
    -- written. Not authority -- the file is still read for that -- but a
    -- removal has to be explicable afterwards, and a row naming only a plugin
    -- that no longer appears anywhere explains nothing.
    declared_type TEXT    NOT NULL,
    -- Who last changed this override. The act itself is in audit_events, which
    -- is hash-chained and append-only; this is the denormalised copy the
    -- Plugins page reads so that "removed by whom, and when" does not require
    -- walking the trail for every row it draws.
    actor         TEXT    NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    CHECK (removed IN (0, 1)),
    CHECK (enabled IS NULL OR enabled IN (0, 1)),
    -- A row that overrides nothing is not a record of anything, and one left
    -- behind after a restore would be a name reserved for no reason. Restoring
    -- an instance whose only override was the removal therefore deletes the
    -- row, and this CHECK is what makes forgetting to a hard error rather than
    -- a slow accumulation of rows meaning nothing.
    CHECK (removed = 1 OR enabled IS NOT NULL)
) STRICT, WITHOUT ROWID;
