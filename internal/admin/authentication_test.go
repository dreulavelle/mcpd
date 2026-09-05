package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/sso"
	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// fakeIdentities stands in for the account store, for the same reason
// fakeAccounts does: what these tests are about is the HTTP layer -- which
// route needs what, and what a refusal looks like from a browser. The rules
// themselves are tested where they live, against a real database.
type fakeIdentities struct {
	registerErr error
	registered  *users.User

	pending  []*users.User
	approved []string
	rejected []string
	actor    string

	// What the collision and invitation branches see. byEmail is the account
	// an address resolves to, linked what it already signs in with, and
	// inviteErr what claiming an invitation answers -- ErrNotFound by default,
	// which is every account that was not invited.
	byEmail    *users.User
	byEmailErr error
	linked     []users.Identity
	linkedErr  error
	invited    *users.User
	inviteErr  error

	// The offer, and what happened to it. The binding is kept because these
	// tests care that it is compared at all: a fake that ignored it would
	// assert nothing about the one thing that makes an offer useless to a
	// browser it was not made to.
	offered      *users.PendingLink
	offerToken   string
	offerBinding string
	offerErr     error
	discarded    int
}

func (f *fakeIdentities) Register(context.Context, users.RegisterRequest) (*users.User, error) {
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	return f.registered, nil
}

func (f *fakeIdentities) UserByIdentity(context.Context, users.Provider, string) (*users.User, error) {
	return nil, users.ErrNotFound
}

func (f *fakeIdentities) IdentitiesFor(context.Context, string) ([]users.Identity, error) {
	return f.linked, f.linkedErr
}

func (f *fakeIdentities) ByEmail(context.Context, string) (*users.User, error) {
	if f.byEmailErr != nil {
		return nil, f.byEmailErr
	}
	if f.byEmail == nil {
		return nil, users.ErrNotFound
	}
	return f.byEmail, nil
}

func (f *fakeIdentities) ClaimInvite(context.Context, users.Identity) (*users.User, error) {
	if f.inviteErr != nil {
		return nil, f.inviteErr
	}
	if f.invited == nil {
		return nil, users.ErrNotFound
	}
	return f.invited, nil
}

func (f *fakeIdentities) OfferLink(_ context.Context, link users.PendingLink, binding string) (string, error) {
	if f.offerErr != nil {
		return "", f.offerErr
	}
	if binding == "" {
		return "", errors.New("an offered link needs a browser to be bound to")
	}
	f.offered = &link
	f.offerBinding = binding
	if f.offerToken == "" {
		f.offerToken = "an-offer-token"
	}
	return f.offerToken, nil
}

// held reports whether this pair names the live offer.
func (f *fakeIdentities) held(token, binding string) bool {
	return f.offered != nil && token != "" && token == f.offerToken &&
		binding != "" && binding == f.offerBinding
}

func (f *fakeIdentities) PendingLinkFor(_ context.Context, token, binding string) (*users.PendingLinkView, error) {
	if !f.held(token, binding) {
		return nil, users.ErrNotFound
	}
	return &users.PendingLinkView{Provider: f.offered.Provider, Email: f.offered.Email}, nil
}

func (f *fakeIdentities) ClaimPendingLink(_ context.Context, token, binding, password string) (*users.User, error) {
	if !f.held(token, binding) {
		return nil, users.ErrNotFound
	}
	if password != "a-sufficiently-long-passphrase" {
		return nil, users.ErrInvalidCredentials
	}
	return f.byEmail, nil
}

func (f *fakeIdentities) DiscardPendingLink(_ context.Context, token, binding string) error {
	f.discarded++
	if f.held(token, binding) {
		f.offered = nil
	}
	return nil
}

func (f *fakeIdentities) LinkIdentity(context.Context, string, users.Identity) error { return nil }

func (f *fakeIdentities) UnlinkIdentity(context.Context, string, string, users.Provider) error {
	return nil
}

func (f *fakeIdentities) PendingRegistrations(context.Context) ([]*users.User, error) {
	return f.pending, nil
}

