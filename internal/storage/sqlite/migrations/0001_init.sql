-- 0001_init: operations, audit, outbox, idempotency.
--
-- Conventions used throughout:
--   * Timestamps are Unix milliseconds stored as INTEGER. They sort and
--     compare correctly in SQL and avoid the format ambiguity that ISO-8601
--     text eventually produces.
--   * JSON columns hold canonical form (sorted keys, no insignificant
--     whitespace) so that payload hashes are reproducible.
--   * STRICT tables reject type coercion, which turns a class of silent
--     application bugs into immediate errors.

CREATE TABLE operations (
    id                  TEXT    PRIMARY KEY,
    plugin              TEXT    NOT NULL,
    action              TEXT    NOT NULL,
    state               TEXT    NOT NULL,
    risk                TEXT    NOT NULL,

    -- Immutable once the operation leaves 'draft'. Enforced by trigger below
    -- as well as by convention, because a payload that can change after
    -- approval makes approval meaningless.
    target_json         TEXT    NOT NULL,
    params_json         TEXT    NOT NULL,
    payload_hash        TEXT    NOT NULL,
    before_json         TEXT,
    desired_json        TEXT,
    precondition_json   TEXT,
    rollback_json       TEXT,
    changes_json        TEXT,
    impact              TEXT    NOT NULL DEFAULT '',

    requested_by        TEXT    NOT NULL,
    requested_at        INTEGER NOT NULL,
    expires_at          INTEGER NOT NULL,

    approved_by         TEXT,
    approved_at         INTEGER,
    approval_expires_at INTEGER,

    attempt_count       INTEGER NOT NULL DEFAULT 0,
    lease_owner         TEXT,
    lease_expires_at    INTEGER,

    terminal_at         INTEGER,
    outcome_verified    INTEGER,
    observed_json       TEXT,
    error_code          TEXT,
    error_detail        TEXT,

    correlation_id      TEXT    NOT NULL,
    idempotency_key     TEXT    NOT NULL,

    CHECK (state IN ('draft','pending_approval','approved','executing',
                     'succeeded','failed','indeterminate',
                     'rejected','expired','cancelled')),
    CHECK (risk IN ('low','medium','high','critical')),
    CHECK (json_valid(target_json)),
    CHECK (json_valid(params_json)),
    -- An approved operation without an execute-by deadline could be executed
    -- indefinitely. Reject the row rather than trusting callers to set it.
    CHECK (state <> 'approved' OR approval_expires_at IS NOT NULL)
) STRICT;

-- Collapses duplicate proposals of the same logical intent.
CREATE UNIQUE INDEX ux_operations_idem
    ON operations (plugin, action, idempotency_key);

-- Reaper: proposals and approvals past their deadline.
CREATE INDEX ix_operations_deadline
    ON operations (state, expires_at)
    WHERE state IN ('pending_approval', 'approved');

-- Reaper: leases whose holder died mid-execution.
CREATE INDEX ix_operations_stale_lease
    ON operations (lease_expires_at)
    WHERE state = 'executing';

-- Executor wake-up scan, and the polling fallback for the in-process bus.
CREATE INDEX ix_operations_claimable
    ON operations (state, requested_at)
    WHERE state = 'approved';

-- Dashboard and API listing.
CREATE INDEX ix_operations_listing
    ON operations (plugin, requested_at DESC);

-- Refuse to rewrite a frozen payload regardless of what the calling code
-- believes it is doing.
CREATE TRIGGER trg_operations_payload_immutable
BEFORE UPDATE OF target_json, params_json, payload_hash ON operations
FOR EACH ROW WHEN OLD.state <> 'draft'
BEGIN
    SELECT RAISE(ABORT, 'operation payload is immutable after submission');
END;

