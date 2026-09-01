package textable

import (
	"context"
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

// maxResponseBody bounds a successful body.
//
// Higher than it looks like it needs to be, for one endpoint: /api/contacts
// does not paginate, so a large tenant's whole contact list arrives in a single
// response. This is the backstop against that being unbounded, not the thing
// that keeps answers sane -- the tools do that, by bounding what they build
// from it.
const maxResponseBody = 32 << 20

// Client talks to one Textable instance as one account.
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

// authorizer applies the configured credential.
//
// An interface of one method rather than a field and a branch per request: the
// choice is made once, at construction, and every call site downstream is free
// of it.
type authorizer interface {
	apply(req *http.Request)
}

// tokenAuth is a Textable service account token, sent as an ordinary bearer
// credential.
//
// Not the accountUid:apiKey pair a *user* token takes. That scheme carries the
// account inside the credential because a user token authenticates as somebody;
// a service account authenticates as itself and says what it may do in its
// scopes instead. Config.Validate refuses a value shaped like the other kind,
// because sending one here gets a 401 indistinguishable from a revoked token.
type tokenAuth struct{ token string }

func (a tokenAuth) apply(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.token)
}

// noAuth is what an unconfigured plugin gets, so that a call made before a
// token is supplied fails at Textable with a 401 rather than panicking on a nil
// interface here.
type noAuth struct{}

func (noAuth) apply(*http.Request) {}

// NewClient builds a client. The credential is passed separately from the
// config so that the Config the plugin retains can be free of it.
func NewClient(hc *http.Client, cfg Config, key string,
	log *slog.Logger, now func() time.Time, cache *readCache,
	observe func(string, time.Duration)) *Client {

	var auth authorizer = noAuth{}
	if key != "" {
		auth = tokenAuth{token: strings.TrimSpace(key)}
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
	if c.cache == nil {
		return c.do(ctx, http.MethodGet, path, params)
	}
	got, err := c.cache.reuse(ctx, http.MethodGet, path, params, func(ctx context.Context) (any, error) {
		return c.do(ctx, http.MethodGet, path, params)
	})
	if err != nil {
		return nil, err
	}
	raw, ok := got.(json.RawMessage)
	if !ok {
		// Cannot happen: this is the only thing put in. Reported rather than
		// asserted because a panic inside a tool call takes the request down.
		return nil, fmt.Errorf("textable: cached value for %s was not a response", path)
	}
	return raw, nil
}

// do performs one request and returns the body.
func (c *Client) do(ctx context.Context, method, path string, params url.Values) (json.RawMessage, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("textable: waiting to call %s: %w", path, err)
	}

	target := c.root + path
	if encoded := params.Encode(); encoded != "" {
		target += "?" + encoded
	}

	started := c.now()
	raw, status, err := c.send(ctx, method, target)
	elapsed := c.now().Sub(started)

	if err != nil {
		c.observe("error", elapsed)
		return nil, err
	}
	// 200 and 201 are both successes here; the rest is a failure whatever the
	// body says. A 204 carries no body and no read endpoint returns one, so it
	// is not special-cased -- if one ever does, it arrives as an empty body and
	// fails to decode, which is louder than being silently treated as success.
	if status != http.StatusOK && status != http.StatusCreated {
		c.observe("error", elapsed)
		return nil, explainRequestFailure(status, path, raw)
	}

	c.observe("ok", elapsed)
	// The upstream half of a tool call. Off by default and the first thing to
	// turn on when an assistant reports something that does not match what
	// somebody sees in Textable: it says what was asked and how much came back.
	//
	// Never the body, and never the query. A successful body here is somebody's
	// contact list -- names, phone numbers, the text of their last message --
	// which is the one thing this integration must not spill into a log file
	// that leaves the machine.
	c.log.DebugContext(ctx, "textable API call",
		"method", method, "path", path, "status", status,
		"bytes", len(raw), "took", elapsed)
	return raw, nil
}

