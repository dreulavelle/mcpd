package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth/users"
)

// invitingAccounts records what Create was asked for, which is the whole of
// what the handler decides: an invitation names a provider and carries no
// password, and the store refuses the combination anyway.
type invitingAccounts struct {
	*fakeAccounts
	created users.CreateRequest
}

func (a *invitingAccounts) Create(_ context.Context, req users.CreateRequest) (*users.User, error) {
	a.created = req
	expires := time.Now().Add(users.InviteTTL)
	u := &users.User{
		ID: "usr_new", Email: req.Email, RoleID: req.RoleID,
		PasswordHash:   users.NoPassword,
		InviteProvider: req.InviteProvider,
		Status:         users.StatusActive,
	}
	if u.Invited() {
		u.InviteExpiresAt = &expires
	} else {
		u.PasswordHash = "$2a$12$a-hash"
	}
	return u, nil
}

func createUser(t *testing.T, s *Server, accounts Accounts, body string) *http.Response {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(csrfHeader, "csrf-token-value")
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "session-token-value"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w.Result()
}

// The dialog offers "with a password you set" or "with Google", and an
// invitation is what the second sends. The row that comes back says so, so the
// list can show that this person has not arrived yet.
func TestCreateUser_AnInvitationNamesAProviderAndCarriesNoPassword(t *testing.T) {
	accounts := &invitingAccounts{fakeAccounts: newFakeAccounts()}
	opts := testOptions(accounts, &fakeIdentities{})
	opts.SSO = newTestSSO(t)
	s := NewServer(opts)

	res := createUser(t, s, accounts,
		`{"email":"newcomer@example.com","role":"role_operator","invite_provider":"google"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/users = %d; want 201", res.StatusCode)
	}
	if accounts.created.InviteProvider != users.ProviderGoogle {
		t.Errorf("invite_provider = %q; want google", accounts.created.InviteProvider)
	}
	if accounts.created.Password != "" {
		t.Error("an invitation was sent with a password beside it")
	}

	var view userView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.InviteProvider != "google" || view.InviteLabel != "Google" {
		t.Errorf("view = %+v; want the provider it is waiting for, in the words the button uses", view)
	}
	if view.InviteExpiresAt == "" {
		t.Error("the row does not say when the invitation stops being claimable")
	}
	if view.HasPassword {
		t.Error("an invited account was reported as having a password")
	}
}

// An invitation naming a provider nobody configured is an account nobody can
// ever sign in to, and the store cannot see the settings that would say so.
func TestCreateUser_AnInvitationNeedsAProviderThisHostHasSetUp(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured bool
		want       int
	}{
		{"a provider nobody configured", false, http.StatusBadRequest},
		{"a provider that is set up", true, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := &invitingAccounts{fakeAccounts: newFakeAccounts()}
			opts := testOptions(accounts, &fakeIdentities{})
			if tc.configured {
				opts.SSO = newTestSSO(t)
			}
			s := NewServer(opts)

			res := createUser(t, s, accounts,
				`{"email":"newcomer@example.com","role":"role_operator","invite_provider":"google"}`)
			if res.StatusCode != tc.want {
				t.Errorf("POST /api/users = %d; want %d", res.StatusCode, tc.want)
			}
		})
	}
}

// The ordinary case is untouched: no invitation, a password, and a row that
// says nothing about invitations at all.
func TestCreateUser_APasswordAccountIsUnchanged(t *testing.T) {
	accounts := &invitingAccounts{fakeAccounts: newFakeAccounts()}
	s := NewServer(testOptions(accounts, &fakeIdentities{}))

	res := createUser(t, s, accounts,
		`{"email":"newcomer@example.com","role":"role_operator","password":"a-sufficiently-long-passphrase"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/users = %d; want 201", res.StatusCode)
	}
	if accounts.created.InviteProvider != "" {
		t.Errorf("invite_provider = %q; want none", accounts.created.InviteProvider)
	}
	var view userView
	if err := json.NewDecoder(res.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.InviteProvider != "" || view.InviteLabel != "" || view.InviteExpiresAt != "" {
		t.Errorf("view = %+v; an account nobody invited must say nothing about invitations", view)
	}
}
