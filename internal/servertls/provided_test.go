package servertls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// A company authority, and certificates it issues, made the way an AD CS or
// any other private authority would: a CA certificate, and leaves signed by it.
type testAuthority struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newAuthority(t *testing.T, subject pkix.Name) testAuthority {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: subject,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return testAuthority{cert: cert, key: key,
		pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

type testLeaf struct {
	certPEM, keyPEM []byte
}

// issue signs a server certificate for hosts. edit adjusts the template for
// the cases a real authority gets wrong.
func (a testAuthority) issue(t *testing.T, hosts []string, edit func(*x509.Certificate)) testLeaf {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: hosts[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	if edit != nil {
		edit(tmpl)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.cert, &key.PublicKey, a.key)
	if err != nil {
		t.Fatal(err)
	}
	return testLeaf{
		certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		// PKCS#1, the "RSA PRIVATE KEY" form an openssl-made key often is.
		keyPEM: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}),
	}
}

func companyCA(t *testing.T) testAuthority {
	return newAuthority(t, pkix.Name{CommonName: "Acme Issuing CA", Organization: []string{"Acme"}})
}

func refusalOf(t *testing.T, err error) string {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) {
		t.Fatalf("want a refusal written for a person, got %v", err)
	}
	return r.Reason
}

// The ordinary case: a certificate from the company's authority for the
// address people use, sent as a certificate and a key.
func TestACompanyCertificateIsAccepted(t *testing.T) {
	ca := companyCA(t)
	leaf := ca.issue(t, []string{"mcpd.example.com", "203.0.113.10"}, nil)

	m, bundle, err := ParseProvided(time.Now(), true, leaf.certPEM, leaf.keyPEM)
	if err != nil {
		t.Fatalf("ParseProvided: %v", err)
	}
	if !m.Provided || m.Issuer != "Acme Issuing CA" || m.Subject != "mcpd.example.com" {
		t.Errorf("described as %+v", m)
	}
	if strings.Join(m.Hosts, ",") != "203.0.113.10,mcpd.example.com" {
		t.Errorf("covers %v", m.Hosts)
	}
	if m.CloudflareOrigin {
		t.Error("a company certificate was taken for Cloudflare's")
	}
	// Stored as one file, so a replacement is one rename.
	if !strings.Contains(string(bundle), "BEGIN CERTIFICATE") || !strings.Contains(string(bundle), "BEGIN PRIVATE KEY") {
		t.Errorf("the bundle should hold the certificate and the key:\n%s", bundle)
	}
}

// A chain pasted root-first is the usual mistake, and serving it in that
// order is refused by some clients. The certificate the key belongs to goes
// first, wherever it was pasted -- here, one file holding everything.
func TestTheLeafIsServedFirstWhateverOrderItCameIn(t *testing.T) {
	ca := companyCA(t)
	leaf := ca.issue(t, []string{"mcpd.example.com"}, nil)
	oneFile := append(append(append([]byte{}, leaf.keyPEM...), ca.pem...), leaf.certPEM...)

	m, _, err := ParseProvided(time.Now(), true, oneFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Certificate.Certificate) != 2 {
		t.Fatalf("chain has %d certificates, want the leaf and the authority", len(m.Certificate.Certificate))
	}
	first, _ := x509.ParseCertificate(m.Certificate.Certificate[0])
	if first.IsCA {
		t.Error("the authority is being served first")
	}
}

