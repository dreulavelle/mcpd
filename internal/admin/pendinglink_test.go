package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth/sso"
	"github.com/spoked/mcpd/internal/auth/users"
)

// callbackFor stands in for a completed provider round trip.
//
// The branch under test is the one that runs after Complete has established an
// identity, and driving a real round trip would need a provider to stand up.
// What these assert is the decision and what it writes to the browser, which
// is the part that lives here; the guarded statements underneath are tested
// against a real database in internal/auth/users.
func callbackFor(t *testing.T, s *Server, binding string, identity *sso.Identity) *http.Response {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/auth/sso/google/callback", nil)
	if binding != "" {
		r.AddCookie(&http.Cookie{Name: ssoCookie, Value: binding})
	}
	w := httptest.NewRecorder()
	s.offerLink(w, r, identity)
	return w.Result()
}

func cookieNamed(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func googleIdentity() *sso.Identity {
	return &sso.Identity{
		Provider: users.ProviderGoogle,
		Subject:  "google-subject-1",
		Email:    "alice@example.com",
		Name:     "Alice",
	}
}

// The dead end this exists to remove. An address a password account already
// holds is no longer refused outright: an offer is recorded and the browser is
// sent to a screen that asks for that account's password.
func TestSSOCallback_AnAddressWithAPasswordAccountOffersToLinkRatherThanRefusing(t *testing.T) {
	accounts := newFakeAccounts()
	identities := &fakeIdentities{byEmail: accounts.user}
	accounts.user.PasswordHash = "$2a$12$a-hash-that-is-not-the-sentinel"
	s := NewServer(testOptions(accounts, identities))

	res := callbackFor(t, s, "the-binding", googleIdentity())
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("callback = %d; want a redirect", res.StatusCode)
	}
	if got := res.Header.Get("Location"); got != "/?sso_error=link_password" {
		t.Errorf("Location = %q; want the connect screen", got)
	}
	if identities.offered == nil {
		t.Fatal("no offer was recorded, so the screen has nothing to draw")
	}
	if identities.offered.Subject != "google-subject-1" ||
		identities.offered.UserID != accounts.user.ID {
		t.Errorf("offer = %+v; want the subject seen here against the matching account",
			identities.offered)
	}

	// The offer is worth nothing without the cookie, and a Set-Cookie added
	// after http.Redirect has written the header is never sent. The recorder
	// snapshots its headers at WriteHeader, so this assertion is the ordering.
	for name, want := range map[string]string{
		ssoLinkCookie: identities.offerToken, ssoLinkBindingCookie: identities.offerBinding,
	} {
		c := cookieNamed(res, name)
		if c == nil || c.Value != want {
			t.Fatalf("%s = %+v; want %q, set before the redirect", name, c, want)
		}
		if !c.HttpOnly {
			t.Errorf("%s must be HttpOnly, or a script on the page can read it", name)
		}
		if c.SameSite != http.SameSiteLaxMode {
			t.Errorf("%s SameSite = %v; want Lax", name, c.SameSite)
		}
		if c.Path != ssoCookiePath {
			t.Errorf("%s Path = %q; want the flow's path, not the whole dashboard", name, c.Path)
		}
	}
	// Names of their own. finish() clears mcpd_sso on every exit from the
	// callback, including this one, so an offer bound to it is one no browser
	// can present.
	if binding := cookieNamed(res, ssoCookie); binding == nil || binding.MaxAge >= 0 {
		t.Error("the binding cookie was not retired on the way out")
	}
	if identities.offerBinding == "the-binding" {
		t.Error("the offer was bound to the flow's binding, which this response retires")
	}
	if session := cookieNamed(res, sessionCookie); session != nil && session.Value != "" {
		t.Error("a session was issued before anybody presented a password")
	}
}

// The bug that shipped in 0.19.0. Register applies the registration policy
// before it notices the address is taken, so on a host with sign-ups switched
// off -- the common case -- an account that already existed was told "this
// host isn't accepting new accounts" and never reached the offer. The person
// the offer is written for was the one person who could not get it.
func TestSSOCallback_AnExistingAccountIsOfferedTheLinkWhenRegistrationIsClosed(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.PasswordHash = "$2a$12$a-hash-that-is-not-the-sentinel"
	identities := &fakeIdentities{
		byEmail:     accounts.user,
		registerErr: users.ErrRegistrationClosed,
	}
	s := NewServer(testOptions(accounts, identities))

	// Through completeSignIn rather than offerLink: the bug was in the order
	// of the decisions above the offer, and a test that starts at the offer
	// cannot see it.
	r := httptest.NewRequest(http.MethodGet, "/api/auth/sso/google/callback", nil)
	w := httptest.NewRecorder()
	s.completeSignIn(w, r, &sso.State{
		Provider: users.ProviderGoogle, Purpose: sso.PurposeSignIn, ReturnTo: "/",
	}, googleIdentity())
	res := w.Result()

	if got := res.Header.Get("Location"); got != "/?sso_error=link_password" {
		t.Fatalf("Location = %q; want the connect screen, not a refusal", got)
	}
	if identities.offered == nil {
		t.Fatal("no offer was recorded")
	}
}

