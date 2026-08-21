package cnmaestro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// Client talks to the cnMaestro API.
//
// Four things it does that a naive client would not, each because the API
// makes the naive choice wrong:
//
//   - Every request passes a guard that refuses anything but a GET and checks
//     the decoded path against the deny-list, so nothing can reach the
//     remote-command endpoints even by constructing a path directly.
//   - Calls go to the host the token response named, not the one tokens were
//     obtained from, because cloud accounts are regionally sharded.
//   - managed_account is attached to every request when configured, because
//     omitting it means different things depending on whether a request names
//     a network.
//   - Listing supports both pagination schemes, because which one an endpoint
//     uses is a per-endpoint fact rather than an API-wide one.
type Client struct {
	http    *http.Client
	tokens  *tokenManager
	limiter *rate.Limiter
	cfg     Config
	log     *slog.Logger
}

// NewClient builds an API client. cfg is assumed already defaulted.
//
// The read-only guard is applied here rather than by the caller, so there is
// no way to construct a client without it -- including in a test, which is
// where an unguarded one would otherwise be most likely to appear.
func NewClient(httpClient *http.Client, cfg Config, clientID, secret string, log *slog.Logger, now func() time.Time) *Client {
	guarded := readOnly(httpClient)
	return &Client{
		http:   guarded,
		tokens: newTokenManager(guarded, cfg.BaseURL, clientID, secret, now),
		// Burst of one: listing walks pages in a tight loop, and a burst
		// allowance would let the first few pages ignore the limit entirely,
		// which is the shape most likely to trip an upstream limit.
		limiter: rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1),
		cfg:     cfg,
		log:     log,
	}
}

// Record is one cnMaestro object, held as decoded JSON rather than as a
// typed struct.
//
// The API returns a oneOf across device types -- cnmatrix, cnwave60,
// enterprise Wi-Fi, NSE and more -- each with its own fields and a type
// discriminator. There is no common shape, and inventing one would drop
// whatever the caller actually asked about.
//
// A map rather than json.RawMessage, because these reach a tool's output
// schema. RawMessage is a []byte, which reflects to "array of integers 0-255"
// while marshalling as an object -- a schema that contradicts the value it
// describes, and one a strict client rejects.
type Record = map[string]any

// envelope is the shape every collection response arrives in.
type envelope struct {
	Data   json.RawMessage `json:"data"`
	Paging Paging          `json:"paging"`
	// Warnings is easy to miss and worth surfacing. The API answers 200 with a
	// partial result rather than failing when part of an estate is
	// unreachable, so a caller that ignores this reports an incomplete picture
	// as a complete one.
	Warnings []string `json:"warnings,omitempty"`
}

// Paging is what the API reports about a page.
type Paging struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
	Total  int `json:"total"`
	// NextContinuationToken is set by the endpoints that have moved off
	// offset paging. Empty means either the last page or an endpoint that
	// never used tokens.
	NextContinuationToken string `json:"next_continuation_token"`
}

// Get fetches a single resource and decodes its data field into out.
func (c *Client) Get(ctx context.Context, path string, params url.Values, out any) ([]string, error) {
	env, err := c.do(ctx, path, params)
	if err != nil {
		return nil, err
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return env.Warnings, fmt.Errorf("cnmaestro: decode %s: %w", path, err)
		}
	}
	return env.Warnings, nil
}

// Page is one page of a collection, plus what it took to get there.
type Page struct {
	Items    []Record
	Total    int
	Warnings []string
	// Truncated reports that MaxItems stopped the walk before the collection
	// was exhausted. Saying so lets a caller narrow the request rather than
	// mistake part of an estate for all of it.
	Truncated bool
}

