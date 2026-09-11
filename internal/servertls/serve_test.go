package servertls

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// serveBoth starts a server answering https and plain http on one port, the
// way the dashboard does, and returns its address and a client that trusts
// mcpd's own authority.
func serveBoth(t *testing.T, holder *Holder) (string, *http.Client) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	handler := RedirectToHTTPS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "served over "+r.Proto)
	}))
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(Sniff(l, holder.TLSConfig())) }()
	t.Cleanup(func() { _ = srv.Close() })

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(holder.Materials().CAPEM)
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		// The redirect is what is under test, so it is looked at rather than
		// followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return l.Addr().String(), client
}

func holderFor(t *testing.T) *Holder {
	t.Helper()
	m, err := EnsureSelfSigned(t.TempDir(), HostsFor("", "127.0.0.1:0"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return NewHolder(m)
}

// Turning https on changes nothing about where the dashboard is reached, so
// every bookmark is still http://. Both have to answer on the one port: https
// served, and plain http sent on to the same address rather than reset.
func TestOnePortServesHTTPSAndSendsPlainHTTPOn(t *testing.T) {
	addr, client := serveBoth(t, holderFor(t))

	resp, err := client.Get("https://" + addr + "/settings?tab=general")
	if err != nil {
		t.Fatalf("https on the shared port: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(body), "served over") {
		t.Fatalf("https should be served, got %d %q", resp.StatusCode, body)
	}

	resp, err = client.Get("http://" + addr + "/settings?tab=general")
	if err != nil {
		t.Fatalf("plain http on the shared port: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("plain http should be redirected temporarily, got %d", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Location"), "https://"+addr+"/settings?tab=general"; got != want {
		t.Errorf("redirected to %q, want %q: same host, same port, same path", got, want)
	}
}

// A permanent redirect is remembered by the browser and never asked about
// again, so turning https off later would strand every browser that had
// visited. The same goes for Strict-Transport-Security.
func TestTheRedirectIsNotAPromise(t *testing.T) {
	addr, client := serveBoth(t, holderFor(t))
	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusPermanentRedirect {
		t.Errorf("got %d; a permanent redirect cannot be taken back", resp.StatusCode)
	}
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("Strict-Transport-Security %q would outlive turning https off", hsts)
	}
}

// 307 keeps the method and the body, so an API call made to the old address
// arrives as what it was rather than as a GET.
func TestARedirectedWriteKeepsItsMethod(t *testing.T) {
	addr, client := serveBoth(t, holderFor(t))
	resp, err := client.Post("http://"+addr+"/api/settings", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("a POST should be redirected with 307, which keeps the method; got %d", resp.StatusCode)
	}
}

// Each connection is looked at in its own goroutine. A client that connects
// and sends nothing must not hold up the one behind it.
func TestASilentConnectionHoldsUpNobody(t *testing.T) {
	addr, client := serveBoth(t, holderFor(t))

	silent, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()

	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("a request behind a silent connection should be served: %v", err)
	}
	resp.Body.Close()
}

// A renewal reaches the next handshake without a restart. The leaf used to be
// reissued only at startup, so a host left running past its year would have
// served an expired certificate.
func TestARenewedCertificateIsServedWithoutARestart(t *testing.T) {
	holder := holderFor(t)
	addr, client := serveBoth(t, holder)

	serial := func() string {
		t.Helper()
		// A fresh connection each time, so the handshake is the one under test.
		client.CloseIdleConnections()
		resp, err := client.Get("https://" + addr + "/")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.TLS.PeerCertificates[0].SerialNumber.String()
	}
	before := serial()

	renewed, err := EnsureSelfSigned(t.TempDir(), HostsFor("", "127.0.0.1:0"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Signed by a different authority in a different directory, so the client
	// has to be told to trust it too.
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(holder.Materials().CAPEM)
	pool.AppendCertsFromPEM(renewed.CAPEM)
	client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}

	holder.Store(renewed)
	if after := serial(); after == before {
		t.Error("the listener is still presenting the certificate it started with")
	}
}

// Closing the listener ends Accept, which is how the server's shutdown stops
// serving; a listener that kept handing out connections would never let it.
func TestClosingTheListenerEndsAccept(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := Sniff(l, holderFor(t).TLSConfig())
	done := make(chan error, 1)
	go func() {
		_, err := s.Accept()
		done <- err
	}()
	_ = s.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("Accept after Close returned %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept did not return after Close")
	}
}