// Three arrivals at a taken address, three answers, and which one depends on
// the account rather than on the provider.
func TestSSOCallback_WhatATakenAddressIsToldDependsOnTheAccount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account func(*users.User)
		linked  []users.Identity
		want    string
	}{
		{
			// Nothing to type. Linking is an act by the signed-in account,
			// from the profile page, and telling somebody to use a password
			// the account does not have is an instruction they cannot carry
			// out.
			name:    "no password of its own",
			account: func(u *users.User) { u.PasswordHash = users.NoPassword },
			want:    "/?sso_error=address_taken",
		},
		{
			// disabled is a decision an administrator made, and it is a
			// different sentence from "this address is taken".
			name: "switched off",
			account: func(u *users.User) {
				u.PasswordHash = "$2a$12$a-hash"
				u.Disabled = true
			},
			want: "/?sso_error=disabled",
		},
		{
			// A subject that changed under an address that did not is the
			// reassignment this whole feature exists to notice, so there is
			// no offer to re-link.
			name:    "already connected to a different account at this provider",
			account: func(u *users.User) { u.PasswordHash = "$2a$12$a-hash" },
			linked: []users.Identity{
				{Provider: users.ProviderGoogle, Subject: "a-different-subject"},
			},
			want: "/?sso_error=other_identity",
		},
		{
			// It has no password and has never been signed in to, so both of
			// the other sentences here would send this person to a credential
			// and a page that do not exist for them.
			name: "invited to sign in with a different provider",
			account: func(u *users.User) {
				u.PasswordHash = users.NoPassword
				u.InviteProvider = users.ProviderEntra
			},
			want: "/?sso_error=invite_other_provider",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := newFakeAccounts()
			tc.account(accounts.user)
			identities := &fakeIdentities{byEmail: accounts.user, linked: tc.linked}
			s := NewServer(testOptions(accounts, identities))

			res := callbackFor(t, s, "the-binding", googleIdentity())
			if got := res.Header.Get("Location"); got != tc.want {
				t.Errorf("Location = %q; want %q", got, tc.want)
			}
			if identities.offered != nil {
				t.Error("an offer was recorded for an account that cannot take one")
			}
			if c := cookieNamed(res, ssoLinkCookie); c != nil && c.Value != "" {
				t.Error("a link cookie was set with no offer behind it")
			}
		})
	}
}

// An offer that cannot be written must not leave somebody looking at a screen
// asking for a password against a row that does not exist. Every failure falls
// back to the refusal that was already there.
func TestSSOCallback_AnOfferThatCannotBeRecordedFallsBackToTheRefusal(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.PasswordHash = "$2a$12$a-hash"
	identities := &fakeIdentities{byEmail: accounts.user, offerErr: errors.New("the database is locked")}
	s := NewServer(testOptions(accounts, identities))

	res := callbackFor(t, s, "the-binding", googleIdentity())
	if got := res.Header.Get("Location"); got != "/?sso_error=address_taken" {
		t.Errorf("Location = %q; want the refusal that was already there", got)
	}
	if c := cookieNamed(res, ssoLinkCookie); c != nil && c.Value != "" {
		t.Error("a link cookie was set for an offer that was never written")
	}
}

// The offer's binding comes from the store, so a callback whose own binding
// cookie has already gone -- a browser that dropped it, a proxy that stripped
// it -- still produces a usable offer. It could not before: binding the row to
// a cookie the caller happened to still have is what made the offer depend on
// something this response was about to retire anyway.
func TestSSOCallback_TheOfferDoesNotDependOnTheFlowsBinding(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.PasswordHash = "$2a$12$a-hash"
	identities := &fakeIdentities{byEmail: accounts.user}
	s := NewServer(testOptions(accounts, identities))

	res := callbackFor(t, s, "", googleIdentity())
	if got := res.Header.Get("Location"); got != "/?sso_error=link_password" {
		t.Errorf("Location = %q; want the connect screen", got)
	}
	if identities.offered == nil {
		t.Fatal("no offer was recorded")
	}
	held := jar(res)
	if res := pendingRequest(t, s, http.MethodGet, "", held); res.StatusCode != http.StatusOK {
		t.Errorf("GET pending = %d; want the offer this browser holds", res.StatusCode)
	}
}

// --- the routes that take the offer up ---------------------------------------

