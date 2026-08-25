package observium

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// The read-only guarantee is enforced at the transport, which is the last
// thing every request passes through. Checking where paths are built would be
// checking one place among several.
func TestTransport_RefusesEverythingButGET(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	defer srv.Close()

	client := readOnly(srv.Client())
	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead,
	} {
		req, err := http.NewRequest(method, srv.URL+apiPrefix+"/sensors", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Do(req); err == nil {
			t.Errorf("%s was allowed through; this integration only reads", method)
		}
	}
	if reached {
		t.Fatal("a refused request still reached the upstream")
	}
}

// The deny-list is what survives the read-only guard being widened for
// mutations, so it is checked on its own rather than being implied by the
// method check. A device deleted through the API takes its history with it.
func TestDenylist_RefusesDestructiveDeviceEndpoints(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		blocked      bool
	}{
		{http.MethodDelete, "/devices/491", true},
		{http.MethodPut, "/devices/491", true},
		{http.MethodPost, "/devices/", true},
		{http.MethodPut, "/alert_checks/12", true},

		// Reaching a blocked path by a different spelling must not work.
		{http.MethodDelete, "/devices/491/", true},
		{http.MethodDelete, "//devices//491", true},
		{http.MethodDelete, "/devices/491?delete_rrd=1", true},

		// The ordinary reads this plugin lives on.
		{http.MethodGet, "/devices", false},
		{http.MethodGet, "/devices/491", false},
		{http.MethodGet, "/alert_checks/12", false},

		// Maintenance windows are deliberately not blocked: they are the
		// intended first mutation, reversible and verifiable.
		{http.MethodPost, "/maintenance/", false},
		{http.MethodDelete, "/maintenance/3", false},
	} {
		err := checkPath(tc.method, tc.path)
		if tc.blocked && err == nil {
			t.Errorf("%s %s should be refused by the deny-list", tc.method, tc.path)
		}
		if !tc.blocked && err != nil {
			t.Errorf("%s %s should be allowed: %v", tc.method, tc.path, err)
		}
	}
}

// A percent-escaped separator is decoded by the server before it routes, so
// the deny-list has to compare the decoded form. Checking URL.Path rather than
// RawPath is what makes that true.
func TestTransport_ComparesTheDecodedPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an escaped blocked path reached the upstream")
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodDelete,
		srv.URL+apiPrefix+"/devices/491%2F", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readOnly(srv.Client()).Do(req); err == nil {
		t.Fatal("an escaped blocked path was allowed through")
	}
}

