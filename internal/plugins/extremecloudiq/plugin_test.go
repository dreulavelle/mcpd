package extremecloudiq

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			`"owner_id":42,"data_center":"US-EAST",`+
			`"expiration_time":"2026-11-25T12:00:00.000+0000","expires_in":7776000}`)
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

// The live API mints a session per call: issued_at is the moment of the
// request and expiration_time is seven days after it. That window is not the
// key's expiry, and treating it as one warned on every start for ever and
// pinned this plugin at degraded -- which is what a warning nobody can act on
// costs.
func TestCheck_DoesNotWarnAboutASlidingSessionWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		issued := fixedNow.Format("2006-01-02T15:04:05.000-0700")
		expires := fixedNow.Add(7 * 24 * time.Hour).Format("2006-01-02T15:04:05.000-0700")
		_, _ = io.WriteString(w, `{"user_name":"api@example.net","owner_id":42,`+
			`"issued_at":"`+issued+`","expiration_time":"`+expires+`","expires_in":604800}`)
	}))
	t.Cleanup(srv.Close)

	p := newFor(t, Config{BaseURL: srv.URL, APIToken: "tok"}, srv.Client())
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h := p.Check(context.Background()); h.State != plugins.HealthyState {
		t.Fatalf("a sliding session window was reported as a failing credential: %+v", h)
	}
}

// A token that is about to stop working is the one thing worth saying before
// it does: afterwards the API answers 401, which is indistinguishable from a
// revoked token, and somebody spends an afternoon on it.
func TestCheck_WarnsBeforeTheTokenExpires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Three days left, well inside the two-week warning, and written in
		// the API's own format -- an offset with no colon, which is not
		// RFC 3339. expires_in is sent alongside and disagrees on purpose: it
		// is the token's whole lifetime, and reading it as a countdown is the
		// bug that made this token permanently seven days from expiring.
		_, _ = io.WriteString(w, `{"user_name":"api@example.net","owner_id":42,`+
			`"expiration_time":"2026-08-30T12:00:00.000+0000","expires_in":604800}`)
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
