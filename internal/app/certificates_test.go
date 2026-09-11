package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/settings"
)

// restartWith stores values, then opens the same data again: the dashboard's
// certificate is read when the process starts, so a restart is the only way a
// change to it reaches the listeners.
func restartWith(t *testing.T, dir string, values map[string]string) *App {
	t.Helper()
	first := newAppIn(t, dir)
	var changes []settings.Change
	for k, v := range values {
		changes = append(changes, settings.Change{Key: k, Value: v})
	}
	if err := first.settings.Apply(context.Background(), "test", changes); err != nil {
		t.Fatalf("apply: %v", err)
	}
	first.db.Close()
	return newAppIn(t, dir)
}

// The two listeners are reached different ways, and a deployment behind a
// proxy may want mcpd's own certificate on one and not the other. Turning it
// on for the dashboard must leave the assistants' listener exactly as it was.
func TestTheDashboardsCertificateLeavesTheAssistantsListenerAlone(t *testing.T) {
	a := restartWith(t, t.TempDir(), map[string]string{
		settings.KeyServerFrontendTLSMode:   "self-signed",
		settings.KeyServerFrontendPublicURL: "https://203.0.113.10",
	})

	if !a.certs.dashboard {
		t.Fatal("the dashboard was asked to serve https and is not")
	}
	if a.server.TLSConfig != nil {
		t.Error("the assistants' listener is serving TLS, and nobody asked it to")
	}
	status := a.tlsStatus(context.Background())
	if !status.Dashboard.On || status.Assistants.On {
		t.Errorf("status = %+v; want the dashboard on and the assistants' listener off", status)
	}
	// The address people use has to be covered: a certificate for localhost
	// alone is refused by every browser that is not on this machine.
	if !slices.Contains(status.Hosts, "203.0.113.10") {
		t.Errorf("certificate covers %v, not the address this page is on", status.Hosts)
	}
	if status.Dashboard.Warning != "" {
		t.Errorf("unexpected warning: %s", status.Dashboard.Warning)
	}
	if !status.Authority {
		t.Error("the authority to install should be offered for download")
	}
}

// Plain http on the dashboard's port is sent on to https rather than served,
// once the dashboard has a certificate.
func TestThePlainDashboardSendsPeopleToHTTPS(t *testing.T) {
	a := restartWith(t, t.TempDir(), map[string]string{
		settings.KeyServerFrontendTLSMode:   "self-signed",
		settings.KeyServerFrontendPublicURL: "https://203.0.113.10",
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://203.0.113.10/settings/authentication", nil)
	a.frontend.Handler.ServeHTTP(w, r)
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("got %d, want a temporary redirect", w.Code)
	}
	if got := w.Header().Get("Location"); got != "https://203.0.113.10/settings/authentication" {
		t.Errorf("redirected to %q", got)
	}
}

// The dashboard is the page this would be fixed on. If its certificate cannot
// be made, it is served over plain http and says why; refusing to start would
// take the page away.
func TestADashboardWhoseCertificateFailsIsServedPlainAndSaysWhy(t *testing.T) {
	dir := t.TempDir()
	first := newAppIn(t, dir)
	if err := first.settings.Apply(context.Background(), "test", []settings.Change{
		{Key: settings.KeyServerFrontendTLSMode, Value: "self-signed"},
	}); err != nil {
		t.Fatal(err)
	}
	first.db.Close()
	// A file where the certificate directory would go, which no permission
	// check can see past -- root included, which is who a container test runs
	// as.
	if err := os.WriteFile(filepath.Join(dir, "tls"), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := newAppIn(t, dir)
	if a.certs.dashboard {
		t.Fatal("the dashboard claims https with no certificate to serve")
	}
	status := a.tlsStatus(context.Background())
	if status.Dashboard.On || status.Dashboard.Problem == "" || status.Dashboard.Detail == "" {
		t.Errorf("status = %+v; want off, with the reason and the error behind it", status)
	}

	w := httptest.NewRecorder()
	a.frontend.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://203.0.113.10/", nil))
	if w.Code == http.StatusTemporaryRedirect {
		t.Error("plain http is being sent to an https that is not there")
	}
}

// Nothing asked for, nothing changed: the default is the deployment behind
// Cloudflare or a proxy, and it must not notice this feature exists.
func TestByDefaultNeitherListenerServesACertificate(t *testing.T) {
	a := newAppIn(t, t.TempDir())
	if a.certs.dashboard || a.certs.assistants || a.server.TLSConfig != nil {
		t.Errorf("certs = %+v; nothing was asked for", a.certs)
	}
	if a.caPEM() != nil {
		t.Error("an authority is offered with no certificate behind it")
	}
}

// Every browser that has not been given the authority yet fails the handshake
// on every connection. That is expected and fixed on the client, so it is not
// a warning; anything else net/http reports still is.
func TestAnUntrustedBrowserIsNotAWarning(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := serverErrorLog(log)

	l.Print("http: TLS handshake error from 203.0.113.20:51000: remote error: tls: unknown certificate")
	l.Print("http: Accept error: accept tcp [::]:8081: accept4: too many open files")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want two lines, got %q", buf.String())
	}
	if !strings.Contains(lines[0], "level=DEBUG") {
		t.Errorf("a refused handshake should be debug: %s", lines[0])
	}
	if !strings.Contains(lines[1], "level=WARN") {
		t.Errorf("anything else should stay a warning: %s", lines[1])
	}
}
