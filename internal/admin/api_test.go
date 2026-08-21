package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The dashboard's static handler serves a single-page application, so an
// unknown path is a client-side route rather than a missing file. Without
// that, a reload on any route but "/" returns 404.
func TestStaticHandler_ServesSPARoutes(t *testing.T) {
	s := NewServer(Options{})

	for _, path := range []string{"/", "/operations", "/operations/op_123", "/plugins"} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.staticHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("%s = %d, want 200", path, w.Code)
			}
			if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Fatalf("content-type = %q, want html", ct)
			}
		})
	}
}

// An unmatched /api path is a genuine 404. Serving HTML there would make a
// broken API call look like a page.
func TestStaticHandler_ApiPathsAreNotRoutes(t *testing.T) {
	s := NewServer(Options{})
	w := httptest.NewRecorder()
	s.staticHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nonexistent", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q, want json", ct)
	}
}

// Vite emits content-hashed filenames, so a bundle's URL changes whenever its
// contents do. That makes an immutable cache safe for assets and unsafe for
// index.html, which names them.
func TestStaticHandler_CachingPolicy(t *testing.T) {
	s := NewServer(Options{})

	w := httptest.NewRecorder()
	s.staticHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index.html cache-control = %q; a cached copy would point at "+
			"bundles that no longer exist after an upgrade", got)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := NewServer(Options{})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/meta", nil))

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for header, expected := range want {
		if got := w.Header().Get(header); got != expected {
			t.Errorf("%s = %q, want %q", header, got, expected)
		}
	}
	csp := w.Header().Get("Content-Security-Policy")
	for _, directive := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !contains(csp, directive) {
			t.Errorf("CSP is missing %q: %s", directive, csp)
		}
	}
}

// Every API route requires a credential. A missing one is 401, never a
// partial response.
func TestAPI_RequiresAuthentication(t *testing.T) {
	s := NewServer(Options{Verifier: rejectingVerifier{}})

	paths := []string{
		"/api/operations", "/api/plugins", "/api/audit", "/api/health",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s without a token = %d, want 401", path, w.Code)
			}
		})
	}
}

// The meta endpoint is unauthenticated because the login page needs it, so it
// must disclose nothing beyond the version and auth scheme.
func TestMeta_IsPublicAndMinimal(t *testing.T) {
	s := NewServer(Options{Verifier: rejectingVerifier{}, Version: "1.2.3"})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/meta", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// needs_setup joins version and auth_mode. It is a fact anyone can
	// establish by trying to register, and the dashboard cannot decide between
	// a sign-in form and a registration form without it.
	want := map[string]bool{"version": true, "auth_mode": true, "needs_setup": true}
	for k := range body {
		if !want[k] {
			t.Errorf("meta discloses %q; it is unauthenticated and must carry "+
				"nothing about the plugins, the configuration, or the host", k)
		}
	}
	if len(body) != len(want) {
		t.Fatalf("meta exposes %d fields (%v), want %d", len(body), body, len(want))
	}
}

func TestParseLimit(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 50}, {"10", 10}, {"0", 50}, {"-5", 50},
		{"abc", 50}, {"9999", 200},
	}
	for _, tc := range tests {
		if got := parseLimit(tc.in, 50, 200); got != tc.want {
			t.Errorf("parseLimit(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
