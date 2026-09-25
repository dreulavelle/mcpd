package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/storage/sqlite"
	"github.com/spoked/mcpd/internal/storage/sqlite/sqlitetest"
)

// The distinction the nullable column exists for. A call refused by the gate
// or a rate limit never reached a handler, so nothing was timed -- and an
// in-process plugin answering in 63us really does round to zero milliseconds,
// so recording zero for both would make a fast call indistinguishable from one
// that never ran.
func TestMeasuredSeparatesFastFromNeverRan(t *testing.T) {
	for _, outcome := range []string{observability.OutcomeDenied, observability.OutcomeRateLimited} {
		if got := measured(outcome, 0); got != nil {
			t.Errorf("%s recorded a duration of %d; it never ran", outcome, *got)
		}
	}

	for _, outcome := range []string{observability.OutcomeOK, observability.OutcomeError} {
		got := measured(outcome, 63*time.Microsecond)
		if got == nil {
			t.Fatalf("%s recorded no duration for a call that ran", outcome)
		}
		if *got != 63 {
			t.Errorf("%s recorded %dus, want 63", outcome, *got)
		}
	}
}

// A call that ran and returned faster than the resolution is still a
// measurement, and must not be reported as one that never happened.
func TestMeasuredKeepsAnImmeasurablyFastCall(t *testing.T) {
	got := measured(observability.OutcomeOK, 0)
	if got == nil {
		t.Fatal("a call that ran was recorded as though it had been refused")
	}
	if *got != 0 {
		t.Errorf("got %dus, want 0", *got)
	}
}

// A failed call keeps why it failed, in the words its caller was given. The
// ledger said "error" and nothing else, and the only way to learn more was to
// find the correlation id in a log that may since have rotated.
func TestToolCall_KeepsWhyACallFailed(t *testing.T) {
	ledger := sqlite.NewToolCallStore(sqlitetest.Open(t), time.Now)
	o := &toolObserver{
		metrics: observability.NewMetrics(), ledger: ledger,
		log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		recording: func(context.Context) bool { return true },
	}
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{ID: "svc:chatgpt"})

	o.ToolCall(ctx, "graylog", "search_messages", observability.OutcomeError, time.Millisecond,
		errors.New("graylog answered 503: the search backend is unavailable"))
	o.ToolCall(ctx, "graylog", "list_streams", observability.OutcomeOK, time.Millisecond, nil)

	calls, err := ledger.Calls(ctx, sqlite.ToolCallFilter{})
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, c := range calls {
		reasons[c.Tool] = c.Reason
	}
	if reasons["search_messages"] != "graylog answered 503: the search backend is unavailable" {
		t.Errorf("failed call reason = %q", reasons["search_messages"])
	}
	if reasons["list_streams"] != "" {
		t.Errorf("a successful call recorded a reason: %q", reasons["list_streams"])
	}
}
