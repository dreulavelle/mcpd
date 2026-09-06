package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/backup"
	"github.com/spoked/mcpd/internal/operations"
)

// Where backups go, and what happened when they went.
//
// The same arrangement chatgpt_accounts has, for the same reason: a collection
// with a credential in each row cannot live in the flat settings store without
// synthesising keys, and a name that must be unique and a retention that must
// be at least one are constraints a table can hold and a key/value store cannot.
//
// The runs half is not configuration at all. It is the record an operator reads
// to answer "did last night work", and it is bounded at the newest hundred
// inside the transaction that adds to it.

// keepRuns is how much history is kept.
//
// A hundred is roughly two years of weekly backups, or three months of daily
// ones with manual runs in between -- long enough that a pattern of failures is
// visible, short enough that the table never becomes something to think about.
const keepRuns = 100

// BackupStore holds the destinations and the run history.
type BackupStore struct {
	db     *DB
	cipher Cipher
	now    func() time.Time
	// storageDir is mcpd's own data directory, held because a local
	// destination may not be it, inside it, or above it -- and that is a
	// property of a valid destination rather than of the handler that happened
	// to receive one.
	storageDir string
}

// NewBackupStore returns a store backed by db.
//
// A nil cipher leaves the store usable for reading and for destinations with no
// credential, and refuses anything that would store one in the clear.
func NewBackupStore(db *DB, cipher Cipher, now func() time.Time, storageDir string) *BackupStore {
	if now == nil {
		now = time.Now
	}
	return &BackupStore{db: db, cipher: cipher, now: now, storageDir: storageDir}
}

// The refusals this store hands back are the backup package's own.
//
// One identity rather than two, so the dashboard can classify with errors.Is
// against a domain error without importing storage, and so a sentence written
// for a person lives beside the rules that produce it. Named here as well
// because that is what the callers in this package read.
var (
	ErrNoSuchDestination  = backup.ErrNoSuchDestination
	ErrDestinationExists  = backup.ErrDestinationExists
	ErrDestinationChanged = backup.ErrDestinationChanged
	ErrNoBackupCipher     = backup.ErrNoCipher
)

// NewRunID mints an identifier for a run.
func (s *BackupStore) NewRunID() string { return newBackupRunID() }

// --- destinations ----------------------------------------------------------

