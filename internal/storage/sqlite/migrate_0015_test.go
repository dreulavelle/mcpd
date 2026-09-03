package sqlite

import (
	"context"
	"testing"
)

// 0015 creates four objects and several partial indexes, which is the shape of
// change that can leave two deployments on the same version number with
// different schemas. A database that upgraded into it has to be
// indistinguishable from one that started there.
func TestMigrate0015_UpgradingMatchesAFreshDatabase(t *testing.T) {
	ctx := context.Background()

	fresh := openDBAt(t, "fresh15.db")
	if _, err := Migrate(ctx, fresh); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	upgraded := openDBAt(t, "upgraded15.db")
	applyThrough(t, upgraded, 14)
	seedUser(t, upgraded, "usr_before_0015", "before@example.com")
	if _, err := Migrate(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if got, want := schemaOf(t, upgraded), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

// An upgrade grants nobody anything. The tables arrive empty, so every account
// that existed before this migration reaches exactly what its own grant lists
// and nothing more -- which is the direction a default has to be wrong in.
func TestMigrate0015_UpgradeGrantsNothing(t *testing.T) {
	ctx := context.Background()
	db := openDBAt(t, "quiet15.db")
	applyThrough(t, db, 14)
	seedUser(t, db, "usr_before_0015", "before@example.com")
	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	for _, table := range []string{"groups", "group_members", "api_keys"} {
		var n int
		if err := db.Reader().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s holds %d rows after an upgrade; it must arrive empty", table, n)
		}
	}
}

// A membership names an account or a key, never both and never neither. The
// CHECK is what makes that so against two writers at once, which no test in Go
// could stand in for.
func TestMigrate0015_AMembershipNamesExactlyOneSubject(t *testing.T) {
	ctx := context.Background()
	db := migrated(t, "members15.db")
	seedUserAtHead(t, db, "usr_1", "a@example.com")
	seedGroup(t, db, "grp_1", "Field")
	seedKey(t, db, "key_1", "connector")

	insert := func(user, key any) error {
		_, err := db.Writer().ExecContext(ctx, `
			INSERT INTO group_members (group_id, user_id, key_id, added_by, added_at)
			VALUES ('grp_1',?,?,'test',0)`, user, key)
		return err
	}
	if err := insert("usr_1", nil); err != nil {
		t.Fatalf("an account membership was refused: %v", err)
	}
	if err := insert(nil, "key_1"); err != nil {
		t.Fatalf("a key membership was refused: %v", err)
	}
	if err := insert("usr_1", "key_1"); err == nil {
		t.Error("a membership naming both an account and a key was accepted")
	}
	if err := insert(nil, nil); err == nil {
		t.Error("a membership naming nothing was accepted")
	}
	// One membership per subject per group, so a group's member count cannot
	// disagree with the people it names.
	if err := insert("usr_1", nil); err == nil {
		t.Error("one account was put in one group twice")
	}
}

// Two keys sharing a secret would be one credential with two names, which
// breaks revocation and makes the trail unable to say which one acted. It is
// the same rule config validation applies to two static tokens sharing a
// secret_ref.
func TestMigrate0015_ASecretBelongsToOneKey(t *testing.T) {
	ctx := context.Background()
	db := migrated(t, "secrets15.db")

	insert := func(id, hash string) error {
		_, err := db.Writer().ExecContext(ctx, `
			INSERT INTO api_keys (id, name, secret_hash, role_id, grants_json,
			                      created_by, created_at, updated_at)
			VALUES (?,?,?,'role_operator','[]','test',0,0)`, id, id, hash)
		return err
	}
	if err := insert("key_1", "digest-a"); err != nil {
		t.Fatalf("first key: %v", err)
	}
	if err := insert("key_2", "digest-a"); err == nil {
		t.Error("two keys shared a secret")
	}
	if err := insert("key_3", "digest-b"); err != nil {
		t.Errorf("a second key with its own secret was refused: %v", err)
	}
}

// Group names are unique case-insensitively: two groups called "Field" and
// "field" are one group as far as anybody reading the list is concerned.
func TestMigrate0015_GroupNamesAreUniqueRegardlessOfCase(t *testing.T) {
	ctx := context.Background()
	db := migrated(t, "names15.db")
	seedGroup(t, db, "grp_1", "Field")
	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO groups (id, name, description, role_id, grants_json, created_by, created_at, updated_at)
		VALUES ('grp_2','field','','','[]','test',0,0)`); err == nil {
		t.Error("two groups whose names differ only in case were accepted")
	}
}

func migrated(t *testing.T, name string) *DB {
	t.Helper()
	db := openDBAt(t, name)
	if _, err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedGroup(t *testing.T, db *DB, id, name string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO groups (id, name, description, role_id, grants_json, created_by, created_at, updated_at)
		VALUES (?,?,'','','[]','test',0,0)`, id, name); err != nil {
		t.Fatalf("seed group: %v", err)
	}
}

func seedKey(t *testing.T, db *DB, id, name string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO api_keys (id, name, secret_hash, role_id, grants_json,
		                      created_by, created_at, updated_at)
		VALUES (?,?,?,'role_operator','[]','test',0,0)`, id, name, "digest-"+id); err != nil {
		t.Fatalf("seed key: %v", err)
	}
}
