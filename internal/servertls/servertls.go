// Package servertls gives mcpd a certificate of its own.
//
// It exists because OAuth is not optional for a ChatGPT connector, and OAuth
// is not optional about HTTPS: RFC 8414 requires an authorization server's
// issuer identifier to use the https scheme, and the MCP specification
// requires authorization servers to implement OAuth 2.1. A connector pointed
// at an http:// issuer is refused with "does not implement OAuth" -- after the
// metadata has been fetched successfully, which makes it a confusing failure
// to diagnose.
//
// A private deployment cannot get a publicly-trusted certificate for an
// address like 192.168.1.10, so mcpd issues its own from a CA it keeps. The
// certificate is not a formality: the tunnel carries a bearer token on every
// request, and that token has no business crossing even a private network in
// the clear.
//
// The CA is generated once and kept. Reissuing it on every start would mean
// anyone who trusted it has to trust it again, so only the leaf is reissued.
package servertls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// File names inside the certificate directory.
const (
	caCertFile     = "ca.pem"
	caKeyFile      = "ca.key"
	serverCertFile = "server.pem"
	serverKeyFile  = "server.key"
)

const (
	// caLifetime is long because replacing the CA is the expensive operation:
	// every browser that trusted it has to be told again.
	caLifetime = 10 * 365 * 24 * time.Hour

	// leafLifetime stays under the 398 days browsers and Apple platforms will
	// accept, so a certificate mcpd issues is not rejected for its duration
	// alone.
	leafLifetime = 397 * 24 * time.Hour

	// renewBefore reissues the leaf while it still has time to spare, so a
	// deployment that restarts occasionally never serves an expired one.
	renewBefore = 30 * 24 * time.Hour
)

// Materials is a server certificate together with the CA that signed it.
type Materials struct {
	// Certificate is what the HTTPS listener serves.
	Certificate tls.Certificate
	// CAPath is the CA certificate on disk. The embedded tunnel client is
	// pointed at it so requests it makes back to mcpd -- the OAuth token
	// endpoint among them -- are trusted rather than refused.
	CAPath string
	// CAPEM is the same certificate, for handing to an operator to install.
	CAPEM []byte
	// Hosts are the names and addresses the certificate is valid for.
	Hosts []string
	// NotAfter is when the server certificate expires.
	NotAfter time.Time
	// Issued reports whether anything was written this time, so startup can
	// say so once rather than on every boot.
	Issued bool
}

// EnsureSelfSigned returns usable certificate material for hosts, generating
// whatever is missing.
//
// It is safe to call on every start. Existing material is reused unless it is
// unreadable, close to expiry, or no longer covers the addresses mcpd is
// reachable at -- the last of which is what happens when public_url changes,
// and is exactly when silently serving the old certificate would be wrong.
func EnsureSelfSigned(dir string, hosts []string, now time.Time) (*Materials, error) {
	hosts = normaliseHosts(hosts)
	if len(hosts) == 0 {
		return nil, fmt.Errorf("servertls: no addresses to certify")
	}
	// 0700: the directory holds private keys.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("servertls: create %s: %w", dir, err)
	}

	ca, caCert, caPEM, err := ensureCA(dir, now)
	if err != nil {
		return nil, err
	}

	leaf, ok, err := loadLeaf(dir, hosts, now)
	if err != nil {
		return nil, err
	}
	issued := false
	if !ok {
		if leaf, err = issueLeaf(dir, ca, caCert, hosts, now); err != nil {
			return nil, err
		}
		issued = true
	}

	parsed, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("servertls: read issued certificate: %w", err)
	}

	return &Materials{
		Certificate: leaf,
		CAPath:      filepath.Join(dir, caCertFile),
		CAPEM:       caPEM,
		Hosts:       hosts,
		NotAfter:    parsed.NotAfter,
		Issued:      issued,
	}, nil
}

// TLSConfig returns the listener configuration.
//
// TLS 1.2 is the floor rather than 1.3 because the certificate is also
// presented to whatever an operator points at the dashboard's connection
// address, and refusing 1.2 buys nothing here that the private CA has not
// already decided.
func (m *Materials) TLSConfig() *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{m.Certificate},
		MinVersion:   tls.VersionTLS12,
	}
}

// HostsFor derives the addresses a certificate must cover from the URL clients
// use and the address mcpd binds.
//
// Loopback is always included: health checks, a local curl, and the operator's
// own testing all arrive that way, and a certificate that fails for them turns
// every local check into a false alarm.
func HostsFor(publicURL, listenAddr string) []string {
	hosts := []string{"localhost", "127.0.0.1", "::1"}

	if u, err := url.Parse(publicURL); err == nil && u.Host != "" {
		if h := u.Hostname(); h != "" {
			hosts = append(hosts, h)
		}
	}
	// The bind address matters when it names a specific interface; a wildcard
	// bind says nothing about how anyone reaches it.
	if host, _, err := net.SplitHostPort(listenAddr); err == nil {
		switch host {
		case "", "0.0.0.0", "::", "[::]":
		default:
			hosts = append(hosts, host)
		}
	}
	return normaliseHosts(hosts)
}

