package sqlite

import (
	"context"
	"path/filepath"
	"strings"
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

// applyThrough runs migrations up to and including version, which is what a
// deployment that has not upgraded yet looks like.
func applyThrough(t *testing.T, db *DB, version int) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Writer().ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT    NOT NULL,
			checksum   TEXT    NOT NULL,
			applied_at INTEGER NOT NULL
		) STRICT`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version > version {
			return
		}
		if err := applyOne(ctx, db.Writer(), m); err != nil {
			t.Fatalf("apply %04d: %v", m.version, err)
		}
	}
}

// 0011 rebuilds the users table to add a constraint, and a rebuild is the one
// migration shape that can leave two deployments on the same version number
// with different schemas -- a forgotten index, a dropped default.
//
// The existing fresh-versus-upgraded test covers an empty database. This one
// upgrades a populated one, because a rebuild that works on an empty table and
// fails on a filled one is the interesting failure.
func TestMigrate0011_UpgradingAPopulatedDatabaseMatchesAFreshOne(t *testing.T) {
	ctx := context.Background()

	fresh := openDBAt(t, "fresh.db")
	if _, err := Migrate(ctx, fresh); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	upgraded := openDBAt(t, "upgraded.db")
	applyThrough(t, upgraded, 10)
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
	ctx := context.Background()
	db := openDBAt(t, "upgrade.db")
	applyThrough(t, db, 10)
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
	ctx := context.Background()
	db := openDBAt(t, "upgrade.db")
	applyThrough(t, db, 10)

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
	ctx := context.Background()
	db := openDBAt(t, "fresh.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	seedAccount(t, db, "usr_1", "alice@example.com", "Alice")

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

func seedSession(t *testing.T, db *DB, hash, id, userID string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO user_sessions (session_hash, id, user_id, csrf_token, created_at, expires_at)
		VALUES (?,?,?,'csrf',0,?)`,
		hash, id, userID, int64(1)<<40); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}
