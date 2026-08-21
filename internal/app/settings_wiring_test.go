package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/tunnel"
)

// A settings form that writes to a store nothing reads is worse than no form
// at all: it reports success and changes nothing. These tests assert that the
// components actually consult the store.

func newSettingsApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("MCPD_TOKEN_SCOPED", tokenScoped)
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(t.TempDir(), "mcpd.db")
	cfg.Storage.RelaxedDurability = true
	cfg.Server.PublicURL = "http://localhost:9080"
	cfg.SecretKeyRef = "env:MCPD_SECRET_KEY"
	cfg.Plugins = map[string]config.PluginConfig{"echo": {Enabled: true}}
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{{
		ID: "scoped", SecretRef: "env:MCPD_TOKEN_SCOPED",
		Principal: "svc:scoped", Role: "admin", Plugins: []string{"*"},
	}}
	// File defaults the store must be able to override.
	cfg.Approval.ProposalTTL = 30 * time.Minute
	cfg.Tunnel.Enabled = false

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

// The bug this test exists for: the tunnel was built from the file at startup
// and never consulted the store, so pasting credentials into the dashboard
// reported success and did nothing.
func TestTunnelConfigComesFromSettings(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if got := a.tunnelConfig(ctx); got.Enabled {
		t.Fatal("with nothing stored, the file's disabled value should hold")
	}

	const id = "tunnel_6a87964313a88191b1cf9d9bf28dde48"
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.KeyTunnelID, Value: `"` + id + `"`},
		{Key: settings.KeyTunnelAPIKey, Value: "sk-runtime-key", Secret: true},
		{Key: settings.KeyTunnelRole, Value: `"approver"`},
	}); err != nil {
		t.Fatal(err)
	}

	got := a.tunnelConfig(ctx)
	if !got.Enabled {
		t.Fatal("enabling the tunnel in settings must reach the tunnel config")
	}
	if got.TunnelID != id {
		t.Fatalf("tunnel id = %q, want the stored value", got.TunnelID)
	}
	if got.APIKey != "sk-runtime-key" {
		t.Fatal("the stored API key must reach the tunnel config")
	}
	if string(got.Principal.Role) != "approver" {
		t.Fatalf("role = %q, want the stored value", got.Principal.Role)
	}
	// A tunnel is a shared credential and must never be treated as a
	// distinguishable identity, whatever is configured.
	if got.Principal.Distinguishable {
		t.Fatal("a tunnel principal must not be distinguishable")
	}
}

// Leaving the plugin list blank means "everything I can see", not "nothing".
func TestTunnelConfig_EmptyGrantsMeanEverything(t *testing.T) {
	a := newSettingsApp(t)
	got := a.tunnelConfig(context.Background())

	if len(got.Principal.Plugins) == 0 {
		t.Fatal("an empty grant would reach nothing, which is never what a blank field meant")
	}
}

// The same class of bug applied to the approval TTLs.
func TestApprovalPolicyComesFromSettings(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if got := a.approvalPolicy(ctx).ProposalTTL; got != 30*time.Minute {
		t.Fatalf("with nothing stored, the file's %s should hold, got %s", 30*time.Minute, got)
	}

	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyApprovalProposalTTL, Value: "45"},
		{Key: settings.KeyApprovalApprovalTTL, Value: "5"},
	}); err != nil {
		t.Fatal(err)
	}

	policy := a.approvalPolicy(ctx)
	if policy.ProposalTTL != 45*time.Minute {
		t.Fatalf("proposal TTL = %s, want the stored 45m", policy.ProposalTTL)
	}
	if policy.ApprovalTTL != 5*time.Minute {
		t.Fatalf("approval TTL = %s, want the stored 5m", policy.ApprovalTTL)
	}
}

// Reading live rather than at startup is what makes a change apply to the next
// operation instead of the next restart.
func TestApprovalPolicyIsReadLive(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	before := a.opsService.PolicyForTest().ProposalTTL
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyApprovalProposalTTL, Value: "90"},
	}); err != nil {
		t.Fatal(err)
	}
	after := a.opsService.PolicyForTest().ProposalTTL

	if before == after {
		t.Fatal("the service snapshotted its policy; a change would not apply until restart")
	}
	if after != 90*time.Minute {
		t.Fatalf("policy TTL = %s, want 90m", after)
	}
}

// ChatGPT refuses to create an OAuth connector unless protected-resource
// discovery succeeds, and discovery is a tunnel command the client can only
// run against a URL. So an OAuth deployment must hand the tunnel mcpd's own
// address; in-memory leaves the connector unusable with no clue why.
func TestTunnelBindsOverHTTPWhenOAuthIsMounted(t *testing.T) {
	ctx := context.Background()

	static := newSettingsApp(t)
	if got := static.tunnelConfig(ctx).MCPServerURL; got != "" {
		t.Fatalf("MCPServerURL = %q, want in-memory under static auth", got)
	}

	oauth := newOAuthSettingsApp(t)
	got := oauth.tunnelConfig(ctx).MCPServerURL
	if got != "https://localhost:9080/mcp" {
		t.Fatalf("MCPServerURL = %q, want mcpd's own MCP endpoint", got)
	}
}

