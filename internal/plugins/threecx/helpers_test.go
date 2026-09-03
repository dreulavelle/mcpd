package threecx

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

const testToken = "tok-abcdef0123456789"

func testDeps() plugins.Deps {
	return plugins.Deps{
		Instance: "threecx",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}
}

// fakePBX is a 3CX that answers the sign-in and a set of OData paths.
//
// It checks two things the real one would not: that every read carries a
// bearer token it issued, and that every read names its fields. The guard in
// this package refuses a select-less read before it is sent, so a fixture that
// sees one has found a hole in the guard rather than in a tool.
type fakePBX struct {
	t       *testing.T
	bodies  map[string]string
	logins  atomic.Int32
	reads   atomic.Int32
	seen    []string
	loginOK bool
	// rejectToken makes reads answer 401 until the next sign-in, to exercise
	// the re-login path.
	rejectToken atomic.Bool
}

func newFakePBX(t *testing.T, bodies map[string]string) (*fakePBX, *httptest.Server) {
	t.Helper()
	f := &fakePBX{t: t, bodies: bodies, loginOK: true}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakePBX) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.seen = append(f.seen, r.Method+" "+r.URL.RequestURI())
	if r.URL.Path == loginPath {
		f.logins.Add(1)
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct{ Username, Password string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !f.loginOK || body.Password != "right-password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.rejectToken.Store(false)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"Status":"AuthSuccess","Token":{"access_token":%q,"expires_in":3600,"token_type":"Bearer"}}`, testToken)
		return
	}

	f.reads.Add(1)
	if r.Header.Get("Authorization") != "Bearer "+testToken || f.rejectToken.Load() {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("$select") == "" {
		f.t.Errorf("a read reached the fake PBX with no $select: %s", r.URL.RequestURI())
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	body, ok := f.bodies[path]
	if !ok {
		f.t.Errorf("unexpected read of %s", r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"","message":"no such path in the fixture"}}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, body)
}

// toolPlugin builds a plugin around a fake PBX serving one customer, Acme.
func toolPlugin(t *testing.T, bodies map[string]string) (*Plugin, *fakePBX) {
	t.Helper()
	f, srv := newFakePBX(t, bodies)
	p := pluginFor(t, srv.Client(), Customer{Name: "Acme", Host: srv.URL, Extension: "100", Password: "right-password"})
	return p, f
}

// pluginFor builds a plugin over the given customers, every one of them
// reached through the fake server's own client so its certificate is trusted.
func pluginFor(t *testing.T, hc *http.Client, customers ...Customer) *Plugin {
	t.Helper()
	p, err := New(testDeps(), Config{Customers: customers})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range p.accounts {
		a.client.http = readOnly(hc, a.host)
	}
	return p
}

// firstClient is the one customer's client, for tests about the client itself.
func firstClient(p *Plugin) *Client { return p.accounts[0].client }

// collection wraps rows as an OData collection response, with a count.
func collection(count int, rows ...string) string {
	return fmt.Sprintf(`{"@odata.context":"x","@odata.count":%d,"value":[%s]}`, count, strings.Join(rows, ","))
}

// mustNotContain fails if the JSON form of v carries any of the words, which
// is how every tool result is checked for a credential that slipped through.
func mustNotContain(t *testing.T, v any, words ...string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range words {
		if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(w)) {
			t.Errorf("the result carries %q: %s", w, raw)
		}
	}
}

// newServer is a plain fake for the client tests that need to control every
// byte of the answer.
func newServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}
