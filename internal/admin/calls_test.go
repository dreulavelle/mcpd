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
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

type fakeCalls struct {
	calls   []sqlite.ToolCall
	callers []sqlite.CallerSummary
	// filter records what the handler passed through, so a test can check the
	// query string was read rather than only that a response came back.
	filter sqlite.ToolCallFilter
	since  time.Time
}

func (f *fakeCalls) Calls(_ context.Context, filter sqlite.ToolCallFilter) ([]sqlite.ToolCall, error) {
	f.filter = filter
	return f.calls, nil
}

func (f *fakeCalls) Callers(_ context.Context, since time.Time) ([]sqlite.CallerSummary, error) {
	f.since = since
	return f.callers, nil
}

func newCallsServer(t *testing.T, accounts Accounts, ledger CallLedger) *Server {
	t.Helper()
	return NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Calls:    ledger,
		Version:  "test",
		Health:   observability.NewHealthRegistry(time.Second),
		KeyGrants: func(context.Context, string) ([]string, error) {
			return []string{}, nil
		},
	})
}

// A row names which systems were reached and by whom, which is a wider view
// than any one account's own work. Same reasoning as the log.
func TestCallsNeedAdmin(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.Role = auth.RoleUser
	accounts.user.Plugins = []string{"echo"}
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
