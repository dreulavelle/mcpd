package admin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

type fakeCalls struct {
	calls   []sqlite.ToolCall
	callers []sqlite.CallerSummary
	summary sqlite.CallSummary
	// err, when set, is what every read answers with.
	err error
	// filter records what the handler passed through, so a test can check the
	// query string was read rather than only that a response came back.
	filter  sqlite.ToolCallFilter
	since   time.Time
	step    time.Duration
	buckets int
}

func (f *fakeCalls) Calls(_ context.Context, filter sqlite.ToolCallFilter) ([]sqlite.ToolCall, error) {
	f.filter = filter
	return f.calls, f.err
}

func (f *fakeCalls) Callers(_ context.Context, since time.Time) ([]sqlite.CallerSummary, error) {
	f.since = since
	return f.callers, f.err
}

func (f *fakeCalls) Summary(_ context.Context, since time.Time, step time.Duration, buckets int) (sqlite.CallSummary, error) {
	f.since, f.step, f.buckets = since, step, buckets
	return f.summary, f.err
}

func newCallsServer(t *testing.T, accounts Accounts, ledger CallLedger) *Server {
	t.Helper()
	return NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Calls:    ledger,
		Version:  "test",
		Health:   observability.NewHealthRegistry(time.Second),
		KeyAccess: func(context.Context, string) (groups.Resolved, error) {
			return groups.Resolved{Permissions: auth.Permissions{}, Grants: auth.Grants{}}, nil
		},
	})
}

// A row names which systems were reached and by whom, gated by history:read
// like the log -- which every built-in role except a subject holding nothing
// at all carries, so the refusal has to come from a principal with no
// permissions rather than from the "operator" name.
func TestCallsNeedHistoryRead(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.resolved = &groups.Resolved{Permissions: auth.Permissions{}, Grants: auth.Grants{}}
	s := newCallsServer(t, accounts, &fakeCalls{})

	for _, path := range []string{"/api/calls", "/api/calls/callers"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)

		if w.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", path, w.Code)
		}
	}
}

func TestCallsReadsTheQuery(t *testing.T) {
	accounts := newFakeAccounts()
	ledger := &fakeCalls{}
	s := newCallsServer(t, accounts, ledger)

	w := asAdmin(t, s, accounts, http.MethodGet,
		"/api/calls?principal=key:abc&plugin=graylog&outcome=denied&limit=50&hours=24", "")
	if w.Code != http.StatusOK {
		t.Fatalf("= %d: %s", w.Code, w.Body.String())
	}
	if ledger.filter.Principal != "key:abc" || ledger.filter.Plugin != "graylog" {
		t.Errorf("filter = %+v", ledger.filter)
	}
	if ledger.filter.Outcome != "denied" || ledger.filter.Limit != 50 {
		t.Errorf("filter = %+v", ledger.filter)
	}
	if ledger.filter.Since.IsZero() {
		t.Error("hours did not become a Since bound")
	}
}

// A cursor from an edited link is ignored rather than refused: the first page
// is a better answer than an error about a parameter nobody knew they sent.
func TestCallsIgnoresABadCursor(t *testing.T) {
	accounts := newFakeAccounts()
	ledger := &fakeCalls{}
	s := newCallsServer(t, accounts, ledger)

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/calls?before=not-a-number", "")
	if w.Code != http.StatusOK {
		t.Fatalf("= %d, want the first page: %s", w.Code, w.Body.String())
	}
	if ledger.filter.Before != 0 {
		t.Errorf("before = %d, want it ignored", ledger.filter.Before)
	}
}

// The cursor is computed by the host, because paging is by id and the browser
// should not have to know that.
func TestCallsOffersACursorOnlyWhenThereIsMore(t *testing.T) {
	accounts := newFakeAccounts()
	full := make([]sqlite.ToolCall, 50)
	for i := range full {
		full[i] = sqlite.ToolCall{ID: int64(100 - i), Principal: "key:a", Plugin: "p", Tool: "t", Outcome: "ok"}
	}
	s := newCallsServer(t, accounts, &fakeCalls{calls: full})

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/calls?limit=50", "")
	if !strings.Contains(w.Body.String(), `"next":"51"`) {
		t.Errorf("a full page offers no cursor: %s", w.Body.String())
	}

	// A short page is the last one.
	s2 := newCallsServer(t, accounts, &fakeCalls{calls: full[:3]})
	w2 := asAdmin(t, s2, accounts, http.MethodGet, "/api/calls?limit=50", "")
	if !strings.Contains(w2.Body.String(), `"next":""`) {
		t.Errorf("a short page offers a cursor: %s", w2.Body.String())
	}
}

func TestCallsNotConfigured(t *testing.T) {
	accounts := newFakeAccounts()
	s := newCallsServer(t, accounts, nil)

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/calls", "")
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("= %d, want 501: %s", w.Code, w.Body.String())
	}
}

