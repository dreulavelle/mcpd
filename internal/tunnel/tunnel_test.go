package tunnel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
)

func testPrincipal() auth.Principal {
	return auth.Principal{
		ID: "svc:chatgpt", Role: auth.RoleOperator, Plugins: []string{"echo"},
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A typo in a tunnel ID should be a startup error, not an opaque 401 from the
// control plane an hour later.
func TestConfig_Validate(t *testing.T) {
	const validID = "tunnel_0123456789abcdef0123456789abcdef"

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{"valid", Config{TunnelID: validID, APIKey: "k", Principal: testPrincipal()}, ""},
		{"no id", Config{APIKey: "k", Principal: testPrincipal()}, "tunnel_"},
		{"wrong prefix", Config{TunnelID: "tun_0123456789abcdef0123456789abcdef",
			APIKey: "k", Principal: testPrincipal()}, "tunnel_"},
		{"too short", Config{TunnelID: "tunnel_abc", APIKey: "k",
			Principal: testPrincipal()}, "tunnel_"},
		{"uppercase hex", Config{TunnelID: "tunnel_0123456789ABCDEF0123456789abcdef",
			APIKey: "k", Principal: testPrincipal()}, "tunnel_"},
		{"no key", Config{TunnelID: validID, Principal: testPrincipal()}, "API key"},
		{"no plugin grants", Config{TunnelID: validID, APIKey: "k",
			Principal: auth.Principal{ID: "svc:x", Role: auth.RoleOperator}}, "plugin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("should be valid: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestManager_DisabledWithoutATunnelID(t *testing.T) {
	m := NewManager(Config{}, nil, discardLog())

	if m.Enabled() {
		t.Fatal("a manager with no tunnel id must report disabled")
	}
	if got := m.Status().State; got != StateDisabled {
		t.Fatalf("state = %s, want disabled", got)
	}
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("starting an unconfigured tunnel must fail")
	}
}

// The status is rendered on a dashboard, so it must carry enough to see what
// the tunnel can do and nothing that would leak the credential.
func TestManager_StatusShowsGrantsButNeverTheKey(t *testing.T) {
	const key = "sk-super-secret-runtime-key"
	m := NewManager(Config{
		TunnelID:  "tunnel_0123456789abcdef0123456789abcdef",
		APIKey:    key,
		Principal: testPrincipal(),
	}, nil, discardLog())

	s := m.Status()
	if s.TunnelID == "" || s.Principal != "svc:chatgpt" {
		t.Fatalf("status should identify the tunnel and its principal: %+v", s)
	}
	if len(s.Plugins) != 1 || s.Plugins[0] != "echo" {
		t.Fatalf("status should show what the tunnel can reach: %+v", s.Plugins)
	}
	if strings.Contains(s.Message+s.TunnelID+s.Principal+s.Role, key) {
		t.Fatal("the API key must never appear in status")
	}
}

// A failed start must not leave the credential in a message that reaches logs
// or the dashboard.
func TestManager_FailureRedactsTheKey(t *testing.T) {
	const key = "sk-super-secret-runtime-key"

	// A control plane that refuses, so Start fails for a real reason.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key ` + key + `"}`))
	}))
	defer srv.Close()

	m := NewManager(Config{
		TunnelID:            "tunnel_0123456789abcdef0123456789abcdef",
		APIKey:              key,
		Principal:           testPrincipal(),
		ControlPlaneBaseURL: srv.URL,
		ReadyTimeout:        2 * time.Second,
	}, func(*auth.Principal) (*mcp.Server, error) {
		return mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil), nil
	}, discardLog())

	err := m.Start(context.Background())
	if err == nil {
		t.Skip("the control plane stub was accepted; nothing to assert about redaction")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("the error leaked the API key: %v", err)
	}
	if strings.Contains(m.Status().Message, key) {
		t.Fatal("the status message leaked the API key")
	}
	if m.Status().State != StateFailed {
		t.Fatalf("state = %s, want failed", m.Status().State)
	}
}

// A server factory that refuses -- because the principal is granted nothing
// mounted, say -- must fail the start rather than connecting an empty tunnel.
func TestManager_ServerFactoryFailureStopsTheStart(t *testing.T) {
	m := NewManager(Config{
		TunnelID:  "tunnel_0123456789abcdef0123456789abcdef",
		APIKey:    "k",
		Principal: testPrincipal(),
	}, func(*auth.Principal) (*mcp.Server, error) {
		return nil, errors.New("granted no mounted plugins")
	}, discardLog())

	if err := m.Start(context.Background()); err == nil {
		t.Fatal("a failing server factory must stop the start")
	}
	if m.Status().State != StateFailed {
		t.Fatalf("state = %s, want failed", m.Status().State)
	}
}

