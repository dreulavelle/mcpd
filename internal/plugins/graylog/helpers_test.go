package graylog

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// testConfig is a configuration with the defaults applied and an address that
// will be replaced by the fake server's.
func testConfig(base string) Config {
	cfg := Config{BaseURL: base, Token: "tok"}
	cfg.withDefaults()
	return cfg
}

// testClient builds a client pointed at a fake Graylog.
func testClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cfg := testConfig(srv.URL)
	return NewClient(srv.Client(), cfg, "tok", "", "",
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		time.Now, nil, func(string, time.Duration) {}), srv
}

// toolPlugin builds a plugin around a fake Graylog.
//
// The struct is built directly rather than through New, which the fake
// server's address would be fine for but which would also rebuild the client.
// What is under test here is the tools.
func toolPlugin(t *testing.T, h http.HandlerFunc) *Plugin {
	t.Helper()
	c, _ := testClient(t, h)
	return &Plugin{
		deps: plugins.Deps{
			Instance: "graylog",
			Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			Now:      time.Now,
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
	p.client.cache = newReadCache("graylog", p.cfg, time.Now, nil)
	return p
}

// jsonOK answers every request with the given body.
func jsonOK(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

// debugLoggerTo builds a logger at debug level writing into w, for the tests
// that are about what must never appear in a log line.
func debugLoggerTo(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}
