package bandwidth

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

// host names which of Bandwidth's products a call is addressed to.
//
// Bandwidth serves one product per host rather than one API under one root, so
// the address is part of what a call is rather than configuration. Naming the
// three makes a call site say which product it is asking, and keeps the URLs
// in one place.
type host int

const (
	hostVoice host = iota
	hostMessaging
	// hostAPI is the gateway: toll-free verification, endpoints and number
	// lookup. It is also where the Dashboard XML API is served, under
	// /api/v2, which is how a later phase reaches numbers and E911 on this
	// same credential.
	hostAPI
)

// Client talks to Bandwidth's read endpoints.
type Client struct {
	http   *http.Client
	tokens *tokenSource

	voice     string
	messaging string
	api       string

	accountID string
	maxItems  int

	observe func(outcome string, d time.Duration)
	now     func() time.Time
}

// NewClient builds a client. The http client it is given is wrapped so every
// request goes through the read-only guard.
func NewClient(hc *http.Client, cfg Config, now func() time.Time,
	observe func(string, time.Duration),
) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	guarded := readOnly(hc)
	if observe == nil {
		observe = func(string, time.Duration) {}
	}
	return &Client{
		http:      guarded,
		tokens:    newTokenSource(guarded, cfg.APIURL, cfg.ClientID, cfg.ClientSecret, now),
		voice:     cfg.VoiceURL,
		messaging: cfg.MessagingURL,
		api:       cfg.APIURL,
		accountID: cfg.AccountID,
		maxItems:  cfg.MaxItems,
		observe:   observe,
		now:       now,
	}
}

// base returns the address for one of Bandwidth's products.
func (c *Client) base(h host) string {
	switch h {
	case hostVoice:
		return c.voice
	case hostMessaging:
		return c.messaging
	default:
		return c.api
	}
}

// AccountID is which account this client reads, for a message that names it.
func (c *Client) AccountID() string { return c.accountID }

// Accounts reports which accounts the credential may reach.
func (c *Client) Accounts(ctx context.Context) ([]string, error) {
	return c.tokens.Accounts(ctx)
}

// get performs one authenticated read and decodes the result into out.
//
// query is applied as-is; a nil value is a call with no parameters. out may be
// nil for a caller that only wants to know the call succeeded.
func (c *Client) get(ctx context.Context, h host, path string, query url.Values, out any) error {
	raw, err := c.do(ctx, h, path, query)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("bandwidth: %s answered %s with something that is "+
			"not the API's JSON: %s", redactURL(c.base(h)), path,
			summarise(http.StatusOK, raw))
	}
	return nil
}

// do performs one authenticated read and returns the body.
func (c *Client) do(ctx context.Context, h host, path string, query url.Values) ([]byte, error) {
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}

	target := c.base(h) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("bandwidth: build the request for %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	started := c.now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.observe("error", c.now().Sub(started))
		return nil, fmt.Errorf("bandwidth: reaching %s: %w",
			redactURL(c.base(h)), err)
	}
	defer resp.Body.Close()

	// Bounded, because a body this integration will not understand is a body
	// it should not read into memory in full either.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	elapsed := c.now().Sub(started)
	if err != nil {
		c.observe("error", elapsed)
		return nil, fmt.Errorf("bandwidth: reading the response from %s: %w", path, err)
	}

	if resp.StatusCode >= 300 {
		c.observe("error", elapsed)
		return nil, explainRequestFailure(resp.StatusCode, path, body)
	}
	c.observe("ok", elapsed)
	return body, nil
}

// Probe makes the cheapest authenticated call there is: the token exchange
// itself.
//
// It reads nothing about anybody's estate, which is the point of using it as
// the startup check -- a host coming up should not need permission to see
// calls in order to report that its credential works. It settles the things a
// wrong configuration could be: the address does not resolve, TLS fails, the
// credential is refused, or the credential is real but cannot reach the
// account this instance was told to read.
func (c *Client) Probe(ctx context.Context) ([]string, error) {
	accounts, err := c.tokens.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		// Not fatal. The claim is the credential describing itself, and a
		// credential that declines to is still usable -- the first real read
		// will say so either way.
		return nil, nil
	}
	for _, a := range accounts {
		if a == c.accountID {
			return accounts, nil
		}
	}
	return accounts, fmt.Errorf("bandwidth: this credential does not cover "+
		"account %s. It covers %s. A credential's accounts are fixed when it "+
		"is created, so either point this instance at one of those or make a "+
		"credential that includes %s",
		c.accountID, strings.Join(accounts, ", "), c.accountID)
}

// Describe says where this instance reads from and what its read-only
// guarantee rests on, for the startup log and the health report.
func (c *Client) Describe() string {
	return fmt.Sprintf("Bandwidth account %s at %s, %s and %s, restricted to a "+
		"named list of read endpoints by its transport",
		c.accountID, redactURL(c.api), redactURL(c.voice), redactURL(c.messaging))
}

// limit caps a caller's page size to what this instance allows.
//
// Applied here rather than at each tool, because forgetting it at one call
// site is how an estate-sized answer reaches a model.
func (c *Client) limit(requested int) int {
	if requested <= 0 || requested > c.maxItems {
		return c.maxItems
	}
	return requested
}
