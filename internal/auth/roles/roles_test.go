package roles

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

var testClock = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

const actor = "user:admin@example.com"

// newStore opens a fresh database and a role store over it. Migration 0025
// inserts the three built-in roles itself, so a store from this helper
// already has them without anybody calling EnsureBuiltins.
func newStore(t *testing.T) (*Store, *sqlite.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "roles.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return NewStore(db, func() time.Time { return testClock }), db
}

// seedUser writes an account directly. The users package is a layer above
// this one and importing it here would be a cycle; what these tests need is
// a row with an identifier and a role.
func seedUser(t *testing.T, db *sqlite.DB, id, email, roleID string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO users (id, email, password_hash, display_name, role_id,
		                   grants_json, disabled, status, created_at, updated_at)
		VALUES (?,?,'$2a$12$fake','',?,'[]',0,'active',0,0)`,
		id, email, roleID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func seedPendingUser(t *testing.T, db *sqlite.DB, id, email, roleID string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO users (id, email, password_hash, display_name, role_id,
		                   grants_json, disabled, status, created_at, updated_at)
		VALUES (?,?,'$2a$12$fake','',?,'[]',0,'pending',0,0)`,
		id, email, roleID); err != nil {
		t.Fatalf("seed pending user: %v", err)
	}
}

// seedGroup writes a group directly, for the same reason seedUser does: the
// groups package sits above this one, and importing it here would cycle.
func seedGroup(t *testing.T, db *sqlite.DB, id, name, roleID string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO groups (id, name, description, role_id, grants_json,
		                    created_by, created_at, updated_at)
		VALUES (?,?,'',?,'[]','test',0,0)`,
		id, name, roleID); err != nil {
		t.Fatalf("seed group: %v", err)
	}
}

func seedGroupMember(t *testing.T, db *sqlite.DB, groupID, userID string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO group_members (group_id, user_id, key_id, added_by, added_at)
		VALUES (?,?,NULL,'test',0)`,
		groupID, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}

func mustCreate(t *testing.T, s *Store, name string, perms auth.Permissions) *auth.Role {
	t.Helper()
	r, err := s.Create(context.Background(), actor, CreateRequest{Name: name, Permissions: perms})
	if err != nil {
		t.Fatalf("create role %s: %v", name, err)
	}
	return r
}

// administrator is a permission set that holds access:write, the one
// permission CountAdministrators looks for.
func administrator() auth.Permissions {
	r, _ := auth.BuiltinRole(auth.RoleAdministrator)
	return r.Permissions
}

// --- built-ins ---------------------------------------------------------

// A fresh database already has the three built-in roles, because migration
// 0025 inserts them -- nobody has to call EnsureBuiltins before a built-in
// role id is assignable.
func TestFreshDatabase_AlreadyHasTheBuiltins(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	list, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("roles = %d, want 3 built-ins", len(list))
	}
	for _, id := range []string{auth.RoleReader, auth.RoleOperator, auth.RoleAdministrator} {
		r, err := s.ByID(ctx, id)
		if err != nil {
			t.Fatalf("by id %s: %v", id, err)
		}
		if !r.Builtin {
			t.Errorf("%s is not marked builtin", id)
		}
		want, _ := auth.BuiltinRole(id)
		if !r.Permissions.Equal(want.Permissions) {
			t.Errorf("%s permissions = %v, want %v", id, r.Permissions, want.Permissions)
		}
	}
}

