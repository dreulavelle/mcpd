package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// toolObserver sends one call to the counters and to the ledger.
//
// Two recipients with different jobs. The counters answer "how much and how
// fast", aggregated, cheap, and scraped; the ledger answers "who", one row at a
// time. Neither can do the other's job -- a metric labelled by principal is
// unbounded cardinality, and a table cannot be a histogram -- so the split is
// the point rather than duplication.
type toolObserver struct {
	metrics *observability.Metrics
	ledger  *sqlite.ToolCallStore
	// stats is the permanent rollup. It is written even when the ledger is
	// switched off: what an operator turns off with that setting is the record
	// of *who*, which is the part that names people. How much and how fast
	// name nobody, and a host that has been recording for a year should not
	// lose that history because somebody declined to keep principals.
	stats *sqlite.ToolStatsStore
	log   *slog.Logger
	// recording reports whether calls are being kept, read per call so turning
	// it off takes effect on the next call rather than the next restart.
	recording func(ctx context.Context) bool
}

// ToolCall records one call in both places.
//
// Written synchronously. A control plane's call rate is a person or an
// assistant deciding to do something, not a request stream, and one insert into
// a WAL-mode SQLite is far below the cost of the upstream call it describes. A
// buffer would trade that for losing the most recent calls in a crash -- which
// are the ones somebody investigating an incident wants most.
func (o *toolObserver) ToolCall(ctx context.Context, plugin, tool, outcome string, d time.Duration) {
	o.metrics.ToolCall(ctx, plugin, tool, outcome, d)

	at := time.Now()
	if o.stats != nil {
		if err := o.stats.RecordCall(ctx, at, plugin, tool, outcome, measured(outcome, d)); err != nil {
			o.log.WarnContext(ctx, "could not roll up a tool call",
				"plugin", plugin, "tool", tool, "error", err)
		}
	}

	if o.ledger == nil || !o.recording(ctx) {
		return
	}

	principal := auth.FromContext(ctx)
	if err := o.ledger.Record(ctx, sqlite.ToolCall{
		Principal:     principal.ID,
		Role:          principal.RoleName,
		Plugin:        plugin,
		Tool:          tool,
		Outcome:       outcome,
		DurationUS:    measured(outcome, d),
		CorrelationID: observability.CorrelationID(ctx),
	}); err != nil {
		// Logged and swallowed. The call itself either worked or did not, and
		// that answer belongs to the caller; failing a tool call because the
		// bookkeeping failed would turn a working integration into an outage.
		o.log.WarnContext(ctx, "could not record a tool call",
			"plugin", plugin, "tool", tool, "error", err)
	}
}

// measured returns the duration only for a call that actually ran.
//
// Decided from the outcome rather than from the duration being zero. A call
// refused by the gate or a rate limit never reached a handler, so there is
// nothing to time and nil says so; a call that ran and returned in under a
// microsecond would otherwise be recorded as though it had been refused.
func measured(outcome string, d time.Duration) *int64 {
	switch outcome {
	case observability.OutcomeDenied, observability.OutcomeRateLimited:
		return nil
	}
	us := d.Microseconds()
	return &us
}

// ToolResultSize records how large one answer was.
//
// Reported separately from the call because measuring it costs a marshal of
// the whole result, so it is only asked for once a call has succeeded. Both
// land on the same rollup row, a sized call being an `ok` one by definition.
func (o *toolObserver) ToolResultSize(ctx context.Context, plugin, tool string, size func() int) {
	o.metrics.ToolResultSize(ctx, plugin, tool, size)

	if o.stats == nil || size == nil {
		return
	}
	// Negative means the caller could not measure it, and a zero would read as
	// a tool that answered with nothing.
	n := size()
	if n < 0 {
		return
	}
	if err := o.stats.RecordResultSize(ctx, time.Now(), plugin, tool, int64(n)); err != nil {
		o.log.WarnContext(ctx, "could not roll up a result size",
			"plugin", plugin, "tool", tool, "error", err)
	}
}

func (o *toolObserver) MutationProposal(plugin, action, outcome string) {
	o.metrics.MutationProposal(plugin, action, outcome)
}

// newToolObserver builds the observer the plugin manager is given.
func (a *App) newToolObserver() *toolObserver {
	return &toolObserver{
		metrics: a.metrics,
		ledger:  a.calls,
		stats:   a.stats,
		log:     a.log,
		recording: func(ctx context.Context) bool {
			return a.settings.Bool(ctx, settings.KeyCallsRecord, true)
		},
	}
}

// pruneToolCalls removes calls past their retention.
//
// Separate from the history retention it runs beside, and with its own setting,
// because the two differ by orders of magnitude. The audit trail gains an entry
// when somebody changes something; this gains one every time an assistant reads
// anything. Tying them to one number would force a choice between keeping
// administrative history for a useful length of time and keeping a call ledger
// that fits on the disk.
func (a *App) pruneToolCalls(ctx context.Context) error {
	const every = time.Hour

	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		timer.Reset(every)

		if a.calls == nil {
			continue
		}
		days := a.settings.Int(ctx, settings.KeyCallsRetentionDays, 30)
		if days <= 0 {
			// Zero keeps everything, which is the same reading the history
			// retention gives it. An operator who wants nothing kept turns
			// recording off instead.
			continue
		}
		cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
		removed, err := a.calls.PruneToolCalls(ctx, cutoff)
		if err != nil {
			a.log.WarnContext(ctx, "could not prune the call ledger", "error", err)
			continue
		}
		if removed > 0 {
			a.log.InfoContext(ctx, "pruned the call ledger",
				"removed", removed, "older_than_days", days)
		}
	}
}
