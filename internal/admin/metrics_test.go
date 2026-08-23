package admin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/observability"
)

func metricsDashboard(t *testing.T, role auth.Role, public bool) *Server {
	t.Helper()
	m := observability.NewMetrics()
	m.ToolCall("cnmaestro", "devices", observability.OutcomeOK, 25*time.Millisecond)
	return NewServer(Options{
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier:      roleVerifier{role: role},
		Metrics:       m.Handler(),
		MetricsPublic: public,
	})
}

func scrape(t *testing.T, s *Server, credential bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if credential {
		r.Header.Set("Authorization", "Bearer test")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// The exposure decision, as a test. Metrics name every plugin, every tool and
// how long each upstream takes, which is more than the unauthenticated
// readiness probe carries on purpose -- so by default a scrape presents a
// credential like any other machine caller.
func TestMetrics_RequireACredentialByDefault(t *testing.T) {
	s := metricsDashboard(t, auth.RoleUser, false)

	if w := scrape(t, s, false); w.Code != http.StatusUnauthorized {
		t.Errorf("an unauthenticated scrape returned %d, want 401", w.Code)
	}

	w := scrape(t, s, true)
	if w.Code != http.StatusOK {
		t.Fatalf("an authenticated scrape returned %d: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "mcpd_tool_calls_total") {
		t.Errorf("the exposition does not carry the tool counter:\n%s", w.Body)
	}
}

// `read` and not `admin`: it is a read of this host's own state, which is what
// read means everywhere else here, and a scraper holds a machine token rather
// than an administrator's.
func TestMetrics_TakeReadRatherThanAdmin(t *testing.T) {
	s := metricsDashboard(t, auth.RoleUser, false)
	if w := scrape(t, s, true); w.Code != http.StatusOK {
		t.Errorf("a reader was refused: %d", w.Code)
	}
}

// The opt-out exists because a Prometheus on a private network is a common
// shape, and refusing to support it produces a token pasted into a scrape
// config and never rotated.
func TestMetrics_PublicDropsTheCheck(t *testing.T) {
	s := metricsDashboard(t, auth.RoleUser, true)
	w := scrape(t, s, false)
	if w.Code != http.StatusOK {
		t.Fatalf("a public endpoint refused an unauthenticated scrape: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "mcpd_tool_calls_total") {
		t.Errorf("the exposition is empty:\n%s", w.Body)
	}
}

// With the endpoint off there is no route at all, rather than one that answers
// with nothing -- a scrape config pointing at it should fail loudly.
func TestMetrics_AbsentWhenSwitchedOff(t *testing.T) {
	s := NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier: roleVerifier{role: auth.RoleAdmin},
	})
	if w := scrape(t, s, true); w.Code == http.StatusOK {
		t.Errorf("a host with metrics off served /metrics: %d", w.Code)
	}
}

// A nil *Metrics has to be usable, because that is what every component holds
// when the endpoint is off.
func TestMetrics_NilRecordsNothingAndDoesNotPanic(t *testing.T) {
	var m *observability.Metrics
	m.ToolCall("p", "t", observability.OutcomeOK, time.Second)
	m.MutationProposal("p", "a", observability.OutcomeRateLimited)
	m.UpstreamRequest("p", "ok", time.Second)
	m.CatalogRequest("s", "fresh")
	m.CatalogFetch("s", time.Second)
	m.CacheEvent("p", "device", "hit")
	m.OutboxPublished("delivered")
	m.AddGauge("x", "y", nil, nil)
	if h := m.Handler(); h == nil {
		t.Error("a nil Metrics must still hand back a handler")
	}
}

// A gauge read at scrape time is what makes the operation counts exact rather
// than a tally this process happens to have kept.
func TestMetrics_GaugeIsReadWhenScraped(t *testing.T) {
	m := observability.NewMetrics()
	reads := 0
	m.AddGauge("mcpd_test_gauge", "A gauge.", []string{"state"},
		func(ctx context.Context) []observability.Sample {
			reads++
			return []observability.Sample{{Labels: []string{"succeeded"}, Value: 3}}
		})

	for range 2 {
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		if !strings.Contains(w.Body.String(), `mcpd_test_gauge{state="succeeded"} 3`) {
			t.Fatalf("gauge missing from the exposition:\n%s", w.Body)
		}
	}
	if reads != 2 {
		t.Errorf("the source was read %d times for two scrapes", reads)
	}
}