// A configuration that is present and wrong should fail here rather than
// later, further away, with a worse message.
//
// Every case names its backend. An empty one resolves to the database -- the
// only backend a Community Edition installation can use -- so a test about the
// API's address that did not say so would be validating the wrong half.
func TestConfigValidate(t *testing.T) {
	api := func(c Config) Config { c.Backend = BackendAPI; return c }
	db := func(c Config) Config {
		c.Backend = BackendDatabase
		c.PageSize, c.MaxItems, c.RequestsPerSecond = 10, 10, 1
		if c.DBPort == 0 {
			c.DBPort = defaultDBPort
		}
		return c
	}

	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"unconfigured is not an error", Config{}, ""},
		{"unknown backend", Config{Backend: "carrier pigeon"}, "backend must be"},

		{"api path in the address", api(Config{BaseURL: "https://o.example.com/api/v0"}), "web root"},
		{"credentials in the address", api(Config{BaseURL: "https://u:p@o.example.com"}), "not in the address"},
		{"bad scheme", api(Config{BaseURL: "ftp://o.example.com"}), "http or https"},
		{"username without password", api(Config{
			BaseURL: "https://o.example.com", Username: "u",
			PageSize: 10, MaxItems: 10, RequestsPerSecond: 1,
		}), "needs a password"},
		{"valid with token", api(Config{
			BaseURL: "https://o.example.com", Token: "t",
			PageSize: 10, MaxItems: 10, RequestsPerSecond: 1,
		}), ""},

		// The database half. A partly-filled connection is the case worth
		// catching: it looks configured and cannot connect.
		{"database unconfigured is not an error", db(Config{}), ""},
		{"database missing its name", db(Config{DBHost: "10.0.0.5", DBUser: "ro"}), "database name"},
		{"database missing its user", db(Config{DBHost: "10.0.0.5", DBName: "observium"}), "username"},
		{"database host given as a URL", db(Config{
			DBHost: "mysql://10.0.0.5", DBName: "observium", DBUser: "ro",
		}), "not a URL"},
		{"database port out of range", db(Config{
			DBHost: "10.0.0.5", DBName: "observium", DBUser: "ro", DBPort: 99999,
		}), "not a port"},
		{"valid database", db(Config{
			DBHost: "10.0.0.5", DBName: "observium", DBUser: "ro",
		}), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("want no error, got %v", err)
			case tc.want != "" && err == nil:
				t.Fatalf("want an error mentioning %q, got none", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

// Alerts answer "is something wrong right now". A cached "nothing is wrong"
// says the network is fine at the moment it stopped being fine, which is the
// worst answer this integration can give.
func TestCacheTTL_AlertsAreNeverHeld(t *testing.T) {
	cfg := Config{StateCacheSeconds: 30, InventoryCacheSeconds: 600}
	for _, path := range []string{"/alerts", "/alert_log", "/alert_checks"} {
		if ttl := cfg.cacheTTL(path); ttl != 0 {
			t.Errorf("%s has a %v cache; it must always be fetched", path, ttl)
		}
	}
	if cfg.cacheTTL("/devices") != 10*time.Minute {
		t.Error("the device list should use the inventory window")
	}
	if cfg.cacheTTL("/sensors") != 30*time.Second {
		t.Error("sensor readings should use the state window")
	}
	// An endpoint the classifier does not know is fetched every time, so a
	// tool added later cannot quietly start being served from memory.
	if cfg.cacheTTL("/something_new") != 0 {
		t.Error("an unrecognised endpoint must not be cached")
	}
}

// Graph links are handed to a person to open in their own browser. A
// credential embedded in one would land in a chat transcript, a model's
// context, and every log between here and there.
func TestGraphURLs_CarryNoCredential(t *testing.T) {
	p := testPlugin(t, "https://observium.example.com")

	got, err := p.graphURLs(context.Background(), graphArgs{
		Kind: "port", ID: 42,
	})
	if err != nil {
		t.Fatalf("graphURLs: %v", err)
	}
	if len(got.Graphs) == 0 {
		t.Fatal("a documented graph type should produce a link")
	}
	for _, g := range got.Graphs {
		u, err := url.Parse(g.URL)
		if err != nil {
			t.Fatalf("built an unparseable URL %q: %v", g.URL, err)
		}
		if u.User != nil {
			t.Errorf("%s carries userinfo", g.URL)
		}
		for key := range u.Query() {
			switch strings.ToLower(key) {
			case "token", "password", "pass", "auth", "api_token":
				t.Errorf("%s carries a credential in %q", g.URL, key)
			}
		}
		if u.Query().Get("id") != "42" {
			t.Errorf("a port is identified with id=, got %q", u.RawQuery)
		}
	}
	if !strings.Contains(got.Note, "cannot see") {
		t.Error("the note must tell the model it cannot read the images")
	}
}

// A device is identified with device= rather than id=. Getting this wrong
// produces a link that renders an error page.
func TestGraphURLs_DeviceUsesDeviceParam(t *testing.T) {
	p := testPlugin(t, "https://observium.example.com")
	got, err := p.graphURLs(context.Background(), graphArgs{Kind: "device", ID: 7})
	if err != nil {
		t.Fatalf("graphURLs: %v", err)
	}
	u, _ := url.Parse(got.Graphs[0].URL)
	if u.Query().Get("device") != "7" {
		t.Errorf("a device is identified with device=, got %q", u.RawQuery)
	}
}

// Guessing a graph type produces a link to an error page, which is a worse
// answer than saying the type is not documented and where to find it.
func TestGraphURLs_UndocumentedKindRefusesRatherThanGuessing(t *testing.T) {
	p := testPlugin(t, "https://observium.example.com")
	_, err := p.graphURLs(context.Background(), graphArgs{Kind: "sensor", ID: 3})
	if err == nil {
		t.Fatal("an undocumented kind must not be guessed at")
	}
	if !strings.Contains(err.Error(), "graph_type") {
		t.Errorf("the error should say how to supply the type: %v", err)
	}
}

func testPlugin(t *testing.T, base string) *Plugin {
	t.Helper()
	// Explicitly the API backend: an empty backend resolves to the database,
	// which is the right default for an operator and the wrong one for a test
	// about graph links.
	cfg := Config{Backend: BackendAPI, BaseURL: base, Token: "t"}
	cfg.withDefaults()
	p, err := New(plugins.Deps{
		Instance: "observium",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTP:     http.DefaultClient,
		Now:      time.Now,
	}, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// Every setting belongs to a backend or to both, and the form has to say
// which. A credential field that is not gated is one an operator is asked to
// fill in for a backend that will never read it; a shared field that *is*
// gated disappears for half the installations that need it.
//
// Listed explicitly rather than derived, so that adding a setting is a
// decision somebody makes here rather than a default they inherit.
func TestSettings_EveryFieldIsGatedToTheBackendThatReadsIt(t *testing.T) {
	shared := map[string]bool{
		// The control itself, and the four that both backends read.
		"backend": true, "max_items": true, "requests_per_second": true,
		"state_cache_seconds": true, "inventory_cache_seconds": true,
	}
	wantAPI := map[string]bool{
		"base_url": true, "token": true, "username": true, "password": true,
		// page_size bounds one API request; the database backend uses LIMIT
		// against max_items and never reads it.
		"page_size": true,
	}
	wantDB := map[string]bool{
		"db_host": true, "db_port": true, "db_name": true,
		"db_user": true, "db_password": true,
	}

	seen := map[string]bool{}
	for _, f := range Type().Settings {
		seen[f.Key] = true
		switch {
		case shared[f.Key]:
			if f.ShowWhen != nil {
				t.Errorf("%q is read by both backends but is hidden for one", f.Key)
			}
		case wantAPI[f.Key]:
			if f.ShowWhen != apiOnly {
				t.Errorf("%q is only read by the API backend and must be gated to it", f.Key)
			}
		case wantDB[f.Key]:
			if f.ShowWhen != databaseOnly {
				t.Errorf("%q is only read by the database backend and must be gated to it", f.Key)
			}
		default:
			t.Errorf("%q is a new setting that no backend has claimed; add it "+
				"to shared, wantAPI or wantDB here so the choice is deliberate", f.Key)
		}
	}
	for _, want := range []map[string]bool{shared, wantAPI, wantDB} {
		for key := range want {
			if !seen[key] {
				t.Errorf("setting %q is expected but no longer declared", key)
			}
		}
	}
}

// The control field must never be conditional itself, or nothing it gates can
// ever be revealed. The host refuses this at startup; this checks the
// declaration mcpd actually ships.
func TestSettings_TheBackendSelectorIsAlwaysVisible(t *testing.T) {
	if err := Type().Validate(); err != nil {
		t.Fatalf("the declaration is refused by the host: %v", err)
	}
	for _, f := range Type().Settings {
		if f.Key == "backend" && f.ShowWhen != nil {
			t.Fatal("the backend selector is itself hidden")
		}
	}
}

// The dropdown asks which licence somebody has, not which mechanism they
// want. Nobody knows offhand whether they want the API or the database;
// everybody knows what they bought, and on Community Edition there is no
// choice to make.
//
// The stored values stay "api" and "database" -- configuration should record
// what changes, and renaming them would break every instance already
// configured to buy nothing.
func TestSettings_BackendIsOfferedAsAnEdition(t *testing.T) {
	var backend settings.Field
	for _, f := range Type().Settings {
		if f.Key == "backend" {
			backend = f
		}
	}
	if backend.Key == "" {
		t.Fatal("there is no backend setting")
	}
	want := map[string]string{
		string(BackendDatabase): "Community Edition",
		string(BackendAPI):      "Subscription",
	}
	for value, label := range want {
		if got := backend.OptionLabels[value]; got != label {
			t.Errorf("option %q is labelled %q, want %q", value, got, label)
		}
	}
	// Every option needs a label. One without falls back to showing "api",
	// which is the vocabulary this exists to keep out of the form.
	for _, o := range backend.Options {
		if backend.OptionLabels[o] == "" {
			t.Errorf("option %q has no label and would render as itself", o)
		}
	}
	// Community Edition first: it is the only one some installations can use,
	// and it is the default.
	if len(backend.Options) == 0 || backend.Options[0] != string(BackendDatabase) {
		t.Error("Community Edition should be the first option and the default")
	}
}
