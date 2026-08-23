package users

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// newStoreWithGroups opens an account store and a group store over one
// database, which is what the running host has: two stores, one union.
func newStoreWithGroups(t *testing.T) (*Store, *groups.Store, *sqlite.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "accounts.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	now := func() time.Time { return testClock }
	return NewStore(db, now), groups.NewStore(db, now), db
}

const adminActor = "user:owner@example.com"

// The union is one function, and an account reaches it through the account
// store without a second implementation appearing here.
func TestEffectiveGrants_IsTheUnion(t *testing.T) {
	s, gs, _ := newStoreWithGroups(t)
	ctx := context.Background()

	u, err := s.Create(ctx, CreateRequest{
		Email: "alice@example.com", Password: "a-sufficiently-long-passphrase",
		Role: auth.RoleUser, Plugins: []string{"netbox"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	g, err := gs.Create(ctx, adminActor, groups.CreateRequest{
		Name: "Field", Plugins: []string{"cnmaestro"},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := gs.AddMember(ctx, adminActor, g.ID, groups.User(u.ID)); err != nil {
		t.Fatalf("add: %v", err)
	}

	granted, err := s.EffectiveGrants(ctx, u.ID)
	if err != nil {
		t.Fatalf("effective grants: %v", err)
	}
	want := []string{"cnmaestro", "netbox"}
	if !slices.Equal(granted, want) {
		t.Fatalf("granted = %v, want %v", granted, want)
	}
	p := u.Principal("ses", granted)
	if !p.CanAccessPlugin("cnmaestro") || !p.CanAccessPlugin("netbox") {
		t.Error("the principal does not reach what the union says it does")
	}
	if p.CanAccessPlugin("echo") {
		t.Error("the principal reached a plugin nothing granted it")
	}
}

// An account can be created straight into a group, in one write, so there is
// no moment where it exists and reaches nothing because a second write has
// not landed.
func TestCreate_JoinsGroupsInTheSameWrite(t *testing.T) {
	s, gs, _ := newStoreWithGroups(t)
	ctx := context.Background()
	g, err := gs.Create(ctx, adminActor, groups.CreateRequest{
		Name: "Field", Plugins: []string{"cnmaestro"},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	u, err := s.Create(ctx, CreateRequest{
		Email: "alice@example.com", Password: "a-sufficiently-long-passphrase",
		Role: auth.RoleUser, Groups: []string{g.ID}, Actor: adminActor,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	granted, err := s.EffectiveGrants(ctx, u.ID)
	if err != nil {
		t.Fatalf("effective grants: %v", err)
	}
	if !slices.Equal(granted, []string{"cnmaestro"}) {
		t.Errorf("granted = %v, want [cnmaestro]", granted)
	}
}

// The default group is what makes approving a stranger one decision rather
// than two: they arrive already in the group the operator nominated, and the
// grant follows the moment they are approved.
func TestRegister_JoinsTheDefaultGroup(t *testing.T) {
	s, gs, _ := newStoreWithGroups(t)
	ctx := context.Background()
	claimInstance(t, s)

	if _, err := gs.Create(ctx, adminActor, groups.CreateRequest{
		Name: "Read only", Plugins: []string{"echo"},
	}); err != nil {
		t.Fatalf("create group: %v", err)
	}

	u, err := s.Register(ctx, RegisterRequest{
		Email: "stranger@example.com", Password: "a-sufficiently-long-passphrase",
		Policy: RegistrationPolicy{Enabled: true, DefaultGroup: "read only"},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !u.Pending() {
		t.Fatal("a password registration waits regardless of the approval setting")
	}
	// Pending holds no capability whatever it is a member of, which is what
	// makes joining now safe.
	granted, err := s.EffectiveGrants(ctx, u.ID)
	if err != nil {
		t.Fatalf("effective grants: %v", err)
	}
	if !slices.Equal(granted, []string{"echo"}) {
		t.Fatalf("granted = %v, want [echo]", granted)
	}
	if u.Principal("ses", granted).Can(auth.CapRead) {
		t.Error("a pending account holds a capability")
	}

	approved, err := s.ApproveRegistration(ctx, adminActor, u.ID, nil)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	granted, err = s.EffectiveGrants(ctx, approved.ID)
	if err != nil {
		t.Fatalf("effective grants: %v", err)
	}
	p := approved.Principal("ses", granted)
	if !p.Can(auth.CapRead) || !p.CanAccessPlugin("echo") {
		t.Errorf("an approved account in the default group reaches %v and holds read=%v; "+
			"approving was supposed to be one decision", granted, p.Can(auth.CapRead))
	}
}

// The setting names a group by name, so a group renamed or deleted underneath
// it stops granting rather than starting to refuse registrations. Narrowing is
// the safe direction for a setting nobody has revisited.
func TestRegister_ADefaultGroupThatIsNotThereGrantsNothing(t *testing.T) {
	s, _, _ := newStoreWithGroups(t)
	ctx := context.Background()
	claimInstance(t, s)

	u, err := s.Register(ctx, RegisterRequest{
		Email: "stranger@example.com", Password: "a-sufficiently-long-passphrase",
		Policy: RegistrationPolicy{Enabled: true, DefaultGroup: "a group nobody made"},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	granted, err := s.EffectiveGrants(ctx, u.ID)
	if err != nil {
		t.Fatalf("effective grants: %v", err)
	}
	if len(granted) != 0 {
		t.Errorf("granted = %v; a default group that does not exist grants nothing", granted)
	}
}

// With no default group configured, nothing changes: a self-registration
// reaches nothing until somebody grants it something, which is what the SSO
// work established and this must not weaken.
func TestRegister_WithoutADefaultGroupReachesNothing(t *testing.T) {
	s, _, _ := newStoreWithGroups(t)
	ctx := context.Background()
	claimInstance(t, s)

	u, err := s.Register(ctx, RegisterRequest{
		Email: "stranger@example.com", Password: "a-sufficiently-long-passphrase",
		Policy: RegistrationPolicy{Enabled: true},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(u.Plugins) != 0 {
		t.Errorf("direct grants = %v, want none", u.Plugins)
	}
	granted, err := s.EffectiveGrants(ctx, u.ID)
	if err != nil {
		t.Fatalf("effective grants: %v", err)
	}
	if len(granted) != 0 {
		t.Errorf("granted = %v; a self-registered account reaches nothing", granted)
	}
}

// Approving can assign groups instead of, or as well as, the default -- in the
// same transaction as the status change, so there is no window in which an
// approved account exists holding nothing.
func TestApproveRegistration_AssignsGroupsAndAuditsThem(t *testing.T) {
	s, gs, db := newStoreWithGroups(t)
	ctx := context.Background()
	claimInstance(t, s)

	g, err := gs.Create(ctx, adminActor, groups.CreateRequest{
		Name: "Field", Plugins: []string{"cnmaestro"},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	u, err := s.Register(ctx, RegisterRequest{
		Email: "stranger@example.com", Password: "a-sufficiently-long-passphrase",
		Policy: RegistrationPolicy{Enabled: true},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	approved, err := s.ApproveRegistration(ctx, adminActor, u.ID, []string{g.ID})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	granted, err := s.EffectiveGrants(ctx, approved.ID)
	if err != nil {
		t.Fatalf("effective grants: %v", err)
	}
	if !slices.Equal(granted, []string{"cnmaestro"}) {
		t.Fatalf("granted = %v, want [cnmaestro]", granted)
	}

	records, err := sqlite.NewAuditStore(db).Recent(ctx, 20)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	for _, r := range records {
		if r.Entry.Kind != "account.approved" {
			continue
		}
		if r.Entry.Actor != adminActor {
			t.Errorf("approval recorded against %q, want %q", r.Entry.Actor, adminActor)
		}
		if want := g.ID; !strings.Contains(string(r.Entry.Detail), want) {
			t.Errorf("approval detail = %s; it must name the group assigned (%s)",
				r.Entry.Detail, want)
		}
		return
	}
	t.Fatal("account.approved is not in the trail")
}

// Deleting an account takes its memberships with it, so a group's member count
// never disagrees with the people it names.
func TestDelete_TakesMembershipsWithIt(t *testing.T) {
	s, gs, db := newStoreWithGroups(t)
	ctx := context.Background()
	// The instance needs an administrator that is not the one being deleted,
	// or the last-administrator guard refuses.
	if _, err := s.CreateFirst(ctx, "owner@example.com",
		"a-sufficiently-long-passphrase", ""); err != nil {
		t.Fatalf("claim: %v", err)
	}
	u, err := s.Create(ctx, CreateRequest{
		Email: "alice@example.com", Password: "a-sufficiently-long-passphrase",
		Role: auth.RoleUser,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	g, err := gs.Create(ctx, adminActor, groups.CreateRequest{Name: "Field"})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if err := gs.AddMember(ctx, adminActor, g.ID, groups.User(u.ID)); err != nil {
		t.Fatalf("add: %v", err)
	}

	if err := s.Delete(ctx, u.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var n int
	if err := db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM group_members WHERE user_id = ?`, u.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d memberships outlived the account they belonged to", n)
	}
}

// claimInstance makes the first account, because Register refuses on an
// unclaimed host: completing a form is not how somebody becomes this host's
// administrator.
func claimInstance(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.CreateFirst(context.Background(), "owner@example.com",
		"a-sufficiently-long-passphrase", ""); err != nil {
		t.Fatalf("claim the instance: %v", err)
	}
}

// countAudit reports how many entries of a kind the trail holds, and fails the
// test if the chain no longer verifies -- an assertion about what is recorded
// is worth nothing if the record can no longer be trusted.
func countAudit(t *testing.T, db *sqlite.DB, kind string) int {
	t.Helper()
	ctx := context.Background()
	store := sqlite.NewAuditStore(db)
	records, err := store.Recent(ctx, 100)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	n := 0
	for _, r := range records {
		if r.Entry.Kind == kind {
			n++
		}
	}
	if _, err := store.VerifyChain(ctx); err != nil {
		t.Fatalf("the audit chain no longer verifies: %v", err)
	}
	return n
}

// An account created straight into groups reaches what they reach the moment
// the write commits, so the memberships have to be in the trail. Without them
// an operator reading the record sees an account appear and has to infer the
// grant, which is the exact question auditing membership exists to answer.
func TestCreate_AuditsTheGroupsAnAccountIsCreatedInto(t *testing.T) {
	s, gs, db := newStoreWithGroups(t)
	ctx := context.Background()

	var ids []string
	for _, name := range []string{"Field", "NOC"} {
		g, err := gs.Create(ctx, adminActor, groups.CreateRequest{
			Name: name, Plugins: []string{"echo"},
		})
		if err != nil {
			t.Fatalf("create group: %v", err)
		}
		ids = append(ids, g.ID)
	}

	u, err := s.Create(ctx, CreateRequest{
		Email: "alice@example.com", Password: "a-sufficiently-long-passphrase",
		Role: auth.RoleUser, Groups: ids, Actor: adminActor,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if got := countAudit(t, db, "group.member_added"); got != 2 {
		t.Fatalf("group.member_added appears %d times, want 2", got)
	}

	// The entries name the administrator who did it and the account it was
	// done to, which is what makes them an answer rather than a note.
	records, err := sqlite.NewAuditStore(db).Recent(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.Entry.Kind != "group.member_added" {
			continue
		}
		if r.Entry.Actor != adminActor {
			t.Errorf("recorded against %q, want %q", r.Entry.Actor, adminActor)
		}
		if !strings.Contains(string(r.Entry.Detail), u.ID) {
			t.Errorf("detail = %s; it must name the account (%s)", r.Entry.Detail, u.ID)
		}
	}
}

// The default group is nobody's decision at the moment it happens: the
// registrant did not choose it and no administrator was asked. Attributing it
// to either would put a decision in the trail that person did not make.
func TestRegister_RecordsTheDefaultGroupAgainstTheSetting(t *testing.T) {
	s, gs, db := newStoreWithGroups(t)
	ctx := context.Background()
	claimInstance(t, s)
	if _, err := gs.Create(ctx, adminActor, groups.CreateRequest{
		Name: "Read only", Plugins: []string{"echo"},
	}); err != nil {
		t.Fatalf("create group: %v", err)
	}

	if _, err := s.Register(ctx, RegisterRequest{
		Email: "stranger@example.com", Password: "a-sufficiently-long-passphrase",
		Policy: RegistrationPolicy{Enabled: true, DefaultGroup: "Read only"},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if got := countAudit(t, db, "group.member_added"); got != 1 {
		t.Fatalf("group.member_added appears %d times, want 1", got)
	}
	records, err := sqlite.NewAuditStore(db).Recent(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.Entry.Kind != "group.member_added" {
			continue
		}
		if r.Entry.Actor != DefaultGroupActor {
			t.Errorf("recorded against %q, want %q -- a setting did this, not a person",
				r.Entry.Actor, DefaultGroupActor)
		}
		return
	}
	t.Fatal("group.member_added is not in the trail")
}

// Approving with a group is one membership and one entry, beside the approval
// itself.
func TestApproveRegistration_AuditsTheMembershipItCreates(t *testing.T) {
	s, gs, db := newStoreWithGroups(t)
	ctx := context.Background()
	claimInstance(t, s)

	g, err := gs.Create(ctx, adminActor, groups.CreateRequest{
		Name: "Field", Plugins: []string{"cnmaestro"},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	u, err := s.Register(ctx, RegisterRequest{
		Email: "stranger@example.com", Password: "a-sufficiently-long-passphrase",
		Policy: RegistrationPolicy{Enabled: true},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := countAudit(t, db, "group.member_added"); got != 0 {
		t.Fatalf("group.member_added appears %d times before approval, want 0", got)
	}

	if _, err := s.ApproveRegistration(ctx, adminActor, u.ID, []string{g.ID}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if got := countAudit(t, db, "group.member_added"); got != 1 {
		t.Errorf("group.member_added appears %d times after approval, want 1", got)
	}
}
