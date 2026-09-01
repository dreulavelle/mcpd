package textable

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// testKey is a service account token: one opaque string, no colon. A value with
// a colon is a *user* token, which Config.Validate refuses.
const testKey = "svc-tok-abcdef0123456789"

// testConfig is a configuration with the defaults applied and an address that
// will be replaced by the fake server's.
func testConfig(base string) Config {
	cfg := Config{BaseURL: base, APIKey: testKey}
	cfg.withDefaults()
	return cfg
}

// testClient builds a client pointed at a fake Textable.
func testClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cfg := testConfig(srv.URL)
	return NewClient(srv.Client(), cfg, testKey,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Now, nil, func(string, time.Duration) {}), srv
}

// toolPlugin builds a plugin around a fake Textable.
//
// The struct is built directly rather than through New, which the fake server's
// address would be fine for but which would also rebuild the client. What is
// under test here is the tools.
func toolPlugin(t *testing.T, h http.HandlerFunc) *Plugin {
	t.Helper()
	c, _ := testClient(t, h)
	return &Plugin{
		deps: plugins.Deps{
			Instance: "textable",
			Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:      time.Now,
		},
		cfg:        c.cfg,
		client:     c,
		configured: true,
	}
}

// jsonOK answers every request with the given body.
func jsonOK(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

// routes answers each path with its own body, and 404s anything else so a test
// that reaches an endpoint it did not mean to fails rather than passes quietly.
func routes(t *testing.T, bodies map[string]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request to %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}
