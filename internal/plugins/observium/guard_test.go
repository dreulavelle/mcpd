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
func TestConfigValidate(t *testing.T) {
	full := func(c Config) Config {
		c.PageSize, c.MaxItems, c.RequestsPerSecond = 10, 10, 1
		return c
	}
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"unconfigured is not an error", Config{}, ""},
		{"api path in the address", Config{BaseURL: "https://o.example.com/api/v0"}, "web root"},
		{"credentials in the address", Config{BaseURL: "https://u:p@o.example.com"}, "not in the address"},
		{"bad scheme", Config{BaseURL: "ftp://o.example.com"}, "http or https"},
		{"username without password", full(Config{
			BaseURL: "https://o.example.com", Username: "u",
		}), "needs a password"},
		{"valid with a token", full(Config{
			BaseURL: "https://o.example.com", Token: "t",
		}), ""},
		{"valid with basic auth", full(Config{
			BaseURL: "https://o.example.com", Username: "u", Password: "p",
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

	got, err := p.getGraphURLs(context.Background(), graphArgs{
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
	got, err := p.getGraphURLs(context.Background(), graphArgs{Kind: "device", ID: 7})
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
	_, err := p.getGraphURLs(context.Background(), graphArgs{Kind: "sensor", ID: 3})
	if err == nil {
		t.Fatal("an undocumented kind must not be guessed at")
	}
	if !strings.Contains(err.Error(), "graph_type") {
		t.Errorf("the error should say how to supply the type: %v", err)
	}
}

func testPlugin(t *testing.T, base string) *Plugin {
	t.Helper()
	cfg := Config{BaseURL: base, Token: "t"}
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
