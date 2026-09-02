package groups

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// A role grants capabilities; a group can only take them away.
//
// The two are deliberately asymmetric. Two mechanisms that both give rights
// make "why can this person approve" answerable only by reading both and
// knowing which wins; one that gives and one that takes away is answerable in
// one direction, and the answer is always the smaller of the two.
func TestCeiling_NarrowsARoleAndNeverWidensIt(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", `["*"]`)

	// No group: no ceiling, and an admin keeps everything the role allows.
	if got, err := s.CeilingFor(ctx, User("usr_1")); err != nil || got != nil {
		t.Fatalf("ceiling = %v, %v; a subject in no group has none", got, err)
	}
	admin := &auth.Principal{ID: "user:a", Role: auth.RoleAdmin, Plugins: []string{"*"}}
	if !admin.Can(auth.CapAdmin) || !admin.Can(auth.CapApprove) {
		t.Fatal("an admin with no ceiling holds every capability its role allows")
	}

	// A group that permits only reading takes the rest away.
	readOnly, err := s.Create(ctx, "user:admin@example.com", CreateRequest{
		Name: "Read Only", Plugins: []string{"*"},
		Capabilities: []auth.Capability{auth.CapRead},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := s.AddMember(ctx, "user:admin@example.com", readOnly.ID, User("usr_1")); err != nil {
		t.Fatalf("add: %v", err)
	}
	ceiling, err := s.CeilingFor(ctx, User("usr_1"))
	if err != nil {
		t.Fatalf("ceiling: %v", err)
	}
	restricted := &auth.Principal{
		ID: "user:a", Role: auth.RoleAdmin, Plugins: []string{"*"}, Ceiling: ceiling,
	}
	if !restricted.Can(auth.CapRead) {
		t.Error("the ceiling permits read, so read survives")
	}
	for _, c := range []auth.Capability{auth.CapPropose, auth.CapApprove, auth.CapAdmin} {
		if restricted.Can(c) {
			t.Errorf("an admin in a read-only group still holds %q", c)
		}
	}

	// And a ceiling cannot hand out what the role never had. A user role has
	// no admin capability, and a group naming it changes nothing.
	permissive, err := s.Create(ctx, "user:admin@example.com", CreateRequest{
		Name: "Everything", Plugins: []string{"*"},
		Capabilities: []auth.Capability{auth.CapRead, auth.CapPropose, auth.CapApprove, auth.CapAdmin},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	seedUser(t, db, "usr_2", "b@example.com", `["*"]`)
	if err := s.AddMember(ctx, "user:admin@example.com", permissive.ID, User("usr_2")); err != nil {
		t.Fatalf("add: %v", err)
	}
	ceiling, err = s.CeilingFor(ctx, User("usr_2"))
	if err != nil {
		t.Fatalf("ceiling: %v", err)
	}
	ordinary := &auth.Principal{
		ID: "user:b", Role: auth.RoleUser, Plugins: []string{"*"}, Ceiling: ceiling,
	}
	if ordinary.Can(auth.CapAdmin) {
		t.Error("a group named admin and the role does not grant it; the role wins")
	}
	if !ordinary.Can(auth.CapApprove) {
		t.Error("what the role does grant and the ceiling permits should survive")
	}
}

// Two groups union their ceilings. Being organised into a second, more
// permissive group must not take away what the first allowed -- a group is how
// people are grouped, and grouping somebody twice should not punish them.
//
// Groups declaring no ceiling are ignored rather than treated as permitting
// everything. Otherwise ordinary membership of a general group would undo every
// restriction, which is the exact shape of the grant bug this codebase already
// had once.
func TestCeiling_UnionsDeclaredCeilingsAndIgnoresUndeclaredOnes(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", `["*"]`)
	const actor = "user:admin@example.com"

	readers, err := s.Create(ctx, actor, CreateRequest{
		Name: "Readers", Plugins: []string{"*"},
		Capabilities: []auth.Capability{auth.CapRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	approvers, err := s.Create(ctx, actor, CreateRequest{
		Name: "Approvers", Plugins: []string{"*"},
		Capabilities: []auth.Capability{auth.CapApprove},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Declares none, and must not lift the restriction the others impose.
	general, err := s.Create(ctx, actor, CreateRequest{Name: "Everyone", Plugins: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range []*Group{readers, approvers, general} {
		if err := s.AddMember(ctx, actor, g.ID, User("usr_1")); err != nil {
			t.Fatalf("add to %s: %v", g.Name, err)
		}
	}

	got, err := s.CeilingFor(ctx, User("usr_1"))
	if err != nil {
		t.Fatal(err)
	}
	want := []auth.Capability{auth.CapApprove, auth.CapRead}
	if !slices.Equal(got, want) {
		t.Fatalf("ceiling = %v, want %v", got, want)
	}
	p := &auth.Principal{ID: "user:a", Role: auth.RoleAdmin, Plugins: []string{"*"}, Ceiling: got}
	if p.Can(auth.CapAdmin) || p.Can(auth.CapPropose) {
		t.Error("a group declaring no ceiling must not undo the ones that do")
	}
}

// "No ceiling" and "permits nothing" are different, and storage must not
// collapse them. NULL is the first; an empty array is the second, and it is a
// real setting -- a way to suspend a group's members without deleting them.
func TestCeiling_TellsNoCeilingFromPermittingNothing(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	const actor = "user:admin@example.com"
	seedUser(t, db, "usr_1", "a@example.com", `["*"]`)
	seedUser(t, db, "usr_2", "b@example.com", `["*"]`)

	unrestricted, err := s.Create(ctx, actor, CreateRequest{Name: "Open", Plugins: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	suspended, err := s.Create(ctx, actor, CreateRequest{
		Name: "Suspended", Plugins: []string{"*"},
		Capabilities: []auth.Capability{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, actor, unrestricted.ID, User("usr_1")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(ctx, actor, suspended.ID, User("usr_2")); err != nil {
		t.Fatal(err)
	}

	open, err := s.CeilingFor(ctx, User("usr_1"))
	if err != nil {
		t.Fatal(err)
	}
	if open != nil {
		t.Errorf("a group declaring no ceiling imposes none, got %v", open)
	}
	shut, err := s.CeilingFor(ctx, User("usr_2"))
	if err != nil {
		t.Fatal(err)
	}
	if shut == nil {
		t.Fatal("a group permitting nothing must not read as imposing no ceiling")
	}
	p := &auth.Principal{ID: "user:b", Role: auth.RoleAdmin, Plugins: []string{"*"}, Ceiling: shut}
	for _, c := range []auth.Capability{auth.CapRead, auth.CapPropose, auth.CapApprove, auth.CapAdmin} {
		if p.Can(c) {
			t.Errorf("a suspended member still holds %q", c)
		}
	}

	// And the round trip through storage keeps them apart.
	back, err := s.ByID(ctx, suspended.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Capabilities == nil {
		t.Error("an empty ceiling came back as no ceiling after a round trip")
	}
	if back, err = s.ByID(ctx, unrestricted.ID); err != nil {
		t.Fatal(err)
	} else if back.Capabilities != nil {
		t.Errorf("a group with no ceiling came back with %v", back.Capabilities)
	}
}

// seedAdmin writes an enabled, active administrator directly, for the same
// reason seedUser does.
func seedAdmin(t *testing.T, db *sqlite.DB, id, email string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO users (id, email, password_hash, display_name, role,
		                   plugins_json, disabled, created_at, updated_at)
		VALUES (?,?,'$2a$12$fake','','admin','["*"]',0,0,0)`, id, email); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
}

// A ceiling that leaves nobody holding admin is refused, exactly as demoting
// the last administrator is. There is no way back from the alternative: the
// page that edits the group needs the capability the change just removed.
func TestCeiling_CannotStrandTheHostWithoutAnAdministrator(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	const actor = "user:admin@example.com"
	seedAdmin(t, db, "usr_a", "a@example.com")

	everyone := mustGroup(t, s, "Everyone", "*")
	if err := s.AddMember(ctx, actor, everyone.ID, User("usr_a")); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Restricting the only administrator's group to reading is refused, and
	// the group is left as it was.
	readOnly := []auth.Capability{auth.CapRead}
	_, err := s.Update(ctx, actor, everyone.ID, UpdateRequest{Capabilities: &readOnly})
	if !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("update = %v, want ErrLastAdmin", err)
	}
	if g, _ := s.ByID(ctx, everyone.ID); g.Capabilities != nil {
		t.Fatalf("the refused ceiling was written: %v", g.Capabilities)
	}

	// A ceiling that keeps admin is fine, whoever is in the group.
	keepsAdmin := []auth.Capability{auth.CapRead, auth.CapAdmin}
	if _, err := s.Update(ctx, actor, everyone.ID, UpdateRequest{Capabilities: &keepsAdmin}); err != nil {
		t.Fatalf("a ceiling permitting admin was refused: %v", err)
	}

	// Ceilings union, so while Everyone permits admin no other group can
	// take it away. With that ceiling removed, the only administrator
	// cannot be put into a group that would.
	var none []auth.Capability
	if _, err := s.Update(ctx, actor, everyone.ID, UpdateRequest{Capabilities: &none}); err != nil {
		t.Fatalf("remove ceiling: %v", err)
	}
	suspended, err := s.Create(ctx, actor, CreateRequest{
		Name: "Suspended", Plugins: []string{"*"}, Capabilities: []auth.Capability{},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.AddMember(ctx, actor, suspended.ID, User("usr_a")); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("add = %v, want ErrLastAdmin", err)
	}
	if members, _ := s.Members(ctx, suspended.ID); len(members) != 0 {
		t.Fatalf("the refused membership was written: %v", members)
	}

	// With a second administrator outside every restricted group, both
	// changes go through: somebody can still put things right.
	seedAdmin(t, db, "usr_b", "b@example.com")
	if err := s.AddMember(ctx, actor, suspended.ID, User("usr_a")); err != nil {
		t.Fatalf("add with another admin remaining: %v", err)
	}
	if _, err := s.Update(ctx, actor, everyone.ID, UpdateRequest{Capabilities: &readOnly}); err != nil {
		t.Fatalf("update with another admin remaining: %v", err)
	}
}
