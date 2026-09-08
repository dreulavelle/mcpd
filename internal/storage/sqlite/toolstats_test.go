package sqlite

import (
	"testing"
	"time"
)

func micros(n int64) *int64 { return &n }

// hour is a fixed point inside one bucket, so a test can put calls in the same
// hour or a different one deliberately rather than by accident of the clock.
var hour = time.Date(2026, 9, 8, 14, 30, 0, 0, time.UTC)

func TestToolStats_FoldsManyCallsIntoOneHour(t *testing.T) {
	s := NewToolStatsStore(newTestDB(t))
	ctx := t.Context()

	for _, d := range []int64{2_000, 4_000, 30_000} {
		if err := s.RecordCall(ctx, hour, "echo", "say", "ok", us(d)); err != nil {
			t.Fatal(err)
		}
	}
	// A different minute of the same hour lands on the same row.
	if err := s.RecordCall(ctx, hour.Add(20*time.Minute), "echo", "say", "ok", micros(6_000)); err != nil {
		t.Fatal(err)
	}

	totals, err := s.Totals(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(totals) != 1 {
		t.Fatalf("want one tool, got %d", len(totals))
	}
	got := totals[0]
	if got.Calls != 4 || got.Timed != 4 {
		t.Fatalf("calls=%d timed=%d, want 4 and 4", got.Calls, got.Timed)
	}
	if got.MeanUS != (2_000+4_000+30_000+6_000)/4 {
		t.Fatalf("mean=%d", got.MeanUS)
	}
	if *got.MinUS != 2_000 || *got.MaxUS != 30_000 {
		t.Fatalf("min=%d max=%d", *got.MinUS, *got.MaxUS)
	}
}

/*
A refusal never reached a handler, so there is nothing to time. It still counts
as a call -- somebody reached for the tool -- but dividing the duration by the
call count instead of by the timed count would report a host that refuses a
great deal as a host that is fast.
*/
func TestToolStats_RefusalsCountButAreNotTimed(t *testing.T) {
	s := NewToolStatsStore(newTestDB(t))
	ctx := t.Context()

	if err := s.RecordCall(ctx, hour, "echo", "say", "ok", micros(10_000)); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := s.RecordCall(ctx, hour, "echo", "say", "denied", nil); err != nil {
			t.Fatal(err)
		}
	}

	totals, _ := s.Totals(ctx, time.Time{})
	got := totals[0]
	if got.Calls != 4 {
		t.Fatalf("calls=%d, want 4", got.Calls)
	}
	if got.Denied != 3 || got.OK != 1 {
		t.Fatalf("denied=%d ok=%d", got.Denied, got.OK)
	}
	if got.Timed != 1 {
		t.Fatalf("timed=%d, want 1 -- a refusal was never timed", got.Timed)
	}
	if got.MeanUS != 10_000 {
		t.Fatalf("mean=%d, want the one call that ran, not a quarter of it", got.MeanUS)
	}
}

