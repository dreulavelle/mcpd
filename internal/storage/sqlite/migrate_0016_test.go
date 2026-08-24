package sqlite

import (
	"context"
	"strings"
	"testing"
)

// A rebuild is the shape of migration that silently loses things: an index
// that is not recreated, a constraint dropped rather than widened, a foreign
// key that comes back without its cascade. Comparing the whole schema against
// a database that started here catches all three at once.
func TestMigrate0016_UpgradingMatchesAFreshDatabase(t *testing.T) {
	ctx := context.Background()

	fresh := openDBAt(t, "fresh16.db")
	if _, err := Migrate(ctx, fresh); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	upgraded := openDBAt(t, "upgraded16.db")
	applyThrough(t, upgraded, 15)
	if _, err := Migrate(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if got, want := schemaOf(t, upgraded), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

// Both tables are rebuilt, so both have to arrive with what was in them. A
// lost identity is an account that can no longer sign in the way it used to,
// and it would surface as "that address already belongs to somebody" on the
// next attempt.
func TestMigrate0016_CarriesIdentitiesAndStatesAcross(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "carried16.db")
	applyThrough(t, db, 15)
	seedUser(t, db, "usr_linked", "linked@example.com")

	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO user_identities (provider, subject, user_id, email, linked_by, created_at)
		VALUES ('google', 'sub-123', 'usr_linked', 'linked@example.com', 'user:linked@example.com', 1)`,
	); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO sso_states
			(state_hash, provider, purpose, binding_hash, redirect_uri, created_at, expires_at)
		VALUES ('hash-1', 'github', 'signin', 'binding-1', 'https://mcpd.example/cb', 1, 2)`,
	); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var subject, linkedBy string
	if err := db.Writer().QueryRowContext(ctx,
		`SELECT subject, linked_by FROM user_identities WHERE provider = 'google'`,
	).Scan(&subject, &linkedBy); err != nil {
		t.Fatalf("the identity did not survive the rebuild: %v", err)
	}
	if subject != "sub-123" || linkedBy != "user:linked@example.com" {
		t.Errorf("identity came back as %q/%q", subject, linkedBy)
	}

	// A state is one person part-way through signing in. Dropping the table
	// would fail their callback with a message about tampering.
	var redirect string
	if err := db.Writer().QueryRowContext(ctx,
		`SELECT redirect_uri FROM sso_states WHERE state_hash = 'hash-1'`,
	).Scan(&redirect); err != nil {
		t.Fatalf("the state did not survive the rebuild: %v", err)
	}
	if redirect != "https://mcpd.example/cb" {
		t.Errorf("redirect_uri came back as %q", redirect)
	}
}

// The point of the migration. Before it, every write for the operator's own
// provider failed on a CHECK -- which is how the feature shipped unable to
// store anything at all.
func TestMigrate0016_TheOperatorsOwnProviderCanBeStored(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "ownprovider16.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedUser(t, db, "usr_own", "own@example.com")

	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO user_identities (provider, subject, user_id, email, linked_by, created_at)
		VALUES ('oidc', 'sub-own', 'usr_own', 'own@example.com', 'user:own@example.com', 1)`,
	); err != nil {
		t.Fatalf("an identity at the operator's own provider was refused: %v", err)
	}
	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO sso_states
			(state_hash, provider, purpose, binding_hash, redirect_uri, created_at, expires_at)
		VALUES ('hash-own', 'oidc', 'signin', 'binding-own', 'https://mcpd.example/cb', 1, 2)`,
	); err != nil {
		t.Fatalf("a sign-in at the operator's own provider was refused: %v", err)
	}
}

// Widened, not removed. The constraint's job is to hold when something
// bypasses the Go enum, so a provider nobody configured still has to bounce.
func TestMigrate0016_AProviderNobodyConfiguredIsStillRefused(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "unknown16.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedUser(t, db, "usr_unknown", "unknown@example.com")

	_, err := db.Writer().ExecContext(ctx, `
		INSERT INTO user_identities (provider, subject, user_id, email, linked_by, created_at)
		VALUES ('okta', 'sub-x', 'usr_unknown', 'unknown@example.com', 'user:x', 1)`)
	if err == nil {
		t.Fatal("a provider this build does not know was stored")
	}
	if !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("refused, but not by the constraint: %v", err)
	}
}

// The rebuild has to bring the cascade back with it. Without it, deleting an
// account leaves its provider links behind -- and the next person to sign in
// at that subject would be handed the deleted account's row.
func TestMigrate0016_DeletingAnAccountStillTakesItsIdentities(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "cascade16.db")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	seedUser(t, db, "usr_gone", "gone@example.com")

	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO user_identities (provider, subject, user_id, email, linked_by, created_at)
		VALUES ('oidc', 'sub-gone', 'usr_gone', 'gone@example.com', 'user:gone@example.com', 1)`,
	); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	if _, err := db.Writer().ExecContext(ctx, `DELETE FROM users WHERE id = 'usr_gone'`); err != nil {
		t.Fatalf("delete account: %v", err)
	}

	var left int
	if err := db.Writer().QueryRowContext(ctx,
		`SELECT count(*) FROM user_identities WHERE user_id = 'usr_gone'`).Scan(&left); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if left != 0 {
		t.Errorf("%d identities outlived the account they belonged to", left)
	}
}
