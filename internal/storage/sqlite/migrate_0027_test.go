package sqlite

import (
	"context"
	"testing"
)

// Two deployments on the same version number must have the same schema, and
// 0027 is the shape of change where that can quietly stop being true: two
// columns added to a table in place, and a new table beside it.
func TestMigrate0027_UpgradingMatchesAFreshDatabase(t *testing.T) {
	ctx := context.Background()

	fresh := openDBAt(t, "fresh27.db")
	if _, err := Migrate(ctx, fresh); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	upgraded := openDBAt(t, "upgraded27.db")
	applyThrough(t, upgraded, 26)
	seedPre27Fixture(t, upgraded)
	if _, err := Migrate(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if got, want := schemaOf(t, upgraded), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

// An account that existed before invitations did is not invited, and the
// upgrade must say so rather than leaving a column a claim can match on. The
// default is what settles it, and a default that had gone the other way would
// make every existing account claimable by whoever holds its address at a
// provider.
func TestMigrate0027_ExistingAccountsAreNotInvited(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "carried27.db")
	applyThrough(t, db, 26)
	seedPre27Fixture(t, db)

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var provider string
	var expires any
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT invite_provider, invite_expires_at FROM users WHERE id = 'usr_before'`).
		Scan(&provider, &expires); err != nil {
		t.Fatalf("read the upgraded account: %v", err)
	}
	if provider != "" {
		t.Errorf("invite_provider = %q; an account that predates invitations is not invited", provider)
	}
	if expires != nil {
		t.Errorf("invite_expires_at = %v; want no expiry on an account with no invitation", expires)
	}
}

// The closed set is the one every other provider column carries. A name
// outside it would be a provider nobody configured, which is an invitation
// nobody can ever claim.
func TestMigrate0027_RefusesAProviderThisBuildDoesNotKnow(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "check27.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedPre27Fixture(t, db)

	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE users SET invite_provider = 'not-a-provider' WHERE id = 'usr_before'`); err == nil {
		t.Error("a provider outside the closed set was accepted")
	}
	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE users SET invite_provider = 'google' WHERE id = 'usr_before'`); err != nil {
		t.Errorf("a provider inside the closed set was refused: %v", err)
	}
}

// One account, written the way a database that predates this migration holds
// one. Enough for the assertions above and no more.
func seedPre27Fixture(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO users (id, email, password_hash, display_name, role_id,
		                   grants_json, disabled, status, created_at, updated_at)
		VALUES ('usr_before', 'before@example.com', 'a-bcrypt-hash', '',
		        'role_administrator', '[]', 0, 'active', 1, 1)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
}
