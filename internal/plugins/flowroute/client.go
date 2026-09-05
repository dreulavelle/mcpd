package flowroute

import (
	"context"
	"encoding/json"
	"errors"
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

// maxResponseBody bounds a successful body. A page is at most two hundred
// entities and the widest of them is a few hundred bytes, so this is a
// backstop against a response that is not the API's rather than a limit
// anything real approaches.
const maxResponseBody = 8 << 20

// entityID is a JSON:API id read from either shape Flowroute sends.
//
// The specification says an id is a string, and Flowroute sends one for every
// entity except an edge strategy, whose id arrives as the number 1. A plain
// string field fails that one response outright -- "cannot unmarshal number
// into Go struct field .0.id" -- and a plain number field fails every other,
// so the type has to accept both.
type entityID string

// UnmarshalJSON accepts a quoted id or a bare number.
func (e *entityID) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		*e = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*e = entityID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("flowroute: an id was neither a string nor a number: %s", trimmed)
	}
	*e = entityID(n.String())
	return nil
}

// String renders the id.
func (e entityID) String() string { return string(e) }

// resource is one JSON:API entity: the envelope every Flowroute object arrives
// in.
type resource struct {
	ID   entityID `json:"id"`
	Type string   `json:"type"`
	// Attributes is left raw so each tool decodes the fields it names. A tool
	// that names none cannot leak one it never asked for.
	Attributes    json.RawMessage         `json:"attributes"`
	Relationships map[string]relationship `json:"relationships"`
}

// relationship points at another entity, which arrives in the document's
// `included` array rather than nested here.
type relationship struct {
	Data *resourceRef `json:"data"`
}

// resourceRef identifies a related entity.
type resourceRef struct {
	ID   entityID `json:"id"`
	Type string   `json:"type"`
}

// related returns the id of a named relationship, or "" when it is null.
func (r resource) related(name string) string {
	rel, ok := r.Relationships[name]
	if !ok || rel.Data == nil {
		return ""
	}
	return rel.Data.ID.String()
}

// document is a whole JSON:API response.
type document struct {
	// Data is an object for a single read and an array for a listing, so it
	// stays raw until the caller says which it expects.
	Data     json.RawMessage `json:"data"`
	Included []resource      `json:"included"`
	Links    struct {
		Self string `json:"self"`
		Next string `json:"next"`
	} `json:"links"`
}

// one decodes a single-entity document.
func (d *document) one() (resource, error) {
	var r resource
	if len(d.Data) == 0 {
		return r, fmt.Errorf("flowroute: the response carried no data")
	}
	if err := json.Unmarshal(d.Data, &r); err != nil {
		return r, fmt.Errorf("flowroute: could not read the response: %w", err)
	}
	return r, nil
}

// many decodes a listing document.
func (d *document) many() ([]resource, error) {
	if len(d.Data) == 0 {
		return nil, nil
	}
	var rs []resource
	if err := json.Unmarshal(d.Data, &rs); err != nil {
		return nil, fmt.Errorf("flowroute: could not read the listing: %w", err)
	}
	return rs, nil
}

// attrs decodes one entity's attributes into v.
func (r resource) attrs(v any) error {
	if len(r.Attributes) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Attributes, v); err != nil {
		return fmt.Errorf("flowroute: could not read a %s: %w", r.Type, err)
	}
	return nil
}

// Client talks to Flowroute's read endpoints as one account.
type Client struct {
	http    *http.Client
	base    string
	log     *slog.Logger
	limiter *rate.Limiter
	observe func(outcome string, d time.Duration)
	now     func() time.Time

	maxItems int

	// The credential lives only here. The Config the plugin retains has every
	// customer's halves blanked, so a dump of it -- a log line, an error, the
	// settings page -- cannot carry one.
	accessKey string
	secretKey string
}

