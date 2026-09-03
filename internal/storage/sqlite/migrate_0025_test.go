package sqlite

import (
	"context"
	"encoding/json"
	"testing"
)

// 0025 replaces two roles, four capabilities and a group ceiling with roles
// and grants that union rather than narrow. A rebuild this size -- three
// tables torn down and rebuilt, a fourth altered in place -- is exactly the
// shape of change that can leave two deployments on the same version number
// with different schemas.
func TestMigrate0025_UpgradingMatchesAFreshDatabase(t *testing.T) {
	ctx := context.Background()

	fresh := openDBAt(t, "fresh25.db")
	if _, err := Migrate(ctx, fresh); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	upgraded := openDBAt(t, "upgraded25.db")
	applyThrough(t, upgraded, 24)
	seedPre25Fixture(t, upgraded)
	if _, err := Migrate(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if got, want := schemaOf(t, upgraded), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

// The upgrade this migration exists for: an administrator's role becomes the
// built-in role that means the same thing, an ordinary user's plugin list
// becomes write grants (an ordinary user could always propose), the three
// built-in roles are there to assign, memberships and sessions survive the
// rebuild, and each place the old and new models disagree -- a group's
// ceiling, a key whose own grant sat inside a group that grants more -- is
// left as a note an operator reads at startup rather than silently dropped or
// silently widened.
func TestMigrate0025_CarriesSubjectsAndNotesWhatChanged(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "carried25.db")
	applyThrough(t, db, 24)
	seedPre25Fixture(t, db)

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	var roleID string
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT role_id FROM users WHERE id = 'usr_admin'`).Scan(&roleID); err != nil {
		t.Fatalf("read user role: %v", err)
	}
	if roleID != "role_administrator" {
		t.Errorf("user role_id = %q, want role_administrator", roleID)
	}

	var grantsJSON string
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT grants_json FROM api_keys WHERE id = 'key_1'`).Scan(&grantsJSON); err != nil {
		t.Fatalf("read key grants: %v", err)
	}
	var grants []map[string]string
	if err := json.Unmarshal([]byte(grantsJSON), &grants); err != nil {
		t.Fatalf("decode grants %q: %v", grantsJSON, err)
	}
	if len(grants) != 1 || grants[0]["plugin"] != "bandwidth" || grants[0]["level"] != "write" {
		t.Errorf(`key grants = %s, want [{"plugin":"bandwidth","level":"write"}]`, grantsJSON)
	}

	var builtinRoles int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM roles WHERE builtin = 1`).Scan(&builtinRoles); err != nil {
		t.Fatalf("count built-in roles: %v", err)
	}
	if builtinRoles != 3 {
		t.Errorf("built-in roles = %d, want 3", builtinRoles)
	}

	var ceilingDropped int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM access_notes WHERE kind = 'ceiling_dropped'`).Scan(&ceilingDropped); err != nil {
		t.Fatalf("count ceiling_dropped notes: %v", err)
	}
	if ceilingDropped == 0 {
		t.Error("the group's dropped capabilities ceiling left no note")
	}

	var reachWidens int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM access_notes WHERE kind = 'reach_widens'`).Scan(&reachWidens); err != nil {
		t.Fatalf("count reach_widens notes: %v", err)
	}
	if reachWidens == 0 {
		t.Error("the key whose own grant now unions with its group's wider one left no note")
	}

	var members int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM group_members WHERE group_id = 'grp_1' AND key_id = 'key_1'`).Scan(&members); err != nil {
		t.Fatalf("count group members: %v", err)
	}
	if members != 1 {
		t.Errorf("group membership = %d, want the key still in its group", members)
	}

	var sessions int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM user_sessions WHERE id = 'ses_1'`).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 1 {
		t.Error("the upgrade signed everybody out; sessions must survive the rebuild")
	}
}

// seedPre25Fixture builds the shape of database 0025 upgrades: an
// administrator with a session, an ordinary user's key granted one plugin of
// its own, and a group that both imposes a capabilities ceiling and grants
// more than the key's own list -- so the migration has something to carry
// across in the ordinary case and something to write a note about in both
// directions the old and new models disagree.
func seedPre25Fixture(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()

	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, display_name, role,
		                   plugins_json, disabled, created_at, updated_at)
		VALUES ('usr_admin','admin@example.com','$2a$12$fake','Admin','admin','["*"]',0,0,0)`,
	); err != nil {
		t.Fatalf("seed admin user: %v", err)
	}
	seedSession(t, db, "hash-1", "ses_1", "usr_admin")

	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO api_keys (id, name, secret_hash, role, plugins_json,
		                      created_by, created_at, updated_at)
		VALUES ('key_1','Connector','digest-1','user','["bandwidth"]','test',0,0)`,
	); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	// A ceiling ("read","approve") and a grant ("*") wider than the key's own
	// -- one row exercising both notes the migration writes.
	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO groups (id, name, description, plugins_json, capabilities_json,
		                    created_by, created_at, updated_at)
		VALUES ('grp_1','Field','','["*"]','["read","approve"]','test',0,0)`,
	); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO group_members (group_id, user_id, key_id, added_by, added_at)
		VALUES ('grp_1', NULL, 'key_1', 'test', 0)`,
	); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}