func (f *fakeIdentities) ApproveRegistration(_ context.Context, actor, id string, groupIDs []string) (*users.User, error) {
	f.actor = actor
	f.approved = append(f.approved, id)
	return &users.User{ID: id, Email: "newcomer@example.com", Status: users.StatusActive}, nil
}

func (f *fakeIdentities) RejectRegistration(_ context.Context, actor, id string) error {
	f.actor = actor
	f.rejected = append(f.rejected, id)
	return nil
}

// A pending account signs in and then holds nothing, and the assertion is at
// the API rather than in the console. A test that only checked what the
// dashboard renders would pass against a build whose endpoints happily served
// an account nobody has approved.
func TestPendingAccount_IsRefusedByEveryEndpointItReaches(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.Status = users.StatusPending
	s := newTestServer(t, accounts)

	// The session endpoint answers, because that is how the console learns to
	// draw a screen saying the account is waiting.
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/session = %d; a pending account must be able to see it is waiting", w.Code)
	}
	var view sessionResponse
	if err := json.NewDecoder(w.Body).Decode(&view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Status != string(users.StatusPending) {
		t.Errorf("status = %q; want pending", view.Status)
	}

	// Everything else is refused, including the routes that only need read --
	// and the account's role is admin, so nothing here is deciding on the
	// strength of a small role.
	for _, path := range []string{
		"/api/plugins", "/api/operations", "/api/settings", "/api/users",
		"/api/health", "/api/audit", "/api/account/identities",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d; want 403 for an account nobody has approved", path, w.Code)
		}
	}

	// The control. The same account, the same routes, once somebody has
	// approved it -- so the refusals above are the pending status and not the
	// test server being short of something.
	accounts.user.Status = users.StatusActive
	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code == http.StatusForbidden {
		t.Error("the same account is refused after approval; the refusals above prove nothing")
	}
}

// Off unless somebody turned it on, and the handler says so without saying
// which of the two reasons applies -- neither is an anonymous caller's
// business.
func TestRegister_RefusedWhenNobodyHasOpenedRegistration(t *testing.T) {
	accounts := newFakeAccounts()
	s := NewServer(testOptions(accounts, &fakeIdentities{
		registerErr: users.ErrRegistrationClosed,
	}))

	res := post(t, s, "/api/register",
		`{"email":"newcomer@example.com","password":"a-sufficiently-long-passphrase"}`)
	if res.Code != http.StatusForbidden {
		t.Fatalf("POST /api/register = %d; want 403", res.Code)
	}
	if body := res.Body.String(); strings.Contains(body, "claim") {
		t.Errorf("the refusal says why: %s", body)
	}
}

// The registration policy is nil when nothing wired one, and the answer to
// "what does this host accept" is then nothing at all rather than everything.
func TestRegistrationPolicy_DefaultsToAcceptingNothing(t *testing.T) {
	s := NewServer(testOptions(newFakeAccounts(), &fakeIdentities{}))
	policy := s.registrationPolicy(context.Background())
	if policy.Enabled {
		t.Error("a host that was told nothing accepts registrations")
	}
	if err := policy.Allows("anyone@example.com"); !errors.Is(err, users.ErrRegistrationClosed) {
		t.Errorf("Allows = %v; want ErrRegistrationClosed", err)
	}
}

// The signed-out page is told what it may offer and nothing else.
func TestAuthOptions_SaysWhatIsAvailableAndNoMore(t *testing.T) {
	s := NewServer(testOptions(newFakeAccounts(), &fakeIdentities{}))

	req := httptest.NewRequest(http.MethodGet, "/api/auth/options", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/auth/options = %d; want 200", w.Code)
	}
	var body authOptionsResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Providers) != 0 {
		t.Errorf("providers = %v; want none on a host with no SSO wired", body.Providers)
	}
	if body.Registration {
		t.Error("registration is offered on a host that accepts none")
	}
}

