package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/settings"
)

// The tunnel attaches its identity to the server as middleware. Attaching that
// to a cached instance writes it into a server other callers share, and adds
// another layer every reconnect -- so the first identity answers for everyone
// and changing the role appears to save and do nothing.
func TestEachTunnelGetsItsOwnServer(t *testing.T) {
	a := newSettingsApp(t)

	first, err := a.tunnelFactory(&auth.Principal{
		ID: "svc:one", Role: auth.RoleUser, Plugins: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("building the first server: %v", err)
	}
	second, err := a.tunnelFactory(&auth.Principal{
		ID: "svc:two", Role: auth.RoleUser, Plugins: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("building the second server: %v", err)
	}

	if first == second {
		t.Fatal("two identities must not share one server")
	}
}

// Changing the role has to reach the running tunnel, which is the whole reason
// the settings store exists.
func TestChangingTheRoleReachesTheTunnel(t *testing.T) {
	t.Setenv("MCPD_SECRET_KEY", "test-encryption-key-at-least-32-chars-long")

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(t.TempDir(), "mcpd.db")
	cfg.Storage.RelaxedDurability = true
	cfg.Server.PublicURL = "http://localhost:9080"
	cfg.SecretKeyRef = "env:MCPD_SECRET_KEY"
	cfg.Plugins = map[string]config.PluginConfig{"echo": {Enabled: true}}
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{{
		ID: "local", SecretRef: "env:MCPD_TOKEN_SCOPED",
		Principal: "user:local", Role: "admin", Plugins: []string{"*"},
	}}
	t.Setenv("MCPD_TOKEN_SCOPED", tokenScoped)

	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })

	ctx := context.Background()
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelRole, Value: `"user"`},
	}); err != nil {
		t.Fatal(err)
	}

	// A connector that cannot approve cannot apply anything: approval happens
	// in the conversation and this is what carries the answer back.
	if got := a.tunnelConfig(ctx).Principal.Role; got != auth.RoleUser {
		t.Fatalf("role = %q, want approver", got)
	}
	principal := a.tunnelConfig(ctx).Principal
	if !principal.Can(auth.CapApprove) {
		t.Fatal("the connector must be able to record an approval")
	}
}
