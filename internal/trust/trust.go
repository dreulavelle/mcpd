// Package trust holds the certificates this host trusts in addition to the
// system roots.
//
// It exists for an ordinary situation that has no good answer without it: an
// integration inside a company reaches an HTTPS address whose certificate was
// issued by that company's own authority, or by the appliance itself. No
// public trust store has heard of either, so the handshake fails.
//
// The two ways out without this package are both worse. Disabling verification
// sends a credential to whatever answers on that address, which is the thing
// TLS is for. Mounting a bundle into the container's system trust store
// widens trust for every outbound connection the process makes and lives in a
// compose file, where nobody reading the dashboard would find it.
//
// # Extras add to the system roots, they do not replace them
//
// An instance pointed at a company CA still reaches public endpoints -- a
// cloud API on a public certificate is the common case, not the exception --
// so Pool starts from the system roots and appends. A pool that held only the
// named certificates would silently break every other address the same plugin
// talks to, and it would break it at the handshake, which reads as the network
// being wrong rather than the trust store.
//
// # Named entries, not one bundle
//
// A bundle fits in a string. What it cannot do is say which of the three
// certificates inside it expires in six weeks, or which one an instance is
// actually relying on. Each certificate is a row, parsed once when it is
// added, so a listing can name a subject and an expiry without re-parsing and
// an unreadable paste is refused where somebody pasted it.
//
// # None of this is secret
//
// A certificate is the public half by construction: the server presents it to
// everyone who connects. So it is stored in the clear and shown in full,
// which is what makes a wrong one debuggable. The security-relevant fact is
// the decision to trust it, and that is what the audit trail records.
package trust

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Errors returned by this package.
//
// Parsing failures are separate values because each one has a different thing
// for an operator to do next, and "invalid certificate" tells them none of it.
var (
	// ErrNotFound reports an unknown certificate.
	ErrNotFound = errors.New("trust: no such certificate")
	// ErrDuplicateName reports a name another certificate already uses.
	ErrDuplicateName = errors.New("trust: a certificate with that name already exists")
	// ErrDuplicateCertificate reports a certificate already stored under
	// another name. Reported with that name, so the answer is "it is already
	// there, called X" rather than a refusal with no way forward.
	ErrDuplicateCertificate = errors.New("trust: that certificate is already stored")
	// ErrNoCertificate reports input with nothing certificate-shaped in it.
	ErrNoCertificate = errors.New("trust: that is not a certificate")
	// ErrPKCS7 reports a .p7b or .p7c bundle, which is a container this host
	// does not open. Separate because the fix is one command rather than a
	// different file.
	ErrPKCS7 = errors.New("trust: that is a PKCS#7 bundle")
	// ErrPrivateKey reports a paste that includes a private key. Refused
	// outright rather than trimmed: a key that reached this table would be
	// stored in the clear and shown in a dashboard, so the only safe response
	// is to refuse the whole thing and say so.
	ErrPrivateKey = errors.New("trust: that includes a private key")
	// ErrMultiple reports more than one certificate in one paste.
	ErrMultiple = errors.New("trust: that is more than one certificate")
)

// Certificate is one stored trust anchor, with the facts parsed from it.
type Certificate struct {
	ID   string
	Name string
	// PEM is the certificate as stored, always PEM whatever was uploaded.
	PEM string

	Subject string
	Issuer  string
	// Fingerprint is the SHA-256 of the DER, lowercase hex. It is what
	// identifies the certificate itself, as opposed to the name somebody gave
	// it, and it is what an operator compares against the appliance to check
	// they are trusting the thing they think they are.
	Fingerprint string
	NotBefore   time.Time
	NotAfter    time.Time
	// IsCA is what the certificate says about itself, not a judgement. See
	// CanAnchor for the question that actually matters.
	IsCA bool
	// KeyUsage is retained because it, too, decides whether this certificate
	// can anchor a chain.
	KeyUsage x509.KeyUsage
	// BasicConstraintsValid records whether the certificate carried the
	// basicConstraints extension at all. Kept beside IsCA because the pair is
	// what distinguishes "says it is not an authority" from "does not say",
	// and only the first stops a chain.
	BasicConstraintsValid bool

	AddedBy string
	AddedAt time.Time
}

// Expired reports whether the certificate is past its validity.
func (c *Certificate) Expired(now time.Time) bool { return now.After(c.NotAfter) }

// NotYetValid reports a certificate whose validity has not started, which is
// usually a clock that disagrees rather than a certificate that is wrong.
func (c *Certificate) NotYetValid(now time.Time) bool { return now.Before(c.NotBefore) }

// CanAnchor reports whether Go will accept this certificate as the root of a
// chain, which is the only thing storing it here can achieve.
//
// A self-signed certificate straight off an appliance usually carries no
// extensions at all, and that is the case this answers yes for: with no
// basicConstraints and no keyUsage, nothing constrains it out of the role, so
// putting it in a root pool works and the connection to that one host starts
// verifying. A certificate that explicitly says `CA:FALSE`, or that names a
// key usage without certificate signing, is a leaf that means it -- trusting
// it changes nothing, and the operator needs to be told that rather than left
// to wonder why the handshake still fails.
//
// This is a warning rather than a refusal. It is a fact about the certificate
// which the page reports; deciding it is useless is a judgement, and the case
// where somebody knows better than this function is exactly the case where
// refusing would be infuriating.
func (c *Certificate) CanAnchor() bool {
	// Mirrors what x509.CheckSignatureFrom enforces on a parent: the
	// basicConstraints check only applies when the extension is present, and
	// the key usage check only when some usage is named.
	if c.IsCAExplicitlyFalse() {
		return false
	}
	if c.KeyUsage != 0 && c.KeyUsage&x509.KeyUsageCertSign == 0 {
		return false
	}
	return true
}