/*
A p95 cannot be recovered from a mean, which is the whole reason the bucket
columns exist. Ninety-nine fast calls and one slow one have a mean that hides
the slow one and a p95 that does not.
*/
func TestToolStats_QuantilesComeFromTheBuckets(t *testing.T) {
	s := NewToolStatsStore(newTestDB(t))
	ctx := t.Context()

	for range 99 {
		if err := s.RecordCall(ctx, hour, "echo", "say", "ok", micros(500)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordCall(ctx, hour, "echo", "say", "ok", micros(5_000_000)); err != nil {
		t.Fatal(err)
	}

	totals, _ := s.Totals(ctx, time.Time{})
	got := totals[0]
	if *got.P50US != 1_000 {
		t.Fatalf("p50=%d, want the 1ms band", *got.P50US)
	}
	// The hundredth call is the slow one, and 95% of 100 lands before it.
	if *got.P95US != 1_000 {
		t.Fatalf("p95=%d, want the 1ms band", *got.P95US)
	}
	if *got.MaxUS != 5_000_000 {
		t.Fatalf("max=%d, want the slow call to survive as the max", *got.MaxUS)
	}
}

/*
Percentiles have to be summed from the buckets across a span, never averaged
across it: the p95 of two hours is not the mean of their two p95s.
*/
func TestToolStats_QuantileSpansHours(t *testing.T) {
	s := NewToolStatsStore(newTestDB(t))
	ctx := t.Context()

	// A fast hour and a slow one. Averaging the two hours' p95s would give
	// something between the bands; summing their buckets gives the real one.
	for range 10 {
		if err := s.RecordCall(ctx, hour, "echo", "say", "ok", micros(500)); err != nil {
			t.Fatal(err)
		}
	}
	for range 10 {
		if err := s.RecordCall(ctx, hour.Add(time.Hour), "echo", "say", "ok", micros(3_000_000)); err != nil {
			t.Fatal(err)
		}
	}

	totals, _ := s.Totals(ctx, time.Time{})
	got := totals[0]
	if got.Timed != 20 {
		t.Fatalf("timed=%d across both hours", got.Timed)
	}
	if *got.P50US != 1_000 {
		t.Fatalf("p50=%d, want the fast half", *got.P50US)
	}
	if *got.P95US != 10_000_000 {
		t.Fatalf("p95=%d, want the band the slow half fell in", *got.P95US)
	}
}

// The overflow band has no upper bound, so no number may be reported for it.
func TestToolStats_QuantileInTheOverflowBandIsNotANumber(t *testing.T) {
	s := NewToolStatsStore(newTestDB(t))
	ctx := t.Context()
	if err := s.RecordCall(ctx, hour, "echo", "slow", "ok", micros(45_000_000)); err != nil {
		t.Fatal(err)
	}
	totals, _ := s.Totals(ctx, time.Time{})
	if totals[0].P50US != nil {
		t.Fatalf("p50=%v, want no number past the last boundary", *totals[0].P50US)
	}
}

/*
Bytes are reported for a call that produced an answer, so `sized` is their own
denominator. A tool called ten times and answering twice has a mean answer of
the two, not a fifth of it.
*/
func TestToolStats_ResultSizesHaveTheirOwnDenominator(t *testing.T) {
	s := NewToolStatsStore(newTestDB(t))
	ctx := t.Context()

	for range 10 {
		if err := s.RecordCall(ctx, hour, "echo", "say", "ok", micros(1_000)); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []int64{1_000, 3_000} {
		if err := s.RecordResultSize(ctx, hour, "echo", "say", n); err != nil {
			t.Fatal(err)
		}
	}

	totals, _ := s.Totals(ctx, time.Time{})
	got := totals[0]
	if got.Calls != 10 {
		t.Fatalf("calls=%d", got.Calls)
	}
	if got.Sized != 2 || got.BytesSum != 4_000 || got.MeanBytes != 2_000 {
		t.Fatalf("sized=%d sum=%d mean=%d", got.Sized, got.BytesSum, got.MeanBytes)
	}
	if *got.MaxBytes != 3_000 {
		t.Fatalf("max bytes=%d", *got.MaxBytes)
	}
}

// A size arriving before any call still lands on the same row rather than
// creating a second one the totals would then double-count.
func TestToolStats_SizeAndCallShareOneRow(t *testing.T) {
	s := NewToolStatsStore(newTestDB(t))
	ctx := t.Context()

	if err := s.RecordResultSize(ctx, hour, "echo", "say", 900); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCall(ctx, hour, "echo", "say", "ok", micros(1_000)); err != nil {
		t.Fatal(err)
	}

	totals, _ := s.Totals(ctx, time.Time{})
	if len(totals) != 1 {
		t.Fatalf("want one row, got %d", len(totals))
	}
	if totals[0].Calls != 1 || totals[0].Sized != 1 {
		t.Fatalf("calls=%d sized=%d", totals[0].Calls, totals[0].Sized)
	}
}

func TestToolStats_SeriesAndPluginTotals(t *testing.T) {
	s := NewToolStatsStore(newTestDB(t))
	ctx := t.Context()

	if err := s.RecordCall(ctx, hour, "echo", "say", "ok", micros(1_000)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCall(ctx, hour.Add(time.Hour), "echo", "shout", "error", micros(2_000)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCall(ctx, hour, "graylog", "search", "ok", micros(9_000)); err != nil {
		t.Fatal(err)
	}

	points, err := s.Series(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("want two hours, got %d", len(points))
	}
	if points[0].Calls != 2 || points[0].OK != 2 || points[0].Failed != 0 {
		t.Fatalf("first hour: %+v", points[0])
	}
	if points[1].Failed != 1 || points[1].OK != 0 {
		t.Fatalf("second hour: %+v", points[1])
	}

	plugins, err := s.ByPlugin(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 2 || plugins[0].Plugin != "echo" {
		t.Fatalf("plugins=%+v", plugins)
	}
	if plugins[0].Tools != 2 || plugins[0].Calls != 2 {
		t.Fatalf("echo: tools=%d calls=%d", plugins[0].Tools, plugins[0].Calls)
	}
}

// `since` bounds the read, so a page asking for a day does not sum a year.
func TestToolStats_SinceExcludesEarlierHours(t *testing.T) {
	s := NewToolStatsStore(newTestDB(t))
	ctx := t.Context()

	if err := s.RecordCall(ctx, hour.Add(-48*time.Hour), "echo", "say", "ok", micros(1_000)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordCall(ctx, hour, "echo", "say", "ok", micros(1_000)); err != nil {
		t.Fatal(err)
	}

	all, _ := s.Totals(ctx, time.Time{})
	if all[0].Calls != 2 {
		t.Fatalf("everything: calls=%d", all[0].Calls)
	}
	recent, _ := s.Totals(ctx, hour.Add(-time.Hour))
	if recent[0].Calls != 1 {
		t.Fatalf("since an hour ago: calls=%d", recent[0].Calls)
	}
}

// An empty table has no span rather than a span starting at the epoch.
func TestToolStats_SpanOfNothingIsZero(t *testing.T) {
	s := NewToolStatsStore(newTestDB(t))
	first, last, err := s.Span(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !first.IsZero() || !last.IsZero() {
		t.Fatalf("first=%v last=%v, want both zero", first, last)
	}
}
