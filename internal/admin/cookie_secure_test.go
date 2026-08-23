package admin

import (
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serverWithPublicURL(t *testing.T, accounts Accounts, publicURL string) *Server {
	t.Helper()
	return NewServer(Options{
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts:          accounts,
		FrontendPublicURL: publicURL,
	})
}

func sessionCookieFrom(t *testing.T, res *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie was set")
	return nil
}

// The bug this defends against appears precisely when an operator does the
// correct thing.
//
// The ordinary production shape is an FQDN with a reverse proxy terminating
// TLS and forwarding plain HTTP. r.TLS is then nil while every browser is
// speaking https, so deciding from the connection alone issues the session
// cookie without Secure -- and the browser will happily send it over plain
// http to the same host afterwards. One downgraded request hands over the
// session. It is invisible in testing, because a direct self-signed
// deployment sets r.TLS and looks fine.
func TestSessionCookie_SecureBehindATerminatingProxy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		publicURL  string
		overTLS    bool
		wantSecure bool
	}{
		{
			name:      "https public url, plain request from the proxy",
			publicURL: "https://mcpd.example.com", wantSecure: true,
		},
		{
			name:      "http public url, plain request",
			publicURL: "http://mcpd.internal:8081", wantSecure: false,
		},
		{
			name:    "serving TLS directly, whatever the configuration says",
			overTLS: true, publicURL: "http://mcpd.internal:8081", wantSecure: true,
		},
		{
			name:    "serving TLS directly with no public url configured",
			overTLS: true, wantSecure: true,
		},
		{
			name:      "no public url and a plain request",
			publicURL: "", wantSecure: false,
		},
		{
			// A configuration that does not parse falls back to the
			// connection rather than to a guess in either direction.
			name:      "a public url that does not parse",
			publicURL: "://not a url", wantSecure: false,
		},
		{
			name:    "a public url that does not parse, over TLS",
			overTLS: true, publicURL: "://not a url", wantSecure: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := newFakeAccounts()
			s := serverWithPublicURL(t, accounts, tc.publicURL)

			r := httptest.NewRequest(http.MethodPost, "/api/session",
				strings.NewReader(`{"email":"alice@example.com","password":"a-sufficiently-long-passphrase"}`))
			if tc.overTLS {
				r.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)

			res := w.Result()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", res.StatusCode)
			}
			if got := sessionCookieFrom(t, res).Secure; got != tc.wantSecure {
				t.Errorf("Secure = %t, want %t", got, tc.wantSecure)
			}
		})
	}
}

// The attributes on a clear have to match the ones it was set with, Secure
// included. A clear that omits it does not replace the cookie the browser is
// holding, so the stale one stays and signing out appears not to work.
func TestSessionCookie_ClearingMatchesTheAttributes(t *testing.T) {
	accounts := newFakeAccounts()
	s := serverWithPublicURL(t, accounts, "https://mcpd.example.com")

	r := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	cookie := sessionCookieFrom(t, w.Result())
	if !cookie.Secure {
		t.Error("the clear must carry Secure, or it does not replace the cookie it is clearing")
	}
	if !cookie.HttpOnly || cookie.MaxAge != -1 {
		t.Errorf("clear cookie = %+v", cookie)
	}
}

// A stale cookie is cleared on the way past, and that clear has the same
// problem if it forgets Secure.
func TestSessionCookie_ClearingAnUnresolvableSessionMatchesToo(t *testing.T) {
	accounts := newFakeAccounts()
	s := serverWithPublicURL(t, accounts, "https://mcpd.example.com")

	r := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "a-token-nothing-matches"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if !sessionCookieFrom(t, w.Result()).Secure {
		t.Error("clearing a stale cookie must carry Secure on an https deployment")
	}
}

// The scheme is read from what the operator configured, never from a header
// the caller sent. A forwarded header is set by whoever is talking to this
// process, and nothing here can tell a proxy's from a client's.
func TestSessionCookie_IgnoresXForwardedProto(t *testing.T) {
	accounts := newFakeAccounts()
	s := serverWithPublicURL(t, accounts, "http://mcpd.internal:8081")

	r := httptest.NewRequest(http.MethodPost, "/api/session",
		strings.NewReader(`{"email":"alice@example.com","password":"a-sufficiently-long-passphrase"}`))
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)

	if sessionCookieFrom(t, w.Result()).Secure {
		t.Error("a caller-set header must not decide a cookie attribute")
	}
}

// A plain-HTTP dashboard beside an https MCP endpoint is the ordinary
// self-signed shape, and it is where deciding the cookie from the wrong URL
// broke signing in outright.
//
// server.public_url describes the MCP endpoint. The dashboard is a separate
// listener, and the two routinely differ in scheme: mcpd serves TLS on the MCP
// port from a certificate it issued itself, while the dashboard is reached
// over plain HTTP on the LAN. Reading public_url marked this cookie Secure on
// a plain-HTTP origin; the browser dropped it, every request after the sign-in
// arrived anonymous, and the page said authentication was required.
func TestSessionCookie_TheMCPEndpointsSchemeDoesNotDecideTheDashboards(t *testing.T) {
	srv := NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: &fakeAccounts{},
		// The MCP endpoint serves TLS; the dashboard does not.
		PublicURL: "https://192.168.50.125:9080",
	})

	req := httptest.NewRequest(http.MethodGet, "http://192.168.50.125:9090/", nil)
	if srv.secureCookies(req) {
		t.Fatal("the dashboard cookie was marked Secure because the MCP endpoint " +
			"serves https; a browser drops it on a plain-http origin and signing " +
			"in silently does nothing")
	}
}

// And the reverse-proxy shape still works, from the dashboard's own URL.
func TestSessionCookie_TheDashboardsOwnURLStillWidens(t *testing.T) {
	srv := NewServer(Options{
		Log:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts:          &fakeAccounts{},
		FrontendPublicURL: "https://mcpd.example.com",
	})

	req := httptest.NewRequest(http.MethodGet, "http://10.0.0.5:8081/", nil)
	if !srv.secureCookies(req) {
		t.Fatal("a dashboard behind a TLS-terminating proxy must still get Secure")
	}
}
