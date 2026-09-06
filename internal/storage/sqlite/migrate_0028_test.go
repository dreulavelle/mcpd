package sqlite

import (
	"context"
	"testing"
)

// Two deployments on the same version number must have the same schema, and a
// migration that adds two tables and three indexes is where that can quietly
// stop being true.
func TestMigrate0028_UpgradingMatchesAFreshDatabase(t *testing.T) {
	ctx := context.Background()

	fresh := openDBAt(t, "fresh28.db")
	if _, err := Migrate(ctx, fresh); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	upgraded := openDBAt(t, "upgraded28.db")
	applyThrough(t, upgraded, 27)
	if _, err := Migrate(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if got, want := schemaOf(t, upgraded), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

// The checksum is what stops a migration being edited after it has run
// somewhere. Recording a different one for the same version would leave two
// deployments claiming schema 28 with different tables in them.
func TestMigrate0028_IsRecordedWithItsChecksum(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "checksum28.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var name, checksum string
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT name, checksum FROM schema_migrations WHERE version = 28`).
		Scan(&name, &checksum); err != nil {
		t.Fatalf("read the recorded migration: %v", err)
	}
	if name != "backup_destinations" {
		t.Errorf("recorded name %q", name)
	}
	if len(checksum) != 64 {
		t.Errorf("checksum %q is not a sha256", checksum)
	}

	// And running again is a no-op rather than a second application.
	applied, err := Migrate(ctx, db)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if applied != 0 {
		t.Errorf("applied %d migrations on a database already at the latest", applied)
	}
}

// The constraints are the point of the table, so each one is asserted against
// the database rather than trusted to the Go that writes through it.
func TestMigrate0028_TheConstraintsHold(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "checks28.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	insert := func(t *testing.T, columns, values string, args ...any) error {
		t.Helper()
		_, err := db.Writer().ExecContext(ctx,
			`INSERT INTO backup_destinations (`+columns+`) VALUES (`+values+`)`, args...)
		return err
	}

	const cols = `id, name, kind, config_json, enabled, keep_last, host_key, created_at, updated_at`
	const vals = `?,?,?,?,?,?,?,?,?`

	t.Run("a kind this build does not have is refused", func(t *testing.T) {
		if err := insert(t, cols, vals,
			"dst_1", "dropbox", "dropbox", "{}", 0, 6, "", 1, 1); err == nil {
			t.Error("a destination of an unknown kind was accepted")
		}
	})

	t.Run("keeping nothing is refused", func(t *testing.T) {
		if err := insert(t, cols, vals,
			"dst_2", "keeps nothing", "local", "{}", 0, 0, "", 1, 1); err == nil {
			t.Error("a destination that keeps no archives was accepted; it would " +
				"delete the backup it had just written")
		}
	})

	t.Run("configuration that is not JSON is refused", func(t *testing.T) {
		if err := insert(t, cols, vals,
			"dst_3", "bad json", "local", "not json", 0, 6, "", 1, 1); err == nil {
			t.Error("a destination with unreadable configuration was accepted")
		}
	})

	// The one that matters most: no trust on first use. An SFTP destination
	// with nothing recorded cannot be switched on, so a run can never learn an
	// identity from whatever answers on the night.
	t.Run("an enabled SFTP destination must have a host key", func(t *testing.T) {
		if err := insert(t, cols, vals,
			"dst_4", "nas", "sftp", "{}", 1, 6, "", 1, 1); err == nil {
			t.Error("an SFTP destination was switched on with no host key recorded")
		}
		if err := insert(t, cols, vals,
			"dst_5", "nas off", "sftp", "{}", 0, 6, "", 1, 1); err != nil {
			t.Errorf("an SFTP destination that is switched off was refused: %v", err)
		}
		if err := insert(t, cols, vals,
			"dst_6", "nas pinned", "sftp", "{}", 1, 6, "SHA256:abc", 1, 1); err != nil {
			t.Errorf("an SFTP destination with a pinned key was refused: %v", err)
		}
	})

	t.Run("one destination per name, however it is capitalised", func(t *testing.T) {
		if err := insert(t, cols, vals,
			"dst_7", "Archive Box", "local", "{}", 0, 6, "", 1, 1); err != nil {
			t.Fatalf("first insert: %v", err)
		}
		if err := insert(t, cols, vals,
			"dst_8", "archive box", "local", "{}", 0, 6, "", 1, 1); err == nil {
			t.Error("two destinations were stored under one name")
		}
	})
}

// At most one run at a time, enforced by the database rather than by a lock one
// process holds. A second run would take a second snapshot of a database the
// first is still copying, and race it to the same names on every destination.
func TestMigrate0028_OnlyOneRunCanBeRunning(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "runs28.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	start := func(id string) error {
		_, err := db.Writer().ExecContext(ctx,
			`INSERT INTO backup_runs (id, started_at, trigger, status)
			 VALUES (?, ?, 'manual', 'running')`, id, 1)
		return err
	}
	if err := start("bkr_1"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := start("bkr_2"); err == nil {
		t.Error("a second backup was allowed to start while one was running")
	}

	// Settling the first frees the way, and two settled runs coexist happily --
	// the index is partial, so it constrains only the running ones.
	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE backup_runs SET status = 'ok', finished_at = 2 WHERE id = 'bkr_1'`); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if err := start("bkr_2"); err != nil {
		t.Errorf("a run was refused after the previous one finished: %v", err)
	}
	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE backup_runs SET status = 'ok', finished_at = 3 WHERE id = 'bkr_2'`); err != nil {
		t.Fatalf("settle: %v", err)
	}

	// A run that is not running has to say when it ended.
	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO backup_runs (id, started_at, trigger, status)
		 VALUES ('bkr_3', 4, 'manual', 'ok')`); err == nil {
		t.Error("a finished run was stored with no finishing time")
	}

	// 'interrupted' is in the closed set; a status outside it is not.
	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO backup_runs (id, started_at, finished_at, trigger, status)
		 VALUES ('bkr_4', 5, 6, 'schedule', 'interrupted')`); err != nil {
		t.Errorf("an interrupted run was refused: %v", err)
	}
	if _, err := db.Writer().ExecContext(ctx,
		`INSERT INTO backup_runs (id, started_at, finished_at, trigger, status)
		 VALUES ('bkr_5', 7, 8, 'schedule', 'probably fine')`); err == nil {
		t.Error("a run with a status nothing reads was accepted")
	}
}
