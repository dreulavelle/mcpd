package mcpremote

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// recorder is a server that remembers what actually arrived, so a test can
// assert on the credential at the far end rather than on the error near it.
type recorder struct {
	*httptest.Server

	mu    sync.Mutex
	hits  int
	auths []string
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.hits++
		r.auths = append(r.auths, req.Header.Get("Authorization"))
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *recorder) seen() (int, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits, append([]string(nil), r.auths...)
}

// unpinnedTransport is headerTransport as it was before the origin pin: it
// stamps the configured headers onto every request it handles, whatever the
// address.
//
// It is here as the control. Without it, "the attacker received no
// credential" is a claim that would also hold if the test never reached the
// attacker at all, or if the header were named something else, or if the
// recorder were broken. With it, the same two servers demonstrably do leak --
// so the guarded case is measuring what it says it measures.
type unpinnedTransport struct {
	headers map[string]string
	next    http.RoundTripper
}

func (t *unpinnedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for name, value := range t.headers {
		clone.Header.Set(name, value)
	}
	return t.next.RoundTrip(clone)
}

const operatorKey = "Bearer SUPER-SECRET-OPERATOR-KEY"

// TestRedirect_CredentialNeverLeavesTheConfiguredOrigin is the headline.
//
// An operator imports a plausible server and configures its API key. The
// server -- or a later compromise of it, or an expired domain someone else
// registered -- answers 302 to an address it chooses. Nothing the operator
// typed may go there.
//
// Go's own protection does not cover this. The standard library strips
// Authorization and Cookie on a cross-domain redirect, but only for headers
// set on the original request; it cannot see one a RoundTripper injects per
// hop, which is what this transport does.
func TestRedirect_CredentialNeverLeavesTheConfiguredOrigin(t *testing.T) {
	attacker := newRecorder(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/", http.StatusFound)
	}))
	t.Cleanup(upstream.Close)

	headers := map[string]string{"Authorization": operatorKey}

	t.Run("unpinned, which is the bug", func(t *testing.T) {
		client := &http.Client{Transport: &unpinnedTransport{
			headers: headers, next: http.DefaultTransport,
		}}
		resp, err := client.Post(upstream.URL+"/mcp", "application/json", nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()

		hits, auths := attacker.seen()
		if hits == 0 {
			t.Fatal("the control never reached the redirect target, so this test " +
				"would prove nothing about the guarded case")
		}
		if len(auths) == 0 || auths[0] != operatorKey {
			t.Fatalf("the control did not leak, so the guarded case below is not "+
				"measuring anything: got %q", auths)
		}
		t.Logf("control: attacker reached %d time(s), received %q", hits, auths[0])
	})

	attacker.mu.Lock()
	attacker.hits, attacker.auths = 0, nil
	attacker.mu.Unlock()

	t.Run("pinned to the configured origin", func(t *testing.T) {
		client, err := newHTTPClient(upstream.URL+"/mcp", headers)
		if err != nil {
			t.Fatalf("client: %v", err)
		}
		resp, reqErr := client.Post(upstream.URL+"/mcp", "application/json", nil)
		if resp != nil {
			resp.Body.Close()
		}

		// The assertion that matters is at the far end, not on the error.
		hits, auths := attacker.seen()
		for _, got := range auths {
			if strings.Contains(got, "SUPER-SECRET") {
				t.Errorf("the credential reached the redirect target: %q", got)
			}
		}
		if hits != 0 {
			t.Errorf("the redirect was followed to another origin %d time(s); it "+
				"should have been refused outright", hits)
		}
		// And the refusal is named, so an operator can act on it.
		if reqErr == nil {
			t.Fatal("expected the request to fail once the redirect was refused")
		}
		if !errors.Is(reqErr, errCrossOriginRedirect) {
			t.Errorf("error = %v, want a cross-origin refusal", reqErr)
		}
	})
}

// TestRedirect_SameOriginStillWorks: the rule is a pin, not a ban. A server
// that redirects /mcp to /mcp/ is doing something ordinary.
func TestRedirect_SameOriginStillWorks(t *testing.T) {
	var mu sync.Mutex
	var landedAuth string
	var landed bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mcp" {
			http.Redirect(w, r, "/mcp/", http.StatusFound)
			return
		}
		mu.Lock()
		landed, landedAuth = true, r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	client, err := newHTTPClient(srv.URL+"/mcp", map[string]string{"Authorization": operatorKey})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := client.Post(srv.URL+"/mcp", "application/json", nil)
	if err != nil {
		t.Fatalf("a same-origin redirect must still be followed: %v", err)
	}
	resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	if !landed {
		t.Fatal("the same-origin redirect was not followed")
	}
	if landedAuth != operatorKey {
		t.Errorf("the credential should still go to the configured origin, got %q", landedAuth)
	}
}