// CreateDestination stores a new destination.
//
// The insert is guarded by the unique index rather than by a prior read, so two
// administrators racing the same name produce one destination and one refusal.
func (s *BackupStore) CreateDestination(
	ctx context.Context, actor string, d backup.Destination,
) (backup.Destination, error) {
	if err := d.Validate(s.storageDir); err != nil {
		return backup.Destination{}, err
	}
	sealed, err := s.encrypt(d.Secret)
	if err != nil {
		return backup.Destination{}, err
	}
	config, err := json.Marshal(d.Settings)
	if err != nil {
		return backup.Destination{}, fmt.Errorf("sqlite: encode destination settings: %w", err)
	}

	now := s.now()
	d.ID = newDestinationID()
	d.CreatedAt, d.UpdatedAt = now, now

	err = s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		_, err := u.exec(`
			INSERT INTO backup_destinations
			  (id, name, kind, config_json, secret, enabled, keep_last, keep_daily,
			   keep_weekly, keep_monthly, host_key, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			d.ID, d.Name, string(d.Kind), string(config), sealed, boolToInt(d.Enabled),
			d.Policy.KeepLast, d.Policy.KeepDaily, d.Policy.KeepWeekly, d.Policy.KeepMonthly,
			strings.TrimSpace(d.HostKey), now.UnixMilli(), now.UnixMilli())
		if err != nil {
			if isUniqueViolation(err, "ux_backup_destinations_name") {
				return ErrDestinationExists
			}
			return fmt.Errorf("sqlite: create backup destination: %w", err)
		}
		return auditDestination(u, "backup.destination.added", actor, d, "add", map[string]any{
			"kind":    string(d.Kind),
			"enabled": d.Enabled,
			"where":   d.Where(),
		})
	})
	if err != nil {
		return backup.Destination{}, err
	}
	return d, nil
}

// UpdateDestination edits one in place.
func (s *BackupStore) UpdateDestination(
	ctx context.Context, actor, id string, up backup.DestinationUpdate,
) (backup.Destination, error) {
	current, ok, err := s.Destination(ctx, id)
	if err != nil {
		return backup.Destination{}, err
	}
	if !ok {
		return backup.Destination{}, ErrNoSuchDestination
	}
	return s.applyUpdate(ctx, actor, current, up)
}

// applyUpdate writes an edit against the version the caller read.
//
// Split from the read above so the guard has a seam a test can reach: what it
// defends is a write built on a row somebody else has since replaced, and
// UpdateDestination reads immediately before writing, so through that door the
// two versions always agree.
func (s *BackupStore) applyUpdate(
	ctx context.Context, actor string, current backup.Destination, up backup.DestinationUpdate,
) (backup.Destination, error) {
	id := current.ID
	next := current
	changed := map[string]any{}
	if up.Name != nil && strings.TrimSpace(*up.Name) != current.Name {
		next.Name = strings.TrimSpace(*up.Name)
		changed["name"] = next.Name
	}
	if up.Settings != nil {
		next.Settings = *up.Settings
		changed["settings"] = "replaced"
	}
	if up.Secret != nil {
		next.Secret = *up.Secret
		// The value is never recorded, only that it moved -- which is the part
		// an operator reading the trail after a destination stopped working
		// actually needs.
		changed["credential"] = map[bool]string{true: "cleared", false: "replaced"}[strings.TrimSpace(*up.Secret) == ""]
	}
	if up.Enabled != nil && *up.Enabled != current.Enabled {
		next.Enabled = *up.Enabled
		changed["enabled"] = next.Enabled
	}
	if up.Policy != nil && *up.Policy != current.Policy {
		next.Policy = *up.Policy
		changed["retention"] = next.Policy
	}
	if up.HostKey != nil && strings.TrimSpace(*up.HostKey) != current.HostKey {
		next.HostKey = strings.TrimSpace(*up.HostKey)
		// Recorded in full. A host key is a public fingerprint, and which one a
		// destination was pinned to is exactly what somebody reading the trail
		// after a refused backup wants to see.
		changed["host_key"] = next.HostKey
	}
	if len(changed) == 0 {
		// Nothing moved. Recording it would put an entry in the trail for an
		// operator who opened a form and closed it.
		return current, nil
	}
	if err := next.Validate(s.storageDir); err != nil {
		return backup.Destination{}, err
	}

	sealed, err := s.encrypt(next.Secret)
	if err != nil {
		return backup.Destination{}, err
	}
	config, err := json.Marshal(next.Settings)
	if err != nil {
		return backup.Destination{}, fmt.Errorf("sqlite: encode destination settings: %w", err)
	}

	now := s.now()
	next.UpdatedAt = now
	err = s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		// Guarded on updated_at as well as id, so two administrators editing
		// one destination at once do not have the second silently overwrite a
		// change the first made and nobody saw.
		//
		// An SFTP destination being switched on with no host key is refused by
		// Validate above and by the table's own CHECK below, so it is not a
		// condition here: repeating it would only make zero rows mean a third
		// thing this has to guess between.
		res, err := u.exec(`
			UPDATE backup_destinations
			   SET name = ?, config_json = ?, secret = ?, enabled = ?,
			       keep_last = ?, keep_daily = ?, keep_weekly = ?, keep_monthly = ?,
			       host_key = ?, updated_at = ?
			 WHERE id = ? AND updated_at = ?`,
			next.Name, string(config), sealed, boolToInt(next.Enabled),
			next.Policy.KeepLast, next.Policy.KeepDaily, next.Policy.KeepWeekly,
			next.Policy.KeepMonthly, next.HostKey, now.UnixMilli(),
			id, current.UpdatedAt.UnixMilli())
		if err != nil {
			if isUniqueViolation(err, "ux_backup_destinations_name") {
				return ErrDestinationExists
			}
			return fmt.Errorf("sqlite: update backup destination: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Zero rows is two different things, and they are two different
			// things to be told: the destination is gone, or somebody else
			// wrote to it between this caller's read and this write. Only the
			// second is worth reopening the form for, so the row is asked for
			// again inside this transaction rather than guessed at.
			var exists int
			if err := u.queryRow(
				`SELECT COUNT(*) FROM backup_destinations WHERE id = ?`, id).
				Scan(&exists); err != nil {
				return fmt.Errorf("sqlite: read backup destination after a lost write: %w", err)
			}
			if exists == 0 {
				return ErrNoSuchDestination
			}
			return ErrDestinationChanged
		}
		return auditDestination(u, "backup.destination.updated", actor, next, "update", changed)
	})
	if err != nil {
		return backup.Destination{}, err
	}
	return next, nil
}

// DeleteDestination forgets a destination.
//
// Nothing on the destination itself is touched. A backup already written there
// is a file somebody may need, and removing a row is mcpd being told to stop
// sending, not being told to delete what it sent.
func (s *BackupStore) DeleteDestination(ctx context.Context, actor, id string) error {
	return s.db.WriteTx(ctx, s.now().UnixMilli(), func(u *UnitOfWork) error {
		var name, kind string
		err := u.queryRow(`SELECT name, kind FROM backup_destinations WHERE id = ?`, id).
			Scan(&name, &kind)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchDestination
		}
		if err != nil {
			return fmt.Errorf("sqlite: read backup destination before removal: %w", err)
		}

		res, err := u.exec(`DELETE FROM backup_destinations WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("sqlite: remove backup destination: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoSuchDestination
		}
		return auditDestination(u, "backup.destination.removed", actor,
			backup.Destination{ID: id, Name: name}, "remove",
			map[string]any{"kind": kind})
	})
}

