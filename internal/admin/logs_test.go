package admin

import (
	"bufio"
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

// streamServer is a server with a log to stream and an administrator signed
// in, which is the only combination that reaches the handler at all.
func streamServer(t *testing.T) (*Server, *slog.Logger, *fakeAccounts) {
	t.Helper()
	log, _, stream := observability.NewStreamingLogger(io.Discard, slog.LevelInfo, "json", true)
	accounts := newFakeAccounts()
	s := NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Version:  "test",
		Logs:     stream,
		Health:   observability.NewHealthRegistry(time.Second),
	})
	return s, log, accounts
}

// open connects to a running server and returns the response, still streaming.
//
// A real server and a real connection rather than a recorder: the handler does
// not return until the request ends, so anything reading a recorder while it
// runs is reading a buffer another goroutine is writing. The race detector
// says so, and it is right.
func open(t *testing.T, s *Server, accounts *fakeAccounts) *http.Response {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/logs/stream", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

// readEvents reads until it has n of them, or the test times out.
func readEvents(t *testing.T, res *http.Response, n int) []string {
	t.Helper()
	var events []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() && len(events) < n {
		if line := sc.Text(); strings.HasPrefix(line, "data: ") {
			events = append(events, strings.TrimPrefix(line, "data: "))
		}
	}
	if len(events) < n {
		t.Fatalf("read %d events; want %d", len(events), n)
	}
	return events
}

// What was logged before anybody opened the page is most of the reason to open
// it.
func TestLogStream_SendsWhatWasAlreadyLogged(t *testing.T) {
	s, log, accounts := streamServer(t)
	log.Info("something happened", "detail", "worth reading")

	res := open(t, s, accounts)
	if got := res.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}

	events := readEvents(t, res, 1)
	if !strings.Contains(events[0], "something happened") {
		t.Errorf("the first event was %s", events[0])
	}
	if !strings.Contains(events[0], "worth reading") {
		t.Errorf("the record's attributes did not survive: %s", events[0])
	}
}

// A line logged after somebody is already watching has to reach them, which is
// the whole feature and not something the backlog proves.
func TestLogStream_SendsWhatHappensNext(t *testing.T) {
	s, log, accounts := streamServer(t)
	log.Info("before anybody watched")

	res := open(t, s, accounts)
	// The backlog first, so the connection is established before the next line
	// is written and this is not accidentally testing the backlog again.
	readEvents(t, res, 1)

	log.Warn("while somebody was watching", "detail", "live")
	events := readEvents(t, res, 1)
	if !strings.Contains(events[0], "while somebody was watching") {
		t.Errorf("the live event was %s", events[0])
	}
	if !strings.Contains(events[0], `"level":"WARN"`) {
		t.Errorf("the level did not survive: %s", events[0])
	}
}

// A credential withheld from the host's own log must not reach a browser by
// this route either.
func TestLogStream_CarriesNoCredential(t *testing.T) {
	s, log, accounts := streamServer(t)
	log.Info("dialling", "api_key", "sk-should-never-appear")

	res := open(t, s, accounts)
	events := readEvents(t, res, 1)
	if strings.Contains(events[0], "sk-should-never-appear") {
		t.Errorf("a credential was streamed to the dashboard: %s", events[0])
	}
	if !strings.Contains(events[0], observability.Redacted) {
		t.Errorf("the value was dropped rather than marked redacted: %s", events[0])
	}
}

// Every event is one data line terminated by a blank one. The records already
// end in a newline, so this is where an off-by-one shows up -- as a stream a
// browser silently mis-frames rather than as anything that looks like a fault.
func TestLogStream_FramesOneEventPerRecord(t *testing.T) {
	s, log, accounts := streamServer(t)
	log.Info("first")
	log.Info("second")

	res := open(t, s, accounts)

	// Asserted as an exact sequence rather than by counting. Counting means
	// reading one line past the last event to see how it ended, and on a quiet
	// host the next line is the heartbeat twenty-five seconds later.
	sc := bufio.NewScanner(res.Body)
	for i, wantData := range []bool{true, false, true, false} {
		if !sc.Scan() {
			t.Fatalf("the stream ended after %d lines", i)
		}
		line := sc.Text()
		switch {
		case wantData && !strings.HasPrefix(line, "data: "):
			t.Fatalf("line %d should be an event, and is %q", i, line)
		case !wantData && line != "":
			t.Fatalf("line %d should terminate the event before it, and is %q", i, line)
		}
	}
}

// The log says which systems were called and by whom, which is a wider view
// than any one account's own work.
func TestLogStream_IsRefusedWithoutAdmin(t *testing.T) {
	s, _, accounts := streamServer(t)
	accounts.user.Role = auth.RoleUser

	r := httptest.NewRequest(http.MethodGet, "/api/logs/stream", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// A host keeping no copy says so. An empty stream would look like a host that
// had fallen silent, which is the one thing somebody opening this page is
// trying to rule out.
func TestLogStream_SaysSoWhenNoCopyIsKept(t *testing.T) {
	accounts := newFakeAccounts()
	s := NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Version:  "test",
		Health:   observability.NewHealthRegistry(time.Second),
	})

	r := httptest.NewRequest(http.MethodGet, "/api/logs/stream", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}
