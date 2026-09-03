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
	"github.com/spoked/mcpd/internal/auth/apikeys"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/observability"
)

// fakeKeys is a stand-in for the key store. What these tests are about is the
// HTTP layer: which responses can carry a secret, and who may reach the
// routes at all.
type fakeKeys struct {
	keys   []*apikeys.Key
	secret string
	// created records the last request, so a test can check what the handler
	// passed through rather than only what came back.
	created apikeys.CreateRequest
	revoked []string
	// rotated records the last rotation, and rotatedSecret what it returns.
	rotated       apikeys.Key
	rotatedGrace  time.Duration
	rotatedSecret string
	rotateErr     error
}

func (f *fakeKeys) List(context.Context) ([]*apikeys.Key, error) { return f.keys, nil }

func (f *fakeKeys) ByID(_ context.Context, id string) (*apikeys.Key, error) {
	for _, k := range f.keys {
		if k.ID == id {
			return k, nil
		}
	}
	return nil, apikeys.ErrNotFound
}

func (f *fakeKeys) Create(_ context.Context, actor string, req apikeys.CreateRequest) (*apikeys.Key, string, error) {
	f.created = req
	k := &apikeys.Key{
		ID: "key_1", Name: req.Name, RoleID: req.RoleID, Grants: req.Grants,
		CreatedBy: actor, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		ExpiresAt: req.ExpiresAt,
	}
	f.keys = append(f.keys, k)
	return k, f.secret, nil
}

func (f *fakeKeys) Update(_ context.Context, _, id string, _ apikeys.UpdateRequest) (*apikeys.Key, error) {
	return f.ByID(context.Background(), id)
}

func (f *fakeKeys) Rotate(_ context.Context, _, id string, grace time.Duration) (*apikeys.Key, string, error) {
	if f.rotateErr != nil {
		return nil, "", f.rotateErr
	}
	f.rotatedGrace = grace
	k, err := f.ByID(context.Background(), id)
	if err != nil {
		return nil, "", err
	}
	f.rotated = *k
	return k, f.rotatedSecret, nil
}

func (f *fakeKeys) Revoke(_ context.Context, _, id string) error {
	f.revoked = append(f.revoked, id)
	return nil
}

func newKeyServer(t *testing.T, accounts Accounts, keys Keys) *Server {
	t.Helper()
	return NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Keys:     keys,
		Version:  "test",
		Health:   observability.NewHealthRegistry(time.Second),
		KeyAccess: func(context.Context, string) (groups.Resolved, error) {
			return groups.Resolved{Permissions: auth.Permissions{}, Grants: auth.Grants{}}, nil
		},
	})
}

func asAdmin(t *testing.T, s *Server, accounts *fakeAccounts, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, reader)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	r.Header.Set(csrfHeader, accounts.session.CSRFToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// The secret is in the reply to the request that created the key, and in no
// other response this API can produce. There is no route that reads one back
// and no view that carries one, which is what makes "copy it now" a fact about
// the system rather than advice.
func TestKeys_TheSecretIsShownOnceAndNeverAgain(t *testing.T) {
	accounts := newFakeAccounts()
	keys := &fakeKeys{secret: "mcpd_a-secret-nobody-may-read-twice"}
	s := newKeyServer(t, accounts, keys)

	w := asAdmin(t, s, accounts, http.MethodPost, "/api/keys",
		`{"name":"agent","role":"role_operator","grants":[{"plugin":"echo","level":"write"}]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/keys = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Secret string  `json:"secret"`
		Key    keyView `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Secret != keys.secret {
		t.Fatalf("secret = %q; creation is the one place it appears", created.Secret)
	}

	// Every other shape this API can return.
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/keys", ""},
		{http.MethodPatch, "/api/keys/key_1", `{"grants":[{"plugin":"netbox","level":"read"}]}`},
		{http.MethodPost, "/api/keys/key_1/revoke", ""},
	} {
		w := asAdmin(t, s, accounts, tc.method, tc.path, tc.body)
		if w.Code >= 400 {
			t.Fatalf("%s %s = %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), keys.secret) {
			t.Errorf("%s %s returned the secret: %s", tc.method, tc.path, w.Body.String())
		}
	}

	// And there is no route that offers one.
	for _, path := range []string{
		"/api/keys/key_1", "/api/keys/key_1/secret", "/api/keys/key_1/reveal",
	} {
		w := asAdmin(t, s, accounts, http.MethodGet, path, "")
		if strings.Contains(w.Body.String(), keys.secret) {
			t.Errorf("GET %s returned the secret", path)
		}
	}
}

// Only an administrator may reach the access routes, which is the owner's
// rule: a key or a group carries a role and a reach, and creating or editing
// one hands out both.
func TestKeys_EveryRouteTakesAccessWrite(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.RoleID = auth.RoleOperator
	s := newKeyServer(t, accounts, &fakeKeys{secret: "mcpd_secret"})

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/keys", ""},
		{http.MethodPost, "/api/keys", `{"name":"agent","role":"role_operator"}`},
		{http.MethodPatch, "/api/keys/key_1", `{"grants":[]}`},
		{http.MethodPost, "/api/keys/key_1/revoke", ""},
		{http.MethodPost, "/api/keys/key_1/rotate", ""},
		{http.MethodGet, "/api/groups", ""},
		{http.MethodPost, "/api/groups", `{"name":"Field"}`},
		{http.MethodPatch, "/api/groups/grp_1", `{"grants":[]}`},
		{http.MethodDelete, "/api/groups/grp_1", ""},
		{http.MethodPost, "/api/groups/grp_1/members", `{"kind":"user","id":"usr_1"}`},
		{http.MethodDelete, "/api/groups/grp_1/members/user/usr_1", ""},
	} {
		w := asAdmin(t, s, accounts, tc.method, tc.path, tc.body)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d for an operator, want 403", tc.method, tc.path, w.Code)
		}
	}
}