// RecordHostKey pins what a server presented, from the Test connection
// endpoint and from nowhere else.
//
// Guarded on the key still being empty, which is what makes this recording a
// first contact rather than overwriting a pin. A server that has changed its
// key is a refusal an operator has to clear deliberately; if this could
// overwrite, a mismatch would be one button press away from being accepted.
// It reports whether it actually recorded one. Zero rows means the destination
// already has a key pinned, or is not an SFTP destination -- neither is an
// error, and both mean the caller must not tell an operator that the key they
// are looking at is now the one mcpd will check against.
func (s *BackupStore) RecordHostKey(ctx context.Context, actor, id, fingerprint string) (bool, error) {
	fingerprint = strings.TrimSpace(fingerprint)
	if fingerprint == "" {
		return false, errors.New("sqlite: no host key to record")
	}
	now := s.now()
	recorded := false
	err := s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		var name string
		err := u.queryRow(`SELECT name FROM backup_destinations WHERE id = ?`, id).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchDestination
		}
		if err != nil {
			return err
		}
		res, err := u.exec(`
			UPDATE backup_destinations
			   SET host_key = ?, updated_at = ?
			 WHERE id = ? AND kind = 'sftp' AND host_key = ''`,
			fingerprint, now.UnixMilli(), id)
		if err != nil {
			return fmt.Errorf("sqlite: record host key: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		recorded = true
		return auditDestination(u, "backup.destination.updated", actor,
			backup.Destination{ID: id, Name: name}, "update",
			map[string]any{"host_key": fingerprint, "recorded_by": "test connection"})
	})
	return recorded, err
}

// ClearHostKey forgets the pinned key, so a rebuilt server can be pinned again.
//
// Its own method rather than part of an update, because it is the one act here
// that reduces what mcpd checks, and it should read that way in the trail.
func (s *BackupStore) ClearHostKey(ctx context.Context, actor, id string) error {
	now := s.now()
	return s.db.WriteTx(ctx, now.UnixMilli(), func(u *UnitOfWork) error {
		var name, previous string
		err := u.queryRow(`SELECT name, host_key FROM backup_destinations WHERE id = ?`, id).
			Scan(&name, &previous)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoSuchDestination
		}
		if err != nil {
			return err
		}
		// Switched off in the same statement: a destination with no key is one
		// a run must not use, and leaving it enabled would fail the table's own
		// constraint anyway.
		if _, err := u.exec(`
			UPDATE backup_destinations
			   SET host_key = '', enabled = 0, updated_at = ?
			 WHERE id = ?`, now.UnixMilli(), id); err != nil {
			return fmt.Errorf("sqlite: clear host key: %w", err)
		}
		return auditDestination(u, "backup.destination.updated", actor,
			backup.Destination{ID: id, Name: name}, "update",
			map[string]any{"host_key": "cleared", "was": previous, "enabled": false})
	})
}

// Destinations returns every destination, by name, with credentials in the
// clear for the runner that is about to authenticate with them.
//
// Only the runner should call this. Everything that merely renders or counts
// destinations wants ListDestinations, which never decrypts.
func (s *BackupStore) Destinations(ctx context.Context) ([]backup.Destination, error) {
	return s.listDestinations(ctx, true)
}

