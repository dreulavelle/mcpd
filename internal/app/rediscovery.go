package app

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/spoked/mcpd/internal/settings"
)

// Re-discovery: asking each remote MCP server what it offers, on a timer.
//
// Discovery was an administrator pressing a button, so a server that added,
// withdrew or rewrote a tool went unnoticed until the next person looked. The
// gate that catches such a change was already built and correct -- an approval
// carries the tool's descriptor hash, so a rewritten description invalidates
// the approval underneath it and the tool stops being served -- it simply only
// ran when somebody thought to run it.
//
// This is that gate on a clock. It adds no new decision: everything it finds is
// handled by the same code path the button uses.
const (
	// How often the loop wakes to see whether anything is due. Far shorter
	// than the interval, because the interval is per server and a host that
	// has been down for a day should not wait another one.
	rediscoveryTick = 15 * time.Minute

	// A pause after start before the first pass. Long enough for plugins to
	// mount and a tunnel to settle, so a restart does not spend its first
	// seconds dialling other people's services.
	rediscoveryWarmup = 3 * time.Minute

	// Between servers in one pass, so a host with a dozen imported servers
	// does not open a dozen connections at once.
	rediscoverySpacing = 5 * time.Second
)

// rediscover re-checks every enabled remote MCP server on the configured
// interval.
//
// Which servers are due is decided per server from a timestamp in the
// database, not from this loop's own clock. That is what makes the interval
// mean what it says across a restart: a host that restarts every hour re-probes
// on the schedule rather than on every start, which would be load on somebody
// else's service that nobody asked for.
func (a *App) rediscover(ctx context.Context) error {
	timer := time.NewTimer(rediscoveryWarmup)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		timer.Reset(rediscoveryTick)

		hours := a.settings.Int(ctx, settings.KeyDiscoveryIntervalHours, 24)
		if hours <= 0 {
			// Zero is the operator saying discovery happens when they press
			// the button. Nothing to do, and the loop keeps running so that
			// turning it back on takes effect without a restart.
			continue
		}
		a.rediscoverDue(ctx, time.Duration(hours)*time.Hour)
	}
}

// rediscoverDue runs one pass, probing the servers whose turn it is.
func (a *App) rediscoverDue(ctx context.Context, every time.Duration) {
	servers, err := a.mcpStore.List(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "could not list remote MCP servers to re-check", "error", err)
		return
	}

	now := time.Now()
	first := true
	for _, srv := range servers {
		if ctx.Err() != nil {
			return
		}
		// A disabled server is not being served, so what it offers today is
		// not a question anybody is relying on the answer to. A document this
		// build cannot parse has no endpoint to dial.
		if !srv.Enabled || srv.Parsed == nil {
			continue
		}
		if !srv.Discovery.Due(now, every) {
			continue
		}

		// Spacing goes between servers rather than before the first, so a host
		// with one server does not wait for nothing.
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-time.After(jitter(rediscoverySpacing)):
			}
		}
		first = false

		a.rediscoverOne(ctx, srv.Name)
	}
}

// rediscoverOne probes one server and reports what it found.
func (a *App) rediscoverOne(ctx context.Context, name string) {
	// The same call the Discover button makes, which is also what records the
	// attempt. Nothing about a scheduled discovery is a different kind of
	// discovery: the snapshot, the diff, the invalidated approvals and the
	// remount are one path, and a second would be a second set of rules about
	// what a changed tool means.
	diff, err := a.DiscoverMCPServer(ctx, rediscoveryActor, name)
	if err != nil {
		// Warn rather than error, and once per interval rather than per tick:
		// a remote server being down is somebody else's outage, and a host
		// that logged it as its own failure every few minutes would bury the
		// things that are this host's problem.
		a.log.WarnContext(ctx, "scheduled re-check of a remote MCP server failed",
			"server", name, "error", err)
		return
	}

	// A pass that changed nothing is the ordinary case and says so quietly.
	// A pass that changed something is the reason this exists, and a changed
	// tool has just stopped being served -- which somebody needs to be able to
	// find without turning debug on.
	if len(diff.Added) == 0 && len(diff.Changed) == 0 && len(diff.Removed) == 0 {
		a.log.DebugContext(ctx, "scheduled re-check found no change", "server", name)
		return
	}
	a.log.InfoContext(ctx, "scheduled re-check found a change",
		"server", name,
		"added", diff.Added, "changed", diff.Changed, "removed", diff.Removed,
		"note", "an added or changed tool is not served until it is approved")
}

// rediscoveryActor is who the audit trail and the tool snapshot record for a
// discovery nobody asked for. Distinct from a person's id, and from the other
// system actors, so "who last looked at this server" has a truthful answer.
const rediscoveryActor = "system:rediscovery"

// jitter spreads work either side of a delay.
//
// Two reasons. Within one host it keeps a pass from arriving as a burst; across
// many hosts running the same image it keeps every deployment from re-checking
// the same public server on the same schedule.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d/2 + time.Duration(rand.Int64N(int64(d)))
}