func TestWhatCannotBeServedIsRefusedWithAReason(t *testing.T) {
	ca := companyCA(t)
	good := ca.issue(t, []string{"mcpd.example.com"}, nil)
	other := ca.issue(t, []string{"other.example.com"}, nil)
	expired := ca.issue(t, []string{"mcpd.example.com"}, func(c *x509.Certificate) {
		c.NotBefore = time.Now().Add(-48 * time.Hour)
		c.NotAfter = time.Now().Add(-24 * time.Hour)
	})
	clientOnly := ca.issue(t, []string{"mcpd.example.com"}, func(c *x509.Certificate) {
		c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	})
	encrypted := pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{1, 2, 3}})
	request := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: []byte{1, 2, 3}})

	for _, tc := range []struct {
		name  string
		parts [][]byte
		want  string
	}{
		{"a key from a different certificate", [][]byte{good.certPEM, other.keyPEM}, "doesn't belong"},
		{"no key at all", [][]byte{good.certPEM}, "no private key"},
		{"no certificate at all", [][]byte{good.keyPEM}, "no certificate"},
		{"a passphrase on the key", [][]byte{good.certPEM, encrypted}, "passphrase"},
		{"a request sent instead of a certificate", [][]byte{request}, "certificate request"},
		{"a binary file", [][]byte{{0x30, 0x82, 0x01, 0x0a}}, "isn't PEM"},
		{"the authority's certificate as the server's", [][]byte{ca.pem, pemKey(t, ca.key)}, "authority's own"},
		{"a certificate for clients, not servers", [][]byte{clientOnly.certPEM, clientOnly.keyPEM}, "web server"},
		{"one that has run out", [][]byte{expired.certPEM, expired.keyPEM}, "ran out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseProvided(time.Now(), true, tc.parts...)
			if reason := refusalOf(t, err); !strings.Contains(reason, tc.want) {
				t.Errorf("refusal %q does not say %q", reason, tc.want)
			}
		})
	}
}

// Uploading an expired certificate is refused, but one already in place that
// runs out is still served: a warning in the browser can be clicked past, and
// falling back to plain http behind an https address is a sign-in that cannot
// work at all.
func TestAnInstalledCertificateThatRunsOutIsStillLoaded(t *testing.T) {
	ca := companyCA(t)
	leaf := ca.issue(t, []string{"mcpd.example.com"}, nil)
	dir := t.TempDir()
	if _, err := StoreProvided(dir, time.Now(), leaf.certPEM, leaf.keyPEM); err != nil {
		t.Fatal(err)
	}

	later := time.Now().Add(365 * 24 * time.Hour)
	m, err := LoadProvided(dir, later)
	if err != nil {
		t.Fatalf("an installed certificate that has since run out should still load: %v", err)
	}
	if !later.After(m.NotAfter) {
		t.Fatal("the test certificate should have run out by then")
	}
}

func TestStoringAndLoadingRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadProvided(dir, time.Now()); !errors.Is(err, ErrNoProvided) {
		t.Fatalf("with nothing installed, want ErrNoProvided, got %v", err)
	}

	ca := companyCA(t)
	leaf := ca.issue(t, []string{"mcpd.example.com"}, nil)
	stored, err := StoreProvided(dir, time.Now(), leaf.certPEM, leaf.keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ProvidedPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the file holds a private key and is %v", info.Mode().Perm())
	}
	loaded, err := LoadProvided(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint != stored.Fingerprint {
		t.Error("what was loaded is not what was stored")
	}

	if err := RemoveProvided(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProvided(dir, time.Now()); !errors.Is(err, ErrNoProvided) {
		t.Errorf("after removing it, want ErrNoProvided, got %v", err)
	}
}

// A certificate from Cloudflare's Origin CA is only trusted by Cloudflare's
// own proxy. It is the obvious thing to download for a domain on Cloudflare,
// and every browser reaching the dashboard directly refuses it.
func TestACloudflareOriginCertificateIsRecognised(t *testing.T) {
	origin := newAuthority(t, pkix.Name{
		Organization:       []string{"CloudFlare, Inc."},
		OrganizationalUnit: []string{"CloudFlare Origin SSL Certificate Authority"},
	})
	leaf := origin.issue(t, []string{"mcpd.example.com"}, nil)

	m, _, err := ParseProvided(time.Now(), true, leaf.certPEM, leaf.keyPEM)
	if err != nil {
		t.Fatalf("it can be served, it just won't be trusted: %v", err)
	}
	if !m.CloudflareOrigin {
		t.Error("a Cloudflare Origin certificate was not recognised")
	}
}

func pemKey(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}
