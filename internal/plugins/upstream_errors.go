package plugins

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Explain turns a failure to reach an upstream into a sentence an operator can
// act on.
//
// What it replaces is a chain: a plugin says it could not reach an address, Go
// says `Get "<the same address>"`, crypto/tls says the certificate could not be
// verified and crypto/x509 says why -- four clauses, the address twice, and the
// part somebody can do something about last. On a dashboard that reads as
// noise, and the operator's next move is a search engine rather than the page
// they are already looking at.
//
// So a recognised cause is rewritten rather than appended to: the sentence is
// built from the innermost error that means something, and the wrappers around
// it are dropped. Anything unrecognised is returned exactly as it arrived,
// because inventing a friendlier version of an error nobody has read yet is
// how a message stops matching what happened.
//
// The original is always wrapped, so errors.Is and errors.As still find what
// they are looking for. This is presentation.
func Explain(err error) error {
	if err == nil {
		return nil
	}
	if msg := explanation(err); msg != "" {
		return explained{err: err, msg: msg}
	}
	return err
}

func explanation(err error) string {
	host := upstreamHost(err)

	// Trust first: it is the one an operator can fix from the dashboard, and
	// it is the one whose stock wording -- "signed by unknown authority" --
	// sounds like an accusation rather than a missing setting.
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return fmt.Sprintf("%s presented a certificate this host does not trust. "+
			"If it is your own -- a company authority, or the appliance's own "+
			"certificate -- add it under Settings, Certificates, and every "+
			"integration here will trust it.", host)
	}

	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return fmt.Sprintf("%s presented a certificate that is not issued for "+
			"that address. It is valid for %s, so the address here has to be "+
			"one of those -- trusting the certificate cannot cover a name it "+
			"does not carry.", host, strings.Join(namesOn(hostname.Certificate), ", "))
	}

	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) && invalid.Reason == x509.Expired {
		return fmt.Sprintf("%s presented a certificate that is outside its "+
			"validity dates. Either it needs reissuing, or this host's clock "+
			"is wrong.", host)
	}

	// A plaintext server on an https:// address. The stock message names a
	// record header, which describes the disappointment rather than the cause.
	if strings.Contains(err.Error(), "server gave HTTP response to HTTPS client") {
		return fmt.Sprintf("%s answered without TLS, so the address should "+
			"begin with http:// rather than https://.", host)
	}

	var dns *net.DNSError
	if errors.As(err, &dns) {
		return fmt.Sprintf("%s could not be resolved. The name is the thing to "+
			"check, along with what this host uses for DNS.", host)
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Sprintf("%s did not answer in time. It may be busy, or "+
			"something between here and there may be dropping the connection "+
			"rather than refusing it.", host)
	}

	// Refused is worth its own sentence because it is the one that means
	// something is listening nowhere rather than something is wrong with TLS,
	// credentials or the request.
	// Matched on the text rather than on syscall.ECONNREFUSED: the constant is
	// spelled differently per platform, and this is presentation -- anything
	// deciding behaviour on the difference would use errors.Is on the original,
	// which Explain keeps wrapped.
	if strings.Contains(err.Error(), "connection refused") {
		return fmt.Sprintf("nothing accepted a connection at %s. The address "+
			"and the port are the thing to check.", host)
	}
	return ""
}

// upstreamHost digs out the address the failure was about, so the sentence can
// name it once instead of the chain naming it twice.
func upstreamHost(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if u, parseErr := url.Parse(urlErr.URL); parseErr == nil && u.Host != "" {
			return u.Host
		}
		return urlErr.URL
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Addr != nil {
		return opErr.Addr.String()
	}
	return "the upstream"
}

// namesOn lists what a certificate is actually issued for, which is the half
// of a name mismatch the stock message leaves the operator to go and look up.
func namesOn(cert *x509.Certificate) []string {
	if cert == nil {
		return []string{"nothing this host could read"}
	}
	out := make([]string, 0, len(cert.DNSNames)+len(cert.IPAddresses))
	out = append(out, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		out = append(out, ip.String())
	}
	if len(out) == 0 && cert.Subject.CommonName != "" {
		out = append(out, cert.Subject.CommonName)
	}
	if len(out) == 0 {
		return []string{"no address at all"}
	}
	return out
}

// explained reads as the rewritten sentence and unwraps to what happened.
type explained struct {
	err error
	msg string
}

func (e explained) Error() string { return e.msg }
func (e explained) Unwrap() error { return e.err }
