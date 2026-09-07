package httpguard_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/plugins/httpguard"
)

// allowed is a table with the two shapes that matter: a plain read, and a read
// that carries an identifier in the path.
var allowed = []httpguard.Rule{
	httpguard.Get(`^/devices$`, "listing devices"),
	httpguard.Get(`^/devices/[^/]+$`, "reading one device"),
	httpguard.Post(`^/search$`, "running a search"),
}

// okTransport stands in for the network, so a permitted request cannot fail on
// DNS and be mistaken for a refused one.
type okTransport struct{ seen *http.Request }

func (t *okTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.seen = r
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
}

func try(t *testing.T, prefix, method, target string) error {
	t.Helper()
	c := httpguard.Client(&http.Client{Transport: &okTransport{}}, "acme", prefix, allowed)
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, err = c.Transport.RoundTrip(req)
	return err
}

func TestGuard_PermitsWhatTheTableNames(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/devices"},
		{http.MethodGet, "/devices/17"},
		{http.MethodPost, "/search"},
	} {
		if err := try(t, "", tc.method, "http://host.invalid"+tc.path); err != nil {
			t.Errorf("%s %s was refused: %v", tc.method, tc.path, err)
		}
	}
}

// Default-deny is the whole guarantee: a path nobody named is refused even when
// it is the harmless-looking neighbour of one that was.
func TestGuard_RefusesWhatTheTableDoesNot(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/accounts"},
		{http.MethodDelete, "/devices/17"},
		{http.MethodPost, "/devices"},
		{http.MethodGet, "/devices/17/reboot"},
	} {
		if err := try(t, "", tc.method, "http://host.invalid"+tc.path); err == nil {
			t.Errorf("%s %s was permitted", tc.method, tc.path)
		}
	}
}

// A path the table knows, reached with a method it does not, says what the path
// is for. That sentence is what makes a bug in a plugin findable.
func TestGuard_NamesWhatAKnownPathIsFor(t *testing.T) {
	err := try(t, "", http.MethodDelete, "http://host.invalid/devices")
	if err == nil {
		t.Fatal("DELETE /devices was permitted")
	}
	if !strings.Contains(err.Error(), "listing devices") {
		t.Errorf("the refusal does not say what the path is for: %v", err)
	}
	if !strings.Contains(err.Error(), "acme:") {
		t.Errorf("the refusal does not name the integration: %v", err)
	}
}

// An installation behind a proxy is an ordinary deployment. A guard that
// trimmed a fixed root would refuse every one of its requests.
func TestGuard_HandlesAPrefix(t *testing.T) {
	if err := try(t, "/gateway", http.MethodGet, "http://host.invalid/gateway/devices"); err != nil {
		t.Errorf("a prefixed path was refused: %v", err)
	}
	// The prefix has to be a whole segment. CutPrefix alone would accept this
	// and hand the table "foo/devices".
	//
	// The reason is asserted, not just the refusal. Every pattern in a rule
	// table is anchored with a leading slash, so "foo/devices" would fail to
	// match anything and be refused regardless -- which means checking only
	// that it was refused does not test this at all. The segment check is what
	// must refuse it, and that is what the sentence says.
	err := try(t, "/gateway", http.MethodGet, "http://host.invalid/gatewayfoo/devices")
	if err == nil {
		t.Fatal("a path that only shares the prefix's letters was permitted")
	}
	if !strings.Contains(err.Error(), "base path") {
		t.Errorf("refused for the wrong reason -- the segment check did not catch it: %v", err)
	}
	// Not under the prefix at all: refused rather than trimmed to something
	// that might match.
	if err := try(t, "/gateway", http.MethodGet, "http://host.invalid/devices"); err == nil {
		t.Error("a path outside the prefix was permitted")
	}
}

// A request must not reach an anchored pattern by a different spelling. This is
// the whole of how an allow-list gets walked past.
func TestGuard_ComparesTheFormTheServerRoutesOn(t *testing.T) {
	// Percent-encoded separator: one segment to a pattern written against
	// RawPath, two to the server.
	if err := try(t, "", http.MethodGet, "http://host.invalid/devices/17%2Freboot"); err == nil {
		t.Error("an encoded separator walked past the anchored pattern")
	}
	// Doubled separators and a trailing slash normalise to the plain form
	// rather than missing the pattern.
	for _, target := range []string{
		"http://host.invalid//devices",
		"http://host.invalid/devices/",
	} {
		if err := try(t, "", http.MethodGet, target); err != nil {
			t.Errorf("%s was refused: %v", target, err)
		}
	}
}

// Following a redirect would carry the Authorization header to whatever host
// the redirect named.
func TestGuard_DoesNotFollowARedirect(t *testing.T) {
	var reached int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		http.Redirect(w, r, "/devices/1", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	c := httpguard.Client(srv.Client(), "acme", "", allowed)
	resp, err := c.Get(srv.URL + "/devices")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("the redirect was followed; status %d", resp.StatusCode)
	}
	if reached != 1 {
		t.Errorf("the server was reached %d times, so the redirect was chased", reached)
	}
}

// A nil client is the construction path a plugin uses before it has one of its
// own. It must still be guarded rather than wide open.
func TestGuard_GuardsANilClient(t *testing.T) {
	c := httpguard.Client(nil, "acme", "", allowed)
	req, err := http.NewRequest(http.MethodDelete, "http://host.invalid/devices", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := c.Transport.RoundTrip(req); err == nil {
		t.Error("a nil client produced an unguarded transport")
	}
}

// The guard refuses before the network, so a refusal costs no request. Nothing
// downstream should ever see one.
func TestGuard_RefusesBeforeTheNetwork(t *testing.T) {
	base := &okTransport{}
	c := httpguard.Client(&http.Client{Transport: base}, "acme", "", allowed)
	req, err := http.NewRequest(http.MethodDelete, "http://host.invalid/devices", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if _, err := c.Transport.RoundTrip(req); err == nil {
		t.Fatal("the request was permitted")
	}
	if base.seen != nil {
		t.Error("a refused request still reached the transport underneath")
	}
}

func TestNormalise(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"devices", "/devices"},
		{"/devices/", "/devices"},
		{"//devices//17", "/devices/17"},
		{"  /devices  ", "/devices"},
		{"/devices?page=2", "/devices"},
		{"/devices#top", "/devices"},
	} {
		if got := httpguard.Normalise(tc.in); got != tc.want {
			t.Errorf("Normalise(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// A malformed pattern is a panic at start-up rather than a rule that silently
// never matches.
func TestGet_PanicsOnAMalformedPattern(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a malformed pattern was accepted")
		}
	}()
	_ = httpguard.Get(`^/devices[$`, "listing devices")
}
