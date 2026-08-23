package users

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// newStoreWithDB is newStore, plus the handle the audit assertions need. A
// test about what is written into the hash-chained trail has to be able to
// read it, and the trail is not the account store's to expose.
func newStoreWithDB(t *testing.T) (*Store, *sqlite.DB) {
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
	return NewStore(db, func() time.Time { return testClock }), db
}

// openPolicy is a host accepting anybody, with no approval step. It is what
// most of these tests want in the background so the rule under test is the one
// that decides.
func openPolicy() RegistrationPolicy {
	return RegistrationPolicy{Enabled: true}
}

// claim makes the instance claimed, which every registration requires.
func claim(t *testing.T, s *Store) *User {
	t.Helper()
	u, err := s.CreateFirst(context.Background(), "owner@example.com",
		"a-sufficiently-long-passphrase", "Owner")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return u
}

// The attack this whole feature is shaped around. Somebody signs in to Google
// as alice@example.com, and there is already an mcpd account for
// alice@example.com. If the provider identity is adopted by that account, then
// whoever controls that address at Google owns the mcpd account -- and
// addresses get recycled, companies get acquired, and a personal Google
// account can be created for a domain the person no longer works at.
//
// So the address is never a way in. Only a row in user_identities is.
func TestRegister_AnUnlinkedIdentityDoesNotBecomeAnExistingAccount(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)

	existing := mustCreate(t, s, "alice@example.com", auth.RoleAdmin)

	_, err := s.Register(ctx, RegisterRequest{
		Email: "Alice@Example.com",
		Identity: &Identity{
			Provider: ProviderGoogle,
			Subject:  "google-subject-for-somebody-else",
			Email:    "alice@example.com",
		},
		Policy: openPolicy(),
	})
	if !errors.Is(err, ErrAddressTaken) {
		t.Fatalf("register with a taken address = %v; want ErrAddressTaken", err)
	}

	// And having been refused, the identity resolves to nobody -- least of all
	// to the account whose address it carried.
	if _, err := s.UserByIdentity(ctx, ProviderGoogle, "google-subject-for-somebody-else"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refused identity resolves to %v; want ErrNotFound", err)
	}
	// The existing account is untouched: same id, still signing in with its
	// own password.
	again, err := s.ByEmail(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("by email: %v", err)
	}
	if again.ID != existing.ID {
		t.Errorf("the account was replaced: %s became %s", existing.ID, again.ID)
	}
	if !again.HasPassword() {
		t.Error("the existing account lost its password")
	}
}

// Linking is the sanctioned way, and it is performed by the account itself
// while signed in. That direction proves the thing the address cannot.
func TestLinkIdentity_TheAccountAttachesItsOwnProvider(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)
	alice := mustCreate(t, s, "alice@example.com", auth.RoleUser)

	if err := s.LinkIdentity(ctx, "user:alice@example.com", Identity{
		Provider: ProviderGoogle,
		Subject:  "google-alice",
		UserID:   alice.ID,
		Email:    "alice@example.com",
	}); err != nil {
		t.Fatalf("link: %v", err)
	}

	got, err := s.UserByIdentity(ctx, ProviderGoogle, "google-alice")
	if err != nil {
		t.Fatalf("by identity: %v", err)
	}
	if got.ID != alice.ID {
		t.Errorf("identity resolved to %s; want %s", got.ID, alice.ID)
	}

	// One provider account cannot be attached to two mcpd accounts, and one
	// mcpd account cannot hold two of the same provider. Both would make "who
	// signed in" unanswerable.
	bob := mustCreate(t, s, "bob@example.com", auth.RoleUser)
	if err := s.LinkIdentity(ctx, "user:bob@example.com", Identity{
		Provider: ProviderGoogle, Subject: "google-alice", UserID: bob.ID,
	}); !errors.Is(err, ErrIdentityLinked) {
		t.Errorf("stealing a linked identity = %v; want ErrIdentityLinked", err)
	}
	if err := s.LinkIdentity(ctx, "user:alice@example.com", Identity{
		Provider: ProviderGoogle, Subject: "google-alice-second-account", UserID: alice.ID,
	}); !errors.Is(err, ErrIdentityLinked) {
		t.Errorf("a second Google on one account = %v; want ErrIdentityLinked", err)
	}
}