-- Operations are never deleted. Retention is handled by archival, not DELETE,
-- so that an audit trail can never reference a vanished operation.
CREATE TRIGGER trg_operations_no_delete
BEFORE DELETE ON operations
BEGIN
    SELECT RAISE(ABORT, 'operations are append-only; archive instead of deleting');
END;


CREATE TABLE operation_transitions (
    seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    operation_id   TEXT    NOT NULL REFERENCES operations(id) ON DELETE RESTRICT,
    from_state     TEXT,
    to_state       TEXT    NOT NULL,
    actor          TEXT    NOT NULL,
    reason         TEXT,
    at             INTEGER NOT NULL,
    correlation_id TEXT    NOT NULL
) STRICT;

CREATE INDEX ix_transitions_op ON operation_transitions (operation_id, seq);


-- Append-only and hash-chained: entry_hash = sha256(prev_hash || canonical row).
-- The chain is what makes tampering detectable rather than merely discouraged.
CREATE TABLE audit_events (
    seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id       TEXT    NOT NULL UNIQUE,
    at             INTEGER NOT NULL,
    kind           TEXT    NOT NULL,
    operation_id   TEXT    REFERENCES operations(id),
    plugin         TEXT,
    action         TEXT,
    actor          TEXT    NOT NULL,
    from_state     TEXT,
    to_state       TEXT,
    risk           TEXT,
    correlation_id TEXT    NOT NULL,
    detail_json    TEXT    NOT NULL DEFAULT '{}',
    prev_hash      TEXT    NOT NULL,
    entry_hash     TEXT    NOT NULL,
    CHECK (json_valid(detail_json))
) STRICT;

CREATE INDEX ix_audit_op ON audit_events (operation_id, seq);
CREATE INDEX ix_audit_at ON audit_events (at);
CREATE INDEX ix_audit_actor ON audit_events (actor, at);

CREATE TRIGGER trg_audit_no_update
BEFORE UPDATE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;

CREATE TRIGGER trg_audit_no_delete
BEFORE DELETE ON audit_events
BEGIN
    SELECT RAISE(ABORT, 'audit_events is append-only');
END;


CREATE TABLE execution_attempts (
    id              TEXT    PRIMARY KEY,
    operation_id    TEXT    NOT NULL REFERENCES operations(id),
    attempt_no      INTEGER NOT NULL,
    instance_id     TEXT    NOT NULL,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    outcome         TEXT,
    upstream_ref    TEXT,
    upstream_status INTEGER,
    verified        INTEGER,
    observed_json   TEXT,
    error_code      TEXT,
    error_detail    TEXT,
    UNIQUE (operation_id, attempt_no)
) STRICT;

CREATE INDEX ix_attempts_op ON execution_attempts (operation_id, attempt_no);


CREATE TABLE outbox_events (
    seq             INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        TEXT    NOT NULL UNIQUE,
    subject         TEXT    NOT NULL,
    operation_id    TEXT,
    correlation_id  TEXT    NOT NULL DEFAULT '',
    payload_json    TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    published_at    INTEGER,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    CHECK (json_valid(payload_json))
) STRICT;

-- Partial index keeps the drain query proportional to the pending backlog
-- rather than to every event ever published.
CREATE INDEX ix_outbox_pending
    ON outbox_events (next_attempt_at, seq)
    WHERE published_at IS NULL;


CREATE TABLE idempotency_records (
    scope         TEXT    NOT NULL,
    key           TEXT    NOT NULL,
    request_hash  TEXT    NOT NULL,
    operation_id  TEXT    REFERENCES operations(id),
    response_json TEXT,
    created_at    INTEGER NOT NULL,
    expires_at    INTEGER NOT NULL,
    PRIMARY KEY (scope, key)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_idem_expiry ON idempotency_records (expires_at);


CREATE TABLE plugin_state (
    plugin     TEXT    NOT NULL,
    key        TEXT    NOT NULL,
    value_json TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (plugin, key),
    CHECK (json_valid(value_json))
) STRICT, WITHOUT ROWID;
