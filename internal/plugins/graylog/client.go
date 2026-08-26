package graylog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// maxResponseBody bounds a successful body. A search is already bounded by the
// size this plugin asks for, so this is a backstop against an endpoint that
// ignores a limit rather than the thing that keeps answers sane.
const maxResponseBody = 64 << 20

// Client talks to one Graylog installation.
type Client struct {
	http    *http.Client
	cfg     Config
	root    string
	auth    authorizer
	log     *slog.Logger
	now     func() time.Time
	limiter *rate.Limiter
	cache   *readCache
	observe func(outcome string, d time.Duration)
}

// authorizer applies whichever credential was configured.
//
// An interface of one method rather than two fields and a branch per request:
// the choice is made once, at construction, and every call site downstream is
// free of it.
type authorizer interface {
	apply(req *http.Request)
}

// tokenAuth is a Graylog access token.
//
// The token goes in the *username* field with the literal string "token" as
// the password. It looks like a bearer token and is not one; sending it as
// `Authorization: Bearer …` gets a 401 that says nothing about why.
type tokenAuth struct{ token string }

func (a tokenAuth) apply(req *http.Request) { req.SetBasicAuth(a.token, "token") }

type basicAuth struct{ user, pass string }

func (a basicAuth) apply(req *http.Request) { req.SetBasicAuth(a.user, a.pass) }

// noAuth is what an unconfigured plugin gets, so that a call made before
// credentials are supplied fails at Graylog with a 401 rather than panicking
// on a nil interface here.
type noAuth struct{}

func (noAuth) apply(*http.Request) {}

// NewClient builds a client. The credential is passed separately from the
// config so that the Config the plugin retains can be free of it.
func NewClient(hc *http.Client, cfg Config, token, user, pass string,
	log *slog.Logger, now func() time.Time, cache *readCache,
	observe func(string, time.Duration)) *Client {

	var auth authorizer = noAuth{}
	switch {
	case token != "":
		auth = tokenAuth{token: token}
	case user != "" && pass != "":
		auth = basicAuth{user: user, pass: pass}
	}

	return &Client{
		http:    readOnly(hc, basePath(cfg.root())),
		cfg:     cfg,
		root:    cfg.root(),
		auth:    auth,
		log:     log,
		now:     now,
		limiter: rate.NewLimiter(rate.Limit(cfg.RequestsPerSecond), 1),
		cache:   cache,
		observe: observe,
	}
}

// Get reads an endpoint, through the cache where that endpoint is cacheable.
func (c *Client) Get(ctx context.Context, path string, params url.Values) (json.RawMessage, error) {
	return c.cached(ctx, http.MethodGet, path, params, nil)
}

// Post sends a request body, through the cache where that endpoint is
// cacheable. The three endpoints this integration exists for -- searches,
// aggregations and events -- are POSTs and are never cacheable, so in practice
// this is only the stream-scoped field listing.
func (c *Client) Post(ctx context.Context, path string, body any) (json.RawMessage, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("graylog: building the request for %s: %w", path, err)
	}
	return c.cached(ctx, http.MethodPost, path, nil, encoded)
}

// cached serves a request from memory where the endpoint permits it.
func (c *Client) cached(ctx context.Context, method, path string,
	params url.Values, body []byte) (json.RawMessage, error) {

	if c.cache == nil {
		return c.do(ctx, method, path, params, body)
	}
	got, err := c.cache.reuse(ctx, method, path, params, body, func(ctx context.Context) (any, error) {
		return c.do(ctx, method, path, params, body)
	})
	if err != nil {
		return nil, err
	}
	raw, ok := got.(json.RawMessage)
	if !ok {
		// Cannot happen: this is the only thing put in. Reported rather than
		// asserted because a panic inside a tool call takes the request down.
		return nil, fmt.Errorf("graylog: cached value for %s was not a response", path)
	}
	return raw, nil
}

