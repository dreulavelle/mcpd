package tunnel

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
)

func groupConfig(plugin, id string) Config {
	cfg := testConfig()
	cfg.Plugin = plugin
	cfg.TunnelID = id
	return cfg
}

func testFactory() ServerFactory {
	return func(*auth.Principal) (*mcp.Server, error) {
		return mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil), nil
	}
}

// A tunnel forwards to one endpoint, so one connector per system means one
// tunnel per system. The group has to actually run all of them.
func TestEachPluginGetsItsOwnTunnel(t *testing.T) {
	g := NewGroup(discardLogger())
	ctx := context.Background()

	err := g.Apply(ctx, []Config{
		groupConfig("", "tunnel_0123456789abcdef0123456789abcdef"),
		groupConfig("echo", "tunnel_1123456789abcdef0123456789abcdef"),
		groupConfig("cnmaestro", "tunnel_2123456789abcdef0123456789abcdef"),
	}, testFactory())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop(ctx) })

	statuses := g.Status()
	if len(statuses) != 3 {
		t.Fatalf("got %d tunnels, want 3", len(statuses))
	}
	if statuses[0].Plugin != "" {
		t.Errorf("first tunnel serves %q, want the aggregate", statuses[0].Plugin)
	}
	if g.Lookup("echo") == nil || g.Lookup("cnmaestro") == nil {
		t.Error("every configured plugin must have a tunnel")
	}
}

// Restarting drops a connector until it reconnects, so a save that changed
// something else must leave a working tunnel alone.
func TestUnchangedTunnelsAreNotRestarted(t *testing.T) {
	g := NewGroup(discardLogger())
	ctx := context.Background()

	echo := groupConfig("echo", "tunnel_1123456789abcdef0123456789abcdef")
	if err := g.Apply(ctx, []Config{echo}, testFactory()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop(ctx) })

	before := g.Lookup("echo")
	if err := g.Apply(ctx, []Config{echo}, testFactory()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if g.Lookup("echo") != before {
		t.Fatal("an unchanged tunnel must not be rebuilt")
	}
}

