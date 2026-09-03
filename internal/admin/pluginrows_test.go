package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// customersField is a collection the way an integration would declare it: a
// required name that identifies the row, an optional list of aliases, an
// address, and a credential.
func customersField() settings.Field {
	return settings.Field{
		Key: "customers", Label: "Customers", Kind: settings.KindCollection,
		Columns: []settings.Field{
			{Key: "name", Label: "Business name", Kind: settings.KindString, Required: true},
			{Key: "aliases", Label: "Aliases", Kind: settings.KindList},
			{Key: "host", Label: "Address", Kind: settings.KindString, Required: true},
			{Key: "password", Label: "Password", Kind: settings.KindSecret, Required: true},
			{Key: "max_items", Label: "Most items", Kind: settings.KindInt, Min: intPtr(1), Max: intPtr(10)},
		},
	}
}

func intPtr(i int) *int { return &i }

const rowsKey = "/api/settings/rows/plugins.pbx.customers"

func newRowsDashboard(t *testing.T, role string) (*Server, *sqlite.PluginRowStore, *[]string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path: filepath.Join(t.TempDir(), "test.db"), RelaxedDurability: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	key, _ := settings.GenerateKey()
	cipher, err := settings.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	store := sqlite.NewPluginRowStore(db, cipher, time.Now)
	var rebuilt []string

	catalog := settings.NewCatalog(settings.PluginGroup("pbx", "3CX", []settings.Field{customersField()}))
	s := NewServer(Options{
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier:          roleVerifier{role: role},
		Health:            observability.NewHealthRegistry(time.Second),
		Catalog:           func() *settings.Catalog { return catalog },
		PluginRows:        store,
		PluginRowsChanged: func(instance string) { rebuilt = append(rebuilt, instance) },
	})
	return s, store, &rebuilt
}

