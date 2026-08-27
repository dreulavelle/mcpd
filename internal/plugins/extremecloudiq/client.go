package extremecloudiq

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

// maxResponseBody bounds a successful body. Every collection here is already
// bounded by the page size this plugin asks for, so this is a backstop against
// an endpoint that ignores a limit rather than the thing that keeps answers
// sane.
const maxResponseBody = 64 << 20

// Record is one row as the API sent it.
//
// A map rather than a struct per collection, deliberately. A device carries
// forty fields and a client fifty-six, the set of them changes with the
// release, and the tools here let a caller choose which ones come back -- so a
// Go struct would be a fourth description of the same thing, out of date the
// moment Extreme adds a field. It also keeps the output schema a model is
// charged for down to a generic object rather than four hundred lines of
// properties nobody reads.
type Record = map[string]any

// paged is the envelope every collection response arrives in.
type paged struct {
	Page       int      `json:"page"`
	Count      int      `json:"count"`
	TotalPages int      `json:"total_pages"`
	TotalCount int      `json:"total_count"`
	Data       []Record `json:"data"`
}

// Client talks to one ExtremeCloud IQ account.
type Client struct {
	http    *http.Client
	cfg     Config
	root    string
	token   string
	log     *slog.Logger
	now     func() time.Time
	limiter *rate.Limiter
	cache   *readCache
	observe func(outcome string, d time.Duration)
}

// NewClient builds a client. The credential is passed separately from the
// config so that the Config the plugin retains can be free of it.
func NewClient(hc *http.Client, cfg Config, token string,
	log *slog.Logger, now func() time.Time, cache *readCache,
	observe func(string, time.Duration)) *Client {

	return &Client{
		http:    readOnly(hc, basePath(cfg.root())),
		cfg:     cfg,
		root:    cfg.root(),
		token:   strings.TrimSpace(token),
		log:     log,
		now:     now,
		limiter: rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1),
		cache:   cache,
		observe: observe,
	}
}

// Get reads an endpoint, through the cache where that endpoint is cacheable.
func (c *Client) Get(ctx context.Context, path string, params url.Values) (json.RawMessage, error) {
	if c.cache == nil {
		return c.do(ctx, http.MethodGet, path, params, nil)
	}
	got, err := c.cache.reuse(ctx, path, params, func(ctx context.Context) (any, error) {
		return c.do(ctx, http.MethodGet, path, params, nil)
	})
	if err != nil {
		return nil, err
	}
	raw, ok := got.(json.RawMessage)
	if !ok {
		// Cannot happen: this is the only thing put in. Reported rather than
		// asserted because a panic inside a tool call takes the request down.
		return nil, fmt.Errorf("extremecloudiq: cached value for %s was not a response", path)
	}
	return raw, nil
}

// Post sends a filter body to one of the diagnostics endpoints and decodes the
// answer.
//
// A read, despite the method. The dashboard grids take a list of site and
// device ids, which does not fit in a query string, so the API takes it in a
// body -- the same reason Graylog's searches are POSTs. transport.go names
// each one it permits, which is what keeps "this integration may POST" from
// meaning "this integration may write".
//
// Never cached, and it does not go through the cache at all rather than
// through it and missing. Everything reachable this way is a question about
// how the estate is *now*, which is the class of answer this plugin never
// holds; routing it past a lookup that would always return zero keeps that
// true by construction rather than by a table staying correct.
func (c *Client) Post(ctx context.Context, path string, params url.Values, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("extremecloudiq: building the request for %s: %w", path, err)
	}
	raw, err := c.do(ctx, http.MethodPost, path, params, encoded)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("extremecloudiq: %s answered with something this "+
			"integration could not read: %w", path, err)
	}
	return nil
}

// CollectPost walks a paginated diagnostics grid.
//
// The same three ceilings as Collect and the same reporting; the difference is
// only that the filter travels in a body and the paging still travels in the
// query string, which is the API's own arrangement rather than a choice here.
func (c *Client) CollectPost(ctx context.Context, path string, params url.Values,
	body any, limit, endpointMax, budget int) (walk, error) {

	if params == nil {
		params = url.Values{}
	}
	size := pageSize(limit, endpointMax)

	var out walk
	spent := 0
	for page := 1; ; page++ {
		q := cloneValues(params)
		q.Set("page", strconv.Itoa(page))
		q.Set("limit", strconv.Itoa(size))

		var got paged
		if err := c.Post(ctx, path, q, body, &got); err != nil {
			return walk{}, err
		}
		if got.TotalCount > out.Total {
			out.Total = got.TotalCount
		}
		done, err := out.take(got.Data, limit, budget, &spent)
		if err != nil {
			return walk{}, err
		}
		if done || len(got.Data) == 0 || len(got.Data) < size ||
			(got.TotalPages > 0 && page >= got.TotalPages) {
			return out, nil
		}
	}
}