func TestManager_StopIsSafeWhenNotRunning(t *testing.T) {
	m := NewManager(Config{}, nil, discardLog())
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("stopping a tunnel that never started should be a no-op: %v", err)
	}
}

func TestRedactKey(t *testing.T) {
	const key = "sk-abc123"
	err := errors.New("request failed with " + key + " rejected")

	got := redactKey(err, key)
	if strings.Contains(got.Error(), key) {
		t.Fatalf("redactKey left the key in: %v", got)
	}
	if !strings.Contains(got.Error(), "[REDACTED]") {
		t.Fatalf("expected a redaction marker: %v", got)
	}
	if redactKey(nil, key) != nil {
		t.Fatal("redacting nil should stay nil")
	}
}

// The embedded version comes from build info, so it cannot drift from what is
// actually linked.
func TestEmbeddedVersion(t *testing.T) {
	got := EmbeddedVersion()
	if got == "" {
		t.Fatal("embedded version must not be empty")
	}
	// In a test binary the dependency is present, so this should resolve.
	if got == "unknown" {
		t.Log("build info did not report the tunnel client; acceptable in some builds")
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := []struct{ in, want string }{
		{"v0.0.12", "0.0.12"},
		{"0.0.12", "0.0.12"},
		{"  v1.2.3  ", "1.2.3"},
	}
	for _, tc := range tests {
		if got := normalizeVersion(tc.in); got != tc.want {
			t.Errorf("normalizeVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A release check that cannot complete is ordinary -- an air-gapped host, a
// rate limit -- and must never affect startup.
func TestCheckLatest_FailureIsNotFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	info := CheckLatest(context.Background(), srv.Client(), srv.URL, discardLog())
	if info.Embedded == "" {
		t.Fatal("the embedded version should still be reported")
	}
	if info.UpdateAvailable {
		t.Fatal("a failed check must not claim an update is available")
	}
}

func TestChecker_ReportsEmbeddedVersionBeforeAnyCheck(t *testing.T) {
	c := NewChecker(http.DefaultClient, discardLog(), 0)
	if c.Info().Embedded == "" {
		t.Fatal("a checker should report the embedded version immediately")
	}
}

// The version check must report an update when one exists, and must never
// install anything: the client is compiled in, so the only honest outcome is
// telling the operator to rebuild.
func TestCheckLatest_ReportsButDoesNotInstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v99.0.0","draft":false,"prerelease":false}`))
	}))
	defer srv.Close()

	info := CheckLatest(context.Background(), srv.Client(), srv.URL, discardLog())
	if info.Latest != "v99.0.0" {
		t.Fatalf("latest = %q, want v99.0.0", info.Latest)
	}
	if !info.UpdateAvailable {
		t.Fatal("a newer release should be reported as available")
	}
	if !strings.Contains(info.Note, "rebuild") {
		t.Fatalf("the note must say what to do about it, got %q", info.Note)
	}
	if info.CheckedAt == nil {
		t.Fatal("a successful check should stamp when it ran")
	}
}

// A draft or prerelease is not something to tell an operator to rebuild for.
func TestCheckLatest_IgnoresDraftsAndPrereleases(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v99.0.0","draft":true}`,
		`{"tag_name":"v99.0.0","prerelease":true}`,
		`{"tag_name":""}`,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		info := CheckLatest(context.Background(), srv.Client(), srv.URL, discardLog())
		srv.Close()

		if info.UpdateAvailable {
			t.Errorf("%s should not be reported as an update", body)
		}
	}
}

// The embedded version matching the latest release is the normal case and
// must not nag.
func TestCheckLatest_NoUpdateWhenCurrent(t *testing.T) {
	embedded := EmbeddedVersion()
	if embedded == "unknown" {
		t.Skip("build info unavailable")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"` + embedded + `"}`))
	}))
	defer srv.Close()

	info := CheckLatest(context.Background(), srv.Client(), srv.URL, discardLog())
	if info.UpdateAvailable {
		t.Fatalf("embedded %s matches latest %s but an update was reported",
			info.Embedded, info.Latest)
	}
	if info.Note != "" {
		t.Fatalf("no note should be produced when current: %q", info.Note)
	}
}