// ListDestinations returns every destination without decrypting anything.
//
// The dashboard and the schedule summary read this on every page load, and
// neither has any use for a credential: what they render is whether one is set,
// which SecretSet answers. Decrypting on that path would run the cipher over
// every stored secret to produce a boolean, and would put a host with an
// unreadable key -- a key rotated without re-entering the credentials -- in the
// position of not being able to draw the page that fixes it.
func (s *BackupStore) ListDestinations(ctx context.Context) ([]backup.Destination, error) {
	return s.listDestinations(ctx, false)
}

func (s *BackupStore) listDestinations(ctx context.Context, decrypt bool) ([]backup.Destination, error) {
	rows, err := s.db.Reader().QueryContext(ctx, destinationColumns+
		` FROM backup_destinations ORDER BY lower(name)`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list backup destinations: %w", err)
	}
	defer rows.Close()

	var out []backup.Destination
	for rows.Next() {
		d, err := s.scanDestination(rows, decrypt)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Destination returns one, with its credential in the clear.
//
// The credential is needed here: this is what an edit reads before it writes
// back, and what Test connection hands to a transport.
func (s *BackupStore) Destination(ctx context.Context, id string) (backup.Destination, bool, error) {
	row := s.db.Reader().QueryRowContext(ctx, destinationColumns+
		` FROM backup_destinations WHERE id = ?`, id)
	d, err := s.scanDestination(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return backup.Destination{}, false, nil
	}
	if err != nil {
		return backup.Destination{}, false, err
	}
	return d, true, nil
}

const destinationColumns = `
	SELECT id, name, kind, config_json, secret, enabled, keep_last, keep_daily,
	       keep_weekly, keep_monthly, host_key, created_at, updated_at,
	       last_run_at, last_ok, last_error, last_detail, last_seen`

func (s *BackupStore) scanDestination(sc scanner, decrypt bool) (backup.Destination, error) {
	var (
		d                   backup.Destination
		kind, config        string
		secret              string
		enabled             int
		createdAt, updated  int64
		lastRunAt, lastOKAt sql.NullInt64
	)
	if err := sc.Scan(&d.ID, &d.Name, &kind, &config, &secret, &enabled,
		&d.Policy.KeepLast, &d.Policy.KeepDaily, &d.Policy.KeepWeekly, &d.Policy.KeepMonthly,
		&d.HostKey, &createdAt, &updated,
		&lastRunAt, &lastOKAt, &d.LastError, &d.LastDetail, &d.LastSeen); err != nil {
		return backup.Destination{}, err
	}
	d.Kind = backup.Kind(kind)
	d.Enabled = enabled == 1
	d.CreatedAt = time.UnixMilli(createdAt)
	d.UpdatedAt = time.UnixMilli(updated)
	if lastRunAt.Valid {
		at := time.UnixMilli(lastRunAt.Int64).UTC()
		d.LastRunAt = &at
	}
	if lastOKAt.Valid {
		// Null stays nil rather than becoming false. "Never ran" and "ran and
		// failed" are different facts and the page renders them differently.
		ok := lastOKAt.Int64 == 1
		d.LastOK = &ok
	}
	if err := json.Unmarshal([]byte(config), &d.Settings); err != nil {
		return backup.Destination{}, fmt.Errorf(
			"sqlite: decode settings for backup destination %q: %w", d.Name, err)
	}
	// Always, and without the cipher: whether a credential is set is a fact the
	// page renders, and it is readable from the column's length alone.
	d.SecretSet = secret != ""
	if secret != "" && decrypt {
		if s.cipher == nil {
			return backup.Destination{}, ErrNoBackupCipher
		}
		plain, err := s.cipher.Decrypt(secret)
		if err != nil {
			return backup.Destination{}, fmt.Errorf(
				"sqlite: decrypt the credential for backup destination %q: %w", d.Name, err)
		}
		d.Secret = plain
	}
	return d, nil
}

func (s *BackupStore) encrypt(secret string) (string, error) {
	if secret == "" {
		return "", nil
	}
	if s.cipher == nil {
		return "", ErrNoBackupCipher
	}
	sealed, err := s.cipher.Encrypt(secret)
	if err != nil {
		return "", fmt.Errorf("sqlite: encrypt a backup destination's credential: %w", err)
	}
	return sealed, nil
}

// --- runs ------------------------------------------------------------------

// StartRun inserts the running row that gates every other run.
//
// The condition is in the statement: the insert only lands when no row is
// already running. Two administrators pressing the button together, or one
// pressing it as the schedule fires, produce one run and one refusal, decided
// by the database rather than by a lock this process holds.
func (s *BackupStore) StartRun(ctx context.Context, run backup.Run) error {
	return s.db.WriteTx(ctx, run.StartedAt.UnixMilli(), func(u *UnitOfWork) error {
		res, err := u.exec(`
			INSERT INTO backup_runs (id, started_at, trigger, status, destinations_json)
			SELECT ?, ?, ?, 'running', '[]'
			 WHERE NOT EXISTS (SELECT 1 FROM backup_runs WHERE status = 'running')`,
			run.ID, run.StartedAt.UnixMilli(), run.Trigger)
		if err != nil {
			return fmt.Errorf("sqlite: start backup run: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return backup.ErrRunInProgress
		}
		// Capped in the same transaction that adds to it, so the table cannot
		// grow past the bound even briefly, and a run that is still going is
		// never the one removed.
		if _, err := u.exec(`
			DELETE FROM backup_runs
			 WHERE status <> 'running'
			   AND id NOT IN (
			       SELECT id FROM backup_runs ORDER BY started_at DESC LIMIT ?)`,
			keepRuns); err != nil {
			return fmt.Errorf("sqlite: trim the backup history: %w", err)
		}
		return nil
	})
}

// FinishRun settles a run that is still marked running.
//
// Guarded on the status, so a run already settled -- by a previous process
// calling it interrupted, say -- is not rewritten by a worker that came back.
func (s *BackupStore) FinishRun(ctx context.Context, run backup.Run) error {
	dests, err := json.Marshal(nonNilDestinations(run.Destinations))
	if err != nil {
		return fmt.Errorf("sqlite: encode a backup run's destinations: %w", err)
	}
	finished := s.now()
	if run.FinishedAt != nil {
		finished = *run.FinishedAt
	}
	return s.db.WriteTx(ctx, finished.UnixMilli(), func(u *UnitOfWork) error {
		res, err := u.exec(`
			UPDATE backup_runs
			   SET finished_at = ?, archive_name = ?, size_bytes = ?, status = ?,
			       error = ?, detail = ?, destinations_json = ?
			 WHERE id = ? AND status = 'running'`,
			finished.UnixMilli(), run.ArchiveName, run.SizeBytes, run.Status,
			run.Error, run.Detail, string(dests), run.ID)
		if err != nil {
			return fmt.Errorf("sqlite: finish backup run: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoRowsAffected
		}
		return nil
	})
}

// MarkInterrupted settles runs a stopped process left behind.
//
// 'interrupted' rather than 'failed', because a run that was halfway through
// may have uploaded to some destinations and not others. Indeterminate is not
// terminal, and a reader told the backup failed would conclude nothing was
// written.
//
// `before` is when this process's runner was built, and it is in the WHERE
// clause rather than assumed. The dashboard's listener and the backup worker
// both start during App.Run, so a backup asked for over the API in that window
// has a running row of its own -- and a sweep that took every running row would
// settle the run it is about to carry out.
func (s *BackupStore) MarkInterrupted(ctx context.Context, before, at time.Time) (int, error) {
	var n int64
	err := s.db.WriteTx(ctx, at.UnixMilli(), func(u *UnitOfWork) error {
		res, err := u.exec(`
			UPDATE backup_runs
			   SET status = 'interrupted', finished_at = ?,
			       error = 'mcpd stopped while this backup was running. Some ' ||
			               'destinations may hold it and some may not.'
			 WHERE status = 'running' AND started_at < ?`,
			at.UnixMilli(), before.UnixMilli())
		if err != nil {
			return fmt.Errorf("sqlite: settle interrupted backup runs: %w", err)
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return int(n), err
}

// RecordDestination writes what a run found at one destination.
//
// last_seen moves only when this run both succeeded and came away with a count
// it trusts -- which is what backup.UnknownSeen says it did not. Both
// conditions are in the statement rather than in Go beforehand, because this is
// the value that stops a truncated listing from being acted on: a run that
// wrote its own small count over the baseline would disarm that check for good,
// and the next short listing would delete real backups.
func (s *BackupStore) RecordDestination(ctx context.Context, out backup.DestinationOutcome) error {
	return s.db.WriteTx(ctx, out.At.UnixMilli(), func(u *UnitOfWork) error {
		_, err := u.exec(`
			UPDATE backup_destinations
			   SET last_run_at = ?, last_ok = ?, last_error = ?, last_detail = ?,
			       last_seen = CASE WHEN ? = 1 AND ? >= 0 THEN ? ELSE last_seen END
			 WHERE id = ?`,
			out.At.UnixMilli(), boolToInt(out.OK), out.Error, out.Detail,
			boolToInt(out.OK), out.Seen, out.Seen, out.ID)
		if err != nil {
			return fmt.Errorf("sqlite: record a backup destination's outcome: %w", err)
		}
		return nil
	})
}

// Runs returns the history, newest first.
func (s *BackupStore) Runs(ctx context.Context, limit int) ([]backup.Run, error) {
	if limit <= 0 || limit > keepRuns {
		limit = 25
	}
	rows, err := s.db.Reader().QueryContext(ctx, runColumns+
		` FROM backup_runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list backup runs: %w", err)
	}
	defer rows.Close()

	var out []backup.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// Run returns one.
func (s *BackupStore) Run(ctx context.Context, id string) (backup.Run, bool, error) {
	run, err := scanRun(s.db.Reader().QueryRowContext(ctx,
		runColumns+` FROM backup_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return backup.Run{}, false, nil
	}
	if err != nil {
		return backup.Run{}, false, err
	}
	return run, true, nil
}

// LatestRun is the most recent run of any kind.
func (s *BackupStore) LatestRun(ctx context.Context) (backup.Run, bool, error) {
	run, err := scanRun(s.db.Reader().QueryRowContext(ctx,
		runColumns+` FROM backup_runs ORDER BY started_at DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return backup.Run{}, false, nil
	}
	if err != nil {
		return backup.Run{}, false, err
	}
	return run, true, nil
}

// LastScheduledRun reports when a scheduled run last started, and whether one
// ever has.
//
// The second half is what stops a schedule that has just been switched on from
// firing immediately: with no scheduled run on record there is nothing to catch
// up to, whatever the calendar says about this morning.
func (s *BackupStore) LastScheduledRun(ctx context.Context) (time.Time, bool, error) {
	var at sql.NullInt64
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT MAX(started_at) FROM backup_runs WHERE trigger = 'schedule'`).Scan(&at)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("sqlite: read the last scheduled backup: %w", err)
	}
	if !at.Valid {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(at.Int64).UTC(), true, nil
}

const runColumns = `
	SELECT id, started_at, finished_at, trigger, archive_name, size_bytes,
	       status, error, detail, destinations_json`

func scanRun(sc scanner) (backup.Run, error) {
	var (
		run       backup.Run
		startedAt int64
		finished  sql.NullInt64
		dests     string
	)
	if err := sc.Scan(&run.ID, &startedAt, &finished, &run.Trigger, &run.ArchiveName,
		&run.SizeBytes, &run.Status, &run.Error, &run.Detail, &dests); err != nil {
		return backup.Run{}, err
	}
	run.StartedAt = time.UnixMilli(startedAt).UTC()
	if finished.Valid {
		at := time.UnixMilli(finished.Int64).UTC()
		run.FinishedAt = &at
	}
	if err := json.Unmarshal([]byte(dests), &run.Destinations); err != nil {
		return backup.Run{}, fmt.Errorf("sqlite: decode a backup run's destinations: %w", err)
	}
	return run, nil
}

// nonNilDestinations keeps the stored JSON an array. A null would make the page
// that maps over it blank on the ordinary state of a run that reached nowhere.
func nonNilDestinations(in []backup.RunDestination) []backup.RunDestination {
	if in == nil {
		return []backup.RunDestination{}
	}
	return in
}

// auditDestination records an administrative act against a destination.
//
// The audit trail rather than settings_history, for the reason a ChatGPT
// account goes there: a destination is where a complete copy of this instance
// -- every account, every credential, the whole trail -- is sent, and deciding
// that is a privilege decision rather than a preference.
//
// A run is deliberately not audited. backup_runs is the record of what
// happened, it is written by the act itself, and a second copy in the trail
// would be a second authority for the same fact.
func auditDestination(
	u *UnitOfWork, kind, actor string, d backup.Destination, action string, detail map[string]any,
) error {
	if detail == nil {
		detail = map[string]any{}
	}
	detail["destination"] = d.Name
	detail["destination_id"] = d.ID
	body, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("sqlite: encode audit detail for %s: %w", kind, err)
	}
	return u.appendAudit(operations.AuditEntry{
		EventID: newEventID(),
		Kind:    kind,
		Action:  action,
		Actor:   actor,
		Detail:  body,
	})
}