// do performs one request and returns the body.
func (c *Client) do(ctx context.Context, method, path string,
	params url.Values, body []byte) (json.RawMessage, error) {

	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("graylog: waiting to call %s: %w", path, err)
	}

	target := c.root + apiPrefix + path
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
	// somebody sees in Graylog: it says what was asked and how much came back.
	//
	// Never the body, and never the query. A successful body here is
	// somebody's log data and the query names their fields and hostnames --
	// which is the one thing this integration must not spill into a log file
	// that leaves the machine.
	c.log.DebugContext(ctx, "graylog API call",
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
		return nil, 0, fmt.Errorf("graylog: building a request for %s: %w",
			redactURL(target), err)
	}
	c.auth.apply(req)
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	// Graylog's cross-site guard. A browser cannot set a custom header
	// cross-origin, so requiring one is how the API tells a deliberate call
	// from a forged one. Absent, every POST here would be refused with a 400
	// that reads like a malformed request.
	//
	// Set on GETs too. They do not need it, and a header that is only
	// sometimes present is one somebody eventually forgets to add.
	req.Header.Set("X-Requested-By", "mcpd")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("graylog: could not reach %s: %w",
			redactURL(target), err)
	}
	defer resp.Body.Close()

	limit := int64(maxErrorBody)
	if resp.StatusCode == http.StatusOK {
		limit = maxResponseBody
	}
	read, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("graylog: reading the response "+
			"from %s: %w", redactURL(target), err)
	}
	return read, resp.StatusCode, nil
}

// serverInfo is what GET /api/system reports about the node answering.
//
// Only the fields this plugin does something with. Graylog returns a dozen
// more, and decoding the ones nobody reads would be a list to keep in step
// with an API that adds to it.
type serverInfo struct {
	Version      string `json:"version"`
	Hostname     string `json:"hostname"`
	NodeID       string `json:"node_id"`
	ClusterID    string `json:"cluster_id"`
	Lifecycle    string `json:"lifecycle"`
	LBStatus     string `json:"lb_status"`
	Timezone     string `json:"timezone"`
	IsProcessing bool   `json:"is_processing"`
}

// Probe makes the cheapest authenticated call there is.
//
// GET /api/system needs no permission beyond being signed in, so it separates
// the four things a wrong configuration could be -- the address does not
// resolve, TLS fails, the credential is refused, or something that is not
// Graylog answered -- from the fifth, which is an account without the
// permission a particular tool needs. Doing it at startup makes a wrong token
// a message on the dashboard rather than a confusing failure inside the first
// tool call an assistant makes.
//
// It also returns the version, which is the single most useful thing to have
// in the startup log when an endpoint later answers 404.
func (c *Client) Probe(ctx context.Context) (serverInfo, error) {
	raw, err := c.do(ctx, http.MethodGet, "/system", nil, nil)
	if err != nil {
		return serverInfo{}, err
	}
	var info serverInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return serverInfo{}, fmt.Errorf("graylog: %s answered /api/system with "+
			"something that is not the API's JSON -- the address may be "+
			"reaching a proxy or a different application: %s",
			redactURL(c.root), summarise(http.StatusOK, raw))
	}
	// A body that decoded but carries none of the fields every Graylog sends
	// is a JSON API that is not this one. Reported here rather than left to
	// fail later against an endpoint that does not exist.
	if info.Version == "" && info.NodeID == "" {
		return serverInfo{}, fmt.Errorf("graylog: %s answered /api/system with "+
			"JSON that names neither a version nor a node id, so it is "+
			"probably not Graylog", redactURL(c.root))
	}
	return info, nil
}

// Root reports the configured web root, for the health message.
func (c *Client) Root() string { return c.root }

// Describe says where this instance reads from and what its read-only
// guarantee rests on, for the startup log and the health report.
func (c *Client) Describe() string {
	return "the API at " + redactURL(c.root) +
		", restricted to a named list of read endpoints by its transport"
}

// basePath is the path component of a configured address, which is what a
// Graylog behind a reverse proxy is reached under and what the transport guard
// has to strip before matching. An address that will not parse yields no
// prefix, which makes the guard refuse everything -- the right way to be wrong
// about a configuration Validate has already turned down.
func basePath(root string) string {
	u, err := url.Parse(root)
	if err != nil {
		return ""
	}
	return u.Path
}

// requestDigest identifies one request for the cache.
//
// The body is hashed rather than kept: it is small today -- a list of stream
// ids -- and a key that grows with a request body is one that stops being a
// key. url.Values.Encode sorts by name, so two callers who set the same
// filters in a different order produce the same digest and two who set
// different ones never do.
func requestDigest(method, path string, params url.Values, body []byte) string {
	var b strings.Builder
	b.WriteString(method)
	b.WriteByte(0)
	b.WriteString(path)
	b.WriteByte(0)
	b.WriteString(params.Encode())
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		b.WriteByte(0)
		b.WriteString(base64.RawStdEncoding.EncodeToString(sum[:]))
	}
	return b.String()
}
