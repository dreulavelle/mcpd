package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
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
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Legacy().Server.PublicURL = ptr("http://localhost:9080")
	cfg.SecretKeyRef = "env:MCPD_SECRET_KEY"
	cfg.Plugins = map[string]config.PluginConfig{"echo": {Enabled: true}}
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{{
		ID: "scoped", SecretRef: "env:MCPD_TOKEN_SCOPED",
		Principal: "svc:scoped", Role: "admin", Plugins: []string{"*"},
	}}
	// File defaults the store must be able to override.
	cfg.Legacy().Approval.ProposalTTL = ptr(30 * time.Minute)
	cfg.Legacy().Tunnel.Enabled = ptr(false)

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

// addAccount stores one ChatGPT account and returns it.
//
// Most tunnel tests want an account only so that a tunnel has a credential to
// connect with; a single stored account resolves without any assignment, which
// is what a deployment that has never thought about accounts has.
func addAccount(t *testing.T, a *App, name string, plugins []string) tunnel.Account {
	t.Helper()
	acct, err := a.chatgpt.Create(context.Background(), "user:test", tunnel.Account{
		Name:    name,
		APIKey:  "sk-runtime-key-" + name,
		RoleID:  auth.RoleOperator,
		Grants:  auth.GrantsAt(plugins, auth.LevelWrite),
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("creating the %s account: %v", name, err)
	}
	return acct
}

// The bug this test exists for: the tunnel was built from the file at startup
// and never consulted the store, so pasting credentials into the dashboard
// reported success and did nothing. The credential moved onto an account since,
// and the same thing has to hold of it.
func TestTunnelConfigComesFromSettingsAndTheAccount(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if got := a.tunnelConfig(ctx); got.Enabled {
		t.Fatal("with nothing stored, the file's disabled value should hold")
	}

	acct := addAccount(t, a, "Work", nil)
	const id = "tunnel_6a87964313a88191b1cf9d9bf28dde48"
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey(id), Value: `"*"`},
	}); err != nil {
		t.Fatal(err)
	}

	configs := a.tunnelConfigs(ctx)
	if len(configs) != 1 {
		t.Fatalf("got %d tunnels, want the aggregate", len(configs))
	}
	got := configs[0]
	if !got.Enabled {
		t.Fatal("enabling the tunnel in settings must reach the tunnel config")
	}
	if got.TunnelID != id {
		t.Fatalf("tunnel id = %q, want the stored value", got.TunnelID)
	}
	if got.APIKey != acct.APIKey {
		t.Fatal("the account's API key must reach the tunnel config")
	}
	if got.Principal.RoleID != auth.RoleOperator {
		t.Fatalf("role = %q, want the account's", got.Principal.RoleID)
	}
	if got.AccountID != acct.ID {
		t.Fatalf("account = %q, want %q -- a tunnel has to say whose it is",
			got.AccountID, acct.ID)
	}
}

// Leaving the plugin list blank means "everything I can see", not "nothing".
func TestTunnelConfig_EmptyGrantsMeanEverything(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	addAccount(t, a, "Work", nil)
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
	if len(configs[0].Principal.Grants.Plugins()) == 0 {
		t.Fatal("an empty grant would reach nothing, which is never what a blank field meant")
	}
}

// A tunnel with no account cannot start. Falling back to some other account's
// key would have a connector quietly authenticate as the wrong workspace,
// which is worse than a tunnel that does not come up.
func TestATunnelWithoutAnAccountDoesNotStart(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey("tunnel_6a87964313a88191b1cf9d9bf28dde48"), Value: `"*"`},
	}); err != nil {
		t.Fatal(err)
	}
	if got := a.tunnelConfigs(ctx); len(got) != 0 {
		t.Fatalf("got %d tunnels with no account stored, want none", len(got))
	}
}

// With several accounts an unassigned tunnel is ambiguous rather than obvious,
// and picking one would be picking whose credential a connector uses.
func TestAnUnassignedTunnelIsAmbiguousWithTwoAccounts(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	addAccount(t, a, "Work", nil)
	second := addAccount(t, a, "Home", nil)
	const id = "tunnel_6a87964313a88191b1cf9d9bf28dde48"
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey(id), Value: `"*"`},
	}); err != nil {
		t.Fatal(err)
	}
	if got := a.tunnelConfigs(ctx); len(got) != 0 {
		t.Fatalf("got %d tunnels, want none until an account is named", len(got))
	}

	// Naming one settles it, and the named one is the one used.
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.TunnelAccountKey(id), Value: `"` + second.ID + `"`},
	}); err != nil {
		t.Fatal(err)
	}
	configs := a.tunnelConfigs(ctx)
	if len(configs) != 1 {
		t.Fatalf("got %d tunnels once an account was named, want one", len(configs))
	}
	if configs[0].APIKey != second.APIKey {
		t.Error("the tunnel used a different account's credential than the one named")
	}
}

