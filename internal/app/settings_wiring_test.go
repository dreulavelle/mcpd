package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/settings"
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