// List walks a paginated collection and returns the items.
//
// Both pagination schemes are handled, chosen by what the response carries
// rather than by a table of endpoints here: a response with a continuation
// token is followed by token, and one without is followed by offset. That way
// an endpoint moving between schemes — which is happening, one at a time,
// ahead of offset's removal in 6.4.0 — does not need this client changed.
func (c *Client) List(ctx context.Context, path string, params url.Values) (Page, error) {
	if params == nil {
		params = url.Values{}
	}
	params.Set("limit", strconv.Itoa(c.cfg.PageSize))

	var page Page
	seenWarnings := map[string]bool{}
	offset := 0
	token := ""

	for {
		q := cloneValues(params)
		switch {
		case token != "":
			q.Set("continuation_token", token)
			// offset and continuation_token together is a contradiction, and
			// the endpoints that accept tokens are the ones deprecating
			// offset.
			q.Del("offset")
		case offset > 0:
			q.Set("offset", strconv.Itoa(offset))
		}

		env, err := c.do(ctx, path, q)
		if err != nil {
			return page, err
		}

		var items []Record
		if len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, &items); err != nil {
				return page, fmt.Errorf("cnmaestro: decode %s: %w", path, err)
			}
		}
		for _, w := range env.Warnings {
			if !seenWarnings[w] {
				seenWarnings[w] = true
				page.Warnings = append(page.Warnings, w)
			}
		}
		if env.Paging.Total > 0 {
			page.Total = env.Paging.Total
		}

		for _, item := range items {
			if len(page.Items) >= c.cfg.MaxItems {
				page.Truncated = true
				return page, nil
			}
			page.Items = append(page.Items, item)
		}

		// A page that came back empty ends the walk whatever the paging says.
		// Without this, an endpoint reporting a total it cannot deliver spins.
		if len(items) == 0 {
			return page, nil
		}

		if next := strings.TrimSpace(env.Paging.NextContinuationToken); next != "" {
			token = next
			continue
		}
		if token != "" {
			// Was following tokens and the response carried none: finished.
			return page, nil
		}

		offset += len(items)
		if env.Paging.Total > 0 && offset >= env.Paging.Total {
			return page, nil
		}
		// An endpoint that reports no total is walked until a short page,
		// which is the only end-of-collection signal it gives.
		if env.Paging.Total == 0 && len(items) < c.cfg.PageSize {
			return page, nil
		}
	}
}

// do performs one request and returns its envelope.
func (c *Client) do(ctx context.Context, path string, params url.Values) (envelope, error) {
	var env envelope

	if err := checkPath(path); err != nil {
		return env, err
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return env, fmt.Errorf("cnmaestro: %w", err)
	}

	token, host, err := c.tokens.token(ctx)
	if err != nil {
		return env, err
	}

	q := cloneValues(params)
	if acct := strings.TrimSpace(c.cfg.ManagedAccount); acct != "" {
		q.Set(managedAccountKV, acct)
	}

	target := host + apiPrefix + path
	if encoded := q.Encode(); encoded != "" {
		target += "?" + encoded
	}

	body, status, err := c.send(ctx, target, token)
	if err != nil {
		return env, err
	}

	// A credential rejected before its expiry has been revoked or rotated
	// upstream. Dropping it means the next call obtains a fresh one rather
	// than reusing something the API has already refused.
	if status == http.StatusUnauthorized {
		c.tokens.invalidate()
	}
	if status != http.StatusOK {
		return env, explainRequestFailure(status, path, body)
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return env, fmt.Errorf("cnmaestro: %s did not return the documented "+
			"envelope: %w", path, err)
	}
	return env, nil
}

// send issues the request and reads the response.
func (c *Client) send(ctx context.Context, target, token string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("cnmaestro: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("cnmaestro: reach %s: %w", redactURL(target), err)
	}
	defer resp.Body.Close()

	limit := int64(maxErrorBody)
	if resp.StatusCode == http.StatusOK {
		limit = 32 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("cnmaestro: read response: %w", err)
	}
	return body, resp.StatusCode, nil
}

// APIHost reports where data calls are going, which is not necessarily where
// tokens were obtained.
func (c *Client) APIHost() string { return c.tokens.host() }

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	return out
}
