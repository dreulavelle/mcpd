package bandwidth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
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
	// lookup.
	hostAPI
	// hostInsights serves aggregates computed over traffic rather than the
	// traffic itself, which is why it is a fifth address rather than a path.
	hostInsights
)

// dashboardPrefix is where the gateway serves the Dashboard API.
//
// The same credential reaches it. That is worth stating because Bandwidth's
// own documentation sends people to dashboard.bandwidth.com with a separate
// username and password, and the gateway route means neither is needed here.
const dashboardPrefix = "/api/v2"

// Client talks to Bandwidth's read endpoints.
type Client struct {
	http   *http.Client
	tokens *tokenSource
	log    *slog.Logger

	voice     string
	messaging string
	api       string
	insights  string

	defaultAccount string
	maxItems       int

	observe func(outcome string, d time.Duration)
	now     func() time.Time
}

// NewClient builds a client. The http client it is given is wrapped so every
// request goes through the read-only guard.
func NewClient(hc *http.Client, cfg Config, log *slog.Logger, now func() time.Time,
	observe func(string, time.Duration),
) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	guarded := readOnly(hc)
	if observe == nil {
		observe = func(string, time.Duration) {}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{
		http:           guarded,
		log:            log,
		tokens:         newTokenSource(guarded, cfg.APIURL, cfg.ClientID, cfg.ClientSecret, now),
		voice:          cfg.VoiceURL,
		messaging:      cfg.MessagingURL,
		api:            cfg.APIURL,
		insights:       cfg.InsightsURL,
		defaultAccount: cfg.DefaultAccountID,
		maxItems:       cfg.MaxItems,
		observe:        observe,
		now:            now,
	}
}

// base returns the address for one of Bandwidth's products.
func (c *Client) base(h host) string {
	switch h {
	case hostVoice:
		return c.voice
	case hostMessaging:
		return c.messaging
	case hostInsights:
		return c.insights
	default:
		return c.api
	}
}

// DefaultAccount is the account an unqualified call reads, or empty.
func (c *Client) DefaultAccount() string { return c.defaultAccount }

// resolveAccount decides which account a call is about.
//
// In order: what the caller asked for, then the configured default, then the
// only account the credential covers if there is exactly one. With several in
// scope and nothing to choose between them, the call is refused and told what
// the options are -- because the alternative is answering confidently about
// whichever account happened to be first, and an operator reading "no port-ins
// are stuck" has no way to tell that it was about the wrong account.
//
// A named account is checked against the credential's own claim, so the
// mistake comes back as a sentence rather than as a 404 on every path.
func (c *Client) resolveAccount(ctx context.Context, requested string) (string, error) {
	requested = strings.TrimSpace(requested)

	// The claim is cached with the token, so this is not a request per call.
	// A credential that declines to name its accounts leaves this empty, and
	// then nothing below can validate -- which is handled rather than fatal.
	covered, err := c.Accounts(ctx)
	if err != nil {
		return "", err
	}

	if requested != "" {
		if len(covered) == 0 || slices.Contains(covered, requested) {
			return requested, nil
		}
		return "", fmt.Errorf("bandwidth: this credential does not cover "+
			"account %s. It covers %s. A credential's accounts are fixed when "+
			"it is created, so reading %s needs a credential that includes it",
			requested, strings.Join(covered, ", "), requested)
	}

	if c.defaultAccount != "" {
		return c.defaultAccount, nil
	}
	switch len(covered) {
	case 1:
		return covered[0], nil
	case 0:
		return "", errors.New("bandwidth: no account was given and this " +
			"credential does not say which it covers. Name one on the call, " +
			"or set a default account in settings")
	default:
		return "", fmt.Errorf("bandwidth: this credential covers %s. Name "+
			"which one to read on the call, or set a default account in "+
			"settings so an unqualified question has an answer",
			strings.Join(covered, ", "))
	}
}

// Accounts reports which accounts the credential may reach.
func (c *Client) Accounts(ctx context.Context) ([]string, error) {
	return c.tokens.Accounts(ctx)
}

// The two things Bandwidth answers with. Sent as Accept, because half of it
// speaks JSON and half speaks XML and a request that asks for the wrong one is
// answered with a refusal rather than a translation.
const (
	acceptJSON = "application/json"
	acceptXML  = "application/xml"
)