// TestHeaderTransport_IsIndependentOfCheckRedirect.
//
// The transport is the last code between a credential and a socket. It has to
// make the decision itself, so that a caller which builds it without the
// client above -- or a future change to CheckRedirect -- cannot turn header
// injection into header exfiltration.
func TestHeaderTransport_IsIndependentOfCheckRedirect(t *testing.T) {
	elsewhere := newRecorder(t)

	configured, err := url.Parse("https://configured.example/mcp")
	if err != nil {
		t.Fatal(err)
	}
	transport := &headerTransport{
		allowed: originOf(configured),
		headers: map[string]string{"Authorization": operatorKey},
		next:    http.DefaultTransport,
	}

	// No http.Client, so no CheckRedirect anywhere near this.
	req, err := http.NewRequest(http.MethodPost, elsewhere.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	resp.Body.Close()

	hits, auths := elsewhere.seen()
	if hits != 1 {
		t.Fatalf("expected the request to be sent, got %d", hits)
	}
	if auths[0] != "" {
		t.Errorf("the transport injected a credential for an address it was not "+
			"configured for: %q", auths[0])
	}
}

// TestHeaderTransport_StripsAPreExistingConfiguredHeader: belt and braces. The
// only way one of these names could already be on an off-origin request is a
// path that put it there for the address we are no longer talking to.
func TestHeaderTransport_StripsAPreExistingConfiguredHeader(t *testing.T) {
	elsewhere := newRecorder(t)

	configured, _ := url.Parse("https://configured.example/mcp")
	transport := &headerTransport{
		allowed: originOf(configured),
		headers: map[string]string{"Authorization": operatorKey},
		next:    http.DefaultTransport,
	}

	req, _ := http.NewRequest(http.MethodPost, elsewhere.URL+"/", nil)
	req.Header.Set("Authorization", operatorKey)

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	resp.Body.Close()

	if _, auths := elsewhere.seen(); auths[0] != "" {
		t.Errorf("a pre-set credential survived an off-origin hop: %q", auths[0])
	}
}

// TestCheckRedirect_RefusesTheDeploymentsOwnNetwork is C5: same root cause,
// different consequence. A public server answering
// "302 Location: http://169.254.169.254/latest/meta-data/" is asking this host
// to fetch its cloud credentials from inside the network.
//
// Unreachable while the origin rule is same-origin, and kept because it is
// what would still hold if that rule were loosened.
//
// The last three cases are the ones that decide whether this rule is usable at
// all. A developer pointing mcpd at a server on loopback, and a deployment
// running one on its own LAN, are the ordinary cases -- an absolute refusal
// would break both, and neither is made safer by it.
func TestCheckRedirect_RefusesTheDeploymentsOwnNetwork(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		target     string
		refused    bool
	}{
		{
			name: "the cloud metadata service", configured: "https://public.test/mcp",
			target: "http://169.254.169.254/latest/meta-data/", refused: true,
		},
		{
			name: "an RFC1918 address", configured: "https://public.test/mcp",
			target: "http://10.0.0.5/mcp", refused: true,
		},
		{
			name: "another RFC1918 range", configured: "https://public.test/mcp",
			target: "http://192.168.1.1/mcp", refused: true,
		},
		{
			name: "loopback", configured: "https://public.test/mcp",
			target: "http://127.0.0.1:9000/mcp", refused: true,
		},
		{
			name: "IPv6 loopback", configured: "https://public.test/mcp",
			target: "http://[::1]:9000/mcp", refused: true,
		},
		{
			name: "an IPv6 unique-local address", configured: "https://public.test/mcp",
			target: "http://[fd00::1]/mcp", refused: true,
		},
		{
			name: "the unspecified address", configured: "https://public.test/mcp",
			target: "http://0.0.0.0/mcp", refused: true,
		},
		{
			name: "a public address", configured: "https://public.test/mcp",
			target: "https://93.184.216.34/mcp",
		},
		{
			name:       "a hostname, which the origin rule already handles",
			configured: "https://public.test/mcp", target: "https://example.test/mcp",
		},
		{
			name:       "a developer's loopback server, which is legitimate",
			configured: "http://127.0.0.1:9000/mcp", target: "http://127.0.0.1:9000/mcp/",
		},
		{
			name: "localhost by name", configured: "http://localhost:9000/mcp",
			target: "http://127.0.0.1:9000/mcp/",
		},
		{
			name:       "a server on the deployment's own LAN, which is also legitimate",
			configured: "https://10.0.0.5/mcp", target: "https://10.0.0.5/mcp/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configured, err := url.Parse(tc.configured)
			if err != nil {
				t.Fatal(err)
			}
			target, err := url.Parse(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			err = checkRedirectAddress(originOf(configured), target)
			if refused := err != nil; refused != tc.refused {
				t.Errorf("refused = %v, want %v (%v)", refused, tc.refused, err)
			}
			if err != nil && !errors.Is(err, errCrossOriginRedirect) {
				t.Errorf("the refusal should be matchable: %v", err)
			}
		})
	}
}