// Claiming an instance is what makes somebody its administrator. A stranger
// completing a flow at Google must never be that: a fresh host reachable from
// the internet would belong to whoever found it first.
func TestRegister_CannotClaimAnUnclaimedInstance(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)

	// Deliberately the most permissive policy there is. Even wide open, an
	// unclaimed instance is not something registration can claim.
	_, err := s.Register(ctx, RegisterRequest{
		Email:    "stranger@example.com",
		Identity: &Identity{Provider: ProviderGoogle, Subject: "google-stranger"},
		Policy:   RegistrationPolicy{Enabled: true},
	})
	if !errors.Is(err, ErrUnclaimed) {
		t.Fatalf("registering on an unclaimed instance = %v; want ErrUnclaimed", err)
	}
	n, err := s.Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d accounts exist; the instance must still be unclaimed", n)
	}

	// The same is true of a password registration; there is one door and it
	// applies the same rule.
	_, err = s.Register(ctx, RegisterRequest{
		Email:    "stranger@example.com",
		Password: "a-sufficiently-long-passphrase",
		Policy:   RegistrationPolicy{Enabled: true},
	})
	if !errors.Is(err, ErrUnclaimed) {
		t.Fatalf("password registration on an unclaimed instance = %v; want ErrUnclaimed", err)
	}
}

