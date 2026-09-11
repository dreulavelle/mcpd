package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/admin"
	"github.com/spoked/mcpd/internal/servertls"
	"github.com/spoked/mcpd/internal/settings"
)

// restartWith stores values, lets prepare act on the running host, then opens
// the same data again: the dashboard's certificate is read when the process
// starts, so a restart is the only way a change to it reaches the listeners.
func restartWith(t *testing.T, dir string, values map[string]string, prepare ...func(*App)) *App {
	t.Helper()
	first := newAppIn(t, dir)
	var changes []settings.Change
	for k, v := range values {
		changes = append(changes, settings.Change{Key: k, Value: v})
	}
	if err := first.settings.Apply(context.Background(), "test", changes); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, p := range prepare {
		p(first)
	}
	first.db.Close()
	return newAppIn(t, dir)
}

// uploadable is a certificate and key of the kind somebody would upload: a
// server certificate for hosts, as a company authority would issue one.
func uploadable(t *testing.T, hosts ...string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: hosts[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func upload(t *testing.T, a *App, hosts ...string) admin.TLSStatus {
	t.Helper()
	cert, key := uploadable(t, hosts...)
	status, err := a.setDashboardCertificate(context.Background(), "user:admin@example.com", cert, key)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	return status
}

// The two listeners are reached different ways, and a deployment behind a
// proxy may want mcpd's own certificate on one and not the other. Turning it
// on for the dashboard must leave the assistants' listener exactly as it was.
func TestTheDashboardsCertificateLeavesTheAssistantsListenerAlone(t *testing.T) {
	a := restartWith(t, t.TempDir(), map[string]string{
		settings.KeyServerFrontendTLSMode:   "self-signed",
		settings.KeyServerFrontendPublicURL: "https://203.0.113.10",
	})

	if !a.certs.dashboard() {
		t.Fatal("the dashboard was asked to serve https and is not")
	}
	if a.server.TLSConfig != nil {
		t.Error("the assistants' listener is serving TLS, and nobody asked it to")
	}
	status := a.tlsStatus(context.Background())
	if !status.Dashboard.On || status.Dashboard.Source != "own" || status.Assistants.On {
		t.Errorf("status = %+v; want the dashboard on mcpd's own and the assistants' listener off", status)
	}
	// The address people use has to be covered: a certificate for localhost
	// alone is refused by every browser that is not on this machine.
	if status.Own == nil || !slices.Contains(status.Own.Hosts, "203.0.113.10") {
		t.Fatalf("certificate = %+v, not covering the address this page is on", status.Own)
	}
	if len(status.Own.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", status.Own.Warnings)
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
	if a.certs.dashboard() {
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
	// Sign-in has to keep working on the plain page it fell back to.
	if !a.dashboardServesTLS() {
		t.Error("the session cookie would follow the address and be dropped on this http page")
	}
}

// Nothing asked for, nothing changed: the default is the deployment behind
// Cloudflare or a proxy, and it must not notice this feature exists.
func TestByDefaultNeitherListenerServesACertificate(t *testing.T) {
	a := newAppIn(t, t.TempDir())
	if a.certs.dashboard() || a.certs.assistants || a.server.TLSConfig != nil {
		t.Errorf("certs = %+v; nothing was asked for", a.certs)
	}
	if a.caPEM() != nil || a.dashboardServesTLS() {
		t.Error("the default deployment is behaving as if it serves https itself")
	}
}

// A certificate from the company's authority, uploaded and then turned on,
// is what the dashboard serves -- and nothing of mcpd's own is made for it.
func TestAnUploadedCertificateIsServedAfterARestart(t *testing.T) {
	a := restartWith(t, t.TempDir(), map[string]string{
		settings.KeyServerFrontendTLSMode:   "custom",
		settings.KeyServerFrontendPublicURL: "https://203.0.113.10",
	}, func(first *App) { upload(t, first, "203.0.113.10", "mcpd.example.com") })

	status := a.tlsStatus(context.Background())
	if !status.Dashboard.On || status.Dashboard.Source != "provided" {
		t.Fatalf("status = %+v; want the dashboard on the uploaded certificate", status)
	}
	if a.server.TLSConfig != nil || status.Own != nil || a.caPEM() != nil {
		t.Error("mcpd's own certificate was made although nothing asked for it")
	}
	if status.Provided == nil || status.Provided.Subject != "203.0.113.10" ||
		!slices.Contains(status.Provided.Hosts, "mcpd.example.com") ||
		len(status.Provided.Warnings) != 0 {
		t.Errorf("provided = %+v", status.Provided)
	}
	if status.RestartNeeded {
		t.Error("nothing is waiting on a restart")
	}
}

// Replacing the certificate in use needs no restart: the next handshake gets
// the new one.
func TestReplacingTheCertificateInUseTakesEffectAtOnce(t *testing.T) {
	a := restartWith(t, t.TempDir(), map[string]string{
		settings.KeyServerFrontendTLSMode:   "custom",
		settings.KeyServerFrontendPublicURL: "https://203.0.113.10",
	}, func(first *App) { upload(t, first, "203.0.113.10") })
	before := a.certs.dash.Materials().Fingerprint

	status := upload(t, a, "203.0.113.10")
	after := a.certs.dash.Materials().Fingerprint
	if after == before {
		t.Fatal("the dashboard is still presenting the certificate it started with")
	}
	if status.Provided.Fingerprint != after {
		t.Error("the page is describing a different certificate from the one being served")
	}
}

// A renewal tool outside mcpd -- acme.sh, certbot, a script against a company
// authority -- can write the replacement into place, and it is served
// without a restart.
func TestAReplacementWrittenToDiskIsPickedUp(t *testing.T) {
	dir := t.TempDir()
	a := restartWith(t, dir, map[string]string{
		settings.KeyServerFrontendTLSMode:   "custom",
		settings.KeyServerFrontendPublicURL: "https://203.0.113.10",
	}, func(first *App) { upload(t, first, "203.0.113.10") })
	before := a.certs.dash.Materials().Fingerprint

	cert, key := uploadable(t, "203.0.113.10")
	if _, err := servertls.StoreProvided(filepath.Join(dir, "tls"), time.Now(), cert, key); err != nil {
		t.Fatal(err)
	}
	// A file system's clock is coarse; make the change unmistakable.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(servertls.ProvidedPath(filepath.Join(dir, "tls")), later, later); err != nil {
		t.Fatal(err)
	}
	a.reloadProvided(context.Background())
	if a.certs.dash.Materials().Fingerprint == before {
		t.Error("the replacement on disk was not picked up")
	}
}

// Removing the certificate the dashboard serves would leave the next restart
// with nothing to serve.
func TestTheCertificateInUseCannotBeRemoved(t *testing.T) {
	a := restartWith(t, t.TempDir(), map[string]string{
		settings.KeyServerFrontendTLSMode: "custom",
	}, func(first *App) { upload(t, first, "203.0.113.10") })

	if err := a.removeDashboardCertificate(context.Background(), "user:admin@example.com"); !errors.Is(err, admin.ErrCertificateInUse) {
		t.Errorf("got %v, want ErrCertificateInUse", err)
	}
}

// Set to use an uploaded certificate before one is uploaded: the dashboard is
// on plain http and says so, sign-in keeps working there, and once the
// certificate is uploaded the page says a restart is all that is left.
func TestYourOwnCertificateBeforeOneIsUploaded(t *testing.T) {
	a := restartWith(t, t.TempDir(), map[string]string{
		settings.KeyServerFrontendTLSMode:   "custom",
		settings.KeyServerFrontendPublicURL: "https://203.0.113.10",
	})
	status := a.tlsStatus(context.Background())
	if status.Dashboard.On || !strings.Contains(status.Dashboard.Problem, "none had been uploaded") {
		t.Fatalf("status = %+v", status.Dashboard)
	}
	if !a.dashboardServesTLS() {
		t.Error("sign-in on the plain page would lose its cookie to the https address")
	}

	status = upload(t, a, "203.0.113.10")
	if !status.RestartNeeded || status.Dashboard.Problem != "" {
		t.Errorf("after uploading: %+v; want only a restart left", status)
	}
}

// A certificate that does not cover the address people use is refused by
// their browsers, which is worth saying before they find out.
func TestAnUploadedCertificateThatMissesTheAddressIsSaid(t *testing.T) {
	a := restartWith(t, t.TempDir(), map[string]string{
		settings.KeyServerFrontendPublicURL: "https://203.0.113.10",
	})
	status := upload(t, a, "mcpd.example.com")
	if status.Provided == nil || len(status.Provided.Warnings) == 0 ||
		!strings.Contains(status.Provided.Warnings[0], "doesn't cover 203.0.113.10") {
		t.Errorf("provided = %+v", status.Provided)
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
