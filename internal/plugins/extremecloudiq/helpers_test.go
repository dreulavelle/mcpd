package extremecloudiq

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// fixedNow is the clock every test runs on, so a window a tool resolved is a
// value a test can assert on rather than a moving one.
var fixedNow = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

func at(t time.Time) func() time.Time { return func() time.Time { return t } }

// testConfig is a configuration with the defaults applied and an address that
// will be replaced by the fake server's.
func testConfig(base string) Config {
	cfg := Config{BaseURL: base, APIToken: "tok"}
	cfg.withDefaults()
	return cfg
}

// testClient builds a client pointed at a fake ExtremeCloud IQ.
func testClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cfg := testConfig(srv.URL)
	return NewClient(srv.Client(), cfg, "tok",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		at(fixedNow), nil, func(string, time.Duration) {}), srv
}

// toolPlugin builds a plugin around a fake ExtremeCloud IQ.
//
// The struct is built directly rather than through New, which the fake
// server's address would be fine for but which would also rebuild the client.
// What is under test here is the tools.
func toolPlugin(t *testing.T, h http.HandlerFunc) *Plugin {
	t.Helper()
	c, _ := testClient(t, h)
	return &Plugin{
		deps: plugins.Deps{
			Instance: "extremecloudiq",
			Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:      at(fixedNow),
		},
		cfg:        c.cfg,
		client:     c,
		configured: true,
	}
}

// cachingPlugin is toolPlugin with the read cache switched on, for the tests
// that are about what may and may not be held.
func cachingPlugin(t *testing.T, h http.HandlerFunc) *Plugin {
	t.Helper()
	p := toolPlugin(t, h)
	p.client.cache = newReadCache("extremecloudiq", p.cfg, at(fixedNow), nil)
	return p
}

// jsonOK answers every request with the given body.
func jsonOK(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

// routes answers each path from a map, and fails the test on a path nothing
// registered -- which is how a tool reaching an endpoint nobody expected shows
// up as that rather than as an empty result.
func routes(t *testing.T, m map[string]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := m[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error_message":"no such route in this test"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

// page renders a paginated envelope holding the given rows.
func page(total int, rows string) string {
	return `{"page":1,"count":1,"total_pages":1,"total_count":` +
		strconv.Itoa(total) + `,"data":[` + rows + `]}`
}