// An account bounds what its tunnels reach. A per-plugin tunnel on an account
// not granted that plugin must not start: assigning a tunnel to an account can
// only ever reduce what it reaches, never widen it.
func TestAnAccountBoundsWhatItsTunnelsReach(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	addAccount(t, a, "Narrow", []string{"something-else"})
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey("tunnel_1123456789abcdef0123456789abcdef"),
			Value: `"echo"`},
	}); err != nil {
		t.Fatal(err)
	}
	if got := a.tunnelConfigs(ctx); len(got) != 0 {
		t.Fatalf("got %d tunnels, want none: the account cannot reach echo", len(got))
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

// The point of a per-plugin tunnel: its connector reaches that system and
// cannot discover any other. In process that separation is the principal's,
// so it is the principal this asserts.
func TestAPerPluginTunnelBindsThatPluginsEndpoint(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	addAccount(t, a, "Work", nil)

	const (
		main = "tunnel_0123456789abcdef0123456789abcdef"
		echo = "tunnel_1123456789abcdef0123456789abcdef"
	)
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey(main), Value: `"*"`},
		{Key: settings.TunnelPluginKey(echo), Value: `"echo"`},
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
	if scoped.TunnelID != echo {
		t.Errorf("TunnelID = %q, want the id stored for echo", scoped.TunnelID)
	}
	if got := scoped.Principal.Grants.Plugins(); len(got) != 1 || got[0] != "echo" {
		t.Errorf("Plugins = %v, want echo alone", got)
	}
}

// Two clients on one tunnel id compete for the same commands, so the reuse has
// to be refused rather than silently halving throughput.
func TestAPerPluginTunnelCannotReuseTheMainTunnelID(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	addAccount(t, a, "Work", nil)

	const id = "tunnel_0123456789abcdef0123456789abcdef"
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey(id), Value: `"*"`},
	}); err != nil {
		t.Fatal(err)
	}
	// One key per tunnel, so a second assignment of the same id is a
	// replacement, not a second client competing for the same commands.
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.TunnelPluginKey(id), Value: `"echo"`},
	}); err != nil {
		t.Fatal(err)
	}

	configs := a.tunnelConfigs(ctx)
	if len(configs) != 1 || configs[0].Plugin != "echo" {
		t.Fatalf("got %+v, want one tunnel, now serving echo", configs)
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

// A nil store still compiles and still satisfies its interface, so the wiring
// only fails when something reads through it -- which was a panic in the
// dashboard rather than an error at startup.
func TestHistoryIsReadableAndPrunable(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if _, err := a.audit.Recent(ctx, 10); err != nil {
		t.Fatalf("reading history: %v", err)
	}
	if _, err := a.audit.Prune(ctx, "user:test", time.Now(), time.Now()); err != nil {
		t.Fatalf("pruning history: %v", err)
	}
	if broken, err := a.audit.VerifyChain(ctx); err != nil || broken != 0 {
		t.Fatalf("VerifyChain = %d, %v", broken, err)
	}
}

// Scoping a connector to one plugin has to hold over the in-process binding
// too, or turning sign-in off would quietly widen every per-plugin connector
// to everything.
func TestAPerPluginTunnelIsScopedWithoutSignIn(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	addAccount(t, a, "Work", nil)

	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey("tunnel_1123456789abcdef0123456789abcdef"),
			Value: `"echo"`},
	}); err != nil {
		t.Fatal(err)
	}

	var scoped *tunnel.Config
	for i, c := range a.tunnelConfigs(ctx) {
		if c.Plugin == "echo" {
			scoped = &a.tunnelConfigs(ctx)[i]
		}
	}
	if scoped == nil {
		t.Fatal("no tunnel was built for echo")
	}
	if got := scoped.Principal.Grants.Plugins(); len(got) != 1 || got[0] != "echo" {
		t.Errorf("Plugins = %v, want echo alone -- that is what scopes it in process", got)
	}
}
