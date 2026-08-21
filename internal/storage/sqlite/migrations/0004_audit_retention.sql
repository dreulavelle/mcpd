-- 0004_audit_retention: let history be pruned, without making it deletable.
--
-- The audit trail is append-only so that tampering is detectable rather than
-- merely discouraged. Retention is in tension with that, and the tension is
-- resolved rather than ignored: a prune is itself an audited act, and what
-- remains afterwards still verifies as a chain.
--
-- Deletion stays refused for every path except one that declares itself. The
-- trigger consults a gate table, and only a transaction that has inserted the
-- gate row can delete. A stray DELETE -- from a bug, a migration, or someone
-- at a sqlite3 prompt -- still aborts.

CREATE TABLE audit_prune_gate (
    id       INTEGER PRIMARY KEY CHECK (id = 1),
    opened_at INTEGER NOT NULL
) STRICT;

DROP TRIGGER trg_audit_no_delete;

CREATE TRIGGER trg_audit_no_delete
BEFORE DELETE ON audit_events
WHEN NOT EXISTS (SELECT 1 FROM audit_prune_gate)
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;