// IsCAExplicitlyFalse distinguishes "says it is not an authority" from "does
// not say". The first stops a chain; the second does not, and collapsing them
// would warn about every appliance certificate this feature exists for.
func (c *Certificate) IsCAExplicitlyFalse() bool { return c.BasicConstraintsValid && !c.IsCA }

// Parse reads one certificate from what an operator pasted or uploaded.
//
// PEM and DER are both accepted, because a certificate arrives as a file about
// as often as it arrives on a clipboard and a Windows CA exports DER under a
// .crt extension without mentioning it. Whatever arrives, PEM is what is
// stored, so everything downstream reads one shape.
func Parse(raw []byte) (*Certificate, error) {
	trimmed := trimBOM(raw)
	if len(strings.TrimSpace(string(trimmed))) == 0 {
		return nil, ErrNoCertificate
	}

	if looksLikePEM(trimmed) {
		return parsePEM(trimmed)
	}
	return parseDER(trimmed)
}

// parsePEM walks every block, because what an operator pastes is frequently
// not what they meant to paste: a chain, a certificate with a key underneath
// it, or a file with explanatory text around the block.
func parsePEM(raw []byte) (*Certificate, error) {
	var found []*x509.Certificate
	rest := raw
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch {
		case strings.Contains(block.Type, "PRIVATE KEY"):
			return nil, ErrPrivateKey
		case block.Type == "PKCS7":
			return nil, ErrPKCS7
		case block.Type == "CERTIFICATE":
			c, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("%w: the PEM block is not a certificate: %w",
					ErrNoCertificate, err)
			}
			found = append(found, c)
		}
		// Anything else -- a CSR, a DH parameter block, a trusted-certificate
		// block from an OpenSSL trust store -- is skipped rather than refused.
		// It is not a certificate and not a key, so it is neither useful nor
		// dangerous, and refusing would fail a paste that also contains the
		// certificate somebody wanted.
	}

	switch len(found) {
	case 0:
		return nil, ErrNoCertificate
	case 1:
		return fromX509(found[0]), nil
	default:
		return nil, fmt.Errorf("%w: it holds %d, for %s. Add them one at a "+
			"time, so each has its own name and its own expiry",
			ErrMultiple, len(found), strings.Join(subjectsOf(found), ", "))
	}
}

// parseDER reads a single binary certificate, and recognises the container it
// is most often confused with.
func parseDER(raw []byte) (*Certificate, error) {
	if c, err := x509.ParseCertificate(raw); err == nil {
		return fromX509(c), nil
	}
	// A .p7b exported by a Windows CA is a DER SEQUENCE too, and to anybody
	// looking at it in a text editor it is the same wall of binary. Naming the
	// conversion is the whole value of telling the two apart.
	if isPKCS7(raw) {
		return nil, fmt.Errorf("%w: convert it first with `openssl pkcs7 "+
			"-print_certs -in bundle.p7b -out certificates.pem`, then add the "+
			"certificates from that file one at a time", ErrPKCS7)
	}
	return nil, ErrNoCertificate
}

func fromX509(c *x509.Certificate) *Certificate {
	sum := sha256.Sum256(c.Raw)
	out := &Certificate{
		Subject:     c.Subject.String(),
		Issuer:      c.Issuer.String(),
		Fingerprint: hex.EncodeToString(sum[:]),
		NotBefore:   c.NotBefore.UTC(),
		NotAfter:    c.NotAfter.UTC(),
		IsCA:        c.IsCA,
		KeyUsage:    c.KeyUsage,

		BasicConstraintsValid: c.BasicConstraintsValid,
		PEM: string(pem.EncodeToMemory(&pem.Block{
			Type: "CERTIFICATE", Bytes: c.Raw,
		})),
	}
	return out
}

// looksLikePEM decides which parser to try first. PEM is text and begins with
// a header; DER begins with an ASN.1 SEQUENCE tag, which is not printable.
func looksLikePEM(raw []byte) bool {
	return strings.Contains(string(raw), "-----BEGIN")
}

// isPKCS7 recognises the signedData content type by its OID, which appears
// near the start of every PKCS#7 structure. A heuristic rather than a parser:
// the point is to produce the right sentence, not to open the container.
func isPKCS7(raw []byte) bool {
	// 1.2.840.113549.1.7.2 -- signedData.
	oid := []byte{0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x07, 0x02}
	limit := min(len(raw), 64)
	return strings.Contains(string(raw[:limit]), string(oid))
}

func subjectsOf(certs []*x509.Certificate) []string {
	out := make([]string, 0, len(certs))
	for _, c := range certs {
		out = append(out, c.Subject.CommonName)
	}
	return out
}

// trimBOM removes the byte order mark Notepad writes, which otherwise sits in
// front of "-----BEGIN" and makes a perfectly good certificate unreadable.
func trimBOM(raw []byte) []byte {
	return []byte(strings.TrimPrefix(string(raw), "\ufeff"))
}

// ValidateName checks the name an operator gives a certificate.
//
// The comma is the interesting refusal. An instance names the certificates it
// trusts in a comma-separated list, so a name with a comma in it would split
// into two names that match nothing, and the instance would come up trusting
// neither -- with a message about a certificate that does not exist and a name
// on the page that plainly does.
func ValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errors.New("trust: a certificate needs a name")
	case utf8.RuneCountInString(name) > 64:
		return "", errors.New("trust: a certificate name is at most 64 characters")
	case strings.Contains(name, ","):
		return "", errors.New("trust: a certificate name cannot contain a comma; " +
			"an instance names the certificates it trusts as a comma-separated list")
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return "", errors.New("trust: a certificate name must be printable")
		}
	}
	return name, nil
}