// A callback carrying a state this host is not waiting for ends as a redirect
// with a reason, not as a bare error page on this host's own domain with no
// way back to the sign-in form.
func TestSSOCallback_AForgedStateIsRefusedAndSentBack(t *testing.T) {
	opts := testOptions(newFakeAccounts(), &fakeIdentities{})
	opts.SSO = newTestSSO(t)
	s := NewServer(opts)

	req := httptest.NewRequest(http.MethodGet,
		"/api/auth/sso/google/callback?code=abc&state=a-state-nobody-issued", nil)
	req.AddCookie(&http.Cookie{Name: ssoCookie, Value: "a-binding-nobody-issued"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("callback = %d; want a redirect", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/?sso_error=state" {
		t.Errorf("Location = %q; want the sign-in page with a reason", got)
	}
	// No session was issued, which is the thing that actually matters.
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			t.Error("a forged state produced a session")
		}
		if c.Name == ssoCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	// The binding is cleared on the way out. It has to happen before the
	// redirect is written -- a Set-Cookie added after the header is flushed is
	// never sent, which would leave the browser holding a binding for a state
	// that has already been consumed.
	if !cleared {
		t.Error("the binding cookie survived the callback")
	}
}

// Pressing cancel retires the row that flow created.
//
// The bug this exists for: the provider-error branch returned before Complete
// ran, so Claim never happened. A cancelled flow left its state unconsumed and
// usable for the rest of its ten minutes by anybody holding the state and the
// binding, and the branch answered for a provider the state was not issued
// for. Single use has to mean used or abandoned, not used or lingering.
func TestSSOCallback_CancellingRetiresTheStateAndReturnsWhereItStarted(t *testing.T) {
	opts := testOptions(newFakeAccounts(), &fakeIdentities{})
	svc := newTestSSO(t)
	opts.SSO = svc
	s := NewServer(opts)

	state, binding := startGitHubFlow(t, svc, "/profile")

	// Cancelling lands back where the flow began, which is how somebody who
	// cancelled a link from their profile finds the button again.
	w := cancelAt(t, s, "github", state, binding)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("cancel = %d; want a redirect", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/profile?sso_error=provider" {
		t.Errorf("Location = %q; want the page the flow started from", got)
	}

	// And the state is spent. A second cancel finds nothing to retire, so it
	// cannot say where the flow began and falls back to the sign-in page --
	// which is exactly the difference between a live row and a retired one.
	again := cancelAt(t, s, "github", state, binding)
	if got := again.Header().Get("Location"); got != "/?sso_error=provider" {
		t.Errorf("Location = %q; the cancelled state was still there", got)
	}
}

// Cancelling one flow must not consume somebody else's row, and must not
// become a bare error page either -- the person pressed cancel, and answering
// with a 500 on this host's own domain is the outcome the redirect exists to
// avoid.
func TestSSOCallback_CancellingCannotSpendAnotherFlowsState(t *testing.T) {
	opts := testOptions(newFakeAccounts(), &fakeIdentities{})
	svc := newTestSSO(t)
	opts.SSO = svc
	s := NewServer(opts)

	for _, tc := range []struct {
		name     string
		provider string
		binding  func(string) string
	}{
		{
			name:     "at another provider's callback",
			provider: "google",
			binding:  func(b string) string { return b },
		},
		{
			name:     "without the browser it was issued to",
			provider: "github",
			binding:  func(string) string { return "" },
		},
		{
			name:     "with somebody else's binding",
			provider: "github",
			binding:  func(string) string { return "a-binding-nobody-issued" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, binding := startGitHubFlow(t, svc, "/profile")

			w := cancelAt(t, s, tc.provider, state, tc.binding(binding))
			if w.Code != http.StatusSeeOther {
				t.Fatalf("cancel = %d; want a redirect, never an error page", w.Code)
			}
			// Nothing was retired, so there is nothing to say where the flow
			// began: the sign-in page, not the state's return_to.
			if got := w.Header().Get("Location"); got != "/?sso_error=provider" {
				t.Errorf("Location = %q; a state it could not claim decided the return", got)
			}

			// The row survived. The real browser can still retire it, and
			// getting its own return_to back is the proof.
			real := cancelAt(t, s, "github", state, binding)
			if got := real.Header().Get("Location"); got != "/profile?sso_error=provider" {
				t.Errorf("Location = %q; the flow's own state had been consumed", got)
			}
		})
	}
}

// Approving is administrative, and the store is told which administrator did
// it -- the audit entry is written from that name, inside the transaction that
// performs the grant.
func TestApproveRegistration_NamesTheActingAdministrator(t *testing.T) {
	accounts := newFakeAccounts()
	identities := &fakeIdentities{}
	s := NewServer(testOptions(accounts, identities))

	req := httptest.NewRequest(http.MethodPost, "/api/registrations/usr_9/approve", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	req.Header.Set(csrfHeader, accounts.session.CSRFToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("approve = %d; want 200: %s", w.Code, w.Body.String())
	}
	if len(identities.approved) != 1 || identities.approved[0] != "usr_9" {
		t.Errorf("approved = %v; want [usr_9]", identities.approved)
	}
	if identities.actor != "user:"+accounts.user.Email {
		t.Errorf("actor = %q; want the signed-in administrator", identities.actor)
	}
}

// Approving decides who may do anything here, so it is an administrator's.
func TestRegistrationQueue_TakesAnAdministrator(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.RoleID = auth.RoleOperator
	s := NewServer(testOptions(accounts, &fakeIdentities{}))

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/registrations"},
		{http.MethodPost, "/api/registrations/usr_9/approve"},
		{http.MethodPost, "/api/registrations/usr_9/reject"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
		req.Header.Set(csrfHeader, accounts.session.CSRFToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d; want 403 for a non-administrator", tc.method, tc.path, w.Code)
		}
	}
}

// testOptions is newTestServer's options, plus the identity store. Split out
// because most tests in this package want the plain server and these want one
// that also knows about registration.
func testOptions(accounts Accounts, identities Registrations) Options {
	return Options{
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts:   accounts,
		Identities: identities,
		Version:    "test",
		Health:     observability.NewHealthRegistry(time.Second),
	}
}

// newTestSSO builds a real flow runner over a real state store, because the
// thing under test is what happens to a state this host never issued -- and a
// fake that answered "no such state" would be asserting itself.
func newTestSSO(t *testing.T) *sso.Service {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "sso.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return sso.NewService(sso.Options{
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		States:       sso.NewStateStore(db, time.Now),
		RedirectBase: func() string { return "https://mcpd.example.com" },
		Providers: func(context.Context) []sso.Config {
			return []sso.Config{
				{Provider: users.ProviderGoogle, ClientID: "id", ClientSecret: "secret"},
				{Provider: users.ProviderGitHub, ClientID: "id", ClientSecret: "secret"},
			}
		},
	})
}

// startGitHubFlow issues a real state and returns what a browser would carry
// back. GitHub rather than Google because its authorization endpoint is a
// constant: starting a Google flow fetches a discovery document, and a test
// about what a callback does should not depend on reaching accounts.google.com.
func startGitHubFlow(t *testing.T, svc *sso.Service, returnTo string) (state, binding string) {
	t.Helper()
	started, err := svc.Start(context.Background(), sso.StartRequest{
		Provider: users.ProviderGitHub,
		Purpose:  sso.PurposeSignIn,
		ReturnTo: returnTo,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	u, err := url.Parse(started.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization url: %v", err)
	}
	return u.Query().Get("state"), started.Binding
}

// cancelAt replays what a provider sends when somebody presses cancel.
func cancelAt(t *testing.T, s *Server, provider, state, binding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/api/auth/sso/"+provider+"/callback?error=access_denied&state="+
			url.QueryEscape(state), nil)
	if binding != "" {
		req.AddCookie(&http.Cookie{Name: ssoCookie, Value: binding})
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func post(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}
