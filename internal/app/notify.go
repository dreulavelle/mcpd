package app

import (
	"context"
	"fmt"

	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/notify"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/settings"
)

// notifyConfig reads where events go.
//
// Per send rather than captured, so changing the address takes effect on the
// next event instead of the next restart -- the same reasoning every other
// live setting follows.
func (a *App) notifyConfig(ctx context.Context) notify.Config {
	return notify.Config{
		URL:    a.settings.String(ctx, settings.KeyNotifyURL, ""),
		Format: notify.Format(a.settings.String(ctx, settings.KeyNotifyFormat, "json")),
		Topic:  a.settings.String(ctx, settings.KeyNotifyTopic, ""),
		Token:  a.settings.String(ctx, settings.KeyNotifyToken, ""),
	}
}

// notifyToolsChanged reports that a remote server is offering something
// different from what was approved.
//
// Worth waking somebody for, because a changed tool has just stopped being
// served: an approval carries the descriptor it was given for, and a rewritten
// description invalidates it. Something that was working is now waiting, and
// nobody asked for that to happen.
func (a *App) notifyToolsChanged(ctx context.Context, server string, diff mcpservers.Diff) {
	if len(diff.Added) == 0 && len(diff.Changed) == 0 && len(diff.Removed) == 0 {
		return
	}
	a.notifier.Notify(ctx, notify.Event{
		Kind:     "mcpservers.tools_changed",
		Severity: notify.SeverityWarning,
		Title:    fmt.Sprintf("%s changed what it offers", server),
		Text: fmt.Sprintf(
			"%d added, %d changed, %d withdrawn. Anything added or changed is "+
				"not served until it is approved again.",
			len(diff.Added), len(diff.Changed), len(diff.Removed)),
	})
}

// notifyDiscoveryFailed reports that a remote server has stopped answering.
//
// Only from the schedule, never from the button: somebody who pressed Discover
// is already looking at the failure.
func (a *App) notifyDiscoveryFailed(ctx context.Context, server string, err error) {
	a.notifier.Notify(ctx, notify.Event{
		Kind:     "mcpservers.discovery_failed",
		Severity: notify.SeverityWarning,
		Title:    fmt.Sprintf("%s is not answering", server),
		Text: "A scheduled check of what it offers failed, so the tool list on " +
			"record is no longer being confirmed. " + err.Error(),
	})
}

// notifyBypassOpened reports that this host has stopped asking.
//
// The event the whole notification feature earns its place with. A window
// somebody opened and forgot is the failure the banner and this both exist to
// prevent, and a message is the half that reaches somebody who has closed the
// tab.
func (a *App) notifyBypassOpened(ctx context.Context, b *operations.Bypass) {
	scope := "every plugin"
	if b.Plugin != "" {
		scope = b.Plugin
	}
	a.notifier.Notify(ctx, notify.Event{
		Kind:     "approvals.bypass_opened",
		Severity: notify.SeverityWarning,
		Title:    "Changes are being approved without asking anyone",
		Text: fmt.Sprintf(
			"%s opened a window covering %s, up to %s risk, until %s. Reason: %s",
			b.CreatedBy, scope, b.Ceiling,
			b.ExpiresAt.UTC().Format("15:04 MST"), b.Reason),
	})
}

// notifyTunnelFailed reports that a connector has stopped serving.
//
// This is the gap the 31 August outage fell through. The tunnels stopped
// around midnight; nothing restarts one that has stopped, and the container's
// healthcheck validates the configuration rather than the connection -- so
// mcpd sat reporting healthy for nine hours with nothing reaching it, and the
// first anybody knew was somebody trying to use it the next morning.
//
// Since then a supervisor restarts a tunnel that failed for a reason worth
// retrying, with backoff, and a watchdog restarts one whose client has been
// failing quietly. So the message says which case this is: one mcpd is
// working on, or one that will not fix itself -- a rejected credential, a
// configuration that cannot start -- which is precisely what makes it worth
// interrupting somebody for. "Reconnecting" is only said when it is true.
//
// Background context rather than a request's: there is no caller here. This
// is reached from the tunnel's own goroutine, and the alternative is a
// cancelled context that would drop the event at the moment it matters.
func (a *App) notifyTunnelFailed(plugin, tunnelID, reason string, retrying bool) {
	text := fmt.Sprintf("It is not serving and will not restart on its own; "+
		"a person has to put it right from the Tunnels page. %s", reason)
	if retrying {
		text = fmt.Sprintf("It is not serving. mcpd is retrying with backoff and "+
			"will say when it is back, or when it has stopped trying. %s", reason)
	}
	a.notifier.Notify(context.Background(), notify.Event{
		Kind:     "tunnels.disconnected",
		Severity: notify.SeverityWarning,
		Title:    fmt.Sprintf("The %s connector has stopped", describeConnector(plugin)),
		Text:     text,
	})
}

// notifyTunnelRecovered closes the loop the failure opened: whoever was told a
// connector had stopped is told it is serving again, so nobody drives in to
// fix something that fixed itself.
func (a *App) notifyTunnelRecovered(plugin, tunnelID string) {
	a.notifier.Notify(context.Background(), notify.Event{
		Kind:     "tunnels.reconnected",
		Severity: notify.SeverityInfo,
		Title:    fmt.Sprintf("The %s connector is back", describeConnector(plugin)),
		Text:     "It reconnected and is serving again. Nothing needs doing.",
	})
}

// describeConnector names a tunnel the way the Tunnels page does.
func describeConnector(plugin string) string {
	if plugin == "" {
		return "everything"
	}
	return plugin
}

// sendTestNotification answers "did I type the address correctly".
//
// Sent rather than queued, and its error returned, because the whole point is
// to find out whether it arrived. Everything else goes through Notify, which
// cannot fail a caller.
func (a *App) sendTestNotification(ctx context.Context) error {
	return a.notifier.Send(ctx, notify.Event{
		Kind:     "notifications.test",
		Severity: notify.SeverityInfo,
		Title:    "mcpd can reach you",
		Text: "This is a test. Real messages arrive when a remote server " +
			"changes a tool, when this host stops asking before approving " +
			"changes, and when something stops answering.",
	})
}