// offered puts a server and a browser into the state the screen renders from.
//
// The cookies come out of the callback's own response, so what the tests below
// present is what a browser would actually be holding.
func offered(t *testing.T) (*Server, *fakeAccounts, *fakeIdentities, []*http.Cookie) {
	t.Helper()
	accounts := newFakeAccounts()
	accounts.user.PasswordHash = "$2a$12$a-hash"
	identities := &fakeIdentities{byEmail: accounts.user}
	s := NewServer(testOptions(accounts, identities))
	res := callbackFor(t, s, "the-binding", googleIdentity())
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("offer: %d", res.StatusCode)
	}
	// The flow's own binding cookie is what the browser arrived with, and the
	// callback retires it. Carrying it in is what makes the jar honest: if the
	// offer were bound to it, everything after this would find nothing.
	return s, accounts, identities,
		jar(res, &http.Cookie{Name: ssoCookie, Value: "the-binding"})
}

// The bug the round-one review caught. The offer was bound to the flow's
// binding cookie and finish() retires that cookie in the same response, so a
// real browser ended the callback holding a token it could not use: the row
// was there, the screen was drawn, and every request for it answered "there is
// nothing waiting".
//
// What makes this assertable is that the cookies come from the response rather
// than from the test. The offer's own binding has to be one the callback set,
// and it must not be the flow's.
func TestPendingLink_TheOfferSurvivesTheCookiesTheCallbackRetires(t *testing.T) {
	s, _, _, held := offered(t)

	names := map[string]bool{}
	for _, c := range held {
		names[c.Name] = true
	}
	if names[ssoCookie] {
		t.Error("the flow's binding cookie survived the callback; finish() must retire it")
	}
	if !names[ssoLinkCookie] || !names[ssoLinkBindingCookie] {
		t.Fatalf("the browser is holding %v; want both halves of the offer", names)
	}

	// And the offer is usable with exactly what the browser has left.
	if res := pendingRequest(t, s, http.MethodGet, "", held); res.StatusCode != http.StatusOK {
		t.Fatalf("GET pending = %d with the cookies the callback left; want 200", res.StatusCode)
	}
}

// jar is what a browser would be holding after a response.
//
// Cookies the response retired are dropped, which is the whole point of the
// helper: hand-injecting a cookie name into a follow-up request is how a test
// asserts a flow that no real browser can complete. The bug this exists for is
// exactly that -- the offer was bound to the flow's binding cookie, the same
// response cleared it, and every later request answered "there is nothing
// waiting" while the tests passed.
func jar(res *http.Response, carry ...*http.Cookie) []*http.Cookie {
	held := map[string]*http.Cookie{}
	for _, c := range carry {
		held[c.Name] = c
	}
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(held, c.Name)
			continue
		}
		held[c.Name] = c
	}
	out := make([]*http.Cookie, 0, len(held))
	for _, c := range held {
		out = append(out, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	return out
}

func pendingRequest(t *testing.T, s *Server, method, body string, cookies []*http.Cookie) *http.Response {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, "/api/auth/sso/pending", nil)
	} else {
		r = httptest.NewRequest(method, "/api/auth/sso/pending", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w.Result()
}

// named picks one cookie out of a jar, for a test that wants to corrupt it.
func named(cookies []*http.Cookie, name, value string) []*http.Cookie {
	out := make([]*http.Cookie, 0, len(cookies))
	for _, c := range cookies {
		if c.Name == name {
			if value == "" {
				continue
			}
			out = append(out, &http.Cookie{Name: name, Value: value})
			continue
		}
		out = append(out, c)
	}
	return out
}

// The screen confirms with the server before it draws a password field. A code
// in the address bar is a parameter somebody can type, and drawing the field
// on the strength of one would ask for a password against nothing.
func TestPendingLink_TheScreenIsOnlyDrawnForAnOfferTheServerConfirms(t *testing.T) {
	s, _, _, held := offered(t)

	res := pendingRequest(t, s, http.MethodGet, "", held)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET pending = %d; want the offer this browser holds", res.StatusCode)
	}
	var view pendingLinkResponse
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.Provider != "google" || view.Label != "Google" || view.Email != "alice@example.com" {
		t.Errorf("pending = %+v; want the provider and the account it names", view)
	}

	for _, tc := range []struct {
		name    string
		cookies []*http.Cookie
	}{
		{"no cookies at all", nil},
		{"a token nobody was issued", named(held, ssoLinkCookie, "a-token-nobody-holds")},
		{"another browser's binding", named(held, ssoLinkBindingCookie, "somebody-elses")},
		{"the token with no binding", named(held, ssoLinkBindingCookie, "")},
		{"the binding with no token", named(held, ssoLinkCookie, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := pendingRequest(t, s, http.MethodGet, "", tc.cookies)
			if res.StatusCode != http.StatusNotFound {
				t.Errorf("GET pending = %d; want 404", res.StatusCode)
			}
		})
	}
}

