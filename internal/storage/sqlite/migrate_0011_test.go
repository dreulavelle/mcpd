package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// displayNameBound is the length 0011 puts on a display name, and the same
// number the users package enforces in Go. Written out rather than imported,
// because importing it would make the storage layer depend on the package that
// depends on it.
const displayNameBound = 64

func openDBAt(t *testing.T, name string) *DB {
	t.Helper()
	db, err := Open(context.Background(), Options{
		Path: filepath.Join(t.TempDir(), name), RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// openDBThrough opens a database migrated up to and including version --
// what a deployment that has not upgraded yet looks like -- in a file of
// its own.
//
// Each version is built once per test binary, from the one below it, and
// every test asking for it starts from a copy. It replaces applyThrough, which
// migrated a database the test had just opened. Building it afresh for every
// test ran the same migrations dozens of times over, several seconds each
// under the race detector, and it was most of this package's time in CI.
// The steps still run in order: a version is the one below it with the next
// migrations applied.
func openDBThrough(t *testing.T, name string, version int) *DB {
	t.Helper()
	image, err := snapshotThrough(version)
	if err != nil {
		t.Fatalf("migrate through %04d: %v", version, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, image, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), Options{Path: path, RelaxedDurability: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

var (
	snapshotsMu sync.Mutex
	snapshots   = map[int][]byte{}
)

// snapshotThrough returns a database file migrated through version. Held
// under one lock while it builds, so tests running in parallel that want the
// same version wait for it rather than each building it.
func snapshotThrough(version int) ([]byte, error) {
	snapshotsMu.Lock()
	defer snapshotsMu.Unlock()
	if image, ok := snapshots[version]; ok {
		return image, nil
	}
	base, from := []byte(nil), 0
	for v, image := range snapshots {
		if v < version && v > from {
			base, from = image, v
		}
	}

	ctx := context.Background()
	dir, err := os.MkdirTemp("", "mcpd-migrate-through-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "through.db")
	if base != nil {
		if err := os.WriteFile(path, base, 0o600); err != nil {
			return nil, err
		}
	}
	db, err := Open(ctx, Options{Path: path, RelaxedDurability: true})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := applyRange(ctx, db, from, version); err != nil {
		return nil, err
	}
	out := filepath.Join(dir, "image.db")
	if err := db.Backup(ctx, out); err != nil {
		return nil, err
	}
	image, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	snapshots[version] = image
	return image, nil
}

// applyRange applies the migrations after from, up to and including to.
func applyRange(ctx context.Context, db *DB, from, to int) error {
	if _, err := db.Writer().ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT    NOT NULL,
			checksum   TEXT    NOT NULL,
			applied_at INTEGER NOT NULL
		) STRICT`); err != nil {
		return err
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range migrations {
		if m.version <= from {
			continue
		}
		if m.version > to {
			return nil
		}
		if err := applyOne(ctx, db.Writer(), m); err != nil {
			return fmt.Errorf("apply %04d: %w", m.version, err)
		}
	}
	return nil
}

// 0011 rebuilds the users table to add a constraint, and a rebuild is the one
// migration shape that can leave two deployments on the same version number
// with different schemas -- a forgotten index, a dropped default.
//
// The existing fresh-versus-upgraded test covers an empty database. This one
// upgrades a populated one, because a rebuild that works on an empty table and
// fails on a filled one is the interesting failure.
func TestMigrate0011_UpgradingAPopulatedDatabaseMatchesAFreshOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// The template is a fresh database migrated in full, built once.
	fresh := newTestDB(t)

	upgraded := openDBThrough(t, "upgraded.db", 10)
	seedAccount(t, upgraded, "usr_1", "alice@example.com", "Alice")
	seedSession(t, upgraded, "hash-1", "ses_1", "usr_1")
	if _, err := Migrate(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if got, want := schemaOf(t, upgraded), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

// The rows come through, and the upgrade must not sign everybody out on the
// way. user_sessions cascades into users, so rebuilding users underneath it
// deletes every session -- which 0007 did, and which is not a reasonable price
// for adding a length check.
func TestMigrate0011_CarriesAccountsAndKeepsSessions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openDBThrough(t, "upgrade.db", 10)
	seedAccount(t, db, "usr_1", "alice@example.com", "Alice")
	seedSession(t, db, "hash-1", "ses_1", "usr_1")

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var name string
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT display_name FROM users WHERE id = 'usr_1'`).Scan(&name); err != nil {
		t.Fatalf("the account did not survive: %v", err)
	}
	if name != "Alice" {
		t.Errorf("display name = %q, want it carried over", name)
	}

	var sessions int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_sessions WHERE id = 'ses_1'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Error("the upgrade signed everybody out; sessions must survive the rebuild")
	}
}

// Rows written before there was a rule are brought inside it, or the rebuild
// fails on a value nobody can now edit.
func TestMigrate0011_NormalisesNamesWrittenBeforeTheRule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openDBThrough(t, "upgrade.db", 10)

	seedAccount(t, db, "usr_long", "long@example.com", strings.Repeat("a", 200))
	seedAccount(t, db, "usr_lines", "lines@example.com", "Alice\nBob")
	seedAccount(t, db, "usr_ok", "ok@example.com", "Carol")

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	read := func(id string) string {
		t.Helper()
		var got string
		if err := db.Reader().QueryRowContext(ctx,
			`SELECT display_name FROM users WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read("usr_long"); len([]rune(got)) != displayNameBound {
		t.Errorf("an over-long name came through at %d characters", len([]rune(got)))
	}
	if got := read("usr_lines"); strings.ContainsAny(got, "\r\n\t") {
		t.Errorf("a name still carries a control character: %q", got)
	}
	if got := read("usr_ok"); got != "Carol" {
		t.Errorf("a name already inside the rule was rewritten to %q", got)
	}
}

// The bound is in the schema, so a value written past this layer -- at a
// sqlite3 prompt, say -- cannot become a value the dashboard renders.
func TestMigrate0011_TheDatabaseRefusesAnOverlongName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openDBAt(t, "fresh.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	seedAccountAtHead(t, db, "usr_1", "alice@example.com", "Alice")

	_, err := db.Writer().ExecContext(ctx,
		`UPDATE users SET display_name = ? WHERE id = 'usr_1'`,
		strings.Repeat("a", displayNameBound+1))
	if err == nil {
		t.Fatal("the database must refuse a display name past the bound")
	}
	if !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Errorf("err = %v, want a CHECK constraint failure", err)
	}
}

func seedAccount(t *testing.T, db *DB, id, email, displayName string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO users (id, email, password_hash, display_name, role,
		                   plugins_json, disabled, created_at, updated_at)
		VALUES (?,?,'hash',?,'admin','["*"]',0,0,0)`,
		id, email, displayName); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

// seedAccountAtHead inserts a user against the schema this build ships --
// role_id and grants_json, from 0025 -- for a test that seeds after Migrate
// has already brought the database all the way there. seedAccount matches the
// schema as it stood before 0025 renamed these columns, for a test seeding an
// upgrade still in progress.
func seedAccountAtHead(t *testing.T, db *DB, id, email, displayName string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO users (id, email, password_hash, display_name, role_id,
		                   grants_json, disabled, created_at, updated_at)
		VALUES (?,?,'hash',?,'role_administrator','[{"plugin":"*","level":"write"}]',0,0,0)`,
		id, email, displayName); err != nil {
		t.Fatalf("seed account: %v", err)
	}
}

func seedSession(t *testing.T, db *DB, hash, id, userID string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO user_sessions (session_hash, id, user_id, csrf_token, created_at, expires_at)
		VALUES (?,?,?,'csrf',0,?)`,
		hash, id, userID, int64(1)<<40); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}
