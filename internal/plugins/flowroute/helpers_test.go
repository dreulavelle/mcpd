package flowroute

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Every number, name and address in this package's tests is invented. The
// 206 555 range is the one Flowroute's own documentation uses for examples,
// and nothing here came off a live account.
const (
	testAccessKey = "a1b2c3d4"
	testSecretKey = "0123456789abcdef0123456789abcdef"
)

func testDeps() plugins.Deps {
	return plugins.Deps{
		Instance: "flowroute",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}
}

// fakeFlowroute is an API that answers a set of paths with fixtures.
//
// It checks two things the real one would not: that every request carries the
// Basic credential this instance was configured with, and that every request
// is a GET. The guard in this package refuses a write before it is sent, so a
// fixture that sees one has found a hole in the guard rather than in a tool.
type fakeFlowroute struct {
	t *testing.T
	// bodies maps a path to its response body. A path with a `?` in the key
	// matches path and raw query together, for testing pagination.
	bodies map[string]string
	// status overrides the status code for a path.
	status map[string]int

	// mu guards the recorded requests. Probes run every customer at once, so
	// these are appended from several goroutines.
	mu    sync.Mutex
	reads atomic.Int32
	seen  []string
	// keys records the access key each request arrived with, so a test can
	// assert that a customer's question was asked with that customer's
	// credential and not a neighbour's.
	keys []string
	// credentials are the pairs this fake accepts. Empty means the single
	// test pair.
	credentials map[string]string
	// absent are paths this account genuinely has nothing for, answered with
	// the resource-shaped 404.
	absent map[string]bool
}

func newFake(t *testing.T) *fakeFlowroute {
	return &fakeFlowroute{
		t:      t,
		bodies: map[string]string{},
		status: map[string]int{},
		absent: map[string]bool{},
	}
}

func (f *fakeFlowroute) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		f.t.Errorf("fake flowroute saw a %s to %s; the guard should have refused it",
			r.Method, r.URL.Path)
		http.Error(w, "not read-only", http.StatusMethodNotAllowed)
		return
	}
	user, pass, ok := r.BasicAuth()
	want, known := testSecretKey, user == testAccessKey
	if len(f.credentials) > 0 {
		want, known = f.credentials[user]
	}
	if !ok || !known || pass != want {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"errors":[{"status":401,"title":"Unauthorized","detail":"Invalid credentials"}]}`)
		return
	}
	f.reads.Add(1)
	f.mu.Lock()
	f.seen = append(f.seen, r.URL.Path+"?"+r.URL.RawQuery)
	f.keys = append(f.keys, user)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if code, ok := f.status[r.URL.Path]; ok {
		w.WriteHeader(code)
		io.WriteString(w, f.bodies[r.URL.Path])
		return
	}
	if f.absent[r.URL.Path] {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"errors":[{"detail":"No such thing","id":"x","status":404,"title":"Resource not found"}]}`)
		return
	}
	if body, ok := f.bodies[r.URL.Path+"?"+r.URL.RawQuery]; ok {
		io.WriteString(w, body)
		return
	}
	if body, ok := f.bodies[r.URL.Path]; ok {
		io.WriteString(w, body)
		return
	}
	// A path the fixture does not know is answered the way Flowroute answers a
	// URL it does not serve, so a tool reaching for the wrong path fails the
	// way it would in production rather than with a helpful empty list.
	w.WriteHeader(http.StatusNotFound)
	io.WriteString(w, `{"errors":[{"status":"404 Not Found: The requested URL was not found on the server."}]}`)
}

// newPlugin builds a one-customer instance pointed at the fake. One customer,
// so a tool call that names none still resolves -- which is what most of these
// tests are about.
func newPlugin(t *testing.T, f *fakeFlowroute) (*Plugin, *httptest.Server) {
	t.Helper()
	return newPluginFor(t, f, Customer{
		Name:      "Acme Dental Group",
		Aliases:   []string{"acme", "ADG"},
		AccessKey: testAccessKey,
		SecretKey: testSecretKey,
	})
}

// newPluginFor builds an instance serving the given customers.
func newPluginFor(t *testing.T, f *fakeFlowroute, customers ...Customer) (*Plugin, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	p, err := New(testDeps(), Config{Customers: customers, BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, srv
}

// decodeJSON is a small helper for asserting on a tool's result.
func mustJSON(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}