// The password is the proof the provider cannot give, and giving it signs the
// person in. Everything about which account and which subject comes out of the
// row, so there is nothing in the request for a caller to aim elsewhere.
func TestPendingLink_TheRightPasswordConnectsTheProviderAndSignsIn(t *testing.T) {
	s, accounts, _, held := offered(t)

	res := pendingRequest(t, s, http.MethodPost,
		`{"password":"a-sufficiently-long-passphrase"}`, held)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST pending = %d; want a session", res.StatusCode)
	}
	var body sessionResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Email != accounts.user.Email || body.CSRFToken != accounts.session.CSRFToken {
		t.Errorf("session = %+v; want the account the offer named", body)
	}
	if c := cookieNamed(res, sessionCookie); c == nil || c.Value != accounts.token {
		t.Fatalf("session cookie = %+v; want the issued token", c)
	}
	// The offer is spent, and both cookies holding it go with it. Leaving one
	// behind would have the next request present half an offer.
	for _, name := range []string{ssoLinkCookie, ssoLinkBindingCookie} {
		if c := cookieNamed(res, name); c == nil || c.MaxAge >= 0 {
			t.Errorf("%s survived the connection", name)
		}
	}
	if len(accounts.loggedIn) != 1 {
		t.Errorf("the sign-in was not recorded: %v", accounts.loggedIn)
	}
}

// Somebody who signed in as another account in another tab must not end up
// with two live sessions and a cookie naming whichever was written last.
func TestPendingLink_SigningInReplacesAnyExistingSession(t *testing.T) {
	s, accounts, _, held := offered(t)

	res := pendingRequest(t, s, http.MethodPost,
		`{"password":"a-sufficiently-long-passphrase"}`,
		append(held, &http.Cookie{Name: sessionCookie, Value: accounts.token}))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST pending = %d", res.StatusCode)
	}
	if len(accounts.deleted) != 1 || accounts.deleted[0] != accounts.token {
		t.Errorf("ended sessions = %v; want the one this browser was holding", accounts.deleted)
	}
}

// One sentence for every wrong password, and no session. How many attempts are
// left is kept in the row: it would only be useful to somebody who is not the
// account's owner.
func TestPendingLink_AWrongPasswordIsRefusedWithoutASession(t *testing.T) {
	s, _, _, held := offered(t)

	res := pendingRequest(t, s, http.MethodPost, `{"password":"not-the-password"}`, held)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST pending = %d; want 401", res.StatusCode)
	}
	if c := cookieNamed(res, sessionCookie); c != nil && c.Value != "" {
		t.Error("a wrong password produced a session")
	}
	// The offer stays for another try, so a typo does not mean starting the
	// whole round trip again.
	if c := cookieNamed(res, ssoLinkCookie); c != nil && c.MaxAge < 0 {
		t.Error("one wrong password retired the offer")
	}
}

// "Not now" retires the row rather than merely navigating away from it. An
// offer left live is one the next person at that browser is holding, and the
// screen it draws asks for a password.
func TestPendingLink_NotNowRetiresTheOffer(t *testing.T) {
	s, _, identities, held := offered(t)

	res := pendingRequest(t, s, http.MethodDelete, "", held)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE pending = %d; want 204", res.StatusCode)
	}
	if identities.discarded != 1 {
		t.Errorf("discarded %d times; want once", identities.discarded)
	}
	for _, name := range []string{ssoLinkCookie, ssoLinkBindingCookie} {
		if c := cookieNamed(res, name); c == nil || c.MaxAge >= 0 {
			t.Errorf("%s survived being put away", name)
		}
	}
	// What the browser has left after that, which is nothing.
	if res := pendingRequest(t, s, http.MethodGet, "", jar(res, held...)); res.StatusCode != http.StatusNotFound {
		t.Errorf("GET pending = %d after Not now; want 404", res.StatusCode)
	}
}

// The routes are unauthenticated by necessity -- the person is not signed in,
// and the point of them is that they are about to be. What must not follow is
// that they answer for a build with no account store behind them.
func TestPendingLink_RefusedWhenAccountsAreNotConfigured(t *testing.T) {
	s := NewServer(testOptions(newFakeAccounts(), nil))
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		body := ""
		if method == http.MethodPost {
			body = `{"password":"a-sufficiently-long-passphrase"}`
		}
		res := pendingRequest(t, s, method, body, []*http.Cookie{
			{Name: ssoLinkCookie, Value: "a-token"},
			{Name: ssoLinkBindingCookie, Value: "a-binding"},
		})
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s pending = %d; want 503", method, res.StatusCode)
		}
	}
}

// Guard against the fake drifting from the interface the server is wired with.
var _ Registrations = (*fakeIdentities)(nil)
