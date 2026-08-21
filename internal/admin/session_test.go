package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/users"
)

// fakeAccounts is a hand-rolled stand-in rather than a real store, because
// what these tests are about is the HTTP layer: cookies, CSRF, and which
// credential wins. A database would only add setup between the assertion and
// the thing asserted.
type fakeAccounts struct {
	user     *users.User
	session  *users.Session
	token    string
	password string

	deleted  []string
	loggedIn []string
}

func newFakeAccounts() *fakeAccounts {
	return &fakeAccounts{
		user: &users.User{
			ID: "usr_1", Email: "alice@example.com", DisplayName: "Alice",
			Role: auth.RoleAdmin, Plugins: []string{auth.Wildcard},
		},
		session: &users.Session{
			ID: "ses_1", UserID: "usr_1", CSRFToken: "csrf-token-value",
			ExpiresAt: time.Now().Add(time.Hour),
		},
		token:    "session-token-value",
		password: "a-sufficiently-long-passphrase",
	}
}

func (f *fakeAccounts) Authenticate(_ context.Context, email, password string) (*users.User, error) {
	if email != f.user.Email || password != f.password {
		return nil, users.ErrInvalidCredentials
	}
	return f.user, nil
}

func (f *fakeAccounts) RecordLogin(_ context.Context, id string) error {
	f.loggedIn = append(f.loggedIn, id)
	return nil
}

func (f *fakeAccounts) NewSession(context.Context, string, time.Duration) (string, *users.Session, error) {
	return f.token, f.session, nil
}

func (f *fakeAccounts) ResolveSession(_ context.Context, token string) (*users.User, *users.Session, error) {
	if token != f.token {
		return nil, nil, users.ErrNotFound
	}
	return f.user, f.session, nil
}

func (f *fakeAccounts) DeleteSession(_ context.Context, token string) error {
	f.deleted = append(f.deleted, token)
	return nil
}

func (f *fakeAccounts) Create(context.Context, users.CreateRequest) (*users.User, error) {
	return f.user, nil
}
func (f *fakeAccounts) List(context.Context) ([]*users.User, error) {
	return []*users.User{f.user}, nil
}
func (f *fakeAccounts) ByID(context.Context, string) (*users.User, error) { return f.user, nil }
func (f *fakeAccounts) Update(context.Context, string, users.UpdateRequest) (*users.User, error) {
	return f.user, nil
}
func (f *fakeAccounts) SetPassword(context.Context, string, string) error { return nil }
func (f *fakeAccounts) Delete(context.Context, string) error              { return nil }

func newTestServer(t *testing.T, accounts Accounts) *Server {
	t.Helper()
	return NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Version:  "test",
	})
}

func signIn(t *testing.T, s *Server, body string) *http.Response {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w.Result()
}

func TestSignIn_SetsAnHttpOnlyCookieAndReturnsACSRFToken(t *testing.T) {
	accounts := newFakeAccounts()
	s := newTestServer(t, accounts)

	res := signIn(t, s, `{"email":"alice@example.com","password":"a-sufficiently-long-passphrase"}`)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie was set")
	}
	if !cookie.HttpOnly {
		t.Error("the session cookie must be HttpOnly, or a script on the page can read it")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", cookie.SameSite)
	}
	if cookie.Value != accounts.token {
		t.Errorf("cookie carries %q, want the issued token", cookie.Value)
	}

	var body sessionResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// The page cannot read the cookie, so the CSRF token has to arrive here.
	if body.CSRFToken != accounts.session.CSRFToken {
		t.Errorf("csrf_token = %q, want the session's", body.CSRFToken)
	}
	if body.Email != "alice@example.com" || body.Role != "admin" {
		t.Errorf("session body = %+v", body)
	}
	if len(accounts.loggedIn) != 1 {
		t.Errorf("the sign-in was not recorded: %v", accounts.loggedIn)
	}
}

// A wrong password and an unknown address must be one answer. Two would let
// the form be used to discover which addresses have accounts.
func TestSignIn_FailuresAreIndistinguishable(t *testing.T) {
	s := newTestServer(t, newFakeAccounts())

	var messages []string
	for _, body := range []string{
		`{"email":"alice@example.com","password":"wrong-but-long-enough"}`,
		`{"email":"nobody@example.com","password":"a-sufficiently-long-passphrase"}`,
	} {
		res := signIn(t, s, body)
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", res.StatusCode)
		}
		var out map[string]string
		_ = json.NewDecoder(res.Body).Decode(&out)
		messages = append(messages, out["error"])
		if len(res.Cookies()) > 0 {
			t.Error("a failed sign-in must not set a cookie")
		}
	}
	if messages[0] != messages[1] {
		t.Errorf("failures differ: %q vs %q", messages[0], messages[1])
	}
}

// The cookie is what a browser sends automatically, including on a request
// another site caused. The header is what a cross-origin page cannot set, so
// requiring it is the whole of the defence.
func TestMutatingRequestsRequireTheCSRFToken(t *testing.T) {
	accounts := newFakeAccounts()
	s := newTestServer(t, accounts)

	newReq := func(method string) *http.Request {
		r := httptest.NewRequest(method, "/api/users", strings.NewReader(`{}`))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
		return r
	}

	// Without the header the cookie alone is not enough.
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, newReq(http.MethodPost))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 without a CSRF token", w.Code)
	}

	// A wrong token is no better than none.
	r := newReq(http.MethodPost)
	r.Header.Set(csrfHeader, "not-the-right-token")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a wrong CSRF token", w.Code)
	}

	// With it, the request is the page's own and proceeds.
	r = newReq(http.MethodPost)
	r.Header.Set(csrfHeader, accounts.session.CSRFToken)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code == http.StatusForbidden {
		t.Fatalf("a correctly stamped request was refused: %s", w.Body.String())
	}

	// Reads carry no CSRF token and must not need one.
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, newReq(http.MethodGet))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200 without a CSRF token: %s", w.Code, w.Body.String())
	}
}

// A cookie that no longer resolves is cleared, so the browser stops presenting
// it and the page can tell it needs to sign in again.
func TestAStaleSessionCookieIsCleared(t *testing.T) {
	s := newTestServer(t, newFakeAccounts())

	r := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "a-token-that-expired"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge >= 0 {
			t.Errorf("stale cookie was not cleared: %+v", c)
		}
	}
}

// Signing out must work even when the session is already gone, or the browser
// is left holding a cookie it cannot get rid of.
func TestSignOutAlwaysClearsTheCookie(t *testing.T) {
	accounts := newFakeAccounts()
	s := newTestServer(t, accounts)

	r := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	if len(accounts.deleted) != 1 || accounts.deleted[0] != accounts.token {
		t.Errorf("the session row was not deleted: %v", accounts.deleted)
	}
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("the cookie was not cleared")
	}
}

// A session resolves to the account's own role, so a viewer cannot reach an
// administrator's endpoints by holding a valid cookie.
func TestSessionCarriesTheAccountsRole(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.Role = auth.RoleViewer
	s := newTestServer(t, accounts)

	r := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a viewer reading accounts", w.Code)
	}
}
