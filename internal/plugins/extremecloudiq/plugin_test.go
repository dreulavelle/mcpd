package extremecloudiq

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/plugins"
)

// An instance nobody has finished configuring still mounts, so its settings
// form has somewhere to live and the health report can say what is missing --
// which is the whole path somebody follows to fix it.
func TestStart_AnUnconfiguredInstanceIsNotAnError(t *testing.T) {
	p := newFor(t, Config{}, nil)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("an unconfigured instance refused to start: %v", err)
	}
	h := p.Check(context.Background())
	if h.State == plugins.HealthyState {
		t.Error("an unconfigured instance reported itself healthy")
	}
	if !strings.Contains(h.Message, "API token") {
		t.Errorf("the health report does not say what is missing: %q", h.Message)
	}
}

// The probe reads the token rather than the estate, so a startup check does
// not need permission to see devices -- and what it learns is what explains a
// baffling report later: the address is regionless, so two tokens on one
// address can be reading two entirely different estates.
func TestStart_ProbesTheTokenAndLogsWhichEstateItReaches(t *testing.T) {
	var reached []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		_, _ = io.WriteString(w, `{"user_name":"api@example.net","role":"MONITOR",`+
			`"owner_id":42,"data_center":"US-EAST","expires_in":7776000}`)
	}))
	t.Cleanup(srv.Close)

	p := newFor(t, Config{BaseURL: srv.URL, APIToken: "tok"}, srv.Client())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(reached) != 1 || reached[0] != "/auth/apitoken/info" {
		t.Errorf("the probe called %v; it should read the token and nothing else", reached)
	}
	if h := p.Check(context.Background()); h.State != plugins.HealthyState {
		t.Errorf("a working instance is not healthy: %+v", h)
	}
}

// A token that is about to stop working is the one thing worth saying before
// it does: afterwards the API answers 401, which is indistinguishable from a
// revoked token, and somebody spends an afternoon on it.
func TestCheck_WarnsBeforeTheTokenExpires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Three days left, well inside the two-week warning.
		_, _ = io.WriteString(w, `{"user_name":"api@example.net","owner_id":42,`+
			`"expires_in":259200}`)
	}))
	t.Cleanup(srv.Close)

	p := newFor(t, Config{BaseURL: srv.URL, APIToken: "tok"}, srv.Client())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := p.Check(context.Background())
	if h.State == plugins.HealthyState {
		t.Fatal("a token expiring in three days reported as plainly healthy")
	}
	if !strings.Contains(h.Message, "expires") {
		t.Errorf("the health report does not mention the expiry: %q", h.Message)
	}
}

// newFor builds a plugin the way the host does, so the constructor's own work
// -- defaults, validation, dropping the credential off the retained config --
// is under test rather than bypassed.
func newFor(t *testing.T, cfg Config, hc *http.Client) *Plugin {
	t.Helper()
	p, err := New(plugins.Deps{
		Instance: "extremecloudiq",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTP:     hc,
		Now:      at(fixedNow),
	}, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The credential must not survive on the config a dump could reach.
	if p.cfg.APIToken != "" {
		t.Error("the retained config still carries the API token")
	}
	return p
}
