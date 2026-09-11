package admin

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/servertls"
)

// The lockout this exists for. The dashboard was asked to serve https but
// could not load its certificate, so it fell back to plain http, while its
// address still says https. Following the address would mark the session
// cookie Secure on an http page; the browser drops it, and every correct
// password signs somebody in and straight back out -- on the page the
// certificate would be fixed from.
func TestSessionCookie_NotSecureWhenTheDashboardFellBackToHTTP(t *testing.T) {
	for _, tc := range []struct {
		name       string
		overTLS    bool
		wantSecure bool
	}{
		{"a plain request to a dashboard that fell back", false, false},
		{"a request that did arrive over TLS", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(Options{
				Log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
				Accounts:           newFakeAccounts(),
				FrontendPublicURL:  fixed("https://mcpd.example.com"),
				DashboardServesTLS: func() bool { return true },
			})
			r := httptest.NewRequest(http.MethodPost, "/api/session",
				strings.NewReader(`{"email":"alice@example.com","password":"a-sufficiently-long-passphrase"}`))
			if tc.overTLS {
				r.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d", w.Code)
			}
			if got := sessionCookieFrom(t, w.Result()).Secure; got != tc.wantSecure {
				t.Errorf("Secure = %t, want %t", got, tc.wantSecure)
			}
		})
	}
}

func asAdministrator(r *http.Request) *http.Request {
	return r.WithContext(auth.WithPrincipal(r.Context(), &auth.Principal{ID: "user:admin@example.com"}))
}

// A refusal is written for the person who uploaded the certificate, and is
// passed through as it was written: which of the ways a certificate can be
// wrong this one is, and what to send instead.
func TestDashboardCertificate_ARefusalIsSaidAsWritten(t *testing.T) {
	const reason = "That certificate ran out on 1 January 2026. Upload its replacement."
	s := NewServer(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		SetDashboardCertificate: func(context.Context, string, []byte, []byte) (TLSStatus, error) {
			return TLSStatus{}, &servertls.Refusal{Reason: reason}
		},
	})
	w := httptest.NewRecorder()
	s.handleSetDashboardCertificate(w, asAdministrator(httptest.NewRequest(http.MethodPut,
		"/api/tls/dashboard-certificate", strings.NewReader(`{"certificate":"x"}`))))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ran out on 1 January 2026") {
		t.Errorf("the refusal was not passed through: %s", w.Body.String())
	}
}

// A leaf, two authorities and a 4096-bit key are larger than the 8 KB every
// other request is held to. The upload has its own, larger limit.
func TestDashboardCertificate_AFullChainFits(t *testing.T) {
	var got []byte
	s := NewServer(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		SetDashboardCertificate: func(_ context.Context, actor string, cert, _ []byte) (TLSStatus, error) {
			got = cert
			if actor != "user:admin@example.com" {
				t.Errorf("recorded as %q", actor)
			}
			return TLSStatus{}, nil
		},
	})
	chain := strings.Repeat("A", 20<<10)
	w := httptest.NewRecorder()
	s.handleSetDashboardCertificate(w, asAdministrator(httptest.NewRequest(http.MethodPut,
		"/api/tls/dashboard-certificate", strings.NewReader(`{"certificate":"`+chain+`"}`))))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if len(got) != len(chain) {
		t.Errorf("received %d bytes of a %d byte chain", len(got), len(chain))
	}
}

// Removing the certificate the dashboard is serving would leave the next
// restart with nothing to serve.
func TestDashboardCertificate_TheOneInUseCannotBeRemoved(t *testing.T) {
	s := NewServer(Options{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		RemoveDashboardCertificate: func(context.Context, string) error {
			return ErrCertificateInUse
		},
	})
	w := httptest.NewRecorder()
	s.handleRemoveDashboardCertificate(w, asAdministrator(httptest.NewRequest(http.MethodDelete,
		"/api/tls/dashboard-certificate", nil)))
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}
