package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed all:migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version  int
	name     string
	body     string
	checksum string
}

// Migrate applies every pending migration inside a single transaction per
// migration, recording the applied version and a checksum of the file.
//
// Migrations are forward-only. There is no down path: rolling a schema change
// back on a database that already holds approved operations is a data-loss
// decision, not an automated one, and pretending otherwise encourages running
// it in an incident.
func Migrate(ctx context.Context, db *DB) (applied int, err error) {
	migrations, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	w := db.Writer()

	if _, err := w.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT    NOT NULL,
			checksum   TEXT    NOT NULL,
			applied_at INTEGER NOT NULL
		) STRICT`); err != nil {
		return 0, fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}

	current, err := appliedVersions(ctx, w)
	if err != nil {
		return 0, err
	}

	for _, m := range migrations {
		if prev, ok := current[m.version]; ok {
			// A changed checksum means a migration file was edited after it
			// ran somewhere. Continuing would leave two databases with the
			// same version number and different schemas.
			if prev != m.checksum {
				return applied, fmt.Errorf(
					"sqlite: migration %04d (%s) was modified after it was applied "+
						"(recorded %s, found %s); create a new migration instead",
					m.version, m.name, short(prev), short(m.checksum))
			}
			continue
		}
		if err := applyOne(ctx, w, m); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

func applyOne(ctx context.Context, w *sql.DB, m migration) error {
	tx, err := w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin migration %04d: %w", m.version, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	if _, err := tx.ExecContext(ctx, m.body); err != nil {
		return fmt.Errorf("sqlite: migration %04d (%s): %w", m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?,?,?,?)`,
		m.version, m.name, m.checksum, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("sqlite: record migration %04d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit migration %04d: %w", m.version, err)
	}
	return nil
}

func appliedVersions(ctx context.Context, w *sql.DB) (map[int]string, error) {
	rows, err := w.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	defer rows.Close()

	out := make(map[int]string)
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, err
		}
		out[v] = sum
	}
	return out, rows.Err()
}

// loadMigrations reads and validates the embedded migration set. File names
// must be NNNN_name.sql; anything else is a mistake that should surface at
// startup rather than being skipped silently.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("sqlite: read embedded migrations: %w", err)
	}

	var out []migration
	seen := make(map[int]string)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(e.Name())
		if err != nil {
			return nil, err
		}
		if dup, ok := seen[version]; ok {
			return nil, fmt.Errorf("sqlite: duplicate migration version %04d: %s and %s",
				version, dup, e.Name())
		}
		seen[version] = e.Name()

		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		out = append(out, migration{
			version:  version,
			name:     name,
			body:     string(body),
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	if len(out) == 0 {
		return nil, errors.New("sqlite: no migrations found")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func parseMigrationName(filename string) (int, string, error) {
	base := strings.TrimSuffix(filename, ".sql")
	idx := strings.Index(base, "_")
	if idx <= 0 {
		return 0, "", fmt.Errorf("sqlite: migration %q must be named NNNN_description.sql", filename)
	}
	version, err := strconv.Atoi(base[:idx])
	if err != nil {
		return 0, "", fmt.Errorf("sqlite: migration %q has a non-numeric version: %w", filename, err)
	}
	if version <= 0 {
		return 0, "", fmt.Errorf("sqlite: migration %q must have a positive version", filename)
	}
	return version, base[idx+1:], nil
}

// LatestSchemaVersion reports the highest migration this build carries, which
// is what the database will be at once Migrate has run.
//
// Distinct from SchemaVersion, which reports what a particular database has
// actually applied. The two differ while an upgrade is pending, and the
// difference is what tells a restore that an archive is from a newer build
// than this one: migrations only go forward, so a database past this number
// has tables this binary does not know and no way back down.
func LatestSchemaVersion() (int, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	latest := 0
	for _, m := range migrations {
		if m.version > latest {
			latest = m.version
		}
	}
	return latest, nil
}

// SchemaVersion reports the highest applied migration version.
func SchemaVersion(ctx context.Context, db *DB) (int, error) {
	var v sql.NullInt64
	err := db.Reader().QueryRowContext(ctx,
		`SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}