// Removing a plugin's tunnel id has to actually stop it, or a connector keeps
// working after the operator believes they revoked it.
func TestARemovedTunnelIsStopped(t *testing.T) {
	g := NewGroup(discardLogger())
	ctx := context.Background()

	if err := g.Apply(ctx, []Config{
		groupConfig("echo", "tunnel_1123456789abcdef0123456789abcdef"),
	}, testFactory()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := g.Apply(ctx, nil, testFactory()); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if g.Lookup("echo") != nil {
		t.Fatal("a tunnel no longer configured must be stopped and forgotten")
	}
	if len(g.Status()) != 0 {
		t.Fatal("status must not report a tunnel that is gone")
	}
}

// The control plane allows one client per tunnel; two would compete for the
// same commands and neither would work reliably.
func TestTheSamePluginCannotBeConfiguredTwice(t *testing.T) {
	g := NewGroup(discardLogger())

	err := g.Apply(context.Background(), []Config{
		groupConfig("echo", "tunnel_1123456789abcdef0123456789abcdef"),
		groupConfig("echo", "tunnel_2123456789abcdef0123456789abcdef"),
	}, testFactory())
	if err == nil {
		t.Fatal("two tunnels for one plugin must be refused")
	}
	if !strings.Contains(err.Error(), "echo") {
		t.Fatalf("error = %v, want it to name the plugin", err)
	}
}

// One bad tunnel id must cost that connector, not every connector.
func TestOneFailingTunnelDoesNotStopTheOthers(t *testing.T) {
	g := NewGroup(discardLogger())
	ctx := context.Background()

	broken := groupConfig("echo", "not-a-tunnel-id")
	good := groupConfig("cnmaestro", "tunnel_2123456789abcdef0123456789abcdef")

	if err := g.Apply(ctx, []Config{broken, good}, testFactory()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	err := g.Start(ctx)
	t.Cleanup(func() { _ = g.Stop(ctx) })
	if err == nil {
		t.Fatal("a malformed tunnel id must be reported")
	}

	var echoState, cnState State
	for _, s := range g.Status() {
		switch s.Plugin {
		case "echo":
			echoState = s.State
		case "cnmaestro":
			cnState = s.State
		}
	}
	if echoState != StateFailed {
		t.Errorf("echo state = %q, want failed", echoState)
	}
	if cnState == StateFailed {
		t.Error("a working tunnel must not be failed by an unrelated one")
	}
}

// Status is what the dashboard renders, and it has to name which connector is
// which or two rows look identical.
func TestStatusNamesTheSystemEachTunnelServes(t *testing.T) {
	g := NewGroup(discardLogger())
	ctx := context.Background()

	if err := g.Apply(ctx, []Config{
		groupConfig("echo", "tunnel_1123456789abcdef0123456789abcdef"),
	}, testFactory()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop(ctx) })

	statuses := g.Status()
	if len(statuses) != 1 || statuses[0].Plugin != "echo" {
		t.Fatalf("status = %+v, want it to name echo", statuses)
	}
}

func TestSameAsIgnoresPluginOrder(t *testing.T) {
	a := groupConfig("", "tunnel_0123456789abcdef0123456789abcdef")
	a.Principal.Plugins = []string{"echo", "cnmaestro"}
	b := a
	b.Principal.Plugins = []string{"cnmaestro", "echo"}

	m := NewManager(a, testFactory(), discardLogger())
	if !m.SameAs(b) {
		t.Fatal("the same grants listed in a different order are not a change")
	}
}

// An HTTP-bound tunnel client probes mcpd as it starts. Connecting before the
// host is answering means that probe fails and stays failed, so configuring
// and connecting have to be separable.
func TestApplyConfiguresWithoutConnecting(t *testing.T) {
	g := NewGroup(discardLogger())
	ctx := context.Background()

	if err := g.Apply(ctx, []Config{
		groupConfig("echo", "tunnel_1123456789abcdef0123456789abcdef"),
	}, testFactory()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	t.Cleanup(func() { _ = g.Stop(ctx) })

	statuses := g.Status()
	if len(statuses) != 1 {
		t.Fatalf("got %d tunnels, want the one configured", len(statuses))
	}
	if statuses[0].State == StateConnected || statuses[0].State == StateStarting {
		t.Fatalf("state = %q, want it configured but not connected", statuses[0].State)
	}
}

// SameAs decides whether a saved setting reaches the running tunnel. A field
// missing from it is a setting that reports success and changes nothing --
// which is worse than one that fails, because it looks like it worked.
func TestSameAsComparesEveryFieldThatChangesBehaviour(t *testing.T) {
	base := groupConfig("echo", "tunnel_1123456789abcdef0123456789abcdef")
	m := NewManager(base, testFactory(), discardLogger())

	changed := []struct {
		name  string
		apply func(*Config)
	}{
		{"enabled", func(c *Config) { c.Enabled = !c.Enabled }},
		{"plugin", func(c *Config) { c.Plugin = "other" }},
		{"tunnel id", func(c *Config) { c.TunnelID = "tunnel_2123456789abcdef0123456789abcdef" }},
		{"api key", func(c *Config) { c.APIKey = "sk-different" }},
		{"control plane", func(c *Config) { c.ControlPlaneBaseURL = "https://elsewhere" }},
		{"diagnostics", func(c *Config) { c.DiagnosticsAddr = "127.0.0.1:1234" }},
		{"debug", func(c *Config) { c.Debug = !c.Debug }},
		{"role", func(c *Config) { c.Principal.Role = auth.RoleAdmin }},
	}
	for _, tc := range changed {
		cfg := base
		tc.apply(&cfg)
		if m.SameAs(cfg) {
			t.Errorf("changing %s was not noticed, so the tunnel would keep the old value", tc.name)
		}
	}
}
