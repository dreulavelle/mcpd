package groups

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

var testClock = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

const actor = "user:admin@example.com"

func newStore(t *testing.T) (*Store, *sqlite.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "groups.db"),
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

// writes and reads spell a grant list the way an administrator says it: these
// plugins, at this level.
func writes(plugins ...string) auth.Grants { return auth.GrantsAt(plugins, auth.LevelWrite) }
func reads(plugins ...string) auth.Grants  { return auth.GrantsAt(plugins, auth.LevelRead) }

// seedUser writes an account directly. The account store is a layer above this
// one and importing it here would be a cycle; what these tests need is a row
// with an identifier, a role and a grant.
func seedUser(t *testing.T, db *sqlite.DB, id, email, roleID string, grants auth.Grants) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO users (id, email, password_hash, display_name, role_id,
		                   grants_json, disabled, status, created_at, updated_at)
		VALUES (?,?,'$2a$12$fake','',?,?,0,'active',0,0)`,
		id, email, roleID, auth.EncodeGrants(grants)); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func seedKey(t *testing.T, db *sqlite.DB, id, roleID string, grants auth.Grants) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO api_keys (id, name, secret_hash, role_id, grants_json,
		                      created_by, created_at, updated_at)
		VALUES (?,?,?,?,?,'test',0,0)`,
		id, id, "digest-"+id, roleID, auth.EncodeGrants(grants)); err != nil {
		t.Fatalf("seed key: %v", err)
	}
}

func mustGroup(t *testing.T, s *Store, name string, grants auth.Grants) *Group {
	t.Helper()
	g, err := s.Create(context.Background(), actor, CreateRequest{Name: name, Grants: grants})
	if err != nil {
		t.Fatalf("create group %s: %v", name, err)
	}
	return g
}

func mustRoledGroup(t *testing.T, s *Store, name, roleID string, grants auth.Grants) *Group {
	t.Helper()
	g, err := s.Create(context.Background(), actor, CreateRequest{
		Name: name, RoleID: roleID, Grants: grants,
	})
	if err != nil {
		t.Fatalf("create group %s: %v", name, err)
	}
	return g
}

func effective(t *testing.T, s *Store, subject Subject) auth.Grants {
	t.Helper()
	got, err := s.Effective(context.Background(), subject)
	if err != nil {
		t.Fatalf("effective grants: %v", err)
	}
	return got
}

// reach names the plugins a subject can get to at all, for the tests whose
// subject is which plugins rather than at what level.
func reach(t *testing.T, s *Store, subject Subject) []string {
	t.Helper()
	return effective(t, s, subject).Plugins()
}

func resolve(t *testing.T, s *Store, subject Subject) Resolved {
	t.Helper()
	got, err := s.Resolve(context.Background(), subject)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return got
}

// The whole of the model, in one test: what a subject holds is its own role
// and grants unioned with those of every group it belongs to. Nothing
// subtracts, and the higher of two levels wins.
//
// This replaced a rule that a subject's own grant beat its groups'. That rule
// was a second answer to the question this one already answers, and "why can
// this person reach that" had to be read twice to be answered once.
func TestResolve_IsTheUnionOfTheSubjectAndItsGroups(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	// Its own: netbox at read, and the Reader role.
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleReader, reads("netbox"))
	// Its group's: netbox at write -- higher, so it wins -- plus cnmaestro,
	// plus the Operator role, which adds deciding on approvals.
	field := mustRoledGroup(t, s, "Field", auth.RoleOperator,
		auth.Grants{{Plugin: "netbox", Level: auth.LevelWrite}, {Plugin: "cnmaestro", Level: auth.LevelRead}})
	if err := s.AddMember(ctx, actor, field.ID, User("usr_1")); err != nil {
		t.Fatalf("add: %v", err)
	}

	got := resolve(t, s, User("usr_1"))
	if got.RoleID != auth.RoleReader || got.RoleName != "Reader" {
		t.Errorf("own role = %q/%q, want the Reader it was seeded with", got.RoleID, got.RoleName)
	}
	if !got.Permissions.Holds(auth.PermApprovalsDecide) {
		t.Error("the group's Operator role did not add approvals:decide; permissions merge")
	}
	if !got.Permissions.Holds(auth.PermSettingsRead) {
		t.Error("the subject lost a permission its own Reader role carried")
	}
	if got.Permissions.Holds(auth.PermSettingsWrite) {
		t.Error("the union invented a permission neither role held")
	}
	want := auth.Grants{
		{Plugin: "cnmaestro", Level: auth.LevelRead},
		{Plugin: "netbox", Level: auth.LevelWrite},
	}
	if !got.Grants.Equal(want) {
		t.Errorf("grants = %v, want %v; the higher level for a plugin wins", got.Grants, want)
	}
}

