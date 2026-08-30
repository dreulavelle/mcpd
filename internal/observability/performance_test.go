package observability

import (
	"context"
	"testing"
	"time"
)

// TestPerformanceCountsOutcomesSeparately checks the four ways a call ends stay
// four numbers.
//
// They are collapsed easily and must not be: a tool that is mostly denied is a
// grant that was never made, and a tool that is mostly rate limited is a limit
// to raise. Summing them into "calls" makes both look like ordinary use.
func TestPerformanceCountsOutcomesSeparately(t *testing.T) {
	m := NewMetrics()
	m.ToolCall(context.Background(), "graylog", "search_messages", OutcomeOK, 100*time.Millisecond)
	m.ToolCall(context.Background(), "graylog", "search_messages", OutcomeOK, 200*time.Millisecond)
	m.ToolCall(context.Background(), "graylog", "search_messages", OutcomeError, 50*time.Millisecond)
	m.ToolCall(context.Background(), "graylog", "search_messages", OutcomeDenied, 0)
	m.ToolCall(context.Background(), "graylog", "search_messages", OutcomeRateLimited, 0)

	p := m.Performance()
	if len(p.Tools) != 1 {
		t.Fatalf("got %d tools, want 1", len(p.Tools))
	}
	got := p.Tools[0].Calls
	want := Outcomes{OK: 2, Error: 1, Denied: 1, RateLimited: 1}
	if got != want {
		t.Fatalf("outcomes = %+v, want %+v", got, want)
	}
	if got.Total() != 5 {
		t.Fatalf("total = %d, want 5", got.Total())
	}
	// Refusals are not timed, so only the three that ran are in the histogram.
	if p.Tools[0].Duration == nil || p.Tools[0].Duration.Count != 3 {
		t.Fatalf("duration count = %v, want 3", p.Tools[0].Duration)
	}
}

// TestPerformanceBucketsAreNotCumulative is the conversion the console depends
// on.
//
// Prometheus buckets are cumulative because that is what aggregates across
// instances. A bar chart drawn from cumulative counts is a staircase that only
// ever climbs, which looks like a plausible distribution and is not one.
func TestPerformanceBucketsAreNotCumulative(t *testing.T) {
	m := NewMetrics()
	// One small answer, one middling, one far past what may be sent.
	for _, n := range []int{100, 1000, 500_000} {
		size := n
		m.ToolResultSize("observium", "list_devices", func() int { return size })
	}

	p := m.Performance()
	if len(p.Tools) != 1 || p.Tools[0].ResultBytes == nil {
		t.Fatalf("no result-size distribution: %+v", p.Tools)
	}
	d := p.Tools[0].ResultBytes
	if d.Count != 3 {
		t.Fatalf("count = %d, want 3", d.Count)
	}

	byBound := map[float64]uint64{}
	var overflow uint64
	var overflows int
	for _, b := range d.Buckets {
		if b.LE == nil {
			overflow, overflows = b.Count, overflows+1
			continue
		}
		byBound[*b.LE] = b.Count
	}
	if byBound[512] != 1 {
		t.Errorf("bucket <=512 = %d, want 1", byBound[512])
	}
	if byBound[2048] != 1 {
		t.Errorf("bucket <=2048 = %d, want 1", byBound[2048])
	}
	if byBound[8192] != 0 {
		t.Errorf("bucket <=8192 = %d, want 0 (cumulative counts leaked in)", byBound[8192])
	}
	if overflows != 1 || overflow != 1 {
		t.Errorf("overflow = %d in %d bucket(s), want 1 in 1", overflow, overflows)
	}
}

// TestPerformanceQuantileStopsAtTheLastBoundary checks a value past every
// bucket reports the last boundary rather than an extrapolation.
//
// The buckets say nothing about what lies beyond them, so a quantile there is
// a guess. Reporting the boundary is the honest floor: "at least this".
func TestPerformanceQuantileStopsAtTheLastBoundary(t *testing.T) {
	m := NewMetrics()
	for _, n := range []int{100, 1000, 500_000} {
		size := n
		m.ToolResultSize("observium", "list_devices", func() int { return size })
	}
	d := m.Performance().Tools[0].ResultBytes

	// Rank 1.5 lands between the first and second boundary and is interpolated.
	if d.P50 <= 512 || d.P50 >= 2048 {
		t.Errorf("p50 = %v, want between 512 and 2048", d.P50)
	}
	last := ResultSizeBuckets[len(ResultSizeBuckets)-1]
	if d.P95 != last {
		t.Errorf("p95 = %v, want the last boundary %v", d.P95, last)
	}
}

// TestPerformanceLeavesOutWhatCouldNotBeMeasured keeps "unmeasurable" and "an
// answer of no size" apart.
func TestPerformanceLeavesOutWhatCouldNotBeMeasured(t *testing.T) {
	m := NewMetrics()
	m.ToolResultSize("echo", "say", func() int { return -1 })

	p := m.Performance()
	for _, tool := range p.Tools {
		if tool.ResultBytes != nil && tool.ResultBytes.Count != 0 {
			t.Fatalf("an unmeasurable result was recorded: %+v", tool.ResultBytes)
		}
	}
}

// TestPerformanceOfNilMetricsIsEmpty covers a host with the endpoint off.
//
// Empty rather than a failure: there is nothing to report, and the console
// should render an empty table rather than an error suggesting a fault.
func TestPerformanceOfNilMetricsIsEmpty(t *testing.T) {
	var m *Metrics
	p := m.Performance()
	if p.Tools == nil || p.Upstream == nil || p.Cache == nil {
		t.Fatalf("nil slices would render as JSON null: %+v", p)
	}
	if len(p.Tools) != 0 {
		t.Fatalf("got %d tools from a nil Metrics", len(p.Tools))
	}
}

// TestPerformanceSeparatesCacheEvents checks a shared fetch is not counted as
// a hit.
//
// They are different wins: a hit cost nothing, a shared fetch still went
// upstream once for several callers. Folding them together overstates what the
// cache is holding.
func TestPerformanceSeparatesCacheEvents(t *testing.T) {
	m := NewMetrics()
	m.CacheEvent("observium", "devices", "hit")
	m.CacheEvent("observium", "devices", "hit")
	m.CacheEvent("observium", "devices", "miss")
	m.CacheEvent("observium", "devices", "shared")

	p := m.Performance()
	if len(p.Cache) != 1 {
		t.Fatalf("got %d cache rows, want 1", len(p.Cache))
	}
	c := p.Cache[0]
	if c.Hit != 2 || c.Miss != 1 || c.Shared != 1 {
		t.Fatalf("cache = %+v, want hit 2 miss 1 shared 1", c)
	}
}
