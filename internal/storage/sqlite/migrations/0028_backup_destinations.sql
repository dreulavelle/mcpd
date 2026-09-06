-- 0028_backup_destinations: where a backup goes, and what happened when it went.
--
-- Until now a backup was a download: somebody pressed a button and a file
-- arrived in their browser. That is the wrong shape for the thing it protects
-- against, because it only happens while somebody is watching, and the machine
-- most in need of a backup is the one nobody has looked at for a month.
--
-- Two tables rather than settings keys. A destination is a collection -- there
-- can be several, each with its own credential and its own retention -- and the
-- flat key/value store has no way to hold one: `backup.destination.3.secret`
-- is a table with the constraints left out. A run is a record of something
-- that happened, which is not configuration at all.
--
-- The schedule itself, and the passphrase runs are sealed with, stay in
-- `settings`: there is one schedule and one passphrase, so they are settings in
-- the ordinary sense and the catalog is their authority.

CREATE TABLE backup_destinations (
    id          TEXT    PRIMARY KEY,
    name        TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    -- The non-secret half of the form, as JSON, because the four kinds ask for
    -- different things and a column per field would be mostly NULL. Validated
    -- in Go against the kind before it is written; json_valid here is the
    -- backstop that keeps a row readable rather than the check that keeps it
    -- meaningful.
    config_json TEXT    NOT NULL DEFAULT '{}',
    -- The one credential a destination has: an SFTP password or private key, an
    -- S3 secret key, a WebDAV password. Encrypted with the same settings key
    -- every other stored credential uses; '' means none is set.
    secret      TEXT    NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,

    -- Retention, per destination. A NAS with room for a year and a bucket
    -- somebody pays for by the gigabyte are not the same policy, and one number
    -- for both would be the smaller of the two.
    keep_last    INTEGER NOT NULL DEFAULT 6,
    keep_daily   INTEGER NOT NULL DEFAULT 0,
    keep_weekly  INTEGER NOT NULL DEFAULT 0,
    keep_monthly INTEGER NOT NULL DEFAULT 0,

    -- The SFTP server's public key, as ssh-keygen prints it
    -- (`SHA256:` and base64). Its own column rather than a field in
    -- config_json because it is the one value in the form that is checked
    -- rather than sent: a mismatch stops the transfer, and a constraint can
    -- say that an enabled SFTP destination must have one.
    host_key    TEXT    NOT NULL DEFAULT '',

    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,

    -- What happened last time. NULL last_ok is "never ran", which is not the
    -- same fact as "ran and failed" and must not be collapsed into it.
    last_run_at INTEGER,
    last_ok     INTEGER,
    -- The sentence and the evidence, apart. Anything rendering last_detail in
    -- prose is a bug; it holds a host, a bucket, a status code.
    last_error  TEXT    NOT NULL DEFAULT '',
    last_detail TEXT    NOT NULL DEFAULT '',
    -- How many of mcpd's own archives the last successful run saw here. A
    -- listing that has suddenly lost most of them is a server answering wrongly
    -- rather than a retention decision, and retention that trusted it would
    -- delete the backups the wrong answer hid.
    last_seen   INTEGER NOT NULL DEFAULT 0,

    CHECK (kind IN ('local', 'sftp', 's3', 'webdav')),
    CHECK (json_valid(config_json)),
    CHECK (enabled IN (0, 1)),
    CHECK (last_ok IS NULL OR last_ok IN (0, 1)),
    CHECK (keep_last >= 1),
    CHECK (keep_daily >= 0 AND keep_weekly >= 0 AND keep_monthly >= 0),
    CHECK (last_seen >= 0),
    CHECK (trim(name) <> ''),
    -- No trust on first use. An SFTP destination with no recorded host key
    -- cannot be enabled, so a run never learns an identity from whatever
    -- answers on the night: the key is recorded by an operator pasting it or by
    -- Test connection, both of which happen while somebody is looking.
    CHECK (kind <> 'sftp' OR enabled = 0 OR host_key <> '')
) STRICT;

-- One destination per name, compared the way a person compares them. The
-- expression index is what a conflict is matched on; see isDestinationConflict.
CREATE UNIQUE INDEX ux_backup_destinations_name
    ON backup_destinations (lower(name));

CREATE TABLE backup_runs (
    id           TEXT    PRIMARY KEY,
    started_at   INTEGER NOT NULL,
    -- NULL only while the run is still going. A row that says nothing about
    -- when it ended and is not running is a row nothing wrote the end of.
    finished_at  INTEGER,
    trigger      TEXT    NOT NULL,
    archive_name TEXT    NOT NULL DEFAULT '',
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    -- 'interrupted' is not 'failed'. A process that stopped mid-run may have
    -- uploaded to some destinations and not others, and calling that a failure
    -- invites somebody to conclude nothing was written.
    status       TEXT    NOT NULL,
    error        TEXT    NOT NULL DEFAULT '',
    detail       TEXT    NOT NULL DEFAULT '',
    -- One entry per destination the run reached: name, kind, whether it worked,
    -- the sentence, the evidence, and how many old archives were removed.
    destinations_json TEXT NOT NULL DEFAULT '[]',

    CHECK (trigger IN ('schedule', 'manual')),
    CHECK (status IN ('running', 'ok', 'partial', 'failed', 'interrupted')),
    CHECK (json_valid(destinations_json)),
    CHECK (status = 'running' OR finished_at IS NOT NULL)
) STRICT;

-- The history is read newest first and capped at the newest hundred.
CREATE INDEX ix_backup_runs_started ON backup_runs (started_at DESC);

-- At most one run at a time, enforced by the database rather than by a lock in
-- one process. A second run would take a second snapshot of a database the
-- first is already copying, and would race it to the same names on every
-- destination.
CREATE UNIQUE INDEX ux_backup_runs_running
    ON backup_runs (status) WHERE status = 'running';
