package app

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"time"

	"github.com/spoked/mcpd/internal/backup"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// newBackupService tells internal/backup what this host is.
//
// Everything it needs is settled here, at the one place that already knows
// where the database lives and which key its secrets are under, so the
// dashboard's handlers can stay handlers.
func (a *App) newBackupService(cfg *config.Config, db *sqlite.DB, log *slog.Logger) *backup.Service {
	// The highest migration this build carries. A failure to read the embedded
	// migrations is not worth refusing to start over -- it would mean the
	// binary is malformed and Migrate has already failed -- so a restore is
	// left with no ceiling to check rather than the host left down.
	latest, err := sqlite.LatestSchemaVersion()
	if err != nil {
		log.Warn("could not read this build's migrations, so a restore cannot "+
			"check an archive's schema against them", "error", err)
	}

	return backup.NewService(backup.ServiceConfig{
		Snapshot:       db.Backup,
		VerifyDatabase: verifyDatabase,
		StorageDir:     cfg.StorageDir(),
		DatabasePath:   cfg.Storage.Path,
		TLSDir:         cfg.TLSDir(),
		ConfigPath:     cfg.Path(),
		// The out-of-process plugins. They are executables an operator put on
		// the host by hand, and an instance restored without them comes up
		// configured for integrations that are not there.
		PluginsDir:     cfg.PluginsDir(),
		KeyFingerprint: func() string { return a.keyFingerprint },
		SchemaVersion: func(ctx context.Context) int {
			v, err := sqlite.SchemaVersion(ctx, db)
			if err != nil {
				return 0
			}
			return v
		},
		MaxSchema: latest,
		Instance:  a.publicURL,
		Version:   Version,
		Log:       log.With("component", "backup"),
	})
}

// newBackupRunner wires the worker that takes backups without anybody asking.
//
// It reads the schedule and the passphrase per run rather than holding them, so
// a change on the page takes effect at the next fire instead of the next
// restart -- and it takes the trust pool as a function for the same reason a
// plugin does: an operator can add a certificate while mcpd runs.
func (a *App) newBackupRunner(log *slog.Logger) *backup.Runner {
	return backup.NewRunner(backup.RunnerConfig{
		Service: a.backups,
		Store:   a.backupStore,
		Schedule: func(ctx context.Context) backup.Schedule {
			return backup.ParseSchedule(
				a.settings.FieldBool(ctx, settings.KeyBackupScheduleEnabled),
				a.settings.FieldString(ctx, settings.KeyBackupScheduleCadence),
				a.settings.Int(ctx, settings.KeyBackupScheduleWeekday, 0),
				a.settings.FieldString(ctx, settings.KeyBackupScheduleTime),
				a.settings.FieldString(ctx, settings.KeyBackupScheduleTimezone),
			)
		},
		Passphrase: func(ctx context.Context) string {
			return a.settings.Secret(ctx, settings.KeyBackupPassphrase, "")
		},
		Pool:   func(context.Context) *x509.CertPool { return a.trustPool.get() },
		Failed: a.notifyBackupFailed,
		Log:    log.With("component", "backup-runner"),
		Now:    time.Now,
	})
}

// verifyDatabase opens an archive's database and checks it before the host
// agrees to replace its own with it.
//
// SQLite's own integrity check, not a read of one table: a file can answer a
// query and still be structurally damaged in pages nothing has touched yet,
// and the point of checking here is that afterwards there is no working
// instance left to notice on.
func verifyDatabase(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	// The default options, deliberately. The ones this deployment configured
	// live in the database being examined, and reading them would mean opening
	// it to decide how to open it.
	db, err := sqlite.Open(ctx, sqlite.Options{Path: path})
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer db.Close()

	if err := db.Integrity(ctx); err != nil {
		return err
	}
	if _, err := sqlite.SchemaVersion(ctx, db); err != nil {
		return fmt.Errorf("read the schema version: %w", err)
	}
	return nil
}
