package app

import (
	"context"
	"testing"

	"github.com/spoked/mcpd/internal/settings"
)

// The first start after the upgrade has to carry forward the tunnels this host
// was already running, because the only other record of what mcpd created was
// a description string every build stamps -- and that is exactly what stopped
// being trusted. An assignment is this host's own record of a tunnel it made,
// written by its own create, so it is what the record is seeded from.
//
// Without this, a working deployment loses every connector off the Tunnels
// page and stops it, on the update meant to secure it.
func TestAdoptExistingTunnels_CarriesForwardWhatWasAssigned(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	const running = "tunnel_6a87964313a88191b1cf9d9bf28dde48"
	const unused = "tunnel_1123456789abcdef0123456789abcdef"
	addAccount(t, a, "Work", nil)
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey(running), Value: `"*"`},
		// Assigned to nothing: an id the page knows and mcpd is not using.
		{Key: settings.TunnelPluginKey(unused), Value: `""`},
	}); err != nil {
		t.Fatal(err)
	}

	// Nothing is this host's until the upgrade records it.
	if got := a.tunnelsMadeHere(ctx); len(got) != 0 {
		t.Fatalf("before the upgrade pass, manages %v; want nothing recorded", got)
	}

	a.adoptExistingTunnels(ctx)

	mine := a.tunnelsMadeHere(ctx)
	if _, ok := mine[running]; !ok {
		t.Errorf("the tunnel this host was running was not carried forward: %v", mine)
	}
	if _, ok := mine[unused]; ok {
		t.Error("a tunnel assigned to nothing was not being run, so there is " +
			"nothing to carry forward")
	}
}

// Safe on every start: it must not overwrite what a create recorded, and a
// second pass must not undo an operator's later change.
func TestAdoptExistingTunnels_IsIdempotent(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	const id = "tunnel_6a87964313a88191b1cf9d9bf28dde48"
	addAccount(t, a, "Work", nil)
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey(id), Value: `"*"`},
		{Key: settings.TunnelNameKey(id), Value: `"mcpd: observium"`},
	}); err != nil {
		t.Fatal(err)
	}

	a.adoptExistingTunnels(ctx)
	a.adoptExistingTunnels(ctx)

	mine := a.tunnelsMadeHere(ctx)
	if len(mine) != 1 {
		t.Fatalf("manages %d tunnels after two passes, want one: %v", len(mine), mine)
	}
	// The name a create recorded survives, rather than being blanked by the
	// pass that only records provenance.
	if mine[id] != "mcpd: observium" {
		t.Errorf("name = %q, want the recorded one kept", mine[id])
	}

	// And a tunnel deliberately marked as not this host's stays that way: the
	// pass writes only where there is no answer, never over one.
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.TunnelMadeHereKey(id), Value: "false"},
	}); err != nil {
		t.Fatal(err)
	}
	a.adoptExistingTunnels(ctx)
	if _, ok := a.tunnelsMadeHere(ctx)[id]; ok {
		t.Error("a tunnel recorded as not made here must not be re-adopted")
	}
}
