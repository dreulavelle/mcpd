package servertls

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestIssuesACertificateForTheAddressesGiven(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	m, err := EnsureSelfSigned(dir, HostsFor("https://192.168.50.125:9080", "0.0.0.0:8080"), now)
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	if !m.Issued {
		t.Fatal("the first call must issue a certificate")
	}

	leaf, err := x509.ParseCertificate(m.Certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.VerifyHostname("192.168.50.125"); err != nil {
		t.Errorf("certificate is not valid for the address clients use: %v", err)
	}
	if err := leaf.VerifyHostname("localhost"); err != nil {
		t.Errorf("certificate is not valid for localhost: %v", err)
	}
	// A certificate that fails on loopback turns every local health check into
	// a false alarm.
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("certificate is not valid on loopback: %v", err)
	}
}

// The CA is what an operator installs. Reissuing it on every start would mean
// trusting it again every time, so only the leaf may be replaced.
func TestTheAuthorityIsKeptAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	first, err := EnsureSelfSigned(dir, []string{"192.168.50.125"}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureSelfSigned(dir, []string{"192.168.50.125"}, now)
	if err != nil {
		t.Fatal(err)
	}

	if string(first.CAPEM) != string(second.CAPEM) {
		t.Fatal("the certificate authority must survive a restart")
	}
	if second.Issued {
		t.Fatal("a usable certificate must be reused, not replaced")
	}
}

// public_url changing is exactly when serving the old certificate would be
// wrong, and it is silent unless the coverage is actually checked.
func TestANewAddressGetsANewCertificate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	first, err := EnsureSelfSigned(dir, []string{"192.168.50.125"}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureSelfSigned(dir, []string{"192.168.50.125", "10.0.0.5"}, now)
	if err != nil {
		t.Fatal(err)
	}

	if !second.Issued {
		t.Fatal("an address the certificate does not cover must trigger a reissue")
	}
	if string(first.CAPEM) != string(second.CAPEM) {
		t.Fatal("reissuing the certificate must not replace the authority")
	}
	leaf, err := x509.ParseCertificate(second.Certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.VerifyHostname("10.0.0.5"); err != nil {
		t.Errorf("the new address is not covered: %v", err)
	}
}

// The whole point of keeping our own CA is that the embedded tunnel client can
// be told about it; if the chain does not verify, the tunnelled OAuth token
// endpoint is unreachable and the connector cannot complete.
func TestTheAuthorityVerifiesTheCertificateItSigned(t *testing.T) {
	dir := t.TempDir()

	m, err := EnsureSelfSigned(dir, []string{"192.168.50.125"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.CAPEM) {
		t.Fatal("the CA it wrote is not usable as a trust root")
	}
	leaf, err := x509.ParseCertificate(m.Certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   "192.168.50.125",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("a client trusting the CA still cannot verify the server: %v", err)
	}
}

// The certificate is served over a real handshake, not only parsed.
func TestItServesARealHandshake(t *testing.T) {
	dir := t.TempDir()

	m, err := EnsureSelfSigned(dir, []string{"127.0.0.1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", m.TLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.(*tls.Conn).Handshake()
	}()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(m.CAPEM)
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		RootCAs:    pool,
		ServerName: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("handshake failed against the CA we published: %v", err)
	}
	conn.Close()
}

// A private key readable by anything else on the box defeats the certificate.
func TestPrivateKeysAreNotReadableByOthers(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureSelfSigned(dir, []string{"127.0.0.1"}, time.Now()); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{caKeyFile, serverKeyFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("%s has mode %o, want no group or world access", name, mode)
		}
	}
}

// Expiry has to be checked, or a long-running deployment eventually serves a
// certificate every client rejects.
func TestAnExpiringCertificateIsReplaced(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	if _, err := EnsureSelfSigned(dir, []string{"127.0.0.1"}, now); err != nil {
		t.Fatal(err)
	}
	later, err := EnsureSelfSigned(dir, []string{"127.0.0.1"}, now.Add(leafLifetime-renewBefore+time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !later.Issued {
		t.Fatal("a certificate close to expiry must be reissued")
	}
}

func TestHostsForCoversTheAddressClientsUse(t *testing.T) {
	hosts := HostsFor("https://192.168.50.125:9080", "0.0.0.0:8080")

	if !contains(hosts, "192.168.50.125") {
		t.Errorf("hosts = %v, want the public address", hosts)
	}
	if !contains(hosts, "127.0.0.1") {
		t.Errorf("hosts = %v, want loopback", hosts)
	}
	// A wildcard bind says nothing about how anyone reaches it, so it must not
	// end up in the certificate.
	if contains(hosts, "0.0.0.0") {
		t.Errorf("hosts = %v, want no wildcard address", hosts)
	}
}

func TestHostsForIncludesASpecificBindAddress(t *testing.T) {
	hosts := HostsFor("https://mcpd.lan:9080", "10.1.2.3:8080")
	if !contains(hosts, "10.1.2.3") {
		t.Errorf("hosts = %v, want the interface it binds", hosts)
	}
}

func TestCoversHandlesEquivalentIPForms(t *testing.T) {
	cert := &x509.Certificate{IPAddresses: []net.IP{net.ParseIP("192.168.50.125")}}
	if !covers(cert, []string{"192.168.50.125"}) {
		t.Fatal("an address written the same way must be recognised")
	}
	if covers(cert, []string{"10.0.0.1"}) {
		t.Fatal("an address not in the certificate must not be reported as covered")
	}
}

func contains(list []string, want string) bool { return slices.Contains(list, want) }