// send performs the HTTP round trip and reads a bounded body.
func (c *Client) send(ctx context.Context, method, target string) (json.RawMessage, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("textable: building a request for %s: %w",
			redactURL(target), err)
	}
	c.auth.apply(req)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("textable: could not reach %s: %w",
			redactURL(target), err)
	}
	defer resp.Body.Close()

	limit := int64(maxErrorBody)
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		limit = maxResponseBody
	}
	read, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("textable: reading the response "+
			"from %s: %w", redactURL(target), err)
	}
	return read, resp.StatusCode, nil
}

// healthReport is what GET /health says about the instance answering.
//
// Only the fields this plugin does something with. The endpoint returns a
// component-by-component breakdown -- heap, latency percentiles, CPU profiling
// -- and decoding the ones nobody reads would be a list to keep in step with an
// API that adds to it.
type healthReport struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	ReleaseID string `json:"releaseId"`
}

// ok reports whether the instance called itself well. The endpoint's status
// vocabulary spans two spellings of the same idea, which is why this is a
// helper rather than a comparison at the call site.
func (h healthReport) ok() bool {
	switch strings.ToLower(h.Status) {
	case "pass", "ok", "up":
		return true
	}
	return false
}

// Probe checks the instance is reachable and is Textable.
//
// Unauthenticated on purpose. It is the first half of a two-part startup check,
// and separating it from the credential is the whole point: a wrong address, a
// TLS failure and a gateway answering instead of the API all fail here, and a
// wrong key fails in ProbeAuth. Told apart at startup, those are two different
// sentences on the dashboard; run together they are one confusing one.
func (c *Client) Probe(ctx context.Context) (healthReport, error) {
	raw, err := c.do(ctx, http.MethodGet, "/health", nil)
	if err != nil {
		return healthReport{}, err
	}
	var h healthReport
	if err := json.Unmarshal(raw, &h); err != nil {
		return healthReport{}, fmt.Errorf("textable: %s answered /health with "+
			"something that is not the API's JSON -- the address may be "+
			"reaching a proxy or a different application: %s",
			redactURL(c.root), summarise(http.StatusOK, raw))
	}
	// A body that decoded but names no status is a JSON API that is not this
	// one. Reported here rather than left to fail later against an endpoint
	// that does not exist.
	if h.Status == "" {
		return healthReport{}, fmt.Errorf("textable: %s answered /health with "+
			"JSON that names no status, so it is probably not Textable",
			redactURL(c.root))
	}
	return h, nil
}

// ProbeAuth checks the token is accepted and can see something.
//
// GET /api/v2/tenants is the right probe on all three counts a probe is judged
// on. It is cheap -- a few hundred bytes, one row per tenant. It proves the
// credential is accepted and that read-all-tenants was granted, which is the
// scope every other tool depends on. And it is the first call the directory
// makes anyway, so a working instance has already answered it once by the time
// anybody asks a question.
//
// It deliberately does not probe GET /api/v2/users/{id}. That endpoint is
// documented as accepting a service account and does not -- it answers 401 to a
// valid token with every read scope -- so probing with it would report every
// healthy installation as having a rejected credential.
func (c *Client) ProbeAuth(ctx context.Context) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("textable: waiting to probe: %w", err)
	}
	raw, status, err := c.send(ctx, http.MethodGet, c.root+"/api/v2/tenants")
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusForbidden:
		// Accepted, but not granted what this integration needs. Named
		// separately because the fix is a scope on the service account rather
		// than a new token.
		return fmt.Errorf("textable: the service account token was accepted but "+
			"is not authorized to list tenants: %s. Grant it the read scopes -- "+
			"read-all-tenants, read-all-users, read-all-organizations and "+
			"read-contacts", summarise(status, raw))
	}
	return explainRequestFailure(status, "/api/v2/tenants", raw)
}

// Describe says where this instance reads from and what its read-only guarantee
// rests on, for the startup log and the health report.
func (c *Client) Describe() string {
	return "the API at " + redactURL(c.root) +
		", restricted to a named list of read endpoints by its transport"
}

// basePath is the path component of a configured address, which is what an
// instance behind a reverse proxy is reached under and what the transport guard
// has to strip before matching. An address that will not parse yields no
// prefix, which makes the guard match against the whole path -- the right way
// to be wrong about a configuration Validate has already turned down.
func basePath(root string) string {
	u, err := url.Parse(root)
	if err != nil {
		return ""
	}
	return u.Path
}
