package trust

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// certOptions describes a certificate a test wants. The zero value is the case
// this feature exists for: a self-signed certificate off an appliance, with no
// extensions on it at all.
type certOptions struct {
	commonName            string
	basicConstraintsValid bool
	isCA                  bool
	keyUsage              x509.KeyUsage
	notBefore, notAfter   time.Time
	ip                    string
}

func makeCert(t *testing.T, o certOptions) (derBytes []byte, pemBytes []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if o.commonName == "" {
		o.commonName = "10.10.12.53"
	}
	if o.notBefore.IsZero() {
		o.notBefore = time.Now().Add(-time.Hour)
	}
	if o.notAfter.IsZero() {
		o.notAfter = time.Now().Add(365 * 24 * time.Hour)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: o.commonName},
		NotBefore:             o.notBefore,
		NotAfter:              o.notAfter,
		BasicConstraintsValid: o.basicConstraintsValid,
		IsCA:                  o.isCA,
		KeyUsage:              o.keyUsage,
	}
	if o.ip != "" {
		tmpl.IPAddresses = []net.IP{net.ParseIP(o.ip)}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return der, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// PEM is what an operator pastes and DER is what a Windows CA exports under a
// .crt extension without saying so. Both have to arrive, and both have to be
// stored as PEM so everything downstream reads one shape.
func TestParse_AcceptsPEMAndDER(t *testing.T) {
	der, pemBytes := makeCert(t, certOptions{commonName: "Work CA"})

	fromPEM, err := Parse(pemBytes)
	if err != nil {
		t.Fatalf("PEM: %v", err)
	}
	fromDER, err := Parse(der)
	if err != nil {
		t.Fatalf("DER: %v", err)
	}
	if fromPEM.Fingerprint != fromDER.Fingerprint {
		t.Errorf("the same certificate parsed two ways gave two fingerprints: %s and %s",
			fromPEM.Fingerprint, fromDER.Fingerprint)
	}
	if !strings.HasPrefix(fromDER.PEM, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("a DER upload was not converted to PEM: %q", fromDER.PEM)
	}
	if !strings.Contains(fromPEM.Subject, "Work CA") {
		t.Errorf("subject = %q, want it to name the common name", fromPEM.Subject)
	}
}

// Notepad writes a byte order mark in front of the header, which otherwise
// makes a perfectly good certificate unreadable.
func TestParse_ToleratesByteOrderMark(t *testing.T) {
	_, pemBytes := makeCert(t, certOptions{})
	if _, err := Parse(append([]byte("\ufeff"), pemBytes...)); err != nil {
		t.Fatalf("a certificate with a BOM in front of it was refused: %v", err)
	}
}

// Each refusal has a different thing for an operator to do next, which is the
// reason they are separate values rather than one "invalid certificate".
func TestParse_Refusals(t *testing.T) {
	_, one := makeCert(t, certOptions{commonName: "One"})
	_, two := makeCert(t, certOptions{commonName: "Two"})

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	for _, tc := range []struct {
		name string
		in   []byte
		want error
	}{
		{"nothing at all", []byte("   \n"), ErrNoCertificate},
		{"prose", []byte("here is the cert we talked about"), ErrNoCertificate},
		{"a chain", append(append([]byte{}, one...), two...), ErrMultiple},
		{"a key beside the certificate", append(append([]byte{}, one...), keyPEM...), ErrPrivateKey},
		{"a key on its own", keyPEM, ErrPrivateKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// A chain refusal names what it found, because "add them one at a time" is
// only actionable if the operator can see which ones it means.
func TestParse_ChainNamesWhatItFound(t *testing.T) {
	_, root := makeCert(t, certOptions{commonName: "Company Root"})
	_, issuing := makeCert(t, certOptions{commonName: "Company Issuing"})

	_, err := Parse(append(append([]byte{}, root...), issuing...))
	if err == nil {
		t.Fatal("a chain was accepted as one certificate")
	}
	for _, want := range []string{"Company Root", "Company Issuing"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message = %q, want it to name %q", err, want)
		}
	}
}

// A .p7b from a Windows CA is a DER SEQUENCE too, and in a text editor it
// looks exactly like a certificate that will not parse. Telling the two apart
// is worth it only because the message names the conversion.
func TestParse_PKCS7SaysHowToConvertIt(t *testing.T) {
	// The signedData OID inside a SEQUENCE, which is what the detector reads.
	bundle := append([]byte{0x30, 0x82, 0x01, 0x00},
		0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x07, 0x02)

	_, err := Parse(bundle)
	if !errors.Is(err, ErrPKCS7) {
		t.Fatalf("err = %v, want it recognised as PKCS#7", err)
	}
	if !strings.Contains(err.Error(), "openssl pkcs7") {
		t.Errorf("message = %q, want it to name the conversion", err)
	}
}

// CanAnchor is the question that decides whether storing a certificate can fix
// anything at all. The first case is the one this feature exists for: an
// appliance certificate with no extensions, which Go accepts as a root because
// nothing on it says otherwise.
func TestCanAnchor(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts certOptions
		want bool
	}{
		{"an appliance certificate with no extensions", certOptions{}, true},
		{"a real CA", certOptions{
			basicConstraintsValid: true, isCA: true,
			keyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		}, true},
		{"a leaf that says CA:FALSE", certOptions{basicConstraintsValid: true}, false},
		{"a leaf that names a usage without certificate signing", certOptions{
			keyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, pemBytes := makeCert(t, tc.opts)
			c, err := Parse(pemBytes)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := c.CanAnchor(); got != tc.want {
				t.Errorf("CanAnchor() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The pool is the system roots plus the extras. A pool holding only the extras
// would break every public endpoint the same plugin talks to, at the
// handshake, which reads as the network being wrong rather than the trust
// store.
func TestPool_AddsToTheSystemRoots(t *testing.T) {
	_, pemBytes := makeCert(t, certOptions{commonName: "Work CA"})
	c, err := Parse(pemBytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	system, err := x509.SystemCertPool()
	if err != nil {
		t.Skipf("no system pool on this machine: %v", err)
	}
	pool, err := Pool([]*Certificate{c})
	if err != nil {
		t.Fatalf("Pool: %v", err)
	}
	if got, want := len(pool.Subjects()), len(system.Subjects())+1; got != want { //nolint:staticcheck // comparing pool contents is the point
		t.Errorf("pool holds %d subjects, want the system %d plus the one added",
			got, want)
	}
}

// Nothing stored means nothing built: nil is Go's own "use the system roots",
// and a deployment that never needed a certificate should carry no pool.
func TestPool_NothingStoredBuildsNothing(t *testing.T) {
	pool, err := Pool(nil)
	if err != nil {
		t.Fatalf("Pool: %v", err)
	}
	if pool != nil {
		t.Error("a pool was built for no certificates")
	}
}

// A name is typed by an operator and then used inside a comma-separated
// setting elsewhere, so a comma in one would split into names that match
// nothing.
func TestValidateName(t *testing.T) {
	for _, tc := range []struct {
		name, in, wants string
	}{
		{"ordinary", "Work CA", ""},
		{"trimmed", "  Work CA  ", ""},
		{"empty", "   ", "needs a name"},
		{"a comma", "Work CA, old", "comma"},
		{"a control character", "Work\x00CA", "printable"},
		{"too long", strings.Repeat("a", 65), "64 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateName(tc.in)
			switch {
			case tc.wants == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.wants == "":
				if got != strings.TrimSpace(tc.in) {
					t.Errorf("name = %q, want it trimmed", got)
				}
			case err == nil:
				t.Fatalf("accepted; it should have mentioned %q", tc.wants)
			case !strings.Contains(err.Error(), tc.wants):
				t.Errorf("message = %v, want it to mention %q", err, tc.wants)
			}
		})
	}
}