// get performs one authenticated read and decodes the result into out.
//
// query is applied as-is; a nil value is a call with no parameters. out may be
// nil for a caller that only wants to know the call succeeded.
func (c *Client) get(ctx context.Context, h host, path string, query url.Values, out any) error {
	raw, err := c.do(ctx, h, path, query, acceptJSON)
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
func (c *Client) do(ctx context.Context, h host, path string, query url.Values, accept string) ([]byte, error) {
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
	req.Header.Set("Accept", accept)

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

	// What a support call turns on: what was asked, and what the upstream
	// said about it. Never the body and never the query -- a successful body
	// here is somebody's call detail, their numbers and who they called, which
	// is the one thing this must not spill into a log file that leaves the
	// machine. The status is the part that distinguishes a wrong credential
	// from a missing role from a path nobody has permission for, and without
	// it a failed call leaves nothing behind at all.
	// The host as well as the path. Bandwidth serves five, the same path
	// shape appears on more than one of them, and a log line that names only
	// the path cannot say which product refused a call.
	c.log.DebugContext(ctx, "bandwidth API call",
		"host", redactURL(c.base(h)), "path", path, "status", resp.StatusCode,
		"bytes", len(body), "took", elapsed)

	if resp.StatusCode >= 300 {
		c.observe("error", elapsed)
		return nil, explainRequestFailure(resp.StatusCode, path, body)
	}
	c.observe("ok", elapsed)
	return body, nil
}

// getXML performs one authenticated read against the Dashboard API and decodes
// the XML into the same shape a JSON response decodes to.
//
// path is written without the /api/v2 prefix, so a call site reads the way
// Bandwidth's own documentation writes it.
func (c *Client) getXML(ctx context.Context, path string, query url.Values) (Record, error) {
	return c.getXMLAt(ctx, dashboardPrefix+path, query)
}

// getXMLAt is getXML for a path that does not live under /api/v2.
//
// Campaign management is served under /api, not /api/v2, so it cannot go
// through the prefixing helper above. Everything after the request is
// identical, including the two failure shapes the Dashboard uses: an empty body
// for an empty collection, and an error carried inside a 200.
func (c *Client) getXMLAt(ctx context.Context, path string, query url.Values) (Record, error) {
	raw, err := c.do(ctx, hostAPI, path, query, acceptXML)
	if err != nil {
		return nil, err
	}
	// 204, or a 200 with nothing in it, is Bandwidth saying the collection is
	// empty -- an account with no port-in orders, which is the ordinary state
	// of most accounts most of the time. Decoding that as XML fails, and
	// reporting the failure would turn "you have no ports in flight" into "the
	// integration is broken".
	if len(bytes.TrimSpace(raw)) == 0 {
		return Record{}, nil
	}
	out, err := decodeXML(raw)
	if err != nil {
		return nil, fmt.Errorf("bandwidth: %s answered %s with something this "+
			"integration could not read: %w", redactURL(c.api), path, err)
	}
	// The Dashboard reports its own failures inside a 200 as often as through
	// a status code, which is why this is checked here rather than left to the
	// status handling in do.
	if err := dashboardError(out, path); err != nil {
		return nil, err
	}
	return out, nil
}

// dashboardError turns an error carried inside a successful response into one.
//
// The Dashboard answers some refusals with 200 and an <ErrorCode> in the body.
// Left alone, that reads as an empty result -- the worst possible outcome,
// because a model told nothing is there will say so with confidence.
func dashboardError(rec Record, path string) error {
	code := text(rec, "ErrorCode")
	description := text(rec, "Description")
	if code == "" && description == "" {
		// The other shape: a nested <Error> element.
		if inner, ok := rec["Error"].(Record); ok {
			code, description = text(inner, "Code"), text(inner, "Description")
		}
	}
	if code == "" && description == "" {
		return nil
	}
	if description == "" {
		description = "no description given"
	}
	return fmt.Errorf("bandwidth: %s refused the read (%s): %s", path, code, description)
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
	if len(accounts) == 0 || c.defaultAccount == "" {
		// Nothing to check. A credential that declines to name its accounts is
		// still usable, and an instance with no default is the ordinary case
		// now -- the caller names the account.
		return accounts, nil
	}
	if slices.Contains(accounts, c.defaultAccount) {
		return accounts, nil
	}
	// A default that cannot work is a misconfiguration worth refusing at
	// startup, because every unqualified call would otherwise fail later with
	// a 404 that says nothing about why.
	return accounts, fmt.Errorf("bandwidth: the default account %s is not one "+
		"this credential covers. It covers %s. Change the default, or leave it "+
		"empty and name an account on each call",
		c.defaultAccount, strings.Join(accounts, ", "))
}

// Describe says where this instance reads from and what its read-only
// guarantee rests on, for the startup log and the health report.
func (c *Client) Describe() string {
	scope := "whichever account a call names"
	if c.defaultAccount != "" {
		scope = "account " + c.defaultAccount + " by default"
	}
	return fmt.Sprintf("Bandwidth at %s, %s and %s, reading %s, restricted to "+
		"a named list of read endpoints by its transport",
		redactURL(c.api), redactURL(c.voice), redactURL(c.messaging), scope)
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
