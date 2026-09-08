package sqlite

import (
	"context"
	"testing"
)

// Two deployments on the same version number must have the same schema.
func TestMigrate0030_UpgradingMatchesAFreshDatabase(t *testing.T) {
	ctx := context.Background()

	fresh := openDBAt(t, "fresh30.db")
	if _, err := Migrate(ctx, fresh); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	upgraded := openDBAt(t, "upgraded30.db")
	applyThrough(t, upgraded, 29)
	if _, err := Migrate(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if got, want := schemaOf(t, upgraded), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

/*
The seed is the whole point of the migration.

A session that predates the column has never reported activity. Left at zero
its idle deadline is 1970, so the first request after the upgrade signs out
every person on the instance. Seeding from `created_at` gives them whatever
remains of the window instead. Nothing else defends this: the checksum stops
the file being edited later, not the behaviour being wrong the first time.
*/
func TestMigrate0030_SeedsLastSeenFromWhenTheSessionBegan(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "seed30.db")
	applyThrough(t, db, 29)

	const created = 1_757_000_000_000
	if err := db.WriteTx(ctx, created, func(tx *UnitOfWork) error {
		if err := tx.Exec(`
			INSERT INTO users (id, email, password_hash, role_id, disabled, created_at, updated_at)
			VALUES ('usr_1', 'someone@example.com', 'x', 'role_administrator', 0, ?, ?)`,
			created, created); err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO user_sessions (session_hash, id, user_id, csrf_token, created_at, expires_at)
			VALUES ('hash', 'ses_1', 'usr_1', 'csrf', ?, ?)`,
			created, created+3_600_000)
	}); err != nil {
		t.Fatalf("seed a pre-0030 session: %v", err)
	}

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var lastSeen int64
	if err := db.Reader().QueryRow(
		`SELECT last_seen_at FROM user_sessions WHERE id = 'ses_1'`).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	if lastSeen != created {
		t.Errorf("last_seen_at = %d, want it seeded from created_at %d -- a zero here "+
			"signs out everybody who was signed in at the upgrade", lastSeen, created)
	}
}

// The column has to be usable by a STRICT table's rules and never null.
func TestMigrate0030_LastSeenIsANonNullInteger(t *testing.T) {
	db := openDBAt(t, "column30.db")
	if _, err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var name, kind string
	var notNull int
	rows, err := db.Reader().Query(`SELECT name, type, "notnull" FROM pragma_table_info('user_sessions')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	found := false
	for rows.Next() {
		if err := rows.Scan(&name, &kind, &notNull); err != nil {
			t.Fatal(err)
		}
		if name == "last_seen_at" {
			found = true
			if kind != "INTEGER" || notNull != 1 {
				t.Errorf("last_seen_at is %s notnull=%d, want INTEGER notnull=1", kind, notNull)
			}
		}
	}
	if !found {
		t.Error("user_sessions has no last_seen_at column")
	}
}
