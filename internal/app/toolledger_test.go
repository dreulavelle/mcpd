package app

import (
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/observability"
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