// GetInto reads an endpoint and decodes it.
func (c *Client) GetInto(ctx context.Context, path string, params url.Values, out any) error {
	raw, err := c.Get(ctx, path, params)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("extremecloudiq: %s answered with something this "+
			"integration could not read: %w", path, err)
	}
	return nil
}

// walk is the result of collecting a paginated endpoint.
type walk struct {
	// Rows are the records collected, in the order the API returned them.
	Rows []Record
	// Total is what the API said the whole collection holds, which is the
	// number a caller needs to know whether they are looking at all of it.
	Total int
	// Truncated says the walk stopped short of Total, and Reason says which
	// ceiling stopped it.
	Truncated bool
	Reason    string
}

// Collect walks a paginated endpoint until the caller's limit, the operator's
// ceiling, or the size of one answer stops it.
//
// The three ceilings are separate on purpose and the result says which one
// bit. "Here are 200 of 4,317 devices" is an answer somebody can narrow; 200
// devices with nothing said about the rest is a wrong answer to "how many
// access points do we have", and a model has no way to tell the two apart.
//
// endpointMax is what that particular endpoint permits per page -- a hundred
// almost everywhere, five hundred for audit logs, two thousand for network
// policies -- because asking for more than the endpoint allows is a 400 rather
// than a page.
func (c *Client) Collect(ctx context.Context, path string, params url.Values,
	limit, endpointMax, budget int) (walk, error) {

	if params == nil {
		params = url.Values{}
	}
	size := pageSize(limit, endpointMax)

	var out walk
	spent := 0
	for page := 1; ; page++ {
		q := cloneValues(params)
		q.Set("page", strconv.Itoa(page))
		q.Set("limit", strconv.Itoa(size))

		var body paged
		if err := c.GetInto(ctx, path, q, &body); err != nil {
			return walk{}, err
		}
		if body.TotalCount > out.Total {
			out.Total = body.TotalCount
		}

		done, err := out.take(body.Data, limit, budget, &spent)
		if err != nil {
			return walk{}, err
		}
		// Three ways a walk ends, and the API is not consistent about which it
		// reports: total_pages is absent on some collections and a short page
		// is the only signal on others.
		if done || len(body.Data) == 0 || len(body.Data) < size ||
			(body.TotalPages > 0 && page >= body.TotalPages) {
			return out, nil
		}
	}
}

// take appends a page, stopping at the row ceiling or the size ceiling and
// recording which one bit. It reports whether the walk is finished.
//
// Shared by both walkers rather than written twice. The two differ only in how
// the filter reaches the API, and a truncation rule that drifted between them
// would mean the wired grid quietly reporting a partial answer as a whole one
// while the wireless grid said so.
func (w *walk) take(rows []Record, limit, budget int, spent *int) (bool, error) {
	for _, row := range rows {
		if limit > 0 && len(w.Rows) >= limit {
			w.Truncated = true
			// Not every collection reports a total. Saying "of 0" would be
			// worse than not saying it, so the count is only quoted when the
			// API gave one.
			if w.Total > 0 {
				w.Reason = fmt.Sprintf("stopped at %d rows of %d; narrow the "+
					"question rather than raising the limit", limit, w.Total)
			} else {
				w.Reason = fmt.Sprintf("stopped at %d rows, and there are more; "+
					"narrow the question rather than raising the limit", limit)
			}
			return true, nil
		}
		cost := rowBytes(row)
		// The first row is never refused for size. An answer of nothing at
		// all, because the one matching record was large, is worse than an
		// answer of one large record.
		if budget > 0 && *spent+cost > budget && len(w.Rows) > 0 {
			w.Truncated = true
			w.Reason = "stopped early because the rows were large; the answer " +
				"would not have fit in a conversation. Ask for a narrower view, " +
				"or fewer fields"
			return true, nil
		}
		*spent += cost
		w.Rows = append(w.Rows, row)
	}
	return false, nil
}

// do performs one request and returns the body.
func (c *Client) do(ctx context.Context, method, path string, params url.Values,
	body []byte) (json.RawMessage, error) {

	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("extremecloudiq: waiting to call %s: %w", path, err)
	}

	target := c.root + path
	if encoded := params.Encode(); encoded != "" {
		target += "?" + encoded
	}

	started := c.now()
	raw, status, err := c.send(ctx, method, target, body)
	elapsed := c.now().Sub(started)

	if err != nil {
		c.observe("error", elapsed)
		return nil, err
	}
	if status != http.StatusOK {
		c.observe("error", elapsed)
		return nil, explainRequestFailure(status, path, raw)
	}

	c.observe("ok", elapsed)
	// The upstream half of a tool call. Off by default and the first thing to
	// turn on when an assistant reports something that does not match what
	// somebody sees in ExtremeCloud IQ: it says what was asked and how much
	// came back.
	//
	// Never the body, and never the query. A successful body here is somebody's
	// estate -- hostnames, MAC addresses, the names of the people connected to
	// it -- which is the one thing this integration must not spill into a log
	// file that leaves the machine.
	c.log.DebugContext(ctx, "extremecloudiq API call",
		"method", method, "path", path, "status", status,
		"bytes", len(raw), "took", elapsed)
	return raw, nil
}