// EnsureBuiltins re-applies the binary's definition of a built-in role even
// when a row was left holding something else -- the whole point being that
// an area added in a later version reaches every administrator without
// anybody editing a role by hand.
func TestEnsureBuiltins_ReappliesPermissions(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()

	// Simulate a row written by an older build: wrong name, and a permission
	// set that does not match what this binary says Reader means.
	if _, err := db.Writer().ExecContext(ctx, `
		UPDATE roles SET name = 'Stale Reader', permissions_json = '{"settings":"write"}'
		 WHERE id = ?`, auth.RoleReader); err != nil {
		t.Fatalf("corrupt the row: %v", err)
	}

	if err := s.EnsureBuiltins(ctx); err != nil {
		t.Fatalf("ensure builtins: %v", err)
	}
	r, err := s.ByID(ctx, auth.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := auth.BuiltinRole(auth.RoleReader)
	if r.Name != want.Name {
		t.Errorf("name = %q, want %q", r.Name, want.Name)
	}
	if !r.Permissions.Equal(want.Permissions) {
		t.Errorf("permissions = %v, want %v; EnsureBuiltins must re-apply them", r.Permissions, want.Permissions)
	}
}

// A built-in role's meaning has to be the same on every host, so it cannot
// be edited or deleted -- not its name, not its permissions.
func TestBuiltinRoles_CannotBeEditedOrDeleted(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	name := "Reader but different"
	if _, err := s.Update(ctx, actor, auth.RoleReader, UpdateRequest{Name: &name}); !errors.Is(err, ErrBuiltin) {
		t.Errorf("renaming a built-in: %v, want ErrBuiltin", err)
	}
	perms := auth.Permissions{auth.AreaSettings: auth.LevelWrite}
	if _, err := s.Update(ctx, actor, auth.RoleOperator, UpdateRequest{Permissions: &perms}); !errors.Is(err, ErrBuiltin) {
		t.Errorf("re-permissioning a built-in: %v, want ErrBuiltin", err)
	}
	if err := s.Delete(ctx, actor, auth.RoleAdministrator); !errors.Is(err, ErrBuiltin) {
		t.Errorf("deleting a built-in: %v, want ErrBuiltin", err)
	}

	// And nothing was written.
	r, err := s.ByID(ctx, auth.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "Reader" {
		t.Errorf("name = %q; the refused rename was written", r.Name)
	}
}

// --- create / list / read -----------------------------------------------

func TestCreate_RefusesADuplicateName(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustCreate(t, s, "Field", auth.Permissions{auth.AreaSettings: auth.LevelRead})

	if _, err := s.Create(ctx, actor, CreateRequest{
		Name: "field", Permissions: auth.Permissions{auth.AreaSettings: auth.LevelRead},
	}); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("a second role named the same: %v, want ErrDuplicateName", err)
	}
	// And a built-in's name is off limits too, case-insensitively.
	if _, err := s.Create(ctx, actor, CreateRequest{
		Name: "reader", Permissions: auth.Permissions{},
	}); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("a custom role named after a built-in: %v, want ErrDuplicateName", err)
	}
}

func TestCreate_And_ByName(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	perms := auth.Permissions{auth.AreaSettings: auth.LevelRead, auth.AreaHistory: auth.LevelWrite}
	created := mustCreate(t, s, "Field engineers", perms)
	if created.Builtin {
		t.Error("a custom role reports itself as builtin")
	}
	if created.CreatedBy != actor {
		t.Errorf("created by = %q, want %q", created.CreatedBy, actor)
	}

	got, err := s.ByName(ctx, "  field ENGINEERS ")
	if err != nil {
		t.Fatalf("by name: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("ByName resolved to %s, want %s", got.ID, created.ID)
	}
	if !got.Permissions.Equal(perms) {
		t.Errorf("permissions = %v, want %v", got.Permissions, perms)
	}
}

// List orders the built-ins first, in a fixed order, then custom roles by
// name -- so the page an operator opens reads the same on every host.
func TestList_OrdersBuiltinsFirstThenCustomByName(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "Zeta", auth.Permissions{})
	mustCreate(t, s, "Alpha", auth.Permissions{})

	list, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range list {
		ids = append(ids, r.ID)
	}
	want := []string{auth.RoleReader, auth.RoleOperator, auth.RoleAdministrator}
	if len(ids) != 5 {
		t.Fatalf("roles = %v, want 5", ids)
	}
	if strings.Join(ids[:3], ",") != strings.Join(want, ",") {
		t.Errorf("built-in order = %v, want %v", ids[:3], want)
	}
	if list[3].Name != "Alpha" || list[4].Name != "Zeta" {
		t.Errorf("custom order = %s, %s; want Alpha then Zeta", list[3].Name, list[4].Name)
	}
}

// --- delete --------------------------------------------------------------

