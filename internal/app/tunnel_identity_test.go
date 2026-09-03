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
	"github.com/spoked/mcpd/internal/tunnel"
)

// The tunnel attaches its identity to the server as middleware. Attaching that
// to a cached instance writes it into a server other callers share, and adds
// another layer every reconnect -- so the first identity answers for everyone
// and changing the role appears to save and do nothing.
func TestEachTunnelGetsItsOwnServer(t *testing.T) {
	a := newSettingsApp(t)

	operator, ok := auth.BuiltinRole(auth.RoleOperator)
	if !ok {
		t.Fatal("role_operator must be a built-in role")
	}

	first, err := a.tunnelFactory(&auth.Principal{
		ID: "svc:one", RoleID: operator.ID, RoleName: operator.Name,
		Permissions: operator.Permissions,
		Grants:      auth.GrantsAt([]string{"echo"}, auth.LevelWrite),
	})
	if err != nil {
		t.Fatalf("building the first server: %v", err)
	}
	second, err := a.tunnelFactory(&auth.Principal{
		ID: "svc:two", RoleID: operator.ID, RoleName: operator.Name,
		Permissions: operator.Permissions,
		Grants:      auth.GrantsAt([]string{"echo"}, auth.LevelWrite),
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
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Legacy().Server.PublicURL = ptr("http://localhost:9080")
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
	acct := addAccount(t, a, "Work", nil)
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey("tunnel_6a87964313a88191b1cf9d9bf28dde48"), Value: `"*"`},
	}); err != nil {
		t.Fatal(err)
	}

	configs := a.tunnelConfigs(ctx)
	if len(configs) != 1 {
		t.Fatalf("got %d tunnels, want the aggregate", len(configs))
	}

	// A connector that cannot approve cannot apply anything: approval happens
	// in the conversation and this is what carries the answer back.
	principal := configs[0].Principal
	if principal.RoleID != auth.RoleOperator {
		t.Fatalf("role = %q, want the account's", principal.RoleID)
	}
	if !principal.Can(auth.PermApprovalsDecide) {
		t.Fatal("the connector must be able to record an approval")
	}

	// And changing it on the account reaches the tunnel, which is the whole
	// reason the account is read at build time rather than at startup.
	admin := auth.RoleAdministrator
	if _, err := a.chatgpt.Update(ctx, "user:test", acct.ID,
		tunnel.AccountUpdate{RoleID: &admin}); err != nil {
		t.Fatal(err)
	}
	if got := a.tunnelConfigs(ctx)[0].Principal.RoleID; got != auth.RoleAdministrator {
		t.Fatalf("role = %q after the account changed, want admin", got)
	}
}

// Narrowing a per-plugin tunnel to the one system it serves keeps the
// account's level for that system -- write stays write. Demoting it to read
// merely because a tunnel names one plugin would leave a connector granted
// write on its account unable to propose anything through the tunnel meant
// to carry exactly that.
func TestPerPluginNarrowingKeepsTheAccountsLevelForThatPlugin(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if _, err := a.chatgpt.Create(ctx, "user:test", tunnel.Account{
		Name:    "Mixed",
		APIKey:  "sk-runtime-key-mixed",
		Grants:  auth.Grants{{Plugin: "echo", Level: auth.LevelWrite}},
		Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey("tunnel_1123456789abcdef0123456789abcdef"),
			Value: `"echo"`},
	}); err != nil {
		t.Fatal(err)
	}

	configs := a.tunnelConfigs(ctx)
	if len(configs) != 1 {
		t.Fatalf("got %d tunnels, want the one for echo", len(configs))
	}
	if lvl := configs[0].Principal.Grants.LevelFor("echo"); lvl != auth.LevelWrite {
		t.Fatalf("level for echo = %q, want write -- narrowing to one plugin "+
			"must keep the account's level for it", lvl)
	}
}