func decodeRow(t *testing.T, body string) rowView {
	t.Helper()
	var v rowView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

// A row is added with its columns typed, its secret encrypted and withheld on
// the way back out, and the instance rebuilt so the plugin sees it.
func TestPluginRows_AddListAndTheSecretNeverComesBack(t *testing.T) {
	s, store, rebuilt := newRowsDashboard(t, auth.RoleAdministrator)

	w := request(t, s, http.MethodPost, rowsKey, map[string]any{"values": map[string]string{
		"name": "Acme", "aliases": "acme, ACME Inc", "host": "acme.example", "password": "s3cret", "max_items": "5",
	}})
	if w.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	created := decodeRow(t, w.Body.String())
	if created.Values["name"] != "Acme" || created.Values["host"] != "acme.example" {
		t.Errorf("values: %+v", created.Values)
	}
	if aliases, _ := created.Values["aliases"].([]any); len(aliases) != 2 || aliases[1] != "ACME Inc" {
		t.Errorf("a list column should be split on commas: %+v", created.Values["aliases"])
	}
	if n, _ := created.Values["max_items"].(float64); n != 5 {
		t.Errorf("an int column should be typed: %+v", created.Values["max_items"])
	}
	if strings.Contains(w.Body.String(), "s3cret") {
		t.Error("the secret came back in the response")
	}
	if len(created.SecretsSet) != 1 || created.SecretsSet[0] != "password" {
		t.Errorf("secrets_set should name the password: %v", created.SecretsSet)
	}
	if len(*rebuilt) != 1 || (*rebuilt)[0] != "pbx" {
		t.Errorf("the instance should be rebuilt after a row is added, got %v", *rebuilt)
	}

	// The plugin, reading the store, gets the credential.
	rows, err := store.List(context.Background(), "pbx", "customers")
	if err != nil || len(rows) != 1 || rows[0].Secrets["password"] != "s3cret" {
		t.Fatalf("stored rows: %+v %v", rows, err)
	}

	w = request(t, s, http.MethodGet, rowsKey, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "s3cret") {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var listed struct {
		Rows  []rowView      `json:"rows"`
		Field settings.Field `json:"field"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Rows) != 1 || listed.Field.Key != "plugins.pbx.customers" || len(listed.Field.Columns) != 5 {
		t.Errorf("listing should carry the rows and the column declarations: %+v", listed)
	}
}

// Every column is validated before anything is written, and a bad row names
// what was wrong with it. A name already taken is a conflict, not a 500.
func TestPluginRows_ValidatesBeforeWriting(t *testing.T) {
	s, store, _ := newRowsDashboard(t, auth.RoleAdministrator)

	w := request(t, s, http.MethodPost, rowsKey, map[string]any{"values": map[string]string{
		"name": "", "host": "", "max_items": "50", "colour": "blue",
	}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"Business name is required", "Address is required", "Password is required", "Most items must be at most 10", `"colour\" is not a column of Customers`} {
		if !strings.Contains(body, want) {
			t.Errorf("problems should say %q, got %s", want, body)
		}
	}
	if rows, _ := store.List(context.Background(), "pbx", "customers"); len(rows) != 0 {
		t.Error("a refused row must not be stored")
	}

	ok := map[string]any{"values": map[string]string{"name": "Acme", "host": "a.example", "password": "p"}}
	if w := request(t, s, http.MethodPost, rowsKey, ok); w.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	dup := map[string]any{"values": map[string]string{"name": "acme", "host": "b.example", "password": "p"}}
	if w := request(t, s, http.MethodPost, rowsKey, dup); w.Code != http.StatusConflict {
		t.Errorf("a duplicate name should be a conflict, got %d %s", w.Code, w.Body.String())
	}
}

// Editing a row keeps a secret that was left blank, replaces one that was
// typed, and clears one that was asked to be cleared -- unless it is required,
// in which case clearing without a replacement is refused.
func TestPluginRows_EditMergesSecrets(t *testing.T) {
	s, store, rebuilt := newRowsDashboard(t, auth.RoleAdministrator)
	w := request(t, s, http.MethodPost, rowsKey, map[string]any{"values": map[string]string{
		"name": "Acme", "host": "a.example", "password": "first",
	}})
	created := decodeRow(t, w.Body.String())
	path := rowsKey + "/" + created.ID

	// Blank secret on an edit means keep.
	w = request(t, s, http.MethodPatch, path, map[string]any{"values": map[string]string{
		"name": "Acme Ltd", "host": "b.example", "password": "",
	}})
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	rows, _ := store.List(context.Background(), "pbx", "customers")
	if rows[0].Identity != "Acme Ltd" || rows[0].Data["host"] != "b.example" || rows[0].Secrets["password"] != "first" {
		t.Errorf("after edit: %+v", rows[0])
	}

	// Typed secret replaces.
	w = request(t, s, http.MethodPatch, path, map[string]any{"values": map[string]string{
		"name": "Acme Ltd", "host": "b.example", "password": "second",
	}})
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	rows, _ = store.List(context.Background(), "pbx", "customers")
	if rows[0].Secrets["password"] != "second" {
		t.Errorf("the typed secret should replace the old one: %+v", rows[0].Secrets)
	}

	// Clearing a required secret with nothing to replace it is refused.
	w = request(t, s, http.MethodPatch, path, map[string]any{
		"values": map[string]string{"name": "Acme Ltd", "host": "b.example"}, "clear_secrets": []string{"password"},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Password is required") {
		t.Errorf("clearing a required secret should be refused, got %d %s", w.Code, w.Body.String())
	}
	if len(*rebuilt) != 3 {
		t.Errorf("each successful write rebuilds the instance once, got %v", *rebuilt)
	}
}

// A row is removed only through its own collection's endpoint, and the
// instance is rebuilt afterwards.
func TestPluginRows_RemoveIsScopedToTheCollection(t *testing.T) {
	s, store, rebuilt := newRowsDashboard(t, auth.RoleAdministrator)
	w := request(t, s, http.MethodPost, rowsKey, map[string]any{"values": map[string]string{
		"name": "Acme", "host": "a.example", "password": "p",
	}})
	created := decodeRow(t, w.Body.String())

	other := "/api/settings/rows/plugins.pbx.sites/" + created.ID
	if w := request(t, s, http.MethodDelete, other, nil); w.Code != http.StatusNotFound {
		t.Errorf("a row reached through another collection's path should be refused, got %d", w.Code)
	}
	if w := request(t, s, http.MethodDelete, rowsKey+"/"+created.ID, nil); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if rows, _ := store.List(context.Background(), "pbx", "customers"); len(rows) != 0 {
		t.Error("the row should be gone")
	}
	if len(*rebuilt) != 2 {
		t.Errorf("add and remove each rebuild, got %v", *rebuilt)
	}
}

// Reading rows takes the settings permission, writing them the write one, and
// a key that is not a table is refused rather than treated as one.
func TestPluginRows_Gating(t *testing.T) {
	asUser, _, _ := newRowsDashboard(t, auth.RoleOperator)
	if w := request(t, asUser, http.MethodPost, rowsKey, map[string]any{"values": map[string]string{}}); w.Code != http.StatusForbidden {
		t.Errorf("an operator adding a row: %d, want 403", w.Code)
	}
	asAdmin, _, _ := newRowsDashboard(t, auth.RoleAdministrator)
	if w := request(t, asAdmin, http.MethodGet, "/api/settings/rows/plugins.pbx.purpose", nil); w.Code != http.StatusNotFound {
		t.Errorf("a scalar setting is not a table, got %d %s", w.Code, w.Body.String())
	}
	if w := request(t, asAdmin, http.MethodGet, "/api/settings/rows/history.retention_days", nil); w.Code != http.StatusNotFound {
		t.Errorf("a host setting is not a plugin table, got %d", w.Code)
	}
}

// PUT /api/settings refuses to write a table whole; the rows have their own
// endpoints, and a whole-table write could not say "keep that secret".
func TestPutSettings_RefusesACollection(t *testing.T) {
	catalog := settings.NewCatalog(settings.PluginGroup("pbx", "3CX", []settings.Field{customersField()}))
	if err := catalog.Validate("plugins.pbx.customers", "[]"); err == nil || !strings.Contains(err.Error(), "one at a time") {
		t.Errorf("want a refusal pointing at the row endpoints, got %v", err)
	}
}