// NewClient builds a client for one customer's account. The http client it is
// given is wrapped so every request goes through the read-only guard.
func NewClient(hc *http.Client, cfg Config, accessKey, secretKey string,
	log *slog.Logger, now func() time.Time, observe func(string, time.Duration),
) (*Client, error) {
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	guarded, err := readOnly(hc, cfg.BaseURL)
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
		http:      guarded,
		base:      cfg.BaseURL,
		log:       log,
		limiter:   rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1),
		observe:   observe,
		now:       now,
		maxItems:  cfg.MaxItems,
		accessKey: accessKey,
		secretKey: secretKey,
	}, nil
}

// Describe says which account this reads, without saying the credential.
//
// The access key is an identifier rather than a secret, but it is half of a
// credential that travels together, and there is nothing an operator does with
// it that the address does not already answer.
func (c *Client) Describe() string {
	if u, err := url.Parse(c.base); err == nil && u.Host != "" {
		return u.Host
	}
	return c.base
}

// Probe proves the credential.
//
// One number, which is the smallest read there is. Flowroute has no endpoint
// that authenticates without touching the account -- no token exchange, no
// whoami -- so the cheapest proof still reads one row.
func (c *Client) Probe(ctx context.Context) error {
	q := url.Values{}
	q.Set("limit", "1")
	_, err := c.get(ctx, "/v2/numbers", q)
	return err
}

// get makes one read and decodes the JSON:API document.
func (c *Client) get(ctx context.Context, path string, q url.Values) (*document, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("flowroute: could not build the request: %w", err)
	}
	req.SetBasicAuth(c.accessKey, c.secretKey)
	req.Header.Set("Accept", "application/json")

	// The path and the parameter names, never the values: a query carries
	// telephone numbers, which are the customer's rather than ours to log.
	c.log.DebugContext(ctx, "flowroute read", "path", path, "params", paramNames(q))

	start := c.now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.observe("error", c.now().Sub(start))
		return nil, err
	}
	defer resp.Body.Close()
	elapsed := c.now().Sub(start)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		c.observe(strconv.Itoa(resp.StatusCode), elapsed)
		return nil, explainRequestFailure(resp.StatusCode, body)
	}
	c.observe("ok", elapsed)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("flowroute: could not read the response: %w", err)
	}
	var doc document
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("flowroute: could not read the response as JSON:API: %w", err)
	}
	return &doc, nil
}

// page is what a walked listing collected, with whether Flowroute holds more.
type page struct {
	items []resource
	// more is what the last page's links.next said. It is the difference
	// between "that is all of them" and "that is as many as you asked for",
	// which a listing has to report or be read as the first when it is the
	// second.
	more bool
}

// list walks a listing until it runs out or until limit items are collected.
//
// Flowroute pages with limit and offset and says whether another page exists
// by sending links.next. The offset is computed here rather than taken from
// that link: following a URL the API composed would send whatever it put in
// it, and the guard would then be arguing with the API about the shape of a
// path instead of this package deciding it.
func (c *Client) list(ctx context.Context, path string, q url.Values, limit int) (page, error) {
	if limit <= 0 || limit > c.maxItems {
		limit = c.maxItems
	}
	var out page
	offset := 0
	for {
		want := limit - len(out.items)
		if want <= 0 {
			// Collected the caller's limit. Whether Flowroute holds more is
			// what the last page's links.next already said.
			return out, nil
		}
		if want > maxPageSize {
			want = maxPageSize
		}
		pq := url.Values{}
		for k, v := range q {
			pq[k] = v
		}
		pq.Set("limit", strconv.Itoa(want))
		pq.Set("offset", strconv.Itoa(offset))

		doc, err := c.get(ctx, path, pq)
		if err != nil {
			// A listing with nothing in it answers 404 rather than an empty
			// array -- "No Port Orders match this query." That is an answer,
			// so it is one here too. A 404 that names no resource is still an
			// error, because it means this package asked for a path Flowroute
			// does not serve.
			if isNotFound(err) {
				return out, nil
			}
			return out, err
		}
		items, err := doc.many()
		if err != nil {
			return out, err
		}
		out.items = append(out.items, items...)
		out.more = strings.TrimSpace(doc.Links.Next) != ""
		if !out.more || len(items) == 0 {
			return out, nil
		}
		offset += len(items)
	}
}

// isNotFound reports the absent-resource 404, not the unserved-path one.
func isNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
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