// A key with no direct grants and no groups reaches nothing, and the page says
// so rather than showing an absent list.
func TestKeys_ANewKeyRendersAsReachingNothing(t *testing.T) {
	accounts := newFakeAccounts()
	keys := &fakeKeys{secret: "mcpd_secret"}
	s := newKeyServer(t, accounts, keys)

	w := asAdmin(t, s, accounts, http.MethodPost, "/api/keys",
		`{"name":"agent","role":"role_operator"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/keys = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Key keyView `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Key.Reaches == nil || len(created.Key.Reaches) != 0 {
		t.Errorf("reaches = %v, want an empty list", created.Key.Reaches)
	}
	if created.Key.Grants == nil {
		t.Error("grants came back null; a page cannot count that")
	}
	if created.Key.Status != string(apikeys.StatusActive) {
		t.Errorf("status = %q, want active", created.Key.Status)
	}
}

// An expiry the handler cannot read is refused with a sentence somebody can
// act on, rather than being dropped and producing a key that never expires.
func TestKeys_ARubbishExpiryIsRefused(t *testing.T) {
	accounts := newFakeAccounts()
	s := newKeyServer(t, accounts, &fakeKeys{secret: "mcpd_secret"})
	w := asAdmin(t, s, accounts, http.MethodPost, "/api/keys",
		`{"name":"agent","role":"role_operator","expires_at":"next tuesday"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST with a bad expiry = %d, want 400", w.Code)
	}
}

// Rotating shows the new secret once, the same way creating does, and the old
// key's identity, role, grants and groups stay -- only the secret moves.
func TestKeys_RotateReturnsTheSecretOnce(t *testing.T) {
	accounts := newFakeAccounts()
	keys := &fakeKeys{
		secret:        "mcpd_original",
		rotatedSecret: "mcpd_rotated-secret-shown-once",
		keys: []*apikeys.Key{{
			ID: "key_1", Name: "agent", RoleID: auth.RoleOperator,
			CreatedBy: "user:alice", CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}},
	}
	s := newKeyServer(t, accounts, keys)

	w := asAdmin(t, s, accounts, http.MethodPost, "/api/keys/key_1/rotate", `{"grace_seconds":3600}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST rotate = %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Secret string  `json:"secret"`
		Key    keyView `json:"key"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Secret != keys.rotatedSecret {
		t.Errorf("secret = %q, want the rotated one", got.Secret)
	}
	if keys.rotatedGrace != time.Hour {
		t.Errorf("grace = %s, want 1h from grace_seconds", keys.rotatedGrace)
	}
	if got.Key.ID != "key_1" {
		t.Errorf("key = %+v, want the same identity", got.Key)
	}
}

// A revoked key cannot be rotated back to life; the handler maps the store's
// refusal onto a 409 an operator can act on, telling them to issue a new key
// instead.
func TestKeys_RotateRefusesARevokedKey(t *testing.T) {
	accounts := newFakeAccounts()
	keys := &fakeKeys{rotateErr: apikeys.ErrRevoked}
	s := newKeyServer(t, accounts, keys)

	w := asAdmin(t, s, accounts, http.MethodPost, "/api/keys/key_1/rotate", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("POST rotate on a revoked key = %d, want 409: %s", w.Code, w.Body.String())
	}
}

// A membership names an account or a key. Anything else is refused at the edge
// rather than reaching the store as a subject nothing can resolve.
func TestGroups_AMemberKindIsAccountOrKey(t *testing.T) {
	accounts := newFakeAccounts()
	s := NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Groups:   &fakeGroups{},
		Version:  "test",
		Health:   observability.NewHealthRegistry(time.Second),
	})
	w := asAdmin(t, s, accounts, http.MethodPost, "/api/groups/grp_1/members",
		`{"kind":"robot","id":"usr_1"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("adding a member of an unknown kind = %d, want 400", w.Code)
	}
}

// fakeGroups is enough for the routing and validation assertions above.
type fakeGroups struct{}

func (fakeGroups) List(context.Context) ([]*groups.Group, error) { return nil, nil }
func (fakeGroups) ByID(context.Context, string) (*groups.Group, error) {
	return nil, groups.ErrNotFound
}

func (fakeGroups) Create(context.Context, string, groups.CreateRequest) (*groups.Group, error) {
	return nil, groups.ErrNotFound
}

func (fakeGroups) Update(context.Context, string, string, groups.UpdateRequest) (*groups.Group, error) {
	return nil, groups.ErrNotFound
}
func (fakeGroups) Delete(context.Context, string, string) error { return nil }
func (fakeGroups) Members(context.Context, string) ([]groups.Member, error) {
	return nil, nil
}

func (fakeGroups) Of(context.Context, groups.Subject) ([]*groups.Group, error) {
	return nil, nil
}

func (fakeGroups) AddMember(context.Context, string, string, groups.Subject) error {
	return nil
}

func (fakeGroups) RemoveMember(context.Context, string, string, groups.Subject) error {
	return nil
}
