package sqlite

import (
	"context"
	"testing"
)

// 0014 adds a column by ALTER and creates three objects beside it, which is
// the shape of change that can leave two deployments on the same version
// number with different schemas -- a forgotten index, a default that only the
// fresh path applies. A database that upgraded into it has to be
// indistinguishable from one that started there.
func TestMigrate0014_UpgradingMatchesAFreshDatabase(t *testing.T) {
	ctx := context.Background()

	fresh := openDBAt(t, "fresh14.db")
	if _, err := Migrate(ctx, fresh); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	upgraded := openDBAt(t, "upgraded14.db")
	applyThrough(t, upgraded, 13)
	seedUser(t, upgraded, "usr_before_0014", "before@example.com")
	if _, err := Migrate(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if got, want := schemaOf(t, upgraded), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

// An account that existed before there was anything to decide about a
// registration is active. Reading it back as pending would take every
// capability away from everybody the moment a host upgraded, which is the
// worst possible way for a default to be wrong.
func TestMigrate0014_ExistingAccountsAreActive(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "carried14.db")
	applyThrough(t, db, 13)
	seedUser(t, db, "usr_before_0014", "before@example.com")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var status string
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT status FROM users WHERE id = ?`, "usr_before_0014").Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q; an account from before this column is active", status)
	}
}

// The column carries its own CHECK, so a status this build does not understand
// cannot reach the table at all.
func TestMigrate0014_StatusIsConstrained(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "constrained14.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedUserAtHead(t, db, "usr_1", "a@example.com")

	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE users SET status = 'whatever' WHERE id = ?`, "usr_1"); err == nil {
		t.Error("a status outside the set was accepted")
	}
}

// One provider identity belongs to one account, and one account holds one
// identity per provider. Both are constraints rather than checks in Go,
// because both have to hold against two writers at once.
func TestMigrate0014_IdentitiesAreUniqueBothWays(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "identities14.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedUserAtHead(t, db, "usr_1", "a@example.com")
	seedUserAtHead(t, db, "usr_2", "b@example.com")

	insert := func(provider, subject, user string) error {
		_, err := db.Writer().ExecContext(ctx, `
			INSERT INTO user_identities (provider, subject, user_id, email, linked_by, created_at)
			VALUES (?,?,?,'','test',0)`, provider, subject, user)
		return err
	}
	if err := insert("google", "sub-1", "usr_1"); err != nil {
		t.Fatalf("first link: %v", err)
	}
	if err := insert("google", "sub-1", "usr_2"); err == nil {
		t.Error("one provider identity was linked to two accounts")
	}
	if err := insert("google", "sub-2", "usr_1"); err == nil {
		t.Error("one account holds two identities at the same provider")
	}
	// A different provider on the same account is the ordinary case.
	if err := insert("github", "12345", "usr_1"); err != nil {
		t.Errorf("a second provider on one account was refused: %v", err)
	}
	if err := insert("okta", "sub-3", "usr_2"); err == nil {
		t.Error("a provider this build does not know reached the table")
	}
}

// seedUser writes an account directly, so a migration test does not depend on
// the account store -- which is a layer above this one and would drag its
// validation into a test about the schema. It matches the schema as it stood
// before 0025 renamed role/plugins_json to role_id/grants_json, for a test
// seeding an upgrade still in progress; seedUserAtHead is its counterpart for
// a test seeding after Migrate has already run everything.
func seedUser(t *testing.T, db *DB, id, email string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO users (id, email, password_hash, display_name, role,
		                   plugins_json, disabled, created_at, updated_at)
		VALUES (?,?,'$2a$12$fake','','admin','["*"]',0,0,0)`, id, email); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

// seedUserAtHead inserts a user against the schema this build ships.
func seedUserAtHead(t *testing.T, db *DB, id, email string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO users (id, email, password_hash, display_name, role_id,
		                   grants_json, disabled, created_at, updated_at)
		VALUES (?,?,'$2a$12$fake','','role_administrator','[{"plugin":"*","level":"write"}]',0,0,0)`,
		id, email); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}