// The overview asks for a fixed number of bars, so the handler decides the
// window and the store is told how many buckets to fill. A test reads back
// what it was told.
func TestCallSummaryAsksForAWholeDayByDefault(t *testing.T) {
	accounts := newFakeAccounts()
	ledger := &fakeCalls{}
	s := newCallsServer(t, accounts, ledger)

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/calls/summary", "")
	if w.Code != http.StatusOK {
		t.Fatalf("= %d: %s", w.Code, w.Body.String())
	}
	if ledger.buckets != 24 || ledger.step != time.Hour {
		t.Errorf("asked for %d buckets of %v, want 24 of an hour", ledger.buckets, ledger.step)
	}
	if !strings.Contains(w.Body.String(), `"hours":24`) {
		t.Errorf("the body does not say the window it answered for: %s", w.Body.String())
	}
}

// A bar labelled 14:00 holds the calls made between 14:00 and 15:00 whenever
// the page is opened. Anchored on the minute, every reload would shift every
// bar and two people reading one host would be reading different hours.
func TestCallSummaryAlignsTheWindowToTheClock(t *testing.T) {
	accounts := newFakeAccounts()
	ledger := &fakeCalls{}
	s := newCallsServer(t, accounts, ledger)

	before := time.Now()
	asAdmin(t, s, accounts, http.MethodGet, "/api/calls/summary?hours=6", "")

	if !ledger.since.Equal(ledger.since.Truncate(time.Hour)) {
		t.Errorf("window starts at %v, which is not an hour boundary", ledger.since)
	}
	// Five whole hours back plus the hour in progress is six buckets.
	want := before.Truncate(time.Hour).Add(-5 * time.Hour)
	if !ledger.since.Equal(want) {
		t.Errorf("window starts at %v, want %v", ledger.since, want)
	}
	if ledger.buckets != 6 {
		t.Errorf("asked for %d buckets, want 6", ledger.buckets)
	}
}

// A week of hourly buckets is 168 rows. A year is 8,760, which is not a shape
// anything draws, so the request is trimmed rather than refused.
func TestCallSummaryCapsTheWindow(t *testing.T) {
	accounts := newFakeAccounts()
	ledger := &fakeCalls{}
	s := newCallsServer(t, accounts, ledger)

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/calls/summary?hours=100000", "")
	if w.Code != http.StatusOK {
		t.Fatalf("= %d: %s", w.Code, w.Body.String())
	}
	if ledger.buckets != 168 {
		t.Errorf("asked for %d buckets, want 168", ledger.buckets)
	}
}

func TestCallSummaryNeedsHistoryRead(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.resolved = &groups.Resolved{Permissions: auth.Permissions{}, Grants: auth.Grants{}}
	s := newCallsServer(t, accounts, &fakeCalls{})

	r := httptest.NewRequest(http.MethodGet, "/api/calls/summary", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("GET /api/calls/summary = %d, want 403", w.Code)
	}
}

func TestCallSummaryNotConfigured(t *testing.T) {
	accounts := newFakeAccounts()
	s := newCallsServer(t, accounts, nil)

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/calls/summary", "")
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("= %d, want 501: %s", w.Code, w.Body.String())
	}
}

// A day nobody called in is still a day: the body carries a bar per hour, so
// the chart is flat rather than absent.
func TestCallSummaryPassesTheBucketsThrough(t *testing.T) {
	accounts := newFakeAccounts()
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	ledger := &fakeCalls{summary: sqlite.CallSummary{
		Buckets: []sqlite.HourBucket{{At: at, OK: 3, Error: 1}},
		Plugins: []sqlite.PluginCalls{{Plugin: "graylog", Calls: 4, Errors: 1}},
		Total:   4, Errors: 1,
	}}
	s := newCallsServer(t, accounts, ledger)

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/calls/summary?hours=1", "")
	body := w.Body.String()
	for _, want := range []string{`"total":4`, `"errors":1`, `"denied":0`, `"plugin":"graylog"`, `"ok":3`} {
		if !strings.Contains(body, want) {
			t.Errorf("body has no %s: %s", want, body)
		}
	}
}

// A wrapped storage error is a sentence for a log, not for a browser. It said
// "sqlite: summarise calls by hour: database is locked" to whoever asked.
func TestCallsAnswerAFailedReadWithASentence(t *testing.T) {
	accounts := newFakeAccounts()
	ledger := &fakeCalls{err: errors.New("sqlite: summarise calls by hour: database is locked")}
	s := newCallsServer(t, accounts, ledger)

	for _, path := range []string{"/api/calls", "/api/calls/callers", "/api/calls/summary"} {
		w := asAdmin(t, s, accounts, http.MethodGet, path, "")
		if w.Code != http.StatusInternalServerError {
			t.Errorf("GET %s = %d, want 500", path, w.Code)
			continue
		}
		body := w.Body.String()
		if strings.Contains(body, "sqlite:") || strings.Contains(body, "database is locked") {
			t.Errorf("GET %s put the storage error in the body: %s", path, body)
		}
		if !strings.Contains(body, "the record of tool calls could not be read") {
			t.Errorf("GET %s = %s, want the handler's own sentence", path, body)
		}
	}
}