// normaliseHosts lowercases, de-duplicates and sorts, so the same set of
// addresses in a different order does not look like a change worth reissuing
// for.
func normaliseHosts(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, h := range in {
		h = strings.ToLower(strings.TrimSpace(strings.Trim(h, "[]")))
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// ensureCA loads the certificate authority, creating it the first time.
func ensureCA(dir string, now time.Time) (*ecdsa.PrivateKey, *x509.Certificate, []byte, error) {
	certPath := filepath.Join(dir, caCertFile)
	keyPath := filepath.Join(dir, caKeyFile)

	certPEM, certErr := os.ReadFile(certPath)
	keyPEM, keyErr := os.ReadFile(keyPath)
	if certErr == nil && keyErr == nil {
		cert, key, err := parseCA(certPEM, keyPEM)
		// An unreadable CA is regenerated rather than treated as fatal: mcpd
		// refusing to start because a file it wrote itself went bad would be a
		// worse outcome than a certificate that has to be trusted again.
		if err == nil && cert.NotAfter.After(now.Add(renewBefore)) {
			return key, cert, certPEM, nil
		}
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("servertls: generate CA key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"mcpd"},
			CommonName:   "mcpd local certificate authority",
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("servertls: create CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("servertls: read created CA: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeFile(certPath, 0o644, certPEM); err != nil {
		return nil, nil, nil, err
	}
	encodedKey, err := encodeKey(key)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := writeFile(keyPath, 0o600, encodedKey); err != nil {
		return nil, nil, nil, err
	}
	return key, cert, certPEM, nil
}

func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, nil, fmt.Errorf("servertls: CA files are not PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// loadLeaf returns the stored server certificate when it is still usable for
// hosts, and reports false when a new one is needed.
func loadLeaf(dir string, hosts []string, now time.Time) (tls.Certificate, bool, error) {
	certPath := filepath.Join(dir, serverCertFile)
	keyPath := filepath.Join(dir, serverKeyFile)

	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, false, nil
	}
	parsed, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, false, nil
	}
	if parsed.NotAfter.Before(now.Add(renewBefore)) {
		return tls.Certificate{}, false, nil
	}
	if !covers(parsed, hosts) {
		return tls.Certificate{}, false, nil
	}
	return pair, true, nil
}

// covers reports whether a certificate is valid for every address given.
func covers(cert *x509.Certificate, hosts []string) bool {
	have := make(map[string]struct{}, len(cert.DNSNames)+len(cert.IPAddresses))
	for _, name := range cert.DNSNames {
		have[strings.ToLower(name)] = struct{}{}
	}
	for _, ip := range cert.IPAddresses {
		have[strings.ToLower(ip.String())] = struct{}{}
	}
	for _, h := range hosts {
		if _, ok := have[h]; ok {
			continue
		}
		// An IP can be written more than one way, so compare the parsed value
		// rather than the text.
		if ip := net.ParseIP(h); ip != nil {
			if slices.ContainsFunc(cert.IPAddresses, ip.Equal) {
				continue
			}
		}
		return false
	}
	return true
}

func issueLeaf(dir string, caKey *ecdsa.PrivateKey, ca *x509.Certificate, hosts []string, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("servertls: generate key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return tls.Certificate{}, err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			Organization: []string{"mcpd"},
			CommonName:   commonName(hosts),
		},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(leafLifetime),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
			continue
		}
		template.DNSNames = append(template.DNSNames, h)
	}

	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("servertls: create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeFile(filepath.Join(dir, serverCertFile), 0o644, certPEM); err != nil {
		return tls.Certificate{}, err
	}
	encodedKey, err := encodeKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writeFile(filepath.Join(dir, serverKeyFile), 0o600, encodedKey); err != nil {
		return tls.Certificate{}, err
	}

	pair, err := tls.X509KeyPair(certPEM, encodedKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("servertls: assemble certificate: %w", err)
	}
	return pair, nil
}

// commonName prefers a hostname over an address, because it is what an
// operator will recognise in a certificate viewer.
func commonName(hosts []string) string {
	for _, h := range hosts {
		if net.ParseIP(h) == nil && h != "localhost" {
			return h
		}
	}
	for _, h := range hosts {
		if h != "localhost" && h != "127.0.0.1" && h != "::1" {
			return h
		}
	}
	return hosts[0]
}

func newSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("servertls: system entropy unavailable: %w", err)
	}
	return serial, nil
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("servertls: encode key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// writeFile writes atomically, so a crash mid-write cannot leave a truncated
// certificate that fails to parse on the next start.
func writeFile(path string, mode os.FileMode, content []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, mode); err != nil {
		return fmt.Errorf("servertls: write %s: %w", path, err)
	}
	// WriteFile honours the umask, so the mode is set explicitly: a private
	// key must not be group- or world-readable.
	if err := os.Chmod(tmp, mode); err != nil {
		return fmt.Errorf("servertls: set permissions on %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("servertls: install %s: %w", path, err)
	}
	return nil
}
