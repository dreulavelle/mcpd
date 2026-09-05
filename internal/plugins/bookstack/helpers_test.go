package bookstack

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// Every name, page and address in this package's tests is invented. Nothing
// here came off a real knowledge base.

const (
	testTokenID     = "tokenid0123456789"
	testTokenSecret = "tokensecret0123456789"
)

func testDeps() plugins.Deps {
	return plugins.Deps{
		Instance: "bookstack",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}
}

// fakeBookStack answers a set of paths with fixtures.
//
// It checks what the real one would not: that every request carries the token
// pair as one header in BookStack's own form, and it records every write so a
// test can assert that planning made none.
type fakeBookStack struct {
	t *testing.T

	mu sync.Mutex
	// bodies maps "METHOD /path" to a response body. A key with a query
	// matches path and query together.
	bodies map[string]string
	status map[string]int
	// writes records every request that was not a GET, so a test can prove
	// that Plan mutates nothing.
	writes []string
	reads  []string
}

func newFake(t *testing.T) *fakeBookStack {
	return &fakeBookStack{
		t:      t,
		bodies: map[string]string{},
		status: map[string]int{},
	}
}

func (f *fakeBookStack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if got := r.Header.Get("Authorization"); got != "Token "+testTokenID+":"+testTokenSecret {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"No authorization token found on the request","code":401}}`)
		return
	}

	key := r.Method + " " + r.URL.Path
	withQuery := key
	if r.URL.RawQuery != "" {
		withQuery += "?" + r.URL.RawQuery
	}

	f.mu.Lock()
	if r.Method == http.MethodGet {
		f.reads = append(f.reads, withQuery)
	} else {
		f.writes = append(f.writes, withQuery)
	}
	body, ok := f.bodies[withQuery]
	if !ok {
		body, ok = f.bodies[key]
	}
	code := f.status[key]
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if code != 0 {
		w.WriteHeader(code)
		io.WriteString(w, body)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":{"message":"Item not found","code":404}}`)
		return
	}
	io.WriteString(w, body)
}

// wrote reports the non-GET requests the fake saw.
func (f *fakeBookStack) wrote() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

// newPlugin builds a configured plugin pointed at the fake.
func newPlugin(t *testing.T, f *fakeBookStack) *Plugin {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	p, err := New(testDeps(), Config{
		Host: srv.URL, TokenID: testTokenID, TokenSecret: testTokenSecret,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// stubApprovals is a non-functioning approval service.
//
// The host refuses to mount a plugin that registers mutations without one, on
// purpose: a change that cannot be approved must not be offered. These tests
// only need registration to succeed, so nothing here is expected to be called.
type stubApprovals struct{}

func (stubApprovals) Propose(context.Context, *auth.Principal, operations.ProposeRequest) (*operations.Operation, error) {
	return nil, errors.New("not used in these tests")
}

func (stubApprovals) Approve(context.Context, *auth.Principal, string, string) (*operations.Operation, error) {
	return nil, errors.New("not used in these tests")
}

func (stubApprovals) Reject(context.Context, *auth.Principal, string, string) (*operations.Operation, error) {
	return nil, errors.New("not used in these tests")
}

func (stubApprovals) Cancel(context.Context, *auth.Principal, string, string) (*operations.Operation, error) {
	return nil, errors.New("not used in these tests")
}

func (stubApprovals) Get(context.Context, *auth.Principal, string) (*operations.Operation, error) {
	return nil, errors.New("not used in these tests")
}

func (stubApprovals) ApproveInline(context.Context, *auth.Principal, string) (*operations.Operation, error) {
	return nil, errors.New("not used in these tests")
}

func (stubApprovals) AwaitOutcome(context.Context, string, time.Duration) (*operations.Operation, error) {
	return nil, errors.New("not used in these tests")
}

func (stubApprovals) List(context.Context, *auth.Principal, string, []operations.OperationState, int) ([]*operations.Operation, error) {
	return nil, errors.New("not used in these tests")
}
