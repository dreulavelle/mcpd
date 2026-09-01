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

// seedUser writes an account directly. The account store is a layer above this
// one and importing it here would be a cycle; what these tests need is a row
// with an identifier and a grant.
func seedUser(t *testing.T, db *sqlite.DB, id, email string, grants string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO users (id, email, password_hash, display_name, role,
		                   plugins_json, disabled, created_at, updated_at)
		VALUES (?,?,'$2a$12$fake','','user',?,0,0,0)`, id, email, grants); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func seedKey(t *testing.T, db *sqlite.DB, id, grants string) {
	t.Helper()
	if _, err := db.Writer().ExecContext(context.Background(), `
		INSERT INTO api_keys (id, name, secret_hash, role, plugins_json,
		                      created_by, created_at, updated_at)
		VALUES (?,?,?,'user',?,'test',0,0)`, id, id, "digest-"+id, grants); err != nil {
		t.Fatalf("seed key: %v", err)
	}
}

func mustGroup(t *testing.T, s *Store, name string, plugins ...string) *Group {
	t.Helper()
	g, err := s.Create(context.Background(), "user:admin@example.com", CreateRequest{
		Name: name, Plugins: plugins,
	})
	if err != nil {
		t.Fatalf("create group %s: %v", name, err)
	}
	return g
}

func effective(t *testing.T, s *Store, subject Subject) []string {
	t.Helper()
	got, err := s.Effective(context.Background(), subject)
	if err != nil {
		t.Fatalf("effective grants: %v", err)
	}
	return got
}

// The whole of the model, in one test: what a subject reaches is its own
// grants unioned with every group it belongs to.
// Groups union with each other, and a subject with no grant of its own reaches
// all of them.
func TestEffective_UnionsTheGroupsOfASubjectThatGrantsItselfNothing(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", `[]`)
	field := mustGroup(t, s, "Field", "cnmaestro")
	noc := mustGroup(t, s, "NOC", "echo", "cnmaestro")

	for _, g := range []*Group{field, noc} {
		if err := s.AddMember(ctx, "user:admin@example.com", g.ID, User("usr_1")); err != nil {
			t.Fatalf("add to %s: %v", g.Name, err)
		}
	}

	want := []string{"cnmaestro", "echo"}
	if got := effective(t, s, User("usr_1")); !slices.Equal(got, want) {
		t.Errorf("granted = %v, want %v; a member of two groups reaches both", got, want)
	}
}

// A grant written on the subject is the whole answer, and a group cannot widen
// past it.
//
// This is the vulnerability that made the change: these were unioned, so a key
// saved as ["bandwidth"] — and displayed as ["bandwidth"] — belonging to a
// group granting ["*"] reached every plugin on the host. An audit against a
// live instance found a key scoped to one integration reading Graylog and
// Textable through their stored credentials. A narrowing that a group can
// erase is not a narrowing.
func TestEffective_ASubjectsOwnGrantIsNotWidenedByItsGroups(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", `["netbox"]`)
	everything := mustGroup(t, s, "Everyone", "*")
	if err := s.AddMember(ctx, "user:admin@example.com", everything.ID, User("usr_1")); err != nil {
		t.Fatalf("add: %v", err)
	}

	want := []string{"netbox"}
	if got := effective(t, s, User("usr_1")); !slices.Equal(got, want) {
		t.Errorf("granted = %v, want %v; a wildcard group must not erase an "+
			"explicit grant", got, want)
	}

	// And a named group is no different: the subject's own list still decides.
	field := mustGroup(t, s, "Field", "cnmaestro")
	if err := s.AddMember(ctx, "user:admin@example.com", field.ID, User("usr_1")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := effective(t, s, User("usr_1")); !slices.Equal(got, want) {
		t.Errorf("granted = %v, want %v; groups apply only to a subject that "+
			"grants itself nothing", got, want)
	}
}

// Removing a group takes its reach away, and it does so on the next request
// rather than the next restart: grants are resolved per call, never frozen
// when a session or a key was issued.
func TestEffective_RemovingAGroupRemovesItsReachOnTheNextRequest(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", `[]`)
	field := mustGroup(t, s, "Field", "cnmaestro")
	if err := s.AddMember(ctx, "user:admin@example.com", field.ID, User("usr_1")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := effective(t, s, User("usr_1")); !slices.Equal(got, []string{"cnmaestro"}) {
		t.Fatalf("granted = %v, want [cnmaestro]", got)
	}

	if err := s.RemoveMember(ctx, "user:admin@example.com", field.ID, User("usr_1")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := effective(t, s, User("usr_1")); len(got) != 0 {
		t.Errorf("granted = %v; the reach went with the membership", got)
	}
}

// Default none, at every level: a new group grants nothing, an account in no
// group reaches nothing, and a key with neither reaches nothing.
func TestEffective_DefaultsToNothingEverywhere(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()

	fresh := mustGroup(t, s, "Fresh")
	if len(fresh.Plugins) != 0 {
		t.Errorf("a new group grants %v; it must grant nothing", fresh.Plugins)
	}

	seedUser(t, db, "usr_1", "a@example.com", `[]`)
	if got := effective(t, s, User("usr_1")); len(got) != 0 {
		t.Errorf("a new account reaches %v; it must reach nothing", got)
	}

	seedKey(t, db, "key_1", `[]`)
	if got := effective(t, s, Key("key_1")); len(got) != 0 {
		t.Errorf("a new key reaches %v; it must reach nothing", got)
	}

	// A member of a group that grants nothing still reaches nothing: joining
	// is not itself a grant.
	if err := s.AddMember(ctx, "user:admin@example.com", fresh.ID, User("usr_1")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if got := effective(t, s, User("usr_1")); len(got) != 0 {
		t.Errorf("granted = %v; a group that grants nothing hands out nothing", got)
	}
}

// An identifier nobody has, and a kind this build does not know, reach nothing
// rather than erroring or -- far worse -- matching something.
func TestEffective_AnUnknownSubjectReachesNothing(t *testing.T) {
	s, db := newStore(t)
	seedUser(t, db, "usr_1", "a@example.com", `["*"]`)

	if got := effective(t, s, User("usr_nobody")); len(got) != 0 {
		t.Errorf("an unknown account reached %v", got)
	}
	// The identifiers are disjoint by construction, and the query keys the
	// kind explicitly so that it would stay disjoint even if they were not.
	if got := effective(t, s, Key("usr_1")); len(got) != 0 {
		t.Errorf("an account's identifier resolved as a key and reached %v", got)
	}
	if got := effective(t, s, Subject{Kind: "something", ID: "usr_1"}); len(got) != 0 {
		t.Errorf("an unknown subject kind reached %v", got)
	}
}

// The wildcard absorbs. A subject in a group granting everything reaches
// everything, and listing the named plugins beside it would render the same
// set as though it were smaller.
func TestUnion_WildcardAbsorbs(t *testing.T) {
	got := Union([]string{"echo"}, []string{auth.Wildcard}, []string{"netbox"})
	if !slices.Equal(got, []string{auth.Wildcard}) {
		t.Errorf("union = %v, want [*]", got)
	}
	// And the order it is folded in does not matter.
	got = Union([]string{auth.Wildcard}, []string{"echo"})
	if !slices.Equal(got, []string{auth.Wildcard}) {
		t.Errorf("union = %v, want [*]", got)
	}
	// Blanks and duplicates are not grants.
	got = Union([]string{"echo", "", " ", "echo"}, []string{"echo"})
	if !slices.Equal(got, []string{"echo"}) {
		t.Errorf("union = %v, want [echo]", got)
	}
	if got := Union(); len(got) != 0 {
		t.Errorf("union of nothing = %v, want empty", got)
	}
}

// Deleting a group narrows and never widens, and it strands nobody: the
// member keeps its own grant and every other group it is in.
func TestDelete_NarrowsAndStrandsNobody(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	// No grant of its own, so its reach is exactly what its groups give it --
	// which is what makes deleting one observable here.
	seedUser(t, db, "usr_1", "a@example.com", `[]`)
	field := mustGroup(t, s, "Field", "cnmaestro")
	noc := mustGroup(t, s, "NOC", "echo")
	for _, g := range []*Group{field, noc} {
		if err := s.AddMember(ctx, "user:admin@example.com", g.ID, User("usr_1")); err != nil {
			t.Fatalf("add: %v", err)
		}
	}

	if err := s.Delete(ctx, "user:admin@example.com", field.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	want := []string{"echo"}
	if got := effective(t, s, User("usr_1")); !slices.Equal(got, want) {
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

// Every act that changes what somebody can reach is in the hash-chained trail,
// written in the transaction that performed it, naming who did it -- and the
// chain still verifies afterwards.
func TestGroupChangesAreAuditedAndTheChainVerifies(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", `[]`)
	const actor = "user:admin@example.com"

	g := mustGroup(t, s, "Field")
	if _, err := s.Update(ctx, actor, g.ID, UpdateRequest{
		Plugins: &[]string{"cnmaestro"},
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
// the new grant leaves "what did this widen" unanswerable.
func TestUpdate_RecordsWhatTheGrantWas(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	g := mustGroup(t, s, "Field", "echo")
	if _, err := s.Update(ctx, "user:admin@example.com", g.ID, UpdateRequest{
		Plugins: &[]string{"echo", "cnmaestro"},
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
		if !strings.Contains(detail, "plugins_before") || !strings.Contains(detail, "echo") {
			t.Errorf("group.updated detail = %s; it must carry the grant it replaced", detail)
		}
		return
	}
	t.Fatal("group.updated is not in the trail")
}

func TestCreate_RefusesADuplicateName(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustGroup(t, s, "Field")
	if _, err := s.Create(ctx, "user:admin@example.com", CreateRequest{Name: "field"}); !errors.Is(err, ErrDuplicateName) {
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
	g := mustGroup(t, s, "Field")
	err := s.AddMember(context.Background(), "user:admin@example.com", g.ID, User("usr_nobody"))
	if !errors.Is(err, ErrNoSuchMember) {
		t.Errorf("adding a missing account: %v, want ErrNoSuchMember", err)
	}
}

// Adding somebody who is already a member writes nothing and is not an error.
// A trail that records non-events is one nobody reads carefully.
func TestAddMember_IsIdempotentAndRecordsNothingTwice(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", `[]`)
	g := mustGroup(t, s, "Field")
	for range 2 {
		if err := s.AddMember(ctx, "user:admin@example.com", g.ID, User("usr_1")); err != nil {
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
	seedUser(t, db, "usr_1", "a@example.com", `[]`)
	err := s.AddMember(context.Background(), "user:admin@example.com",
		"grp_nobody", User("usr_1"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("adding to a missing group: %v, want ErrNotFound", err)
	}
}

// A key joins a group the same way an account does, and the two memberships
// are distinct rows even when the identifiers look alike.
func TestAddMember_AccountsAndKeysAreSeparateMemberships(t *testing.T) {
	s, db := newStore(t)
	ctx := context.Background()
	seedUser(t, db, "usr_1", "a@example.com", `[]`)
	seedKey(t, db, "key_1", `[]`)
	g := mustGroup(t, s, "Field", "cnmaestro")

	for _, subject := range []Subject{User("usr_1"), Key("key_1")} {
		if err := s.AddMember(ctx, "user:admin@example.com", g.ID, subject); err != nil {
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
		if got := effective(t, s, subject); !slices.Equal(got, []string{"cnmaestro"}) {
			t.Errorf("%s reaches %v, want [cnmaestro]", subject.ID, got)
		}
	}

	// And taking one out leaves the other.
	if err := s.RemoveMember(ctx, "user:admin@example.com", g.ID, User("usr_1")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if got := effective(t, s, Key("key_1")); !slices.Equal(got, []string{"cnmaestro"}) {
		t.Errorf("the key lost its reach when an account left: %v", got)
	}
}
