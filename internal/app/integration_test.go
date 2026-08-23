package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/config"
)

const (
	tokenScoped   = "scoped-agent-token-0000000000000000000000"
	tokenWildcard = "wildcard-admin-token-00000000000000000000"
)

// ptr supplies a value the way the startup file would.
//
// The moved settings are pointers on config.Legacy because presence is the
// question there -- a file that says nothing about a key is different from one
// that sets it to the default -- so a test that wants a deployment to have had
// a value in its file says so the same way a file does.
func ptr[T any](v T) *T { return &v }

// newTestApp builds a fully wired host against a temporary database.
func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()

	t.Setenv("MCPD_TOKEN_SCOPED", tokenScoped)
	t.Setenv("MCPD_TOKEN_WILDCARD", tokenWildcard)

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "mcpd.db")
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Legacy().Server.PublicURL = ptr("https://mcp.test.invalid")
	cfg.Plugins = map[string]config.PluginConfig{
		"echo": {Enabled: true, Required: true},
	}
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{
		{
			ID: "scoped", SecretRef: "env:MCPD_TOKEN_SCOPED",
			Principal: "svc:scoped", Role: "user",
			Plugins: []string{"echo"},
		},
		{
			ID: "wildcard", SecretRef: "env:MCPD_TOKEN_WILDCARD",
			Principal: "svc:wildcard", Role: "admin",
			Plugins: []string{"*"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(context.Background(), cfg, log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

// mcpRequest issues a JSON-RPC call against a plugin endpoint.
func mcpRequest(t *testing.T, h http.Handler, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHost_PluginEndpointIsMounted(t *testing.T) {
	a := newTestApp(t)
	if names := a.PluginNames(); len(names) != 1 || names[0] != "echo" {
		t.Fatalf("mounted plugins = %v, want [echo]", names)
	}
}

// The headline requirement: a token scoped to one plugin must not be able to
// reach another, and must not learn that the other exists.
func TestHost_PerPluginScoping(t *testing.T) {
	a := newTestApp(t)
	h := a.Handler()

	listTools := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	}

	tests := []struct {
		name       string
		path       string
		token      string
		wantStatus int
		reason     string
	}{
		{
			name: "granted plugin is reachable", path: "/mcp/echo", token: tokenScoped,
			wantStatus: http.StatusOK,
		},
		{
			name: "wildcard reaches the same plugin", path: "/mcp/echo", token: tokenWildcard,
			wantStatus: http.StatusOK,
		},
		{
			name: "ungranted plugin is indistinguishable from absent",
			path: "/mcp/proxmox", token: tokenScoped,
			wantStatus: http.StatusNotFound,
			reason:     "a scoped agent must not learn which other plugins are deployed",
		},
		{
			name: "unmounted plugin is 404 even for an admin",
			path: "/mcp/proxmox", token: tokenWildcard,
			wantStatus: http.StatusNotFound,
		},
		{
			name: "missing credential is rejected", path: "/mcp/echo", token: "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "invalid credential is rejected", path: "/mcp/echo", token: "not-a-real-token",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := mcpRequest(t, h, tc.path, tc.token, listTools)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)\nbody: %s",
					w.Code, tc.wantStatus, tc.reason, w.Body.String())
			}
		})
	}
}

// An unauthenticated request must not reveal whether a plugin exists: a 401
// for a real endpoint and a 401 for an imaginary one look identical.
func TestHost_UnauthenticatedResponsesDoNotProbePluginExistence(t *testing.T) {
	a := newTestApp(t)
	h := a.Handler()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}

	real := mcpRequest(t, h, "/mcp/echo", "", body)
	fake := mcpRequest(t, h, "/mcp/ghost", "", body)

	if real.Code != fake.Code {
		t.Fatalf("mounted plugin returned %d but unknown returned %d; "+
			"the difference lets an unauthenticated caller enumerate plugins",
			real.Code, fake.Code)
	}
}

