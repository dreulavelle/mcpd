package servertls

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// providedFile is a certificate somebody else issued, with its private key,
// for the dashboard to present.
//
// One file rather than a certificate with a key beside it, so that replacing
// it is one rename. Two files written one after the other can be read between
// the writes as a new key with the old certificate -- a pair that cannot be
// served -- and the reload that reads it runs while connections are arriving.
const providedFile = "dashboard.pem"

// ProvidedPath is where the provided certificate lives in dir.
//
// Something outside mcpd that renews a certificate -- acme.sh, certbot, a
// script against a company authority -- can write a new bundle here, the key
// and the chain in one PEM file, and mcpd picks it up without a restart.
func ProvidedPath(dir string) string { return filepath.Join(dir, providedFile) }

// ErrNoProvided reports that no certificate has been provided yet.
var ErrNoProvided = errors.New("servertls: no certificate has been provided")

// Refusal is a certificate that cannot be served, said for the person who
// provided it: what is wrong with it, and what to send instead.
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return r.Reason }

func refuse(format string, args ...any) error {
	return &Refusal{Reason: fmt.Sprintf(format, args...)}
}

// ParseProvided reads a certificate, the chain above it and its private key,
// and says whether they can be served. It returns the materials and the one
// PEM bundle to store them as.
//
// They may arrive as one bundle or as two pieces: parts is every piece of
// text that was sent, and each is searched for certificates and for the key.
// The order inside is not trusted. The certificate the key belongs to is
// served first wherever it was pasted, because a chain pasted root-first is
// the ordinary mistake, and a server that sends its chain out of order is
// accepted by some clients and refused by others.
//
// current refuses a certificate that has already run out. An upload is
// refused for it; a file already in place is not, because an expired
// certificate is a browser warning somebody can click past, while plain http
// behind an https address is a sign-in that cannot work at all.
func ParseProvided(now time.Time, current bool, parts ...[]byte) (*Materials, []byte, error) {
	var (
		certs  []*x509.Certificate
		key    crypto.Signer
		sawPEM bool
	)
	for _, part := range parts {
		rest := bytes.TrimPrefix(part, []byte("\xef\xbb\xbf"))
		for {
			var block *pem.Block
			block, rest = pem.Decode(rest)
			if block == nil {
				break
			}
			sawPEM = true
			switch block.Type {
			case "CERTIFICATE":
				c, err := x509.ParseCertificate(block.Bytes)
				if err != nil {
					return nil, nil, refuse("One of the certificates here could not be read. " +
						"Send it again as it came from the authority.")
				}
				certs = append(certs, c)
			case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
				if strings.Contains(block.Headers["Proc-Type"], "ENCRYPTED") {
					return nil, nil, errPassphrase
				}
				if key != nil {
					return nil, nil, refuse("There are two private keys here. Send only the one " +
						"that belongs to this certificate.")
				}
				k, err := parseKey(block)
				if err != nil {
					return nil, nil, refuse("The private key could not be read. Send it in PEM, " +
						"without a passphrase.")
				}
				key = k
			case "ENCRYPTED PRIVATE KEY":
				return nil, nil, errPassphrase
			case "CERTIFICATE REQUEST", "NEW CERTIFICATE REQUEST":
				return nil, nil, refuse("That is a certificate request, not a certificate. Send " +
					"the request to your certificate authority, and upload the certificate it gives back.")
			default:
				return nil, nil, refuse("There is a %q section here, which is neither a "+
					"certificate nor a private key.", block.Type)
			}
		}
	}

	switch {
	case !sawPEM:
		return nil, nil, refuse("That isn't PEM text. A .pfx or .p12 file has to be " +
			"converted to PEM first, with its private key unencrypted.")
	case len(certs) == 0:
		return nil, nil, refuse("There is no certificate here. Send the certificate as " +
			"well as its private key.")
	case key == nil:
		return nil, nil, refuse("There is no private key here. Send the key the certificate " +
			"was issued for, as well as the certificate.")
	}

	leafAt := -1
	for i, c := range certs {
		if publicMatches(key, c.PublicKey) {
			leafAt = i
			break
		}
	}
	if leafAt < 0 {
		return nil, nil, refuse("The private key doesn't belong to any of these certificates. " +
			"Send the key that was made together with the certificate request.")
	}
	leaf := certs[leafAt]
	if leaf.IsCA {
		return nil, nil, refuse("That is a certificate authority's own certificate, not one " +
			"issued to a server. Upload the certificate issued for this host.")
	}
	if !serverUsage(leaf) {
		return nil, nil, refuse("That certificate isn't issued for use by a web server, so " +
			"browsers would refuse it. Ask the authority for one with server authentication.")
	}
	if current && now.After(leaf.NotAfter) {
		return nil, nil, refuse("That certificate ran out on %s. Upload its replacement.",
			leaf.NotAfter.UTC().Format("2 January 2006"))
	}

	// The leaf first, then everything else in the order it came, without the
	// leaf a second time if it was pasted twice.
	chain := [][]byte{leaf.Raw}
	var bundle bytes.Buffer
	_ = pem.Encode(&bundle, &pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw})
	for i, c := range certs {
		if i == leafAt || bytes.Equal(c.Raw, leaf.Raw) {
			continue
		}
		chain = append(chain, c.Raw)
		_ = pem.Encode(&bundle, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("servertls: encode key: %w", err)
	}
	_ = pem.Encode(&bundle, &pem.Block{Type: "PRIVATE KEY", Bytes: der})

	m := &Materials{
		Certificate: tls.Certificate{Certificate: chain, PrivateKey: key, Leaf: leaf},
		Provided:    true,
	}
	describe(m, leaf)
	return m, bundle.Bytes(), nil
}