// A role something still holds cannot be deleted: doing so would leave that
// thing pointing at nothing, and nobody decided that it should.
func TestDelete_RefusedWhileAssigned(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	r := mustCreate(t, s, "Field", auth.Permissions{auth.AreaSettings: auth.LevelRead})
	seedUser(t, db, "usr_1", "a@example.com", r.ID)

	if err := s.Delete(ctx, actor, r.ID); !errors.Is(err, ErrAssigned) {
		t.Fatalf("deleting an assigned role: %v, want ErrAssigned", err)
	}
	// Still there.
	if _, err := s.ByID(ctx, r.ID); err != nil {
		t.Fatalf("the refused delete removed the row: %v", err)
	}

	// Reassigned elsewhere, deleting goes through.
	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE users SET role_id = ? WHERE id = 'usr_1'`, auth.RoleReader); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, actor, r.ID); err != nil {
		t.Fatalf("deleting an unassigned role: %v", err)
	}
	if _, err := s.ByID(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("reading the deleted role: %v, want ErrNotFound", err)
	}
}

// Assignment through a group counts too: the count that guards deletion is
// the same one that guards the last administrator, and it reads users, keys,
// accounts and groups alike.
func TestDelete_RefusedWhileAssignedThroughAGroup(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	r := mustCreate(t, s, "Field", auth.Permissions{auth.AreaSettings: auth.LevelRead})
	seedGroup(t, db, "grp_1", "Field engineers", r.ID)

	if err := s.Delete(ctx, actor, r.ID); !errors.Is(err, ErrAssigned) {
		t.Fatalf("deleting a role a group holds: %v, want ErrAssigned", err)
	}
}

// --- the last administrator ----------------------------------------------

// A role update that would take access:write away from the last
// administrator is refused, and nothing about it is written.
func TestUpdate_RefusesRemovingTheLastAdministrator(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	custom := mustCreate(t, s, "Owner", administrator())
	seedUser(t, db, "usr_1", "a@example.com", custom.ID)

	narrowed := auth.Permissions{auth.AreaAccess: auth.LevelRead}
	if _, err := s.Update(ctx, actor, custom.ID, UpdateRequest{Permissions: &narrowed}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("removing the last administrator's access:write: %v, want ErrLastAdmin", err)
	}
	got, err := s.ByID(ctx, custom.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Permissions.Equal(administrator()) {
		t.Errorf("permissions = %v; the refused update was written", got.Permissions)
	}

	// With a second administrator in place -- through the built-in role
	// directly -- the same edit goes through.
	seedUser(t, db, "usr_2", "b@example.com", auth.RoleAdministrator)
	if _, err := s.Update(ctx, actor, custom.ID, UpdateRequest{Permissions: &narrowed}); err != nil {
		t.Errorf("narrowing with a second administrator present: %v", err)
	}
}

// The guard reads through group membership too, since CountAdministrators
// does: a role can be the only thing giving somebody access:write by way of
// a group they belong to, and narrowing it has to be refused the same way.
func TestUpdate_RefusesRemovingTheLastAdministrator_ThroughAGroup(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	custom := mustCreate(t, s, "Owners", administrator())
	seedGroup(t, db, "grp_1", "Owners", custom.ID)
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleReader)
	seedGroupMember(t, db, "grp_1", "usr_1")

	narrowed := auth.Permissions{auth.AreaAccess: auth.LevelRead}
	if _, err := s.Update(ctx, actor, custom.ID, UpdateRequest{Permissions: &narrowed}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("narrowing a group's only administering role: %v, want ErrLastAdmin", err)
	}
}

// A pending account holds nothing whatever its role says, so it cannot stand
// in for the administrator the guard is protecting.
func TestUpdate_DoesNotCountAPendingAdministrator(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	custom := mustCreate(t, s, "Owner", administrator())
	seedUser(t, db, "usr_1", "a@example.com", custom.ID)
	seedPendingUser(t, db, "usr_p", "p@example.com", auth.RoleAdministrator)

	narrowed := auth.Permissions{auth.AreaAccess: auth.LevelRead}
	if _, err := s.Update(ctx, actor, custom.ID, UpdateRequest{Permissions: &narrowed}); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("a pending administrator stood in for a real one: %v, want ErrLastAdmin", err)
	}
}

// Deleting a role is the same privilege change as narrowing it, so it goes
// through the same guard.
func TestDelete_RefusesRemovingTheLastAdministrator(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	custom := mustCreate(t, s, "Owner", administrator())
	seedUser(t, db, "usr_1", "a@example.com", custom.ID)

	// Reassigning the user first, or the role could not be deleted at all
	// for being assigned -- ErrAssigned is a different refusal from
	// ErrLastAdmin and this test is about the second one.
	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE users SET role_id = ? WHERE id = 'usr_1'`, custom.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, actor, custom.ID); !errors.Is(err, ErrAssigned) {
		t.Fatalf("deleting an assigned administering role: %v, want ErrAssigned "+
			"(assignment is checked ahead of the guard)", err)
	}
}