func TestHost_ToolsListAndCall(t *testing.T) {
	a := newTestApp(t)
	h := a.Handler()

	// tools/list must expose the plugin-prefixed names.
	w := mcpRequest(t, h, "/mcp/echo", tokenScoped, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"echo_echo", "echo_status"} {
		if !strings.Contains(body, want) {
			t.Errorf("tools/list does not advertise %q\nbody: %s", want, body)
		}
	}

	// tools/call must dispatch through the host gate to the plugin handler.
	w = mcpRequest(t, h, "/mcp/echo", tokenScoped, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name":      "echo_echo",
			"arguments": map[string]any{"message": "hello mcpd"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); !strings.Contains(got, "hello mcpd") {
		t.Fatalf("tool result did not echo the message\nbody: %s", got)
	}
}

func TestHost_HealthEndpoints(t *testing.T) {
	a := newTestApp(t)
	h := a.Handler()

	// Liveness performs no dependency checks and needs no credentials.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("live status = %d", w.Code)
	}

	// Readiness aggregates component checks.
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("ready status = %d: %s", w.Code, w.Body.String())
	}
	var report struct {
		Status string `json:"status"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "up" {
		t.Fatalf("readiness = %s, want up: %s", report.Status, w.Body.String())
	}
	seen := map[string]bool{}
	for _, c := range report.Checks {
		seen[c.Name] = true
	}
	for _, want := range []string{"database", "outbox", "plugins"} {
		if !seen[want] {
			t.Errorf("readiness report omits the %s check", want)
		}
	}
}

// mcpd is no longer an authorization server, so there is nowhere to point a
// client at. The metadata document described one, and a client following it
// would be sent somewhere that cannot issue it a token.
func TestHost_ProtectedResourceMetadataIsGone(t *testing.T) {
	a := newTestApp(t)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(
		http.MethodGet, "/.well-known/oauth-protected-resource", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("metadata status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// The challenge still asks for a bearer credential; it just no longer claims
// there is a place to go and obtain one.
func TestHost_ChallengeAsksForABearerToken(t *testing.T) {
	a := newTestApp(t)
	w := mcpRequest(t, a.Handler(), "/mcp/echo", "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list",
	})
	if got := w.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q, want a plain Bearer challenge", got)
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// Every response carries a correlation ID so a caller can quote it and an
// operator can find the matching log lines.
func TestHost_CorrelationIDIsAlwaysReturned(t *testing.T) {
	a := newTestApp(t)
	h := a.Handler()

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if w.Header().Get("X-Correlation-Id") == "" {
		t.Fatal("a generated correlation ID must be returned")
	}

	// A supplied ID is honoured so a trace can span client and server.
	r := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	r.Header.Set("X-Correlation-Id", "client-supplied-123")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if got := w.Header().Get("X-Correlation-Id"); got != "client-supplied-123" {
		t.Fatalf("correlation ID = %q, want the client-supplied value", got)
	}

	// A hostile value is sanitised rather than echoed into structured logs.
	r = httptest.NewRequest(http.MethodGet, "/health/live", nil)
	r.Header.Set("X-Correlation-Id", "bad\nvalue\"with{injection}")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	got := w.Header().Get("X-Correlation-Id")
	if strings.ContainsAny(got, "\n\"{}") {
		t.Fatalf("correlation ID %q was not sanitised", got)
	}
}

// A plugin enabled in configuration but absent from the binary must fail
// startup loudly, rather than silently serving nothing at that endpoint.
func TestNew_RefusesUnknownPlugin(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCPD_TOKEN_SCOPED", tokenScoped)

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "mcpd.db")
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Plugins = map[string]config.PluginConfig{"nonexistent": {Enabled: true}}
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{{
		ID: "scoped", SecretRef: "env:MCPD_TOKEN_SCOPED",
		Principal: "svc:scoped", Role: "user", Plugins: []string{"nonexistent"},
	}}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	_, err := New(context.Background(), cfg, log)
	if err == nil {
		t.Fatal("expected startup to fail for a plugin not compiled into the binary")
	}
	if !strings.Contains(err.Error(), "not compiled into this binary") {
		t.Fatalf("error should explain the cause, got: %v", err)
	}
}

func TestSplitToolName(t *testing.T) {
	tests := []struct{ in, plugin, bare string }{
		{"echo_echo", "echo", "echo"},
		{"cnmaestro_list_devices", "cnmaestro", "list_devices"},
		{"noprefix", "noprefix", ""},
	}
	for _, tc := range tests {
		p, b, _ := splitToolName(tc.in)
		if p != tc.plugin || b != tc.bare {
			t.Errorf("splitToolName(%q) = (%q,%q), want (%q,%q)", tc.in, p, b, tc.plugin, tc.bare)
		}
	}
}

// One endpoint serving everything a credential is granted. It exists because
// a transport may only be able to target a single address -- OpenAI's tunnel
// binds one MCP server URL per tunnel -- so per-plugin endpoints would mean
// one tunnel per integration.
func TestHost_AggregateEndpoint(t *testing.T) {
	a := newTestApp(t)
	h := a.Handler()

	listTools := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	}

	// A wildcard credential sees every mounted plugin's tools.
	w := mcpRequest(t, h, "/mcp", tokenWildcard, listTools)
	if w.Code != http.StatusOK {
		t.Fatalf("aggregate endpoint = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "echo_echo") {
		t.Fatalf("the aggregate catalogue is missing echo's tools: %s", w.Body.String())
	}

	// And a call routes through to the right plugin.
	w = mcpRequest(t, h, "/mcp", tokenWildcard, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name":      "echo_echo",
			"arguments": map[string]any{"message": "via aggregate"},
		},
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "via aggregate") {
		t.Fatalf("tool call through the aggregate endpoint failed: %s", w.Body.String())
	}
}

// The aggregate endpoint is not a way around scoping. Its catalogue is exactly
// the plugins the presented credential grants.
func TestHost_AggregateRespectsScoping(t *testing.T) {
	a := newTestApp(t)
	h := a.Handler()

	// The scoped token grants only echo, which is the one mounted plugin, so
	// it sees echo and would see nothing more if others were mounted.
	w := mcpRequest(t, h, "/mcp", tokenScoped, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "echo_echo") {
		t.Fatal("a granted plugin should appear in the aggregate catalogue")
	}
	// Nothing from a plugin the token does not grant may appear.
	for _, forbidden := range []string{"proxmox_", "netbox_", "cnmaestro_"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the aggregate catalogue leaked %s tools to a scoped token", forbidden)
		}
	}
}

func TestHost_AggregateRequiresCredentials(t *testing.T) {
	a := newTestApp(t)
	h := a.Handler()

	for _, token := range []string{"", "not-a-real-token"} {
		w := mcpRequest(t, h, "/mcp", token, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/list",
		})
		if w.Code == http.StatusOK {
			t.Fatalf("the aggregate endpoint accepted token %q", token)
		}
	}
}