var errPassphrase = refuse("The private key is protected by a passphrase. Export it without " +
	"one: mcpd has to read it on every start, with nobody there to type it.")

// StoreProvided checks a certificate and its key and installs them in dir as
// the dashboard's certificate. A certificate that has run out is refused.
func StoreProvided(dir string, now time.Time, parts ...[]byte) (*Materials, error) {
	m, bundle, err := ParseProvided(now, true, parts...)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("servertls: create %s: %w", dir, err)
	}
	// 0600: the file holds a private key.
	if err := writeFile(ProvidedPath(dir), 0o600, bundle); err != nil {
		return nil, err
	}
	return m, nil
}

// LoadProvided reads the certificate installed in dir. One that has run out
// is still returned; see ParseProvided for why.
func LoadProvided(dir string, now time.Time) (*Materials, error) {
	data, err := os.ReadFile(ProvidedPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNoProvided
	}
	if err != nil {
		return nil, fmt.Errorf("servertls: read %s: %w", ProvidedPath(dir), err)
	}
	m, _, err := ParseProvided(now, false, data)
	return m, err
}

// RemoveProvided deletes the certificate installed in dir, if there is one.
func RemoveProvided(dir string) error {
	err := os.Remove(ProvidedPath(dir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("servertls: remove %s: %w", ProvidedPath(dir), err)
	}
	return nil
}

func parseKey(block *pem.Block) (crypto.Signer, error) {
	var (
		k   any
		err error
	)
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		k, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		k, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	}
	if err != nil {
		return nil, err
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("servertls: the key cannot sign")
	}
	return signer, nil
}

// publicMatches reports whether a certificate was issued for key. RSA, ECDSA
// and Ed25519 public keys all compare themselves.
func publicMatches(key crypto.Signer, pub any) bool {
	k, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool })
	return ok && k.Equal(pub)
}

// serverUsage reports whether a certificate may authenticate a TLS server:
// either it says nothing about extended usage, or it names server auth.
func serverUsage(c *x509.Certificate) bool {
	if len(c.ExtKeyUsage) == 0 && len(c.UnknownExtKeyUsage) == 0 {
		return true
	}
	for _, u := range c.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth || u == x509.ExtKeyUsageAny {
			return true
		}
	}
	return false
}

// describe fills in what a page shows about a certificate.
func describe(m *Materials, leaf *x509.Certificate) {
	hosts := append([]string{}, leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		hosts = append(hosts, ip.String())
	}
	m.Hosts = normaliseHosts(hosts)
	m.Subject = nameOf(leaf.Subject)
	m.Issuer = nameOf(leaf.Issuer)
	m.NotBefore, m.NotAfter = leaf.NotBefore, leaf.NotAfter
	sum := sha256.Sum256(leaf.Raw)
	m.Fingerprint = strings.ToUpper(hex.EncodeToString(sum[:]))
	// Cloudflare's Origin CA signs certificates that only Cloudflare's own
	// proxy trusts. They are an easy thing to download for a domain and a
	// natural thing to try here, and a browser reaching the dashboard directly
	// refuses every one of them -- so it is recognised and said.
	m.CloudflareOrigin = strings.Contains(strings.ToLower(leaf.Issuer.String()), "cloudflare origin")
}

// nameOf is the part of a distinguished name a person recognises.
func nameOf(n pkix.Name) string {
	switch {
	case n.CommonName != "":
		return n.CommonName
	case len(n.Organization) > 0:
		return n.Organization[0]
	default:
		return n.String()
	}
}
