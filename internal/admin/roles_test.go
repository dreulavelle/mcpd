package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/roles"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// fakeRoles is a stand-in for the role store. What these tests are about is
// the HTTP layer: which status a refusal from the store becomes, and what a
// listing carries alongside the roles themselves.
type fakeRoles struct {
	roles []*auth.Role

	createErr error
	updateErr error
	deleteErr error

	created   roles.CreateRequest
	updated   roles.UpdateRequest
	deletedID string
}

func (f *fakeRoles) List(context.Context) ([]*auth.Role, error) { return f.roles, nil }

func (f *fakeRoles) ByID(_ context.Context, id string) (*auth.Role, error) {
	for _, r := range f.roles {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, roles.ErrNotFound
}

func (f *fakeRoles) Create(_ context.Context, actor string, req roles.CreateRequest) (*auth.Role, error) {
	f.created = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	r := &auth.Role{
		ID: "role_custom", Name: req.Name, Description: req.Description,
		Permissions: req.Permissions, CreatedBy: actor,
	}
	f.roles = append(f.roles, r)
	return r, nil
}

func (f *fakeRoles) Update(_ context.Context, _, id string, req roles.UpdateRequest) (*auth.Role, error) {
	f.updated = req
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return f.ByID(context.Background(), id)
}

func (f *fakeRoles) Delete(_ context.Context, _, id string) error {
	f.deletedID = id
	return f.deleteErr
}

func newRolesServer(t *testing.T, accounts Accounts, r Roles) *Server {
	t.Helper()
	return NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Roles:    r,
		Version:  "test",
		Health:   observability.NewHealthRegistry(time.Second),
	})
}

// Listing serves the permission matrix's vocabulary alongside the roles, so
// the editor draws its areas from what the server knows rather than a copy of
// it kept in step by hand.
func TestRoles_ListReturnsAreas(t *testing.T) {
	accounts := newFakeAccounts()
	admin, _ := auth.BuiltinRole(auth.RoleAdministrator)
	s := newRolesServer(t, accounts, &fakeRoles{roles: []*auth.Role{&admin}})

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/roles", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/roles = %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Roles []roleView `json:"roles"`
		Count int        `json:"count"`
		Areas []areaView `json:"areas"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || len(got.Roles) != 1 || got.Roles[0].ID != auth.RoleAdministrator {
		t.Fatalf("roles = %+v", got.Roles)
	}
	if len(got.Areas) != len(auth.Areas) {
		t.Fatalf("areas = %d, want %d -- the matrix the editor draws itself from", len(got.Areas), len(auth.Areas))
	}
}

// Create maps a duplicate name to 409, and everything else the store refuses
// -- an unusable name, a level an area cannot be held at -- to 400 with the
// store's own words, which are actionable.
func TestRoles_CreateMapsErrorsToStatus(t *testing.T) {
	accounts := newFakeAccounts()
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"duplicate name", roles.ErrDuplicateName, http.StatusConflict},
		{"any other refusal", errors.New("roles: a level this area cannot hold"), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRoles{createErr: tc.err}
			s := newRolesServer(t, accounts, fr)
			w := asAdmin(t, s, accounts, http.MethodPost, "/api/roles", `{"name":"Auditor"}`)
			if w.Code != tc.want {
				t.Fatalf("POST /api/roles = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	// The happy path: a role is returned, not merely a 201 with an empty body.
	fr := &fakeRoles{}
	s := newRolesServer(t, accounts, fr)
	w := asAdmin(t, s, accounts, http.MethodPost, "/api/roles",
		`{"name":"Auditor","permissions":{"history":"read"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/roles = %d, want 201: %s", w.Code, w.Body.String())
	}
	if fr.created.Name != "Auditor" || fr.created.Permissions[auth.AreaHistory] != auth.LevelRead {
		t.Errorf("create request = %+v", fr.created)
	}
	var view roleView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Name != "Auditor" {
		t.Errorf("view = %+v", view)
	}
}

// Update maps not-found to 404, a built-in role or a name collision or the
// last-administrator guard to 409, and everything else to 400.
func TestRoles_UpdateMapsErrorsToStatus(t *testing.T) {
	accounts := newFakeAccounts()
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"not found", roles.ErrNotFound, http.StatusNotFound},
		{"builtin", roles.ErrBuiltin, http.StatusConflict},
		{"duplicate name", roles.ErrDuplicateName, http.StatusConflict},
		{"last admin", roles.ErrLastAdmin, http.StatusConflict},
		{"any other refusal", errors.New("roles: that will not parse"), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRoles{updateErr: tc.err}
			s := newRolesServer(t, accounts, fr)
			w := asAdmin(t, s, accounts, http.MethodPatch, "/api/roles/role_custom",
				`{"description":"changed"}`)
			if w.Code != tc.want {
				t.Fatalf("PATCH /api/roles/role_custom = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// Delete maps not-found to 404, a built-in role or a role still assigned to
// 409, and leaves anything else as the store's own internal-error path.
func TestRoles_DeleteMapsErrorsToStatus(t *testing.T) {
	accounts := newFakeAccounts()
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"not found", roles.ErrNotFound, http.StatusNotFound},
		{"builtin", roles.ErrBuiltin, http.StatusConflict},
		{"assigned", roles.ErrAssigned, http.StatusConflict},
		{"an unexpected failure", errors.New("roles: the database is unhappy"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRoles{deleteErr: tc.err}
			s := newRolesServer(t, accounts, fr)
			w := asAdmin(t, s, accounts, http.MethodDelete, "/api/roles/role_custom", "")
			if w.Code != tc.want {
				t.Fatalf("DELETE /api/roles/role_custom = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}

	fr := &fakeRoles{}
	s := newRolesServer(t, accounts, fr)
	w := asAdmin(t, s, accounts, http.MethodDelete, "/api/roles/role_custom", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/roles/role_custom = %d, want 204: %s", w.Code, w.Body.String())
	}
	if fr.deletedID != "role_custom" {
		t.Errorf("deleted = %q, want role_custom", fr.deletedID)
	}
}

// A Reader session may look at this host's configuration and may not change
// it, and it may not see who has access at all -- access is the one area
// Reader does not hold, by design, because listing accounts and keys is a
// wider view than any one account's own work.
func TestReaderSession_ReadsSettingsButCannotWriteThemOrSeeAccess(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.RoleID = auth.RoleReader

	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "reader.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := settings.NewStore(db, nil, time.Now)

	s := NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Settings: store,
		Version:  "test",
		Health:   observability.NewHealthRegistry(time.Second),
	})

	get := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	get.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, get)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/settings as a reader = %d, want 200: %s", w.Code, w.Body.String())
	}

	put := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(`{"values":{}}`))
	put.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	put.Header.Set(csrfHeader, accounts.session.CSRFToken)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, put)
	if w.Code != http.StatusForbidden {
		t.Fatalf("PUT /api/settings as a reader = %d, want 403: %s", w.Code, w.Body.String())
	}

	users := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	users.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, users)
	if w.Code != http.StatusForbidden {
		t.Fatalf("GET /api/users as a reader = %d, want 403: %s", w.Code, w.Body.String())
	}
}