// Groups union with each other, and a subject with no grant of its own reaches
// all of them.
func TestEffective_UnionsTheGroupsOfASubjectThatGrantsItselfNothing(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleOperator, nil)
	field := mustGroup(t, s, "Field", writes("cnmaestro"))
	noc := mustGroup(t, s, "NOC", writes("echo", "cnmaestro"))

	for _, g := range []*Group{field, noc} {
		if err := s.AddMember(ctx, actor, g.ID, User("usr_1")); err != nil {
			t.Fatalf("add to %s: %v", g.Name, err)
		}
	}

	want := []string{"cnmaestro", "echo"}
	if got := reach(t, s, User("usr_1")); !slices.Equal(got, want) {
		t.Errorf("granted = %v, want %v; a member of two groups reaches both", got, want)
	}
}

// A subject in no group holds exactly what its own row says, and nothing has
// to be present for that to be the answer.
func TestResolve_ASubjectInNoGroupHoldsItsOwn(t *testing.T) {
	s, db := newStore(t)
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleOperator, writes("echo"))

	got := resolve(t, s, User("usr_1"))
	if !got.Grants.Equal(writes("echo")) {
		t.Errorf("grants = %v, want echo at write", got.Grants)
	}
	if !got.Permissions.Holds(auth.PermApprovalsDecide) {
		t.Error("an Operator in no group cannot decide on approvals")
	}
	if got.Permissions.Holds(auth.PermAccessWrite) {
		t.Error("an Operator in no group can manage access")
	}
}

// A group with no role hands out reach and nothing else. It is a real
// arrangement -- "these people can see cnmaestro" says nothing about what they
// may do on this host -- and an empty role must not read as a permissive one.
func TestResolve_AGroupWithNoRoleContributesNoPermissions(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleReader, nil)
	reachOnly := mustGroup(t, s, "Field", writes("cnmaestro"))
	if reachOnly.RoleID != "" {
		t.Fatalf("group role = %q, want none", reachOnly.RoleID)
	}
	if err := s.AddMember(ctx, actor, reachOnly.ID, User("usr_1")); err != nil {
		t.Fatalf("add: %v", err)
	}

	got := resolve(t, s, User("usr_1"))
	if !got.Grants.Equal(writes("cnmaestro")) {
		t.Errorf("grants = %v; the group's reach is handed over", got.Grants)
	}
	reader, _ := auth.BuiltinRole(auth.RoleReader)
	if !got.Permissions.Equal(reader.Permissions) {
		t.Errorf("permissions = %v, want exactly the Reader's %v; a group with "+
			"no role adds nothing", got.Permissions, reader.Permissions)
	}
}