// The origin has to match the OAuth issuer, or the tunnel refuses to reach the
// authorization server's endpoints on a private address and the token exchange
// fails after the person has already approved.
func TestTunnelMCPURLSharesTheIssuerOrigin(t *testing.T) {
	a := newOAuthSettingsApp(t)

	mcpURL := a.tunnelConfig(context.Background()).MCPServerURL
	if !strings.HasPrefix(mcpURL, a.cfg.Server.PublicURL) {
		t.Fatalf("MCP URL %q must share an origin with the issuer %q",
			mcpURL, a.cfg.Server.PublicURL)
	}
}

func newOAuthSettingsApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(t.TempDir(), "mcpd.db")
	cfg.Storage.RelaxedDurability = true
	// https because an OAuth issuer must be one; the validator now refuses the
	// combination that produced "does not implement OAuth".
	cfg.Server.PublicURL = "https://localhost:9080"
	cfg.SecretKeyRef = "env:MCPD_SECRET_KEY"
	cfg.Plugins = map[string]config.PluginConfig{"echo": {Enabled: true}}
	cfg.Auth.Mode = "oauth"
	cfg.Tunnel.Enabled = false

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

// Ordering bug this guards: the tunnel was constructed before the certificate
// existed, so it was told about an empty CA path and every request it made
// back to mcpd failed with "tls: bad certificate" -- including OAuth
// discovery, which is the whole reason for serving https in the first place.
func TestTheTunnelIsToldAboutOurCertificate(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "mcpd.db")
	cfg.Storage.RelaxedDurability = true
	cfg.Server.PublicURL = "https://127.0.0.1:9080"
	cfg.Server.TLS = config.TLS{Mode: "self-signed", Dir: filepath.Join(dir, "tls")}
	cfg.SecretKeyRef = "env:MCPD_SECRET_KEY"
	cfg.Plugins = map[string]config.PluginConfig{"echo": {Enabled: true}}
	cfg.Auth.Mode = "oauth"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })

	if a.tls == nil {
		t.Fatal("self-signed mode must produce certificate material")
	}
	got := a.tunnelConfig(context.Background()).TrustedCAFile
	if got == "" {
		t.Fatal("the tunnel must be told about the CA, or it cannot reach mcpd over https")
	}
	if got != a.tls.CAPath {
		t.Fatalf("TrustedCAFile = %q, want the CA mcpd issued from (%q)", got, a.tls.CAPath)
	}
}

// The point of a per-plugin tunnel: its connector reaches that system's
// endpoint and cannot discover any other. If it bound the aggregate instead,
// the separation would be cosmetic.
func TestAPerPluginTunnelBindsThatPluginsEndpoint(t *testing.T) {
	a := newOAuthSettingsApp(t)
	ctx := context.Background()

	const (
		main = "tunnel_0123456789abcdef0123456789abcdef"
		echo = "tunnel_1123456789abcdef0123456789abcdef"
	)
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.KeyTunnelID, Value: `"` + main + `"`},
		{Key: settings.PluginTunnelKey("echo"), Value: `"` + echo + `"`},
	}); err != nil {
		t.Fatal(err)
	}

	configs := a.tunnelConfigs(ctx)
	if len(configs) != 2 {
		t.Fatalf("got %d tunnels, want the aggregate plus echo", len(configs))
	}

	var scoped *tunnel.Config
	for i := range configs {
		if configs[i].Plugin == "echo" {
			scoped = &configs[i]
		}
	}
	if scoped == nil {
		t.Fatal("no tunnel was built for echo")
	}
	if !strings.HasSuffix(scoped.MCPServerURL, "/mcp/echo") {
		t.Errorf("MCPServerURL = %q, want echo's own endpoint", scoped.MCPServerURL)
	}
	if scoped.TunnelID != echo {
		t.Errorf("TunnelID = %q, want the id stored for echo", scoped.TunnelID)
	}
	if len(scoped.Principal.Plugins) != 1 || scoped.Principal.Plugins[0] != "echo" {
		t.Errorf("Plugins = %v, want echo alone", scoped.Principal.Plugins)
	}
}

// Two clients on one tunnel id compete for the same commands, so the reuse has
// to be refused rather than silently halving throughput.
func TestAPerPluginTunnelCannotReuseTheMainTunnelID(t *testing.T) {
	a := newOAuthSettingsApp(t)
	ctx := context.Background()

	const id = "tunnel_0123456789abcdef0123456789abcdef"
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.KeyTunnelID, Value: `"` + id + `"`},
		{Key: settings.PluginTunnelKey("echo"), Value: `"` + id + `"`},
	}); err != nil {
		t.Fatal(err)
	}

	configs := a.tunnelConfigs(ctx)
	if len(configs) != 1 {
		t.Fatalf("got %d tunnels, want only the main one", len(configs))
	}
}

// Tunnels are made and assigned on the Tunnels page, where the id comes from
// the tunnel that was just created. A settings field asking an operator to
// paste that id back in would ask them to copy a value the app already has.
func TestSettingsDoesNotOfferPerPluginTunnelFields(t *testing.T) {
	for _, g := range settings.Schema() {
		for _, f := range g.Fields {
			if settings.PluginFromTunnelKey(f.Key) != "" {
				t.Errorf("settings still offers %q", f.Key)
			}
		}
	}
}
