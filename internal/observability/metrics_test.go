package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func expose(t *testing.T, m *Metrics) string {
	t.Helper()
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("scrape returned %d: %s", w.Code, w.Body)
	}
	return w.Body.String()
}

func mustContain(t *testing.T, body string, lines ...string) {
	t.Helper()
	for _, want := range lines {
		if !strings.Contains(body, want) {
			t.Errorf("exposition is missing %q", want)
		}
	}
}

func TestMetrics_ToolCallsCarryPluginToolAndOutcome(t *testing.T) {
	m := NewMetrics()
	m.ToolCall(context.Background(), "cnmaestro", "devices", OutcomeOK, 50*time.Millisecond)
	m.ToolCall(context.Background(), "cnmaestro", "devices", OutcomeError, time.Second)
	m.ToolCall(context.Background(), "cnmaestro", "alarms", OutcomeRateLimited, 0)
	m.ToolCall(context.Background(), "weather", "getWeather", OutcomeDenied, 0)

	mustContain(t, expose(t, m),
		`mcpd_tool_calls_total{outcome="ok",plugin="cnmaestro",tool="devices"} 1`,
		`mcpd_tool_calls_total{outcome="error",plugin="cnmaestro",tool="devices"} 1`,
		`mcpd_tool_calls_total{outcome="rate_limited",plugin="cnmaestro",tool="alarms"} 1`,
		`mcpd_tool_calls_total{outcome="denied",plugin="weather",tool="getWeather"} 1`,
	)
}

// A call refused before the handler ran took microseconds and is not a
// measurement of anything. Timing it would drag every quantile towards zero
// and hide exactly the latency the histogram exists to show.
func TestMetrics_RefusedCallsAreCountedButNotTimed(t *testing.T) {
	m := NewMetrics()
	m.ToolCall(context.Background(), "cnmaestro", "devices", OutcomeRateLimited, 0)
	m.ToolCall(context.Background(), "cnmaestro", "devices", OutcomeDenied, 0)

	body := expose(t, m)
	if strings.Contains(body, "mcpd_tool_call_duration_seconds_count") {
		t.Errorf("refused calls were timed:\n%s", body)
	}

	m.ToolCall(context.Background(), "cnmaestro", "devices", OutcomeOK, 100*time.Millisecond)
	mustContain(t, expose(t, m),
		`mcpd_tool_call_duration_seconds_count{plugin="cnmaestro",tool="devices"} 1`)
}

func TestMetrics_TheRestOfTheSurface(t *testing.T) {
	m := NewMetrics()
	m.MutationProposal("cnmaestro", "device.reboot", OutcomeRateLimited)
	m.UpstreamRequest("cnmaestro", "ok", 250*time.Millisecond)
	m.CatalogRequest("registry.modelcontextprotocol.io", "fresh")
	m.CatalogFetch("registry.modelcontextprotocol.io", 2*time.Second)
	m.CacheEvent("cnmaestro", "device", "hit")
	m.OutboxPublished("delivered")

	mustContain(t, expose(t, m),
		`mcpd_mutation_proposals_total{action="device.reboot",outcome="rate_limited",plugin="cnmaestro"} 1`,
		`mcpd_upstream_request_duration_seconds_count{outcome="ok",plugin="cnmaestro"} 1`,
		`mcpd_catalog_requests_total{result="fresh",source="registry.modelcontextprotocol.io"} 1`,
		`mcpd_catalog_fetch_duration_seconds_count{source="registry.modelcontextprotocol.io"} 1`,
		`mcpd_plugin_cache_events_total{event="hit",kind="device",plugin="cnmaestro"} 1`,
		`mcpd_outbox_published_total{result="delivered"} 1`,
	)
}

// The buckets have to reach past the interesting values. A cnMaestro device
// walk against a large estate takes longer than the client library's default
// ten-second ceiling, and a bucket set that stops below the values it is
// measuring reports every one of them identically.
func TestMetrics_BucketsReachSlowUpstreams(t *testing.T) {
	m := NewMetrics()
	m.UpstreamRequest("cnmaestro", "ok", 45*time.Second)
	mustContain(t, expose(t, m),
		`mcpd_upstream_request_duration_seconds_bucket{outcome="ok",plugin="cnmaestro",le="30"} 0`,
		`mcpd_upstream_request_duration_seconds_bucket{outcome="ok",plugin="cnmaestro",le="60"} 1`,
	)
}

// A gauge whose source cannot answer reports nothing for that scrape rather
// than failing the scrape or holding a number that has stopped being true.
func TestMetrics_AGaugeThatCannotAnswerReportsNothing(t *testing.T) {
	m := NewMetrics()
	m.AddGauge("mcpd_test_unreachable", "A gauge whose source is down.",
		[]string{"state"},
		func(context.Context) []Sample { return nil })
	m.ToolCall(context.Background(), "p", "t", OutcomeOK, time.Millisecond)

	body := expose(t, m)
	if strings.Contains(body, "mcpd_test_unreachable") {
		t.Errorf("a gauge with no samples appeared in the exposition:\n%s", body)
	}
	mustContain(t, body, "mcpd_tool_calls_total")
}

// The registry is this host's own. A collector something else registered on
// the default registry -- a dependency doing it on import, most likely -- must
// not appear here.
func TestMetrics_ExposesOnlyWhatThisHostDeclared(t *testing.T) {
	body := expose(t, NewMetrics())
	for _, foreign := range []string{"go_goroutines", "process_cpu_seconds_total"} {
		if strings.Contains(body, foreign) {
			t.Errorf("%q is in the exposition; the default registry leaked in", foreign)
		}
	}
}