// Removing a group takes its reach away, and it does so on the next request
// rather than the next restart: access is resolved per call, never frozen
// when a session or a key was issued.
func TestEffective_RemovingAGroupRemovesItsReachOnTheNextRequest(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleOperator, nil)
	field := mustGroup(t, s, "Field", writes("cnmaestro"))
	if err := s.AddMember(ctx, actor, field.ID, User("usr_1")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := reach(t, s, User("usr_1")); !slices.Equal(got, []string{"cnmaestro"}) {
		t.Fatalf("granted = %v, want [cnmaestro]", got)
	}

	if err := s.RemoveMember(ctx, actor, field.ID, User("usr_1")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := reach(t, s, User("usr_1")); len(got) != 0 {
		t.Errorf("granted = %v; the reach went with the membership", got)
	}
}

// Default none, at every level: a new group grants nothing, an account in no
// group reaches nothing, and a key with neither reaches nothing.
func TestEffective_DefaultsToNothingEverywhere(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()

	fresh := mustGroup(t, s, "Fresh", nil)
	if len(fresh.Grants) != 0 {
		t.Errorf("a new group grants %v; it must grant nothing", fresh.Grants)
	}

	seedUser(t, db, "usr_1", "a@example.com", auth.RoleOperator, nil)
	if got := reach(t, s, User("usr_1")); len(got) != 0 {
		t.Errorf("a new account reaches %v; it must reach nothing", got)
	}

	seedKey(t, db, "key_1", auth.RoleOperator, nil)
	if got := reach(t, s, Key("key_1")); len(got) != 0 {
		t.Errorf("a new key reaches %v; it must reach nothing", got)
	}

	// A member of a group that grants nothing still reaches nothing: joining
	// is not itself a grant.
	if err := s.AddMember(ctx, actor, fresh.ID, User("usr_1")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := reach(t, s, User("usr_1")); len(got) != 0 {
		t.Errorf("granted = %v; a group that grants nothing hands out nothing", got)
	}
}

// An identifier nobody has, and a kind this build does not know, reach nothing
// rather than erroring or -- far worse -- matching something.
func TestEffective_AnUnknownSubjectReachesNothing(t *testing.T) {
	s, db := newStore(t)
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleOperator, writes(auth.Wildcard))

	if got := reach(t, s, User("usr_nobody")); len(got) != 0 {
		t.Errorf("an unknown account reached %v", got)
	}
	// The identifiers are disjoint by construction, and the query keys the
	// kind explicitly so that it would stay disjoint even if they were not.
	if got := reach(t, s, Key("usr_1")); len(got) != 0 {
		t.Errorf("an account's identifier resolved as a key and reached %v", got)
	}
	if got := reach(t, s, Subject{Kind: "something", ID: "usr_1"}); len(got) != 0 {
		t.Errorf("an unknown subject kind reached %v", got)
	}
}

// Deleting a group narrows and never widens, and it strands nobody: the
// member keeps its own grant and every other group it is in.
func TestDelete_NarrowsAndStrandsNobody(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	// No grant of its own, so its reach is exactly what its groups give it --
	// which is what makes deleting one observable here.
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleOperator, nil)
	field := mustGroup(t, s, "Field", writes("cnmaestro"))
	noc := mustGroup(t, s, "NOC", writes("echo"))
	for _, g := range []*Group{field, noc} {
		if err := s.AddMember(ctx, actor, g.ID, User("usr_1")); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	if err := s.Delete(ctx, actor, field.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	want := []string{"echo"}
	if got := reach(t, s, User("usr_1")); !slices.Equal(got, want) {
		t.Errorf("granted = %v, want %v; deleting a group takes its grant and nothing else", got, want)
	}
	// The account is still there, and still signs in.
	var n int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE id = 'usr_1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("deleting a group deleted an account")
	}
	// And the membership went with the group rather than being left to point
	// at nothing.
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM group_members WHERE group_id = ?`, field.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d memberships outlived the group they belonged to", n)
	}
	if _, err := s.ByID(ctx, field.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("reading the deleted group: %v, want ErrNotFound", err)
	}
}

// --- the last administrator ------------------------------------------------

// A group can be the only thing giving somebody access:write, and once it is,
// every way of taking it back has to be refused. There is no path back from
// the alternative: undoing the change needs the permission it just removed.
//
// The three ways, all covered here because they are one rule with three doors:
// removing the person from the group, taking the group's role away, and
// deleting the group.
func TestLastAdministratorCannotBeStrandedThroughAGroup(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	// Their own role administers nothing. Everything they can do about access
	// arrives through the group.
	seedUser(t, db, "usr_a", "a@example.com", auth.RoleReader, writes(auth.Wildcard))
	admins := mustRoledGroup(t, s, "Administrators", auth.RoleAdministrator, writes(auth.Wildcard))
	if err := s.AddMember(ctx, actor, admins.ID, User("usr_a")); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := s.RemoveMember(ctx, actor, admins.ID, User("usr_a")); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("removing the only administrator from the group: %v, want ErrLastAdmin", err)
	}
	if members, _ := s.Members(ctx, admins.ID); len(members) != 1 {
		t.Fatalf("the refused removal was written; members = %v", members)
	}

	none := ""
	if _, err := s.Update(ctx, actor, admins.ID, UpdateRequest{RoleID: &none}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("taking the group's role away: %v, want ErrLastAdmin", err)
	}
	reader := auth.RoleReader
	if _, err := s.Update(ctx, actor, admins.ID, UpdateRequest{RoleID: &reader}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demoting the group's role: %v, want ErrLastAdmin", err)
	}
	if g, _ := s.ByID(ctx, admins.ID); g.RoleID != auth.RoleAdministrator {
		t.Fatalf("the refused role change was written: %q", g.RoleID)
	}

	if err := s.Delete(ctx, actor, admins.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleting the group: %v, want ErrLastAdmin", err)
	}
	if _, err := s.ByID(ctx, admins.ID); err != nil {
		t.Fatalf("the refused delete was written: %v", err)
	}

	// With a second administrator holding it directly, somebody can still put
	// things right, and all three go through.
	seedUser(t, db, "usr_b", "b@example.com", auth.RoleAdministrator, writes(auth.Wildcard))
	if err := s.RemoveMember(ctx, actor, admins.ID, User("usr_a")); err != nil {
		t.Errorf("removing with another administrator remaining: %v", err)
	}
	if _, err := s.Update(ctx, actor, admins.ID, UpdateRequest{RoleID: &reader}); err != nil {
		t.Errorf("demoting with another administrator remaining: %v", err)
	}
	if err := s.Delete(ctx, actor, admins.ID); err != nil {
		t.Errorf("deleting with another administrator remaining: %v", err)
	}
}

// A pending account holds nothing whatever its row says, so a group's role
// cannot make it the administrator that keeps the guard satisfied.
func TestLastAdministrator_DoesNotCountAPendingMember(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_a", "a@example.com", auth.RoleAdministrator, writes(auth.Wildcard))
	if _, err := db.Writer().ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, display_name, role_id,
		                   grants_json, disabled, status, created_at, updated_at)
		VALUES ('usr_p','p@example.com','$2a$12$fake','',?,'[]',0,'pending',0,0)`,
		auth.RoleReader); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	admins := mustRoledGroup(t, s, "Administrators", auth.RoleAdministrator, nil)
	if err := s.AddMember(ctx, actor, admins.ID, User("usr_p")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.AddMember(ctx, actor, admins.ID, User("usr_a")); err != nil {
		t.Fatalf("add: %v", err)
	}

	// usr_a holds access:write directly as well, so removing them from the
	// group is fine; deleting the group leaves only the pending member
	// holding it, which is nobody.
	if err := s.Delete(ctx, actor, admins.ID); err != nil {
		t.Fatalf("delete with a real administrator outside the group: %v", err)
	}

	// Now the real administrator's access comes only from a group again, and
	// a pending member of it does not stand in for them.
	admins = mustRoledGroup(t, s, "Administrators", auth.RoleAdministrator, nil)
	seedUser(t, db, "usr_c", "c@example.com", auth.RoleReader, nil)
	for _, id := range []string{"usr_c", "usr_p"} {
		if err := s.AddMember(ctx, actor, admins.ID, User(id)); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	// usr_a still administers directly, so make them stop.
	if _, err := db.Writer().ExecContext(ctx,
		`UPDATE users SET role_id = ? WHERE id = 'usr_a'`, auth.RoleReader); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(ctx, actor, admins.ID, User("usr_c")); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("removing the only active administrator: %v, want ErrLastAdmin; "+
			"a pending member holds nothing", err)
	}
}

// --- roles -----------------------------------------------------------------

// A group can only hand out a role that exists. Assigning one that does not
// would leave every member pointing at nothing, and nobody decided that.
func TestRoleMustExist(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	if _, err := s.Create(ctx, actor, CreateRequest{
		Name: "Ghost", RoleID: "role_nobody_made",
	}); !errors.Is(err, ErrNoSuchRole) {
		t.Errorf("creating with an unknown role: %v, want ErrNoSuchRole", err)
	}

	g := mustGroup(t, s, "Field", nil)
	missing := "role_nobody_made"
	if _, err := s.Update(ctx, actor, g.ID, UpdateRequest{RoleID: &missing}); !errors.Is(err, ErrNoSuchRole) {
		t.Errorf("assigning an unknown role: %v, want ErrNoSuchRole", err)
	}
	// A built-in is there from the migration, so it is assignable without
	// anything having called EnsureBuiltins first.
	operator := auth.RoleOperator
	got, err := s.Update(ctx, actor, g.ID, UpdateRequest{RoleID: &operator})
	if err != nil {
		t.Fatalf("assigning a built-in role: %v", err)
	}
	if got.RoleID != auth.RoleOperator || got.RoleName != "Operator" {
		t.Errorf("role = %q/%q, want the Operator", got.RoleID, got.RoleName)
	}
}

// --- audit -----------------------------------------------------------------

// Every act that changes what somebody can reach is in the hash-chained trail,
// written in the transaction that performed it, naming who did it -- and the
// chain still verifies afterwards.
func TestGroupChangesAreAuditedAndTheChainVerifies(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleOperator, nil)

	g := mustGroup(t, s, "Field", nil)
	if _, err := s.Update(ctx, actor, g.ID, UpdateRequest{
		Grants: ptr(writes("cnmaestro")),
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := s.AddMember(ctx, actor, g.ID, User("usr_1")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.RemoveMember(ctx, actor, g.ID, User("usr_1")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := s.Delete(ctx, actor, g.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	audit := sqlite.NewAuditStore(db)
	records, err := audit.Recent(ctx, 50)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	want := map[string]bool{
		"group.created":        false,
		"group.updated":        false,
		"group.member_added":   false,
		"group.member_removed": false,
		"group.deleted":        false,
	}
	for _, r := range records {
		if _, ok := want[r.Entry.Kind]; !ok {
			continue
		}
		want[r.Entry.Kind] = true
		if r.Entry.Actor != actor {
			t.Errorf("%s was recorded against %q, want %q", r.Entry.Kind, r.Entry.Actor, actor)
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

// A privilege change has to say what it changed *from*. An entry carrying only
// the new grant leaves "what did this widen" unanswerable -- and a role change
// is the same fact about what a member may do.
func TestUpdate_RecordsWhatTheGrantAndRoleWere(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	g := mustRoledGroup(t, s, "Field", auth.RoleReader, writes("echo"))
	operator := auth.RoleOperator
	if _, err := s.Update(ctx, actor, g.ID, UpdateRequest{
		Grants: ptr(writes("echo", "cnmaestro")),
		RoleID: &operator,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	records, err := sqlite.NewAuditStore(db).Recent(ctx, 10)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	for _, r := range records {
		if r.Entry.Kind != "group.updated" {
			continue
		}
		detail := string(r.Entry.Detail)
		for _, want := range []string{"grants_before", "echo", "role_before", auth.RoleReader} {
			if !strings.Contains(detail, want) {
				t.Errorf("group.updated detail = %s; it must carry %q, what it replaced",
					detail, want)
			}
		}
		return
	}
	t.Fatal("group.updated is not in the trail")
}

// --- names and membership --------------------------------------------------

func TestCreate_RefusesADuplicateName(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustGroup(t, s, "Field", nil)
	if _, err := s.Create(ctx, actor, CreateRequest{Name: "field"}); !errors.Is(err, ErrDuplicateName) {
		t.Errorf("second group named the same: %v, want ErrDuplicateName", err)
	}
}

// A name is rendered in a list and appears beside an audit entry, so the rules
// are the ones a display name is held to.
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
		{"bidi override", "Field‮", false},
		{"too long", string(make([]rune, MaxNameRunes+1)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateName(tc.in)
			if (err == nil) != tc.ok {
				t.Errorf("ValidateName(%q) error = %v, want ok=%v", tc.in, err, tc.ok)
			}
		})
	}
}

// Adding a subject that is not there is refused rather than writing a
// membership naming nobody.
func TestAddMember_RefusesASubjectThatIsNotThere(t *testing.T) {
	s, _ := newStore(t)
	g := mustGroup(t, s, "Field", nil)
	err := s.AddMember(context.Background(), actor, g.ID, User("usr_nobody"))
	if !errors.Is(err, ErrNoSuchMember) {
		t.Errorf("adding a missing account: %v, want ErrNoSuchMember", err)
	}
}

// Adding somebody who is already a member writes nothing and is not an error.
// A trail that records non-events is one nobody reads carefully.
func TestAddMember_IsIdempotentAndRecordsNothingTwice(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleOperator, nil)
	g := mustGroup(t, s, "Field", nil)
	for range 2 {
		if err := s.AddMember(ctx, actor, g.ID, User("usr_1")); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	records, err := sqlite.NewAuditStore(db).Recent(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	added := 0
	for _, r := range records {
		if r.Entry.Kind == "group.member_added" {
			added++
		}
	}
	if added != 1 {
		t.Errorf("group.member_added appears %d times; adding a member twice is one grant", added)
	}
}

// A group deleted between a page load and a write is a refusal with a sentence
// somebody can read, rather than a foreign-key violation from the driver.
func TestAddMember_RefusesAGroupThatIsNotThere(t *testing.T) {
	s, db := newStore(t)
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleOperator, nil)
	err := s.AddMember(context.Background(), actor, "grp_nobody", User("usr_1"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("adding to a missing group: %v, want ErrNotFound", err)
	}
}

// A key joins a group the same way an account does, and the two memberships
// are distinct rows even when the identifiers look alike.
func TestAddMember_AccountsAndKeysAreSeparateMemberships(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", auth.RoleOperator, nil)
	seedKey(t, db, "key_1", auth.RoleOperator, nil)
	g := mustGroup(t, s, "Field", writes("cnmaestro"))

	for _, subject := range []Subject{User("usr_1"), Key("key_1")} {
		if err := s.AddMember(ctx, actor, g.ID, subject); err != nil {
			t.Fatalf("add %s: %v", subject.ID, err)
		}
	}
	members, err := s.Members(ctx, g.ID)
	if err != nil {
		t.Fatalf("members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %d, want 2", len(members))
	}
	for _, subject := range []Subject{User("usr_1"), Key("key_1")} {
		if got := reach(t, s, subject); !slices.Equal(got, []string{"cnmaestro"}) {
			t.Errorf("%s reaches %v, want [cnmaestro]", subject.ID, got)
		}
	}

	// And taking one out leaves the other.
	if err := s.RemoveMember(ctx, actor, g.ID, User("usr_1")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := reach(t, s, Key("key_1")); !slices.Equal(got, []string{"cnmaestro"}) {
		t.Errorf("the key lost its reach when an account left: %v", got)
	}
}

func ptr[T any](v T) *T { return &v }