// send performs the HTTP round trip and reads a bounded body.
func (c *Client) send(ctx context.Context, method, target string, body []byte) (json.RawMessage, int, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("extremecloudiq: building a request for %s: %w",
			redactURL(target), err)
	}
	if c.token != "" {
		// A plain bearer token. Unlike Graylog -- where an access token is
		// presented as a basic-auth username -- this one is exactly what it
		// looks like.
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("extremecloudiq: could not reach %s: %w",
			redactURL(target), err)
	}
	defer resp.Body.Close()

	limit := int64(maxErrorBody)
	if resp.StatusCode == http.StatusOK {
		limit = maxResponseBody
	}
	read, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("extremecloudiq: reading the "+
			"response from %s: %w", redactURL(target), err)
	}
	return read, resp.StatusCode, nil
}

// tokenInfo is what GET /auth/apitoken/info reports about the credential.
//
// Only the fields this plugin does something with.
type tokenInfo struct {
	UserName       string   `json:"user_name"`
	Role           string   `json:"role"`
	OwnerID        int64    `json:"owner_id"`
	DataCenter     string   `json:"data_center"`
	Scopes         []string `json:"scopes"`
	ExpirationTime string   `json:"expiration_time"`
	ExpiresIn      int64    `json:"expires_in"`
}

// Probe makes the cheapest authenticated call there is.
//
// GET /auth/apitoken/info describes the token itself, so it separates the four
// things a wrong configuration could be -- the address does not resolve, TLS
// fails, the token is refused, or something that is not the API answered --
// from the fifth, which is a token whose scopes do not cover a particular
// read. Doing it at startup makes a wrong token a message on the dashboard
// rather than a confusing failure inside the first tool call an assistant
// makes.
//
// It reads nothing about anybody's estate, which is the other reason it is the
// right probe: a startup check should not need permission to see devices.
func (c *Client) Probe(ctx context.Context) (tokenInfo, error) {
	raw, err := c.do(ctx, http.MethodGet, "/auth/apitoken/info", nil, nil)
	if err != nil {
		return tokenInfo{}, err
	}
	var info tokenInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return tokenInfo{}, fmt.Errorf("extremecloudiq: %s answered "+
			"/auth/apitoken/info with something that is not the API's JSON -- "+
			"the address may be reaching a proxy or a different application: %s",
			redactURL(c.root), summarise(http.StatusOK, raw))
	}
	// A body that decoded but names neither the account nor the token's owner
	// is a JSON API that is not this one. Reported here rather than left to
	// fail later against an endpoint that does not exist.
	if info.UserName == "" && info.OwnerID == 0 {
		return tokenInfo{}, fmt.Errorf("extremecloudiq: %s answered "+
			"/auth/apitoken/info with JSON that names neither a user nor an "+
			"owner, so it is probably not ExtremeCloud IQ", redactURL(c.root))
	}
	return info, nil
}

// Expiry reports when the token stops working, and whether that is knowable.
//
// The API gives both a timestamp and a countdown; the countdown is the one
// that is always populated, and the timestamp is the one worth showing.
func (t tokenInfo) Expiry(now time.Time) (time.Time, bool) {
	if parsed, err := time.Parse(time.RFC3339, t.ExpirationTime); err == nil {
		return parsed, true
	}
	if t.ExpiresIn > 0 {
		return now.Add(time.Duration(t.ExpiresIn) * time.Second), true
	}
	return time.Time{}, false
}

// Root reports the configured address, for the health message.
func (c *Client) Root() string { return c.root }

// Describe says where this instance reads from and what its read-only
// guarantee rests on, for the startup log and the health report.
func (c *Client) Describe() string {
	return "the API at " + redactURL(c.root) +
		", restricted to a named list of read endpoints by its transport"
}

// basePath is the path component of a configured address. Empty for the
// ordinary case -- the API lives at the root of its host -- and non-empty only
// for an installation reached through a gateway that prefixes a path.
func basePath(root string) string {
	u, err := url.Parse(root)
	if err != nil {
		return ""
	}
	return u.Path
}

// cloneValues copies query parameters so a page number can be set without
// mutating the caller's map, which is reused across pages.
func cloneValues(in url.Values) url.Values {
	out := make(url.Values, len(in)+2)
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// rowBytes is roughly what one record costs in a result.
//
// Encoded, because that is the only honest measure of a row whose shape varies
// with the view a caller asked for; counting fields would call a device in
// STATUS view the same size as one in FULL.
func rowBytes(r Record) int {
	b, err := json.Marshal(r)
	if err != nil {
		return 0
	}
	return len(b)
}
