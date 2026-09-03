package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/observability"
)

// fakeAccounts is a hand-rolled stand-in rather than a real store, because
// what these tests are about is the HTTP layer: cookies, CSRF, and which
// credential wins. A database would only add setup between the assertion and
// the thing asserted.
type fakeAccounts struct {
	// resolved, when set, is returned by Resolve verbatim -- for a test that
	// wants to say exactly what a subject may do and reach without leaning on
	// a built-in role. Nil means "expand the account's own role and grants",
	// which is what a fake with no groups means: Resolve answers with the
	// account's own, the same way the real store does for a subject in no
	// group.
	resolved *groups.Resolved
	user     *users.User
	session  *users.Session
	token    string
	password string

	deleted  []string
	loggedIn []string
	// updatedID and updatedReq record the last edit, so a test can check
	// which account an endpoint aimed at as well as what it changed.
	updatedID  string
	updatedReq users.UpdateRequest
	// count stands in for how many accounts exist, which is what decides
	// whether the instance still offers registration.
	count int
	// resolveErr makes resolving what the account may do and reach fail,
	// which has to refuse the request rather than fall back to the row's own
	// role and grants.
	resolveErr error
}

func newFakeAccounts() *fakeAccounts {
	return &fakeAccounts{
		user: &users.User{
			ID: "usr_1", Email: "alice@example.com", DisplayName: "Alice",
			RoleID: auth.RoleAdministrator,
			Grants: auth.GrantsAt([]string{auth.Wildcard}, auth.LevelWrite),
		},
		session: &users.Session{
			ID: "ses_1", UserID: "usr_1", CSRFToken: "csrf-token-value",
			ExpiresAt: time.Now().Add(time.Hour),
		},
		token:    "session-token-value",
		password: "a-sufficiently-long-passphrase",
		count:    1,
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

// Resolve stands in for the union of an account's own role and grants with
// its groups'. The fake has no groups, so by default it answers with the
// account's own role (expanded through auth.BuiltinRole) and its own grants
// -- which is what the union of one subject is. A test that wants something
// else -- a role a group narrows, a subject holding nothing at all -- sets
// resolved explicitly.
func (f *fakeAccounts) Resolve(_ context.Context, _ string) (groups.Resolved, error) {
	if f.resolveErr != nil {
		return groups.Resolved{}, f.resolveErr
	}
	if f.resolved != nil {
		return *f.resolved, nil
	}
	role, _ := auth.BuiltinRole(f.user.RoleID)
	return groups.Resolved{
		RoleID: f.user.RoleID, RoleName: role.Name,
		Permissions: role.Permissions, Grants: f.user.Grants,
	}, nil
}

func (f *fakeAccounts) Count(context.Context) (int, error) { return f.count, nil }

func (f *fakeAccounts) CreateFirst(_ context.Context, email, password, _ string) (*users.User, error) {
	if f.count > 0 {
		return nil, users.ErrAlreadyClaimed
	}
	f.count = 1
	f.user.Email = email
	return f.user, nil
}

func (f *fakeAccounts) Create(context.Context, users.CreateRequest) (*users.User, error) {
	return f.user, nil
}
func (f *fakeAccounts) List(context.Context) ([]*users.User, error) {
	return []*users.User{f.user}, nil
}
func (f *fakeAccounts) ByID(context.Context, string) (*users.User, error) { return f.user, nil }
func (f *fakeAccounts) Update(_ context.Context, id string, req users.UpdateRequest) (*users.User, error) {
	f.updatedID, f.updatedReq = id, req
	if req.DisplayName != nil {
		f.user.DisplayName = *req.DisplayName
	}
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
		// Enough for the endpoints these tests actually execute. The rest are
		// asserted on their authorization outcome, which is decided before any
		// handler runs.
		Health: observability.NewHealthRegistry(time.Second),
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
	if body.Email != "alice@example.com" || body.Role != auth.RoleAdministrator {
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

// A session resolves to the account's own role, so an operator cannot reach an
// administrator's endpoints by holding a valid cookie.
func TestSessionCarriesTheAccountsRole(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.RoleID = auth.RoleOperator
	s := newTestServer(t, accounts)

	r := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an operator reading accounts", w.Code)
	}
}

// The session says what the account may actually do, after its groups have
// had their say. The page draws its controls from this list; when it derived
// the set from the role instead, an administrator whose group permitted only
// reading saw every button and had every click refused.
func TestSessionCarriesTheEffectivePermissions(t *testing.T) {
	cases := []struct {
		name     string
		resolved groups.Resolved
		want     []string
	}{
		{
			"an unrestricted administrator",
			mustResolve(auth.RoleAdministrator, auth.GrantsAt([]string{auth.Wildcard}, auth.LevelWrite)),
			permissionNames(mustResolve(auth.RoleAdministrator, nil).Permissions.List()),
		},
		{
			// A group narrows what a role otherwise carries: the union still
			// applies, but here the subject's own role is Reader, so nothing
			// is added.
			"a reader stays a reader",
			mustResolve(auth.RoleReader, nil),
			permissionNames(mustResolve(auth.RoleReader, nil).Permissions.List()),
		},
		{
			"a subject holding nothing at all",
			groups.Resolved{Permissions: auth.Permissions{}, Grants: auth.Grants{}},
			[]string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accounts := newFakeAccounts()
			accounts.resolved = &tc.resolved
			s := newTestServer(t, accounts)

			r := httptest.NewRequest(http.MethodGet, "/api/session", nil)
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
			}

			var body struct {
				Permissions []string `json:"permissions"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			// Never null on the wire: the page reads the list as what is
			// held, and "nothing" has to arrive as an empty list.
			if body.Permissions == nil {
				t.Fatalf("permissions is null; want %v", tc.want)
			}
			if strings.Join(body.Permissions, ",") != strings.Join(tc.want, ",") {
				t.Errorf("permissions = %v, want %v", body.Permissions, tc.want)
			}
		})
	}
}

// mustResolve expands a built-in role the way groups.Resolve would for a
// subject holding it directly, for a test that wants to name a role rather
// than spell out its permissions.
func mustResolve(roleID string, grants auth.Grants) groups.Resolved {
	role, _ := auth.BuiltinRole(roleID)
	return groups.Resolved{
		RoleID: role.ID, RoleName: role.Name,
		Permissions: role.Permissions, Grants: grants,
	}
}

// A new instance says so, so the dashboard can offer to claim it rather than
// asking for credentials nobody has yet.
func TestMetaReportsWhetherSetupIsNeeded(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.count = 0
	s := newTestServer(t, accounts)

	get := func() bool {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body["needs_setup"] == true
	}

	if !get() {
		t.Error("an instance with no accounts must report needs_setup")
	}
	accounts.count = 1
	if get() {
		t.Error("an instance with an account must not report needs_setup")
	}
}

// Registration is unauthenticated by necessity, and signs the new
// administrator straight in.
func TestRegisterFirst_ClaimsAndSignsIn(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.count = 0
	s := newTestServer(t, accounts)

	r := httptest.NewRequest(http.MethodPost, "/api/setup",
		strings.NewReader(`{"email":"first@example.com","password":"a-sufficiently-long-passphrase"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body.String())
	}
	var cookie bool
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.HttpOnly {
			cookie = true
		}
	}
	if !cookie {
		t.Error("registering must sign the new administrator in")
	}
	var body sessionResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.CSRFToken == "" {
		t.Error("the session must carry a CSRF token")
	}
}

// Once an instance has an account, registration is a way to mint an
// administrator on a running host. It has to be refused.
func TestRegisterFirst_RefusedOnceClaimed(t *testing.T) {
	accounts := newFakeAccounts() // count starts at 1
	s := newTestServer(t, accounts)

	r := httptest.NewRequest(http.MethodPost, "/api/setup",
		strings.NewReader(`{"email":"mallory@example.com","password":"a-sufficiently-long-passphrase"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", w.Code)
	}
	if len(w.Result().Cookies()) > 0 {
		t.Error("a refused registration must not set a cookie")
	}
}

// The line between the two roles is administering the host. An operator
// reaches the operating endpoints and is refused the administrative ones.
func TestOperatorRoleCannotAdminister(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.RoleID = auth.RoleOperator
	s := newTestServer(t, accounts)

	req := func(method, path string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
		r.Header.Set(csrfHeader, accounts.session.CSRFToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code
	}

	// Administering: settings, tunnels, accounts, clearing history.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPut, "/api/settings"},
		{http.MethodPost, "/api/tunnels"},
		{http.MethodPost, "/api/tunnel/start"},
		{http.MethodGet, "/api/users"},
		{http.MethodDelete, "/api/audit"},
	} {
		if got := req(tc.method, tc.path); got != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403 for an operator", tc.method, tc.path, got)
		}
	}

	// Operating: reading the host is an operator's business, and deciding on a
	// proposed change is too.
	if got := req(http.MethodGet, "/api/health"); got == http.StatusForbidden {
		t.Error("GET /api/health was refused; an operator may see the host's state")
	}
	access, err := accounts.Resolve(context.Background(), accounts.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !accounts.user.Principal("ses", access).Can(auth.PermApprovalsDecide) {
		t.Error("an operator must be able to decide on a proposed change; that is the point of the two roles")
	}
}

// Resolving what an account may do and reach is a read of the database, and a
// read can fail. Falling back to the row's own role and grants would answer a
// question about groups with an answer that ignores them, and a database that
// cannot be read is not a reason to hand somebody a different set of rights --
// so the request is refused.
func TestPrincipalFor_RefusesWhenAccessCannotBeResolved(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.resolveErr = errors.New("the database is unhappy")
	s := newTestServer(t, accounts)

	r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 when access cannot be resolved", w.Code)
	}
}
