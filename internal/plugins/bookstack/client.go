package bookstack

import (
	"bytes"
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

// maxResponseBody bounds a successful body. A book export or a page's HTML is
// the large case; this is a backstop against a response that is not the API's
// rather than a limit anything real approaches.
const maxResponseBody = 32 << 20

// listing is the envelope every BookStack listing arrives in: the rows, and
// how many exist in total.
//
// The total is what makes paging arithmetic rather than a cursor, and it is
// also what a listing reports when it stops short -- "20 of 322" is a more
// useful answer than "20".
type listing struct {
	Data  json.RawMessage `json:"data"`
	Total int             `json:"total"`
}

// Client talks to one BookStack instance as one token.
type Client struct {
	http    *http.Client
	root    string
	log     *slog.Logger
	limiter *rate.Limiter
	observe func(outcome string, d time.Duration)
	now     func() time.Time

	maxItems int

	// The token pair lives only here. The Config the plugin retains has both
	// halves blanked, so a dump of it -- a log line, an error, the settings
	// page -- cannot carry one.
	tokenID     string
	tokenSecret string
}

// NewClient builds a client. The http client it is given is wrapped so every
// request goes through the endpoint guard.
func NewClient(hc *http.Client, cfg Config, log *slog.Logger, now func() time.Time,
	observe func(string, time.Duration),
) (*Client, error) {
	root := cfg.root()
	if root == "" {
		return nil, fmt.Errorf("bookstack: the address %q could not be read", cfg.Host)
	}
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	g, err := guarded(hc, root)
	if err != nil {
		return nil, err
	}
	if observe == nil {
		observe = func(string, time.Duration) {}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	return &Client{
		http:        g,
		root:        root,
		log:         log,
		limiter:     rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1),
		observe:     observe,
		now:         now,
		maxItems:    cfg.MaxItems,
		tokenID:     cfg.TokenID,
		tokenSecret: cfg.TokenSecret,
	}, nil
}

// Describe says which instance this reads, without saying the credential.
func (c *Client) Describe() string {
	if u, err := url.Parse(c.root); err == nil && u.Host != "" {
		return u.Host
	}
	return c.root
}

// SystemInfo is what the instance says about itself.
type SystemInfo struct {
	Version    string `json:"version"`
	InstanceID string `json:"instance_id"`
	AppName    string `json:"app_name"`
	BaseURL    string `json:"base_url"`
}

// Probe proves the token and reports what answered.
//
// /api/system is the one endpoint that authenticates without touching
// anybody's content, which makes it the honest startup check: it says the
// address is right, the token is accepted, and what version is on the other
// end, and it reads no page.
func (c *Client) Probe(ctx context.Context) (SystemInfo, error) {
	var out SystemInfo
	body, err := c.do(ctx, http.MethodGet, "/api/system", nil, nil)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("bookstack: could not read the instance's details: %w", err)
	}
	return out, nil
}

// get makes one read and returns the raw body.
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, q, nil)
}

// send makes one write with a JSON body.
func (c *Client) send(ctx context.Context, method, path string, payload any) ([]byte, error) {
	return c.do(ctx, method, path, nil, payload)
}

// do performs one request against the instance.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, payload any) ([]byte, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	u := c.root + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("bookstack: could not encode the request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, fmt.Errorf("bookstack: could not build the request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+c.tokenID+":"+c.tokenSecret)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	// The method, the path and the parameter names -- never the values, and
	// never the payload. A page's body is the customer's writing, and a
	// query carries whatever somebody searched for.
	c.log.DebugContext(ctx, "bookstack request",
		"method", method, "path", path, "params", paramNames(q))

	start := c.now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.observe("error", c.now().Sub(start))
		return nil, err
	}
	defer resp.Body.Close()
	elapsed := c.now().Sub(start)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		c.observe(strconv.Itoa(resp.StatusCode), elapsed)
		return nil, explainRequestFailure(resp.StatusCode, raw)
	}
	c.observe("ok", elapsed)

	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("bookstack: could not read the response: %w", err)
	}
	return out, nil
}

// page is what a walked listing collected.
type page struct {
	rows []json.RawMessage
	// total is how many exist upstream, which is what lets a truncated answer
	// say "20 of 322" rather than leaving a model to assume it saw everything.
	total int
	more  bool
}

// list walks a listing until it runs out or until limit rows are collected.
//
// BookStack pages with count and offset and reports the total, so this is
// arithmetic: ask for what is left of the caller's limit, stop when the rows
// run out or the total is reached.
func (c *Client) list(ctx context.Context, path string, q url.Values, limit int) (page, error) {
	if limit <= 0 || limit > c.maxItems {
		limit = c.maxItems
	}
	var out page
	offset := 0
	for {
		want := limit - len(out.rows)
		if want <= 0 {
			out.more = out.total > len(out.rows)
			return out, nil
		}
		if want > maxPageSize {
			want = maxPageSize
		}
		pq := url.Values{}
		for k, v := range q {
			pq[k] = v
		}
		pq.Set("count", strconv.Itoa(want))
		pq.Set("offset", strconv.Itoa(offset))

		raw, err := c.get(ctx, path, pq)
		if err != nil {
			return out, err
		}
		var l listing
		if err := json.Unmarshal(raw, &l); err != nil {
			return out, fmt.Errorf("bookstack: could not read the listing: %w", err)
		}
		var rows []json.RawMessage
		if len(l.Data) > 0 {
			if err := json.Unmarshal(l.Data, &rows); err != nil {
				return out, fmt.Errorf("bookstack: could not read the listing's rows: %w", err)
			}
		}
		out.total = l.Total
		out.rows = append(out.rows, rows...)
		if len(rows) == 0 || len(out.rows) >= l.Total {
			return out, nil
		}
		offset += len(rows)
	}
}

// decodeRows turns a walked listing's rows into a typed slice.
func decodeRows[T any](p page) ([]T, error) {
	out := make([]T, 0, len(p.rows))
	for _, raw := range p.rows {
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("bookstack: could not read a row: %w", err)
		}
		out = append(out, v)
	}
	return out, nil
}

// paramNames is the keys of a query, for a debug line that must not carry the
// values.
func paramNames(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	names := make([]string, 0, len(q))
	for k := range q {
		names = append(names, k)
	}
	sortStrings(names)
	return strings.Join(names, ",")
}

// sortStrings is sort.Strings, kept local so a debug line pulls in nothing.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
