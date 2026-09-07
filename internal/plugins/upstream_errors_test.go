package plugins

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
)

// The message an operator actually met: four clauses, the address twice, and
// the part they can do something about last.
//
//	graylog did not start with the new settings: could not reach
//	https://10.10.12.53/api/system: Get "https://10.10.12.53/api/system":
//	tls: failed to verify certificate: x509: certificate signed by unknown
//	authority
func TestExplain_UnknownAuthorityPointsAtTheCertificatesPage(t *testing.T) {
	wrapped := fmt.Errorf("could not reach https://10.10.12.53/api/system: %w",
		&url.Error{
			Op:  "Get",
			URL: "https://10.10.12.53/api/system",
			Err: fmt.Errorf("tls: failed to verify certificate: %w",
				x509.UnknownAuthorityError{}),
		})

	got := Explain(wrapped).Error()

	if strings.Count(got, "10.10.12.53") != 1 {
		t.Errorf("the address appears %d times in %q, want once",
			strings.Count(got, "10.10.12.53"), got)
	}
	for _, leak := range []string{"x509:", "tls:", "Get \""} {
		if strings.Contains(got, leak) {
			t.Errorf("message = %q, want it free of %q", got, leak)
		}
	}
	if !strings.Contains(got, "Certificates") {
		t.Errorf("message = %q, want it to name where to fix this", got)
	}
	// Presentation only. Anything deciding on the cause still has to find it.
	var unknown x509.UnknownAuthorityError
	if !errors.As(Explain(wrapped), &unknown) {
		t.Error("the original error must still be reachable")
	}
}

// A name mismatch is the failure trusting a certificate cannot fix, so the
// message says what the certificate is for rather than inviting another
// attempt at the trust store.
func TestExplain_HostnameMismatchNamesWhatTheCertificateCovers(t *testing.T) {
	cert := &x509.Certificate{DNSNames: []string{"logs.internal.example"}}
	cert.IPAddresses = append(cert.IPAddresses, net.ParseIP("10.10.12.53"))

	err := &url.Error{
		Op: "Get", URL: "https://10.10.12.99/api/system",
		Err: x509.HostnameError{Certificate: cert, Host: "10.10.12.99"},
	}

	got := Explain(err).Error()
	for _, want := range []string{"10.10.12.99", "logs.internal.example", "10.10.12.53"} {
		if !strings.Contains(got, want) {
			t.Errorf("message = %q, want it to mention %q", got, want)
		}
	}
	if strings.Contains(got, "Certificates") {
		t.Errorf("message = %q, want it not to send somebody to the trust store "+
			"for a failure that page cannot fix", got)
	}
}

// Every other cause worth its own sentence, and the one rule that matters for
// all of them: an unrecognised error is passed through untouched.
func TestExplain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		wants string
	}{
		{
			"an expired certificate",
			&url.Error{Op: "Get", URL: "https://logs.example/api", Err: x509.CertificateInvalidError{
				Reason: x509.Expired,
			}},
			"outside its validity dates",
		},
		{
			"plaintext behind an https address",
			&url.Error{Op: "Get", URL: "https://logs.example/api",
				Err: errors.New("server gave HTTP response to HTTPS client")},
			"should begin with http://",
		},
		{
			"a name that does not resolve",
			&url.Error{Op: "Get", URL: "https://logs.example/api",
				Err: &net.DNSError{Err: "no such host", Name: "logs.example"}},
			"could not be resolved",
		},
		{
			"nothing listening",
			&url.Error{Op: "Get", URL: "https://logs.example:9000/api",
				Err: errors.New("dial tcp 10.0.0.1:9000: connect: connection refused")},
			"nothing accepted a connection",
		},
		{
			"something nobody has taught it about",
			errors.New("the upstream said no"),
			"the upstream said no",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Explain(tc.err).Error()
			if !strings.Contains(got, tc.wants) {
				t.Errorf("message = %q, want it to contain %q", got, tc.wants)
			}
		})
	}
}

// An unrecognised error is returned as it arrived, not wrapped in a friendlier
// sentence that no longer matches what happened.
func TestExplain_LeavesTheUnrecognisedAlone(t *testing.T) {
	inner := errors.New("those credentials were rejected")
	if got := Explain(inner); got != inner {
		t.Fatalf("Explain returned %v, want the error untouched", got)
	}
	if Explain(nil) != nil {
		t.Error("Explain(nil) must be nil")
	}
}

// RedactURL is the one function standing between a configured address and a log
// line, and six integrations depend on it. Nothing covered it.
func TestRedactURL(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			// The reason this function exists. An operator may put credentials
			// in a configured address, and nothing rejects one that does.
			name: "strips userinfo",
			in:   "https://someone:hunter2@api.example.com/v1/accounts",
			want: "https://api.example.com/v1/accounts",
		},
		{
			name: "strips a username with no password",
			in:   "https://someone@api.example.com/v1",
			want: "https://api.example.com/v1",
		},
		{
			// A query string is where a token ends up when somebody follows an
			// older integration guide.
			name: "strips the query",
			in:   "https://api.example.com/v1?access_token=abc123",
			want: "https://api.example.com/v1",
		},
		{
			name: "strips both at once",
			in:   "https://someone:hunter2@api.example.com/v1?token=abc123",
			want: "https://api.example.com/v1",
		},
		{
			name: "keeps an ordinary address whole",
			in:   "https://api.example.com",
			want: "https://api.example.com",
		},
		{
			// The path is kept: it says which endpoint was being reached, and
			// it is the part an operator matches against their own config.
			name: "keeps the path and the port",
			in:   "https://api.example.com:8443/v1/accounts/9000001",
			want: "https://api.example.com:8443/v1/accounts/9000001",
		},
		{
			// An address that will not parse is the one most likely to be
			// malformed *because* it carries something unexpected, so it is
			// described rather than echoed.
			name: "describes an address it cannot parse",
			in:   "https://api.example.com/\x7f\x00",
			want: "the configured address",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactURL(tc.in); got != tc.want {
				t.Errorf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