// A pending account proves who it is and holds nothing. The assertion is at
// the authorizer, because that is where every decision in the process is made
// -- a test that only checked what the console renders would pass against a
// build whose API let a pending account approve an operation.
func TestPending_AuthenticatesAndHoldsNoCapability(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)

	pending, err := s.Register(ctx, RegisterRequest{
		Email:    "newcomer@example.com",
		Password: "a-sufficiently-long-passphrase",
		Policy:   RegistrationPolicy{Enabled: true, RequireApproval: true},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if pending.Status != StatusPending {
		t.Fatalf("status = %q; want pending", pending.Status)
	}
	// Pending is not disabled. They are different facts and the row says so.
	if pending.Disabled {
		t.Error("a pending registration must not be recorded as disabled")
	}

	// It authenticates: that is how it proves who it is, and it is what lets
	// the console show a screen saying it is waiting.
	signedIn, err := s.Authenticate(ctx, "newcomer@example.com", "a-sufficiently-long-passphrase")
	if err != nil {
		t.Fatalf("a pending account must still be able to authenticate: %v", err)
	}

	authorizer := auth.NewAuthorizer()
	p := signedIn.Principal("ses_test", signedIn.Plugins)
	for _, c := range []auth.Capability{auth.CapRead, auth.CapPropose, auth.CapApprove, auth.CapAdmin} {
		if p.Can(c) {
			t.Errorf("a pending account holds %q", c)
		}
	}
	if d := authorizer.AuthorizeEndpoint(p, "anything"); d.Allowed {
		t.Error("the authorizer let a pending account reach a plugin endpoint")
	}
	if d := authorizer.AuthorizeTool(p, "anything", auth.CapPropose); d.Allowed {
		t.Error("the authorizer let a pending account call a tool")
	}
	if d := authorizer.AuthorizeAdmin(p); d.Allowed {
		t.Error("the authorizer let a pending account administer the host")
	}

	// A session resolves for it, which is the other half of "may authenticate".
	if _, _, err := s.NewSession(ctx, pending.ID, time.Hour); err != nil {
		t.Fatalf("new session: %v", err)
	}
}

// Approving is a privilege grant: the moment somebody gains the ability to do
// anything here. It belongs in the hash-chained trail, naming who decided, and
// written by the same transaction as the grant -- an entry that could be
// committed separately from the change would be a record that can disagree
// with the database.
func TestApproveRegistration_IsAuditedWithTheActingAdministrator(t *testing.T) {
	ctx := context.Background()
	s, db := newStoreWithDB(t)
	claim(t, s)

	pending, err := s.Register(ctx, RegisterRequest{
		Email:    "newcomer@example.com",
		Password: "a-sufficiently-long-passphrase",
		Policy:   RegistrationPolicy{Enabled: true, RequireApproval: true},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	approved, err := s.ApproveRegistration(ctx, "user:owner@example.com", pending.ID, nil)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.Status != StatusActive {
		t.Errorf("status after approval = %q; want active", approved.Status)
	}
	if !approved.Principal("ses", approved.Plugins).Can(auth.CapRead) {
		t.Error("an approved account still holds nothing")
	}

	entry := auditEntry(t, db, "account.approved")
	if entry.actor != "user:owner@example.com" {
		t.Errorf("audit actor = %q; want the administrator who approved", entry.actor)
	}
	if entry.subject != "newcomer@example.com" {
		t.Errorf("audit subject = %q; want the account approved", entry.subject)
	}
	if !strings.Contains(entry.detail, pending.ID) {
		t.Errorf("audit detail %q does not name the account", entry.detail)
	}
	// The chain still verifies, which is what makes the entry evidence rather
	// than a log line.
	if _, err := sqlite.NewAuditStore(db).VerifyChain(ctx); err != nil {
		t.Errorf("audit chain: %v", err)
	}

	// Approving twice grants once. The second is refused by the condition in
	// the WHERE clause rather than by a read before it, so two administrators
	// racing produce one grant and one refusal.
	if _, err := s.ApproveRegistration(ctx, "user:owner@example.com", pending.ID, nil); !errors.Is(err, ErrNotPending) {
		t.Errorf("approving twice = %v; want ErrNotPending", err)
	}
	if n := auditCount(t, db, "account.approved"); n != 1 {
		t.Errorf("%d approval entries; want exactly one", n)
	}
}

// A rejection removes the row and keeps the record. The address is free again,
// and what happened is still answerable.
func TestRejectRegistration_RemovesTheAccountAndKeepsTheRecord(t *testing.T) {
	ctx := context.Background()
	s, db := newStoreWithDB(t)
	claim(t, s)

	pending, err := s.Register(ctx, RegisterRequest{
		Email:    "newcomer@example.com",
		Password: "a-sufficiently-long-passphrase",
		Policy:   RegistrationPolicy{Enabled: true, RequireApproval: true},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := s.RejectRegistration(ctx, "user:owner@example.com", pending.ID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, err := s.ByID(ctx, pending.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the rejected account is still there: %v", err)
	}
	if entry := auditEntry(t, db, "account.rejected"); entry.actor != "user:owner@example.com" {
		t.Errorf("audit actor = %q; want the administrator who rejected", entry.actor)
	}
}

// A rejection that left the identity behind would reserve the provider account
// against the person ever asking again, and against an administrator making
// the account deliberately.
func TestRejectRegistration_FreesTheProviderAccountAndTheAddress(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)

	policy := RegistrationPolicy{Enabled: true, RequireApproval: true}
	first, err := s.Register(ctx, RegisterRequest{
		Email:    "newcomer@example.com",
		Identity: &Identity{Provider: ProviderGoogle, Subject: "google-newcomer"},
		Policy:   policy,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := s.RejectRegistration(ctx, "user:owner@example.com", first.ID); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, err := s.UserByIdentity(ctx, ProviderGoogle, "google-newcomer"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the identity outlived the account it belonged to: %v", err)
	}
	// Asking again works, which is the point.
	if _, err := s.Register(ctx, RegisterRequest{
		Email:    "newcomer@example.com",
		Identity: &Identity{Provider: ProviderGoogle, Subject: "google-newcomer"},
		Policy:   policy,
	}); err != nil {
		t.Errorf("asking again after a rejection was refused: %v", err)
	}
}

// The allow-list is one rule applied at one place, so it cannot hold for the
// form and not for Google.
func TestRegistrationPolicy_TheAllowListAppliesToBothDoors(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)

	policy := RegistrationPolicy{Enabled: true, AllowedDomains: []string{"corp.com"}}

	for _, tc := range []struct {
		name string
		req  RegisterRequest
	}{
		{
			name: "password",
			req: RegisterRequest{
				Email:    "outsider@elsewhere.example",
				Password: "a-sufficiently-long-passphrase",
				Policy:   policy,
			},
		},
		{
			name: "provider",
			req: RegisterRequest{
				Email:    "outsider@elsewhere.example",
				Identity: &Identity{Provider: ProviderGitHub, Subject: "12345"},
				Policy:   policy,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Register(ctx, tc.req); !errors.Is(err, ErrDomainNotAllowed) {
				t.Fatalf("register = %v; want ErrDomainNotAllowed", err)
			}
		})
	}

	// And an address inside the list is accepted through both.
	if _, err := s.Register(ctx, RegisterRequest{
		Email: "insider@corp.com",
		Identity: &Identity{
			Provider: ProviderGitHub, Subject: "12345", Email: "insider@corp.com",
		},
		Policy: policy,
	}); err != nil {
		t.Fatalf("an allowed domain was refused: %v", err)
	}
}

func TestRegistrationPolicy_Allows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy RegistrationPolicy
		email  string
		want   error
	}{
		{
			name:   "the zero value accepts nothing",
			policy: RegistrationPolicy{},
			email:  "anyone@example.com",
			want:   ErrRegistrationClosed,
		},
		{
			name:   "no list means any address",
			policy: RegistrationPolicy{Enabled: true},
			email:  "anyone@example.com",
		},
		{
			name:   "a leading @ is what people type",
			policy: RegistrationPolicy{Enabled: true, AllowedDomains: []string{"@corp.com"}},
			email:  "someone@corp.com",
		},
		{
			name:   "a subdomain is not the domain",
			policy: RegistrationPolicy{Enabled: true, AllowedDomains: []string{"corp.com"}},
			email:  "someone@evil.corp.com.attacker.example",
			want:   ErrDomainNotAllowed,
		},
		{
			name:   "case does not decide",
			policy: RegistrationPolicy{Enabled: true, AllowedDomains: []string{"Corp.COM"}},
			email:  "someone@corp.com",
		},
		{
			name:   "an address with no domain is outside every list",
			policy: RegistrationPolicy{Enabled: true, AllowedDomains: []string{"corp.com"}},
			email:  "nonsense",
			want:   ErrDomainNotAllowed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.policy.Allows(tc.email); !errors.Is(err, tc.want) {
				t.Errorf("Allows(%q) = %v; want %v", tc.email, err, tc.want)
			}
		})
	}
}

// An account with no password of its own cannot be signed in to with one, and
// the refusal is by name rather than by a comparison happening not to match.
func TestAuthenticate_AnSSOOnlyAccountRefusesEveryPassword(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)

	u, err := s.Register(ctx, RegisterRequest{
		Email:    "sso-only@example.com",
		Identity: &Identity{Provider: ProviderGoogle, Subject: "google-sso-only"},
		Policy:   openPolicy(),
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if u.HasPassword() {
		t.Fatal("an account registered through a provider reports a password")
	}
	if u.PasswordHash != NoPassword {
		t.Fatalf("password_hash = %q; want the sentinel", u.PasswordHash)
	}

	// The sentinel itself is the obvious thing to try, and the empty string is
	// the other. Neither is a password, and neither gets in.
	for _, attempt := range []string{NoPassword, "", "a-sufficiently-long-passphrase", "!"} {
		if _, err := s.Authenticate(ctx, "sso-only@example.com", attempt); !errors.Is(err, ErrNoPassword) {
			t.Errorf("Authenticate with %q = %v; want ErrNoPassword", attempt, err)
		}
	}

	// An administrator may still give the account a password, which is how
	// somebody recovers when a provider goes away.
	if err := s.SetPassword(ctx, u.ID, "a-brand-new-passphrase"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if _, err := s.Authenticate(ctx, "sso-only@example.com", "a-brand-new-passphrase"); err != nil {
		t.Errorf("after a password was set, signing in failed: %v", err)
	}
}

// Unlinking the only way in is a deletion wearing the appearance of an edit,
// and the person doing it is usually the one who would be locked out.
func TestUnlinkIdentity_RefusesTheOnlyWayIn(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)

	u, err := s.Register(ctx, RegisterRequest{
		Email:    "sso-only@example.com",
		Identity: &Identity{Provider: ProviderGoogle, Subject: "google-sso-only"},
		Policy:   openPolicy(),
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := s.UnlinkIdentity(ctx, "user:sso-only@example.com", u.ID, ProviderGoogle); !errors.Is(err, ErrLastCredential) {
		t.Fatalf("unlinking the only credential = %v; want ErrLastCredential", err)
	}

	// With a password in place it is an ordinary edit.
	if err := s.SetPassword(ctx, u.ID, "a-sufficiently-long-passphrase"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if err := s.UnlinkIdentity(ctx, "user:sso-only@example.com", u.ID, ProviderGoogle); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if _, err := s.UserByIdentity(ctx, ProviderGoogle, "google-sso-only"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the identity still resolves after being unlinked: %v", err)
	}
}

// Registration is off unless somebody turned it on, and this is the store's
// half of that: the zero policy refuses whatever else is true.
func TestRegister_RefusedWhenRegistrationIsClosed(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)

	for _, tc := range []struct {
		name string
		req  RegisterRequest
	}{
		{"password", RegisterRequest{
			Email:    "newcomer@example.com",
			Password: "a-sufficiently-long-passphrase",
		}},
		{"provider", RegisterRequest{
			Email:    "newcomer@example.com",
			Identity: &Identity{Provider: ProviderGoogle, Subject: "google-newcomer"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Register(ctx, tc.req); !errors.Is(err, ErrRegistrationClosed) {
				t.Fatalf("register = %v; want ErrRegistrationClosed", err)
			}
		})
	}
}

// The password door proves nothing about the address, so it waits whatever the
// approval setting says.
//
// The bug this exists for: with registration on, approval off and an allow-list
// of corp.com -- three switches a settings form presents as independent -- any
// anonymous caller could create an *active* account for any address at
// corp.com and walk in holding read, propose and approve. The allow-list means
// "who may have an account" through a provider that checked the address, and
// only "what may be typed" through a form that did not.
func TestRegister_ThePasswordDoorAlwaysWaits(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)

	// The dangerous combination, spelled out: open, no approval step, and a
	// domain somebody would want to impersonate.
	policy := RegistrationPolicy{
		Enabled: true, RequireApproval: false, AllowedDomains: []string{"corp.com"},
	}

	typed, err := s.Register(ctx, RegisterRequest{
		Email:    "boss@corp.com",
		Password: "a-sufficiently-long-passphrase",
		Policy:   policy,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if typed.Status != StatusPending {
		t.Fatalf("status = %q; an address nobody checked must wait", typed.Status)
	}
	p := typed.Principal("ses", typed.Plugins)
	for _, c := range []auth.Capability{auth.CapRead, auth.CapPropose, auth.CapApprove} {
		if p.Can(c) {
			t.Errorf("an unchecked address walked in holding %q", c)
		}
	}

	// A provider checked the address, so the same policy lets it straight in.
	proved, err := s.Register(ctx, RegisterRequest{
		Email: "someone@corp.com",
		Identity: &Identity{
			Provider: ProviderGoogle, Subject: "google-someone", Email: "someone@corp.com",
		},
		Policy: policy,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if proved.Status != StatusActive {
		t.Errorf("status = %q; a proved address may skip the queue when the "+
			"operator turned approval off", proved.Status)
	}
}

func TestRegistrationPolicy_StatusFor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy RegistrationPolicy
		proved bool
		want   Status
	}{
		{"proved, approval off", RegistrationPolicy{Enabled: true}, true, StatusActive},
		{"proved, approval on", RegistrationPolicy{Enabled: true, RequireApproval: true}, true, StatusPending},
		{"unproved, approval off", RegistrationPolicy{Enabled: true}, false, StatusPending},
		{"unproved, approval on", RegistrationPolicy{Enabled: true, RequireApproval: true}, false, StatusPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.policy.StatusFor(tc.proved); got != tc.want {
				t.Errorf("StatusFor(%v) = %q; want %q", tc.proved, got, tc.want)
			}
		})
	}
}

// A self-registration reaches nothing until an administrator lists something.
//
// The wildcard was the obvious default and the wrong one: it made approving a
// stranger decide two things at once while presenting itself as one -- whether
// they may have an account, and what they may reach.
func TestRegister_GrantsNoPluginAccess(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)

	u, err := s.Register(ctx, RegisterRequest{
		Email:    "newcomer@example.com",
		Identity: &Identity{Provider: ProviderGoogle, Subject: "google-newcomer"},
		Policy:   openPolicy(),
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(u.Plugins) != 0 {
		t.Fatalf("plugins = %v; a self-registered account starts reaching nothing", u.Plugins)
	}

	// Active, so it holds its capabilities -- and still reaches no
	// integration, which is a grant an administrator makes deliberately.
	p := u.Principal("ses", u.Plugins)
	if !p.Can(auth.CapRead) {
		t.Error("an approved account holds no capabilities at all")
	}
	if p.CanAccessPlugin("echo") || p.CanAccessPlugin(auth.Wildcard) {
		t.Error("a self-registered account reaches an integration nobody granted it")
	}
}

// A provider's display name is not something anybody here typed, and the rules
// about what this host will render are met by real names: an emoji joined with
// U+200D, or a name carrying a bidirectional mark. Refusing the registration
// over one would make an account impossible for that person while the browser
// said the provider did not finish.
func TestRegister_DropsAProviderNameThisHostWillNotRender(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	claim(t, s)

	for _, tc := range []struct {
		name string
		from string
	}{
		{"an emoji with a zero-width joiner", "Alice \U0001F468‍\U0001F4BB"},
		{"a name carrying a right-to-left mark", "‏سارة"},
		{"a name over the length bound", strings.Repeat("n", MaxDisplayNameRunes+1)},
		{"a name with a newline in it", "Alice\nSmith"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			email := "person" + strconv.Itoa(len(tc.name)) + "@example.com"
			u, err := s.Register(ctx, RegisterRequest{
				Email:       email,
				DisplayName: tc.from,
				Identity: &Identity{
					Provider: ProviderGitHub, Subject: "github-" + email, Email: email,
				},
				Policy: openPolicy(),
			})
			if err != nil {
				t.Fatalf("a provider name this host will not render refused the account: %v", err)
			}
			if u.DisplayName != "" {
				t.Errorf("display name = %q; an unusable one is dropped", u.DisplayName)
			}
			// And the account renders as its address, which is what an account
			// with no name has always done.
			if u.Name() != email {
				t.Errorf("Name() = %q; want the address", u.Name())
			}
		})
	}

	// A name somebody typed is still refused with a reason: they can see the
	// field and fix it.
	if _, err := s.Register(ctx, RegisterRequest{
		Email:       "typed@example.com",
		Password:    "a-sufficiently-long-passphrase",
		DisplayName: "Typed\nName",
		Policy:      openPolicy(),
	}); err == nil {
		t.Error("a name typed into the form was accepted with a control character in it")
	}
}

// The last-administrator guard counts administrators who can administer.
//
// The bug this exists for: the guard predates `status` and counted a pending
// account's role. A pending administrator holds no capability, so if the only
// real one then demoted themselves the guard permitted it -- leaving a host
// with nobody holding admin, and nobody able to approve the pending account
// the guard had counted. There is no way back from that from inside the
// dashboard.
func TestGuardLastAdmin_DoesNotCountAPendingAdministrator(t *testing.T) {
	ctx := context.Background()
	s, _ := newStoreWithDB(t)
	owner := claim(t, s)

	pending, err := s.Register(ctx, RegisterRequest{
		Email:    "newcomer@example.com",
		Password: "a-sufficiently-long-passphrase",
		Policy:   RegistrationPolicy{Enabled: true, RequireApproval: true},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Promoted while still waiting. Permitted -- the role is a grant somebody
	// may make in advance -- and it must not register as the host gaining an
	// administrator.
	admin := auth.RoleAdmin
	if _, err := s.Update(ctx, pending.ID, UpdateRequest{Role: &admin}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	promoted, err := s.ByID(ctx, pending.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if promoted.Status != StatusPending {
		t.Fatalf("status = %q; promoting must not approve", promoted.Status)
	}
	if promoted.Principal("ses", promoted.Plugins).Can(auth.CapAdmin) {
		t.Fatal("a pending account holds admin")
	}

	// Now the only real administrator tries to edit themselves out. Each of
	// the three ways has to be refused.
	user := auth.RoleUser
	if _, err := s.Update(ctx, owner.ID, UpdateRequest{Role: &user}); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("demoting the last real administrator = %v; want ErrLastAdmin", err)
	}
	disabled := true
	if _, err := s.Update(ctx, owner.ID, UpdateRequest{Disabled: &disabled}); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("disabling the last real administrator = %v; want ErrLastAdmin", err)
	}
	if err := s.Delete(ctx, owner.ID); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("deleting the last real administrator = %v; want ErrLastAdmin", err)
	}

	// Once the pending administrator is approved it counts, and the owner can
	// stand down.
	if _, err := s.ApproveRegistration(ctx, "user:owner@example.com", pending.ID, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, err := s.Update(ctx, owner.ID, UpdateRequest{Role: &user}); err != nil {
		t.Errorf("with a second real administrator, standing down was refused: %v", err)
	}
}

// --- audit helpers ---------------------------------------------------------

type auditRow struct {
	actor   string
	subject string
	detail  string
}

func auditEntry(t *testing.T, db *sqlite.DB, kind string) auditRow {
	t.Helper()
	var row auditRow
	err := db.Reader().QueryRowContext(context.Background(), `
		SELECT actor, COALESCE(plugin,''), COALESCE(detail_json,'{}')
		  FROM audit_events WHERE kind = ? ORDER BY seq DESC LIMIT 1`, kind).
		Scan(&row.actor, &row.subject, &row.detail)
	if err != nil {
		t.Fatalf("no %s entry in the audit trail: %v", kind, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(row.detail), &decoded); err != nil {
		t.Fatalf("%s detail is not JSON: %v", kind, err)
	}
	return row
}

func auditCount(t *testing.T, db *sqlite.DB, kind string) int {
	t.Helper()
	var n int
	if err := db.Reader().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_events WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", kind, err)
	}
	return n
}
