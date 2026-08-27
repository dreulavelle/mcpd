package tunnel

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
)

func testPrincipal() auth.Principal {
	return auth.Principal{
		ID: "svc:chatgpt", Role: auth.RoleUser, Plugins: []string{"echo"},
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
			Principal: auth.Principal{ID: "svc:x", Role: auth.RoleUser}}, "plugin"},
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
	}, func(*auth.Principal) (*mcp.Server, error) {
		return mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil), nil
	}, discardLog())

	err := m.Start(context.Background())
	defer m.Stop(context.Background())

	// Start no longer blocks on readiness, so it may succeed here and fail
	// asynchronously. Either way, nothing may carry the key.
	if err != nil && strings.Contains(err.Error(), key) {
		t.Fatalf("the error leaked the API key: %v", err)
	}
	if strings.Contains(m.Status().Message, key) {
		t.Fatal("the status message leaked the API key")
	}
}

// A healthy but idle tunnel is slow to report ready, because the client only
// does so after a completed control-plane poll and an empty poll waits out its
// timeout. Blocking on that tore down working tunnels.
func TestManager_StartDoesNotBlockOnReadiness(t *testing.T) {
	// A control plane that accepts but never returns from a poll.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	}))
	defer srv.Close()

	m := NewManager(Config{
		TunnelID:            "tunnel_0123456789abcdef0123456789abcdef",
		APIKey:              "sk-runtime",
		Principal:           testPrincipal(),
		ControlPlaneBaseURL: srv.URL,
	}, func(*auth.Principal) (*mcp.Server, error) {
		return mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil), nil
	}, discardLog())

	done := make(chan error, 1)
	go func() { done <- m.Start(context.Background()) }()

	select {
	case <-done:
		// Returned promptly, which is the point.
	case <-time.After(10 * time.Second):
		t.Fatal("Start blocked waiting for readiness; an idle tunnel would be " +
			"torn down despite working")
	}

	if state := m.Status().State; state == StateFailed {
		t.Fatal("a tunnel that has not yet completed its first poll must not " +
			"be reported as failed")
	}
	_ = m.Stop(context.Background())
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

// Two settings changes arriving together must not interleave.
//
// The dashboard's settings watcher spawns a goroutine per change, so two saves
// in quick succession run two Reconfigures against the same manager at the
// same time. Each does stop, replace, then start — and before the ops lock
// existed nothing serialised those three, so one reconfiguration's Start could
// run against the other's configuration.
//
// It surfaced as a data race rather than as a wrong tunnel: Start read m.cfg
// after releasing the field lock, including from two goroutines that outlive
// it, while Reconfigure wrote it. CI caught it under -race on a run where an
// unrelated change had made the server factory slow enough to widen the
// window. That is the shape reproduced here — Enabled is set, because
// Reconfigure returns before ever reaching Start without it, and Start is
// where the racing read lives.
func TestReconfigure_DoesNotInterleaveWithAnother(t *testing.T) {
	// A control plane that accepts the connection and then says nothing, so
	// the tunnel gets far enough to leave its goroutines behind — which are
	// the ones whose reads of the configuration raced.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	// Slow enough that two reconfigurations genuinely overlap, which is the
	// condition CI hit by accident when a fifth plugin made building the
	// server slower. It reads nothing, so anything the detector reports is the
	// manager's own doing.
	slow := func(*auth.Principal) (*mcp.Server, error) {
		time.Sleep(2 * time.Millisecond)
		return mcp.NewServer(&mcp.Implementation{Name: "t", Version: "1"}, nil), nil
	}

	const (
		idA = "tunnel_0123456789abcdef0123456789abcdef"
		idB = "tunnel_fedcba9876543210fedcba9876543210"
	)
	config := func(i int) Config {
		c := Config{
			Enabled: true, Plugin: "echo", TunnelID: idA, APIKey: "sk-a",
			Principal:           auth.Principal{ID: "svc:a", Role: auth.RoleUser, Plugins: []string{"echo"}, TokenID: "a"},
			ControlPlaneBaseURL: srv.URL,
		}
		if i%2 == 1 {
			c.TunnelID, c.APIKey = idB, "sk-b"
			c.Principal = auth.Principal{ID: "svc:b", Role: auth.RoleUser, Plugins: []string{"echo"}, TokenID: "b"}
		}
		return c
	}

	m := NewManager(config(0), slow, discardLog())
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Not asserted on: against a control plane that answers nothing
			// these may fail, and the failure is not what this is about. What
			// must hold is that the race detector stays quiet.
			_ = m.Reconfigure(ctx, config(i))
		}(i)
	}
	wg.Wait()

	// Whichever reconfiguration won, the manager holds one whole configuration
	// rather than a mixture. A tunnel id from one save beside a key from
	// another would authenticate as the wrong ChatGPT workspace.
	got := m.Config()
	switch got.TunnelID {
	case idA:
		if got.APIKey != "sk-a" || got.Principal.ID != "svc:a" {
			t.Errorf("mixed configuration: tunnel %s with key %q and principal %q",
				got.TunnelID, got.APIKey, got.Principal.ID)
		}
	case idB:
		if got.APIKey != "sk-b" || got.Principal.ID != "svc:b" {
			t.Errorf("mixed configuration: tunnel %s with key %q and principal %q",
				got.TunnelID, got.APIKey, got.Principal.ID)
		}
	default:
		t.Errorf("the manager ended on a tunnel id nobody configured: %q", got.TunnelID)
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = m.Stop(stopCtx)
}
