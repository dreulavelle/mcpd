package users

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

var testClock = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*Store, func(time.Time)) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "test.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	clock := testClock
	return NewStore(db, func() time.Time { return clock }), func(at time.Time) { clock = at }
}

func mustCreate(t *testing.T, s *Store, email string, role auth.Role) *User {
	t.Helper()
	u, err := s.Create(context.Background(), CreateRequest{
		Email:    email,
		Password: "a-sufficiently-long-passphrase",
		Role:     role,
		Plugins:  []string{auth.Wildcard},
	})
	if err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	return u
}

func TestNormalizeEmail(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		wantErr  bool
	}{
		{in: "  Alice@Example.COM ", want: "alice@example.com"},
		{in: "Alice <alice@example.com>", want: "alice@example.com"},
		{in: "", wantErr: true},
		{in: "not-an-address", wantErr: true},
		{in: "@example.com", wantErr: true},
	} {
		got, err := NormalizeEmail(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeEmail(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeEmail(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Case and surrounding whitespace must not create a second account for one
// person, which is the whole reason addresses are normalised before storage.
func TestCreate_EmailIsNormalisedAndUnique(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	u := mustCreate(t, s, "  Alice@Example.COM ", auth.RoleAdmin)
	if u.Email != "alice@example.com" {
		t.Fatalf("stored email %q, want alice@example.com", u.Email)
	}

	_, err := s.Create(ctx, CreateRequest{
		Email: "ALICE@example.com", Password: "another-long-passphrase",
		Role: auth.RoleUser, Plugins: []string{"echo"},
	})
	if !errors.Is(err, ErrDuplicateEmail) {
		t.Fatalf("second registration of the same address: %v, want ErrDuplicateEmail", err)
	}
}

func TestCreate_RejectsEmptyPluginGrant(t *testing.T) {
	s, _ := newStore(t)
	_, err := s.Create(context.Background(), CreateRequest{
		Email: "b@example.com", Password: "a-sufficiently-long-passphrase",
		Role: auth.RoleUser,
	})
	if err == nil {
		t.Fatal("an account granting no plugins reaches nothing; creating one must be refused")
	}
}

func TestAuthenticate(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustCreate(t, s, "alice@example.com", auth.RoleAdmin)

	// The address is normalised on the way in, so however it was typed works.
	if _, err := s.Authenticate(ctx, "ALICE@example.com ", "a-sufficiently-long-passphrase"); err != nil {
		t.Fatalf("correct credentials: %v", err)
	}

	// A wrong password and an unknown address are the same answer, so the form
	// cannot be used to discover which addresses have accounts.
	for _, tc := range []struct{ name, email, password string }{
		{"wrong password", "alice@example.com", "wrong-but-long-enough"},
		{"unknown address", "nobody@example.com", "a-sufficiently-long-passphrase"},
		{"malformed address", "not-an-address", "a-sufficiently-long-passphrase"},
	} {
		if _, err := s.Authenticate(ctx, tc.email, tc.password); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s: %v, want ErrInvalidCredentials", tc.name, err)
		}
	}
}

// A disabled account must not sign in even with the right password.
func TestAuthenticate_DisabledAccountIsRefused(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustCreate(t, s, "admin@example.com", auth.RoleAdmin)
	u := mustCreate(t, s, "bob@example.com", auth.RoleUser)

	off := true
	if _, err := s.Update(ctx, u.ID, UpdateRequest{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, "bob@example.com", "a-sufficiently-long-passphrase"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled account signed in: %v", err)
	}
}

func TestSessions(t *testing.T) {
	s, setClock := newStore(t)
	ctx := context.Background()
	u := mustCreate(t, s, "alice@example.com", auth.RoleAdmin)

	token, sess, err := s.NewSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	if sess.CSRFToken == "" {
		t.Fatal("a session without a CSRF token cannot guard a mutating request")
	}

	got, resolved, err := s.ResolveSession(ctx, token)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.ID != u.ID || resolved.ID != sess.ID {
		t.Fatalf("resolved %s/%s, want %s/%s", got.ID, resolved.ID, u.ID, sess.ID)
	}

	// The principal names the session rather than only the person, so the
	// trail can tell two sign-ins apart.
	p := got.Principal(resolved.ID)
	if p.ID != "user:alice@example.com" || p.TokenID != sess.ID {
		t.Fatalf("principal = %+v", p)
	}
	if p.Role != auth.RoleAdmin || !p.Can(auth.CapAdmin) {
		t.Fatalf("principal lost its role: %+v", p)
	}

	if _, _, err := s.ResolveSession(ctx, "not-a-real-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token: %v, want ErrNotFound", err)
	}

	// Past its expiry the session stops resolving, without anything having to
	// delete the row first.
	setClock(testClock.Add(2 * time.Hour))
	if _, _, err := s.ResolveSession(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session still resolved: %v", err)
	}

	setClock(testClock)
	if err := s.DeleteSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResolveSession(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session still resolved: %v", err)
	}
}

// Rights are re-read per request rather than frozen at sign-in, so switching an
// account off has to take effect on a session it already holds.
func TestUpdate_DisablingEndsLiveSessions(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustCreate(t, s, "admin@example.com", auth.RoleAdmin)
	u := mustCreate(t, s, "bob@example.com", auth.RoleUser)

	token, _, err := s.NewSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	off := true
	if _, err := s.Update(ctx, u.ID, UpdateRequest{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResolveSession(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a disabled account kept browsing: %v", err)
	}
}

// A password change is often made because the old one is believed to have
// leaked. Leaving live sessions behind would defeat that.
func TestSetPassword_EndsLiveSessions(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	u := mustCreate(t, s, "alice@example.com", auth.RoleAdmin)

	token, _, err := s.NewSession(ctx, u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassword(ctx, u.ID, "a-brand-new-long-passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ResolveSession(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session survived a password change: %v", err)
	}
	if _, err := s.Authenticate(ctx, "alice@example.com", "a-brand-new-long-passphrase"); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
}

// Locking everyone out of administration is not an edit anyone means to make.
func TestLastAdminIsProtected(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	admin := mustCreate(t, s, "admin@example.com", auth.RoleAdmin)
	mustCreate(t, s, "viewer@example.com", auth.RoleUser)

	viewer := auth.RoleUser
	if _, err := s.Update(ctx, admin.ID, UpdateRequest{Role: &viewer}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demoting the last admin: %v, want ErrLastAdmin", err)
	}
	off := true
	if _, err := s.Update(ctx, admin.ID, UpdateRequest{Disabled: &off}); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("disabling the last admin: %v, want ErrLastAdmin", err)
	}
	if err := s.Delete(ctx, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("deleting the last admin: %v, want ErrLastAdmin", err)
	}

	// With a second administrator present, the same edits go through.
	second := mustCreate(t, s, "second@example.com", auth.RoleAdmin)
	if _, err := s.Update(ctx, admin.ID, UpdateRequest{Role: &viewer}); err != nil {
		t.Fatalf("demoting one of two admins: %v", err)
	}
	if err := s.Delete(ctx, second.ID); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("the demotion left one admin; deleting them: %v, want ErrLastAdmin", err)
	}
}

func TestPassword(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Error("a short password must be refused")
	}
	// bcrypt truncates past 72 bytes, which would make two different long
	// passwords equivalent.
	if err := ValidatePassword(string(make([]byte, 73))); err == nil {
		t.Error("an over-long password must be refused rather than truncated")
	}
	if err := ValidatePassword("a-sufficiently-long-passphrase"); err != nil {
		t.Errorf("a reasonable passphrase was refused: %v", err)
	}
}

// A new instance is claimed by whoever registers first, and they become the
// administrator: there is nobody to grant them the role afterwards.
func TestCreateFirst_ClaimsAnEmptyInstance(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	u, err := s.CreateFirst(ctx, " Alice@Example.COM ", "a-sufficiently-long-passphrase", "Alice")
	if err != nil {
		t.Fatalf("CreateFirst: %v", err)
	}
	if u.Email != "alice@example.com" {
		t.Errorf("email = %q, want it normalised", u.Email)
	}
	if u.Role != auth.RoleAdmin {
		t.Errorf("role = %q, want admin", u.Role)
	}
	if len(u.Plugins) != 1 || u.Plugins[0] != auth.Wildcard {
		t.Errorf("plugins = %v, want the wildcard", u.Plugins)
	}
	if _, err := s.Authenticate(ctx, "alice@example.com", "a-sufficiently-long-passphrase"); err != nil {
		t.Errorf("the registered account cannot sign in: %v", err)
	}
}

// Registration claims an unclaimed instance. Once claimed it must refuse, or
// it is a way to mint an administrator on a running host.
func TestCreateFirst_RefusesOnceClaimed(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	if _, err := s.CreateFirst(ctx, "alice@example.com", "a-sufficiently-long-passphrase", ""); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateFirst(ctx, "mallory@example.com", "another-long-passphrase", "")
	if !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("second registration: %v, want ErrAlreadyClaimed", err)
	}

	// And the instance still belongs to the first registrant alone.
	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Email != "alice@example.com" {
		t.Fatalf("accounts = %v, want alice alone", list)
	}
}

// Two browsers reaching an unclaimed instance at the same moment must produce
// one administrator, not two. The emptiness check lives inside the write
// transaction for exactly this.
func TestCreateFirst_ConcurrentClaimsProduceOneAdmin(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()

	const racers = 8
	var wg sync.WaitGroup
	errs := make([]error, racers)
	wg.Add(racers)
	for i := range racers {
		go func() {
			defer wg.Done()
			_, errs[i] = s.CreateFirst(ctx,
				fmt.Sprintf("racer%d@example.com", i), "a-sufficiently-long-passphrase", "")
		}()
	}
	wg.Wait()

	var won int
	for _, err := range errs {
		if err == nil {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d registrations succeeded, want exactly 1", won)
	}
	n, err := s.Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d accounts exist, want 1", n)
	}
}

// A user does everything the integrations exist to do; an administrator
// additionally administers the host. That one line is the whole role model.
func TestRolesSeparateOperatingFromAdministering(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	admin := mustCreate(t, s, "admin@example.com", auth.RoleAdmin)
	user := mustCreate(t, s, "user@example.com", auth.RoleUser)

	up := user.Principal("ses_u")
	for _, c := range []auth.Capability{auth.CapRead, auth.CapPropose, auth.CapApprove} {
		if !up.Can(c) {
			t.Errorf("a user should hold %s", c)
		}
	}
	if up.Can(auth.CapAdmin) {
		t.Error("a user must not hold admin; that is the line between the two roles")
	}

	ap := admin.Principal("ses_a")
	for _, c := range []auth.Capability{auth.CapRead, auth.CapPropose, auth.CapApprove, auth.CapAdmin} {
		if !ap.Can(c) {
			t.Errorf("an administrator should hold %s", c)
		}
	}
	_ = ctx
}