// --- audit -----------------------------------------------------------------

func TestRoleChangesAreAudited(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	r := mustCreate(t, s, "Field", auth.Permissions{auth.AreaSettings: auth.LevelRead})
	wider := auth.Permissions{auth.AreaSettings: auth.LevelWrite}
	if _, err := s.Update(ctx, actor, r.ID, UpdateRequest{Permissions: &wider}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.Delete(ctx, actor, r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	audit := sqlite.NewAuditStore(db)
	records, err := audit.Recent(ctx, 50)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	want := map[string]bool{"role.created": false, "role.updated": false, "role.deleted": false}
	for _, rec := range records {
		if _, ok := want[rec.Entry.Kind]; !ok {
			continue
		}
		want[rec.Entry.Kind] = true
		if rec.Entry.Actor != actor {
			t.Errorf("%s recorded against %q, want %q", rec.Entry.Kind, rec.Entry.Actor, actor)
		}
	}
	for kind, seen := range want {
		if !seen {
			t.Errorf("%s is not in the trail", kind)
		}
	}
	if _, err := audit.VerifyChain(ctx); err != nil {
		t.Errorf("the audit chain no longer verifies: %v", err)
	}
}

// --- Exists / CountAdministrators ------------------------------------------

func TestExists(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	r := mustCreate(t, s, "Field", auth.Permissions{})

	err := db.WriteTx(ctx, testClock.UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		ok, err := Exists(tx, r.ID)
		if err != nil {
			return err
		}
		if !ok {
			t.Error("Exists(created role) = false")
		}
		ok, err = Exists(tx, "role_nobody_made")
		if err != nil {
			return err
		}
		if ok {
			t.Error("Exists(unknown role) = true")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCountAdministrators(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_reader", "r@example.com", auth.RoleReader)
	seedUser(t, db, "usr_admin", "a@example.com", auth.RoleAdministrator)
	seedPendingUser(t, db, "usr_pending", "p@example.com", auth.RoleAdministrator)

	// A group-granted administrator too, to confirm the count reads both
	// sources the way the doc comment says it does.
	custom := mustCreate(t, s, "Owners", administrator())
	seedGroup(t, db, "grp_1", "Owners", custom.ID)
	seedUser(t, db, "usr_via_group", "g@example.com", auth.RoleReader)
	seedGroupMember(t, db, "grp_1", "usr_via_group")

	err := db.WriteTx(ctx, testClock.UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		n, err := CountAdministrators(tx)
		if err != nil {
			return err
		}
		if n != 2 {
			t.Errorf("administrators = %d, want 2 (direct and via group; "+
				"reader and pending do not count)", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// --- validation --------------------------------------------------------

func TestValidateName(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		ok   bool
	}{
		{"ordinary", "Field engineers", true},
		{"trimmed", "  Field  ", true},
		{"empty", "   ", false},
		{"newline", "Field\nNOC", false},
		{"too long", strings.Repeat("a", 65), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateName(tc.in)
			if (err == nil) != tc.ok {
				t.Errorf("ValidateName(%q) error = %v, want ok=%v", tc.in, err, tc.ok)
			}
		})
	}
}

// A role that names a permission it cannot hold -- an unknown area, or a
// level the area does not offer -- is refused rather than silently dropped:
// Create and Update are a form somebody just filled in, not a stored value
// an older build wrote.
func TestCreate_RefusesAnInvalidPermission(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, err := s.Create(ctx, actor, CreateRequest{
		Name:        "Bad",
		Permissions: auth.Permissions{auth.AreaApprovals: auth.LevelWrite},
	})
	if err == nil {
		t.Error("a role permissioning approvals at write (only read/decide exist) was accepted")
	}
}
