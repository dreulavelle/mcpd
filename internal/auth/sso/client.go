package sso

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxBody bounds what a provider can make this process hold.
//
// A discovery document is a couple of kilobytes and a JWKS a few more. The
// bound is generous against both and is still a bound, because everything read
// here is a third party's text arriving in whatever quantity they choose to
// send -- the same rule the registry reader works under.
const maxBody = 1 << 20

// requestTimeout bounds one call to a provider. A browser is waiting on the
// far side of the callback, so a provider that has stopped answering should
// cost a refusal rather than a hung request.
const requestTimeout = 15 * time.Second

// newHTTPClient returns the client this package dials providers with.
//
// Redirects are refused rather than followed. Every address here is one this
// host chose -- a discovery document's endpoints, or a provider's documented
// API -- and a 302 from one of them is either a misconfiguration or somebody
// steering this process at an address it did not pick, which is exactly the
// case the remote-MCP client pins its origin against. A token request carries
// the client secret, so following one would be handing a credential to
// whoever answered.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// getJSON fetches a document and decodes it, bounded.
func getJSON(ctx context.Context, c *http.Client, url string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return do(c, req, out)
}

// postForm posts an application/x-www-form-urlencoded body and decodes the
// JSON answer.
func postForm(ctx context.Context, c *http.Client, url string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// GitHub's token endpoint answers with a form-encoded body unless asked
	// otherwise, and the OIDC providers ignore the header. Asking is what
	// makes one decoder cover all three.
	req.Header.Set("Accept", "application/json")
	return do(c, req, out)
}

func do(c *http.Client, req *http.Request, out any) error {
	res, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrProvider, req.URL.Host, err)
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrProvider, req.URL.Host, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// The body goes into the operator's log and never to the browser: it
		// is a third party's text, and on a token endpoint it can name the
		// client id.
		return fmt.Errorf("%w: %s answered %d: %s",
			ErrProvider, req.URL.Host, res.StatusCode, snippet(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: %s answered with something that is not JSON: %v",
			ErrProvider, req.URL.Host, err)
	}
	return nil
}

// snippet bounds what a provider's error text can put in one log line.
func snippet(b []byte) string {
	const limit = 200
	s := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, string(b))
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
