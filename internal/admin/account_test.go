package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
)

// asAccount performs a request carrying the session cookie and its CSRF token,
// which is what a signed-in page sends.
func asAccount(t *testing.T, s *Server, accounts *fakeAccounts, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	r.Header.Set(csrfHeader, accounts.session.CSRFToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// Naming yourself is the one thing about an account its holder is the
// authority on. Before this endpoint existed the Account page could only tell
// a non-administrator to go and ask somebody else to type their name for them.
func TestAccount_APersonMayNameThemselvesWithoutBeingAnAdministrator(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.Role = auth.RoleUser
	s := newTestServer(t, accounts)

	w := asAccount(t, s, accounts, http.MethodPatch, "/api/account", `{"display_name":"Alice A."}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	if accounts.updatedID != "usr_1" {
		t.Errorf("the edit aimed at %q, want the signed-in account", accounts.updatedID)
	}
	if accounts.updatedReq.DisplayName == nil || *accounts.updatedReq.DisplayName != "Alice A." {
		t.Errorf("display name not passed through: %+v", accounts.updatedReq)
	}
	// Nothing else about the account may travel with it. Role, grants and
	// disabled are somebody else's decisions.
	if accounts.updatedReq.Role != nil || accounts.updatedReq.Plugins != nil ||
		accounts.updatedReq.Disabled != nil {
		t.Errorf("the self-service edit carried more than a name: %+v", accounts.updatedReq)
	}

	var view userView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Name != "Alice A." || view.DisplayName != "Alice A." {
		t.Errorf("view = %+v", view)
	}
	if !view.Self {
		t.Error("the account edited is the one signed in, so it is marked self")
	}
}

// Naming somebody else is administration and still needs it. The self-service
// route cannot be turned into a way around this one, because it carries no
// identifier to point at another account.
func TestAccount_NamingSomebodyElseStillNeedsAdministrator(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.Role = auth.RoleUser
	s := newTestServer(t, accounts)

	w := asAccount(t, s, accounts, http.MethodPatch, "/api/users/usr_2", `{"display_name":"Bob"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a user editing another account", w.Code)
	}

	accounts.user.Role = auth.RoleAdmin
	admin := newTestServer(t, accounts)
	w = asAccount(t, admin, accounts, http.MethodPatch, "/api/users/usr_2", `{"display_name":"Bob"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for an administrator: %s", w.Code, w.Body)
	}
	if accounts.updatedID != "usr_2" {
		t.Errorf("the edit aimed at %q, want the account in the path", accounts.updatedID)
	}
}

// A bearer token authenticates a script, which is a principal with no account.
// There is no row for it to name.
func TestAccount_ABearerTokenHasNothingToName(t *testing.T) {
	accounts := newFakeAccounts()
	s := NewServer(Options{
		Log:      newTestServer(t, accounts).opts.Log,
		Verifier: roleVerifier{role: auth.RoleAdmin},
		Accounts: accounts,
	})

	w := request(t, s, http.MethodPatch, "/api/account", map[string]string{"display_name": "Whatever"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", w.Code, w.Body)
	}
}

// A PATCH that says nothing is a mistake rather than a request to clear the
// name, which is what an absent field would otherwise be read as.
func TestAccount_AnEmptyPatchIsRefused(t *testing.T) {
	accounts := newFakeAccounts()
	s := newTestServer(t, accounts)

	w := asAccount(t, s, accounts, http.MethodPatch, "/api/account", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// The cookie travels on a request another site caused; the header cannot be
// set cross-origin. Renaming an account is a write, so it needs the header.
func TestAccount_RenamingNeedsTheCSRFToken(t *testing.T) {
	accounts := newFakeAccounts()
	s := newTestServer(t, accounts)

	r := httptest.NewRequest(http.MethodPatch, "/api/account",
		strings.NewReader(`{"display_name":"Mallory"}`))
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 without the CSRF header", w.Code)
	}
}

// An account with no display name still has to render as something. Both
// fields are here on purpose: one for the heading, one for the input.
func TestSession_ReportsAName_EvenWithoutADisplayName(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.DisplayName = ""
	s := newTestServer(t, accounts)

	res := signIn(t, s, `{"email":"alice@example.com","password":"a-sufficiently-long-passphrase"}`)
	var body sessionResponse
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Name != "alice@example.com" {
		t.Errorf("name = %q, want the address as the fallback", body.Name)
	}
	if body.DisplayName != "" {
		t.Errorf("display_name = %q, want the stored value so an edit form round-trips", body.DisplayName)
	}
}
