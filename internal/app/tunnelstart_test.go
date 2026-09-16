package app

import (
	"context"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/tunnel"
)

// The bug this test exists for: the tunnel workers were started only when the
// group already held a tunnel, which is precisely what a host being set up for
// the first time does not have. With nothing assigned at boot the group was
// never told the host had reached serving, so a tunnel added afterwards was
// built and left stopped -- the Tunnels page read "Off" with the switch on,
// Restart reported success and did nothing, and only restarting mcpd cleared
// it. Adding a tunnel to a host that booted without one has to connect it.
func TestATunnelAddedAfterBootingWithNoneIsStarted(t *testing.T) {
	a := newSettingsApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		a.waitForWorkers(5 * time.Second)
	})

	// The state a fresh install is in: no tunnel assigned, so the group holds
	// no managers and has nothing to report.
	if got := a.tunnelConfigs(ctx); len(got) != 0 {
		t.Fatalf("got %d tunnels before one was added, want none", len(got))
	}

	a.startTunnelWorkers(ctx)
	// Stands in for the MCP listener answering, which is what the worker waits
	// on before it connects anything.
	close(a.serving)

	// Now add one, the way the dashboard does: an account to connect with, the
	// assignment, and the switch. The control plane is a port nothing answers
	// on, so the attempt fails locally rather than reaching OpenAI -- failing
	// to connect is a different state from never having been started, which is
	// the distinction this test turns on.
	addAccount(t, a, "Work", nil)
	const id = "tunnel_6a87964313a88191b1cf9d9bf28dde48"
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelControlPlane, Value: `"http://127.0.0.1:1"`},
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey(id), Value: `"*"`},
	}); err != nil {
		t.Fatal(err)
	}
	if got := a.tunnelConfigs(ctx); len(got) != 1 {
		t.Fatalf("got %d tunnel configs after adding one, want it bound to the account", len(got))
	}

	// The settings watcher re-applies in a goroutine of its own, so the state
	// is polled rather than read once.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var state tunnel.State
		for _, s := range a.tunnels.Status() {
			if s.TunnelID == id {
				state = s.State
			}
		}
		if state != "" && state != tunnel.StateStopped {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("tunnel state = %q, want it started: a tunnel added to a host "+
				"that booted without one must not be left stopped", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
