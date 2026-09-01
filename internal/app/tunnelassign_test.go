package app

import (
	"context"
	"testing"

	"github.com/spoked/mcpd/internal/settings"
)

// The bug: assignments were keyed by plugin, so pointing a second tunnel at a
// plugin overwrote the first tunnel's binding. The first ChatGPT account did
// not lose access -- it lost its connector, silently, and mcpd stopped running
// it. Two workspaces sharing one integration is the ordinary case in a company
// and it was impossible.
func TestTwoTunnelsMayServeOnePlugin(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)

	const (
		first  = "tunnel_1123456789abcdef0123456789abcdef"
		second = "tunnel_2123456789abcdef0123456789abcdef"
	)
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.TunnelPluginKey(first), Value: `"echo"`},
		{Key: settings.TunnelAccountKey(first), Value: `"acct_one"`},
		{Key: settings.TunnelPluginKey(second), Value: `"echo"`},
		{Key: settings.TunnelAccountKey(second), Value: `"acct_two"`},
	}); err != nil {
		t.Fatal(err)
	}

	got := a.assignedTunnels(ctx)
	if len(got) != 2 {
		t.Fatalf("got %d assignments, want both tunnels", len(got))
	}
	// Sorted, so the answer does not move between reads.
	if got[0].TunnelID != first || got[1].TunnelID != second {
		t.Fatalf("order = %s, %s", got[0].TunnelID, got[1].TunnelID)
	}
	for _, at := range got {
		if at.Plugin != "echo" {
			t.Errorf("%s serves %q, want echo", at.TunnelID, at.Plugin)
		}
	}
	if got[0].Account == got[1].Account {
		t.Error("both tunnels name the same account; each should keep its own")
	}
}

// Settings under "tunnel." that are not assignments must not be read as one,
// or "tunnel.enabled" becomes a tunnel called "enabled".
func TestOtherTunnelSettingsAreNotMistakenForAssignments(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)

	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: `true`},
		{Key: settings.KeyTunnelPlugins, Value: `["echo"]`},
	}); err != nil {
		t.Fatal(err)
	}
	if got := a.assignedTunnels(ctx); len(got) != 0 {
		t.Fatalf("got %d assignments from settings that are not assignments: %+v", len(got), got)
	}
}

// The upgrade has to carry existing assignments across, or every tunnel comes
// up unassigned on a host that was working a minute earlier.
func TestMigrationMovesAssignmentsOntoTheTunnelsOwnKey(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)

	const id = "tunnel_1123456789abcdef0123456789abcdef"
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.PluginTunnelKey("echo"), Value: `"` + id + `"`},
		{Key: settings.PluginTunnelAccountKey("echo"), Value: `"acct_one"`},
	}); err != nil {
		t.Fatal(err)
	}

	a.migrateTunnelAssignments(ctx)

	got := a.assignedTunnels(ctx)
	if len(got) != 1 {
		t.Fatalf("got %d assignments after the migration, want 1", len(got))
	}
	if got[0].TunnelID != id || got[0].Plugin != "echo" || got[0].Account != "acct_one" {
		t.Fatalf("migrated to %+v", got[0])
	}
	// The old rows stay, so rolling back to the previous build still finds
	// them rather than coming up with nothing assigned.
	if a.settings.String(ctx, settings.PluginTunnelKey("echo"), "") != id {
		t.Error("the old key was removed; a rollback would lose the assignment")
	}
}

// Running twice must not undo an operator's later edit.
func TestMigrationDoesNotOverwriteALaterAssignment(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)

	const id = "tunnel_1123456789abcdef0123456789abcdef"
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.PluginTunnelKey("echo"), Value: `"` + id + `"`},
		{Key: settings.PluginTunnelAccountKey("echo"), Value: `"acct_one"`},
	}); err != nil {
		t.Fatal(err)
	}
	a.migrateTunnelAssignments(ctx)

	// Somebody moves it to another plugin afterwards.
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.TunnelPluginKey(id), Value: `"graylog"`},
	}); err != nil {
		t.Fatal(err)
	}
	a.migrateTunnelAssignments(ctx)

	if got := a.assignedTunnels(ctx); len(got) != 1 || got[0].Plugin != "graylog" {
		t.Fatalf("the migration undid a later edit: %+v", got)
	}
}