// TestRedirect_ChainIsBounded. Go's default is ten, which is generous for an
// endpoint that should not be redirecting at all.
func TestRedirect_ChainIsBounded(t *testing.T) {
	var mu sync.Mutex
	var hops int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hops++
		mu.Unlock()
		// Same origin every time, so only the chain length can stop this.
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	client, err := newHTTPClient(srv.URL+"/mcp", nil)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	resp, err := client.Post(srv.URL+"/mcp", "application/json", nil)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("an endless redirect loop must be stopped")
	}

	mu.Lock()
	defer mu.Unlock()
	if hops > maxRedirects+1 {
		t.Errorf("followed %d hops, want at most %d", hops, maxRedirects+1)
	}
}

func TestOrigin_NormalisesDefaultPortsAndCase(t *testing.T) {
	same := func(a, b string) bool {
		ua, _ := url.Parse(a)
		ub, _ := url.Parse(b)
		return originOf(ua).equals(originOf(ub))
	}
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"https://x.test/mcp", "https://x.test:443/other", true},
		{"http://x.test/mcp", "http://x.test:80/other", true},
		{"https://X.TEST/mcp", "https://x.test/mcp", true},
		{"https://x.test/mcp", "http://x.test/mcp", false},
		{"https://x.test/mcp", "https://x.test:8443/mcp", false},
		{"https://x.test/mcp", "https://evil.test/mcp", false},
		{"https://x.test/mcp", "https://x.test.evil.test/mcp", false},
	} {
		if got := same(tc.a, tc.b); got != tc.want {
			t.Errorf("%s vs %s: same origin = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestRedirect_PlaintextIsADifferentOrigin.
//
// Called out separately because it is the variant that looks harmless: the
// host is unchanged, only the scheme drops. Downgrading to http would put the
// credential on the wire in the clear, so the scheme is part of the origin and
// this is refused like any other hop off it.
func TestRedirect_PlaintextIsADifferentOrigin(t *testing.T) {
	plaintext := newRecorder(t)

	// A TLS upstream that tries to send us to a plaintext address.
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plaintext.URL+"/", http.StatusFound)
	}))
	t.Cleanup(upstream.Close)

	client, err := newHTTPClient(upstream.URL+"/mcp", map[string]string{"Authorization": operatorKey})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	// The fixture's certificate is not one this process trusts; borrow the
	// server's own client so the test is about the redirect, not about TLS.
	client.Transport = &headerTransport{
		allowed: originOf(mustParse(t, upstream.URL+"/mcp")),
		headers: map[string]string{"Authorization": operatorKey},
		next:    upstream.Client().Transport,
	}

	resp, reqErr := client.Post(upstream.URL+"/mcp", "application/json", nil)
	if resp != nil {
		resp.Body.Close()
	}

	hits, auths := plaintext.seen()
	for _, got := range auths {
		if strings.Contains(got, "SUPER-SECRET") {
			t.Errorf("the credential went out over plaintext http: %q", got)
		}
	}
	if hits != 0 {
		t.Errorf("a downgrade to http was followed %d time(s)", hits)
	}
	if reqErr == nil || !errors.Is(reqErr, errCrossOriginRedirect) {
		t.Errorf("expected a cross-origin refusal, got %v", reqErr)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// TestBoundedBody_StopsAnOversizedResponse is C10.
//
// The caps in the runtime above are checked as the SDK yields tools, which is
// after it has decoded the whole page -- so a bound meant to stop a server
// exhausting this host has to be where the bytes arrive.
func TestBoundedBody_StopsAnOversizedResponse(t *testing.T) {
	const limit = 1024

	var size int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), size))
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{Transport: &boundedTransport{next: http.DefaultTransport, limit: limit}}

	t.Run("a body at the limit reads normally", func(t *testing.T) {
		size = limit
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if len(body) != limit {
			t.Errorf("read %d bytes, want %d", len(body), limit)
		}
	})

	t.Run("a body past the limit fails at read", func(t *testing.T) {
		size = limit * 4
		resp, err := client.Get(srv.URL)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		read, err := io.ReadAll(resp.Body)
		if !errors.Is(err, errResponseTooLarge) {
			t.Fatalf("error = %v, want errResponseTooLarge", err)
		}
		if len(read) > limit+1 {
			t.Errorf("read %d bytes past the %d limit before failing", len(read), limit)
		}
	})
}

// The real client wires the bound in, whether or not headers are configured.
func TestNewHTTPClient_AlwaysBoundsTheResponseBody(t *testing.T) {
	for _, headers := range []map[string]string{nil, {"Authorization": operatorKey}} {
		client, err := newHTTPClient("https://x.test/mcp", headers)
		if err != nil {
			t.Fatalf("client: %v", err)
		}
		transport := client.Transport
		if h, ok := transport.(*headerTransport); ok {
			transport = h.next
		}
		if _, ok := transport.(*boundedTransport); !ok {
			t.Errorf("response bodies are not bounded with headers=%v: %T", headers != nil, transport)
		}
	}
}
