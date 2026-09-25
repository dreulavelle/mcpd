// Package sqlitetest gives a test a migrated database without migrating one.
//
// Every test that needed a database opened a fresh file and ran every
// migration against it. That is a fraction of a second normally and about
// five seconds under the race detector -- the SQLite driver is pure Go, so
// the detector instruments every page it touches -- and there are hundreds of
// such tests. It was most of CI's ten minutes.
//
// So the migrations run once per test binary, into a template, and each test
// gets a byte copy of it. What a test sees is identical to what it saw before:
// the same schema and the same rows migrations seed, in a file of its own.
// The migrations themselves are still exercised, in full, by the template and
// by the migration tests in package sqlite, which build their databases step
// by step on purpose and do not use this.
package sqlitetest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/spoked/mcpd/internal/storage/sqlite"
)

var (
	once     sync.Once
	template []byte
	failure  error
)

// Template returns a migrated, empty database file's bytes, built once.
func Template(t testing.TB) []byte {
	t.Helper()
	once.Do(func() { template, failure = build() })
	if failure != nil {
		t.Fatalf("sqlitetest: build the migrated template: %v", failure)
	}
	return template
}

func build() ([]byte, error) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "mcpd-sqlitetest-")
	if err != nil {
		return nil, err
	}
	// Held in memory rather than left on disk: there is no hook for the end
	// of a test binary, and a file per run would pile up in the temp dir.
	defer os.RemoveAll(dir)

	db, err := sqlite.Open(ctx, sqlite.Options{
		Path: filepath.Join(dir, "template.db"), RelaxedDurability: true,
	})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		return nil, err
	}
	// VACUUM INTO, through the same Backup a real host uses: one
	// self-contained file, with nothing left behind in a -wal.
	out := filepath.Join(dir, "copy.db")
	if err := db.Backup(ctx, out); err != nil {
		return nil, err
	}
	return os.ReadFile(out)
}

// Seed writes a migrated database to path, for a test that hands a path to
// something that opens the database itself -- the app, from its config.
//
// A path that already holds a database is left alone. Tests restart the app
// on the same file to prove something survives a restart, and seeding again
// would replace what they wrote with an empty database.
func Seed(t testing.TB, path string) {
	t.Helper()
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.WriteFile(path, Template(t), 0o600); err != nil {
		t.Fatalf("sqlitetest: seed %s: %v", path, err)
	}
}

// Open returns a migrated database in the test's own temp dir, closed when
// the test ends.
//
// Migrate still runs, against a database that needs nothing: it is how the
// schema's checksums are verified on every open, and a test should open a
// database the way the host does.
func Open(t testing.TB) *sqlite.DB {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")
	Seed(t, path)
	db, err := sqlite.Open(ctx, sqlite.Options{Path: path, RelaxedDurability: true})
	if err != nil {
		t.Fatalf("sqlitetest: open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("sqlitetest: migrate: %v", err)
	}
	return db
}
