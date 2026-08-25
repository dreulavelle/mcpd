package observium

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

// Client talks to one Observium installation.
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
// the choice between a token and a password is made once, at construction,
// and every call site downstream is free of it.
type authorizer interface {
	apply(req *http.Request)
}

type bearerAuth struct{ token string }

func (a bearerAuth) apply(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+a.token)
}

type basicAuth struct{ user, pass string }

func (a basicAuth) apply(req *http.Request) {
	req.SetBasicAuth(a.user, a.pass)
}

// noAuth is what an unconfigured plugin gets, so that a call made before
// credentials are supplied fails at Observium with a 401 rather than panicking
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
		auth = bearerAuth{token: token}
	case user != "" && pass != "":
		auth = basicAuth{user: user, pass: pass}
	}

	return &Client{
		http:    readOnly(hc),
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

// envelope is Observium's response wrapper.
//
// The entity collection is not a field here because its *name* varies with the
// endpoint -- "devices", "ports", "sensors" -- so it is captured as the
// remaining raw JSON and resolved by name at the call site.
type envelope struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	Count     int    `json:"count"`
	PageSize  int    `json:"pagesize"`
	PageNo    int    `json:"pageno"`
	CountPage int    `json:"countpage"`
}

// Page is one tool call's worth of entities, plus what the caller needs to
// know about what they are not seeing.
type Page struct {
	// Items are the entities, ordered by their numeric id.
	Items []map[string]any
	// Total is what Observium said the unfiltered-by-page count was, which is
	// how a caller learns their filter matched more than they received.
	Total int
	// Truncated reports that MaxItems stopped the walk. It is surfaced in
	// every tool result rather than logged, because a model shown 500 of 4000
	// ports and not told so will answer as though it saw the estate.
	Truncated bool
}

// Get fetches a single entity or a collection under one key.
//
// The key is the envelope field the entities live under, which Observium names
// after the endpoint rather than uniformly -- /neighbours/ answers under
// "neighbours", /alert_log/ under "alert_log". Passing it in is less clever
// than deriving it from the path and survives the API not being consistent.
func (c *Client) Get(ctx context.Context, path, key string, params url.Values) (Page, error) {
	if c.cache == nil {
		return c.walk(ctx, path, key, params)
	}
	got, err := c.cache.reuse(ctx, path, params, func(ctx context.Context) (any, error) {
		return c.walk(ctx, path, key, params)
	})
	if err != nil {
		return Page{}, err
	}
	page, ok := got.(Page)
	if !ok {
		// Cannot happen: this is the only thing put in. Reported rather than
		// asserted because a panic inside a tool call takes the request down.
		return Page{}, fmt.Errorf("observium: cached value for %s was not a page", path)
	}
	return page, nil
}

// walk pages through a collection until it is exhausted or MaxItems is hit.
//
// Observium paginates only when asked: without a pagesize it returns the whole
// table in one response, which on a large estate is a slow query and a large
// body. Asking for pages is therefore not only about bounding what a tool
// returns, it is about not making the upstream build the entire answer first.
func (c *Client) walk(ctx context.Context, path, key string, params url.Values) (Page, error) {
	var out Page

	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	q.Set("pagesize", strconv.Itoa(c.cfg.PageSize))

	for pageNo := 1; ; pageNo++ {
		q.Set("pageno", strconv.Itoa(pageNo))

		env, raw, err := c.do(ctx, path, q)
		if err != nil {
			return Page{}, err
		}
		if env.Count > out.Total {
			out.Total = env.Count
		}

		items, err := decodeCollection(raw, key)
		if err != nil {
			return Page{}, fmt.Errorf("observium: %s: %w", path, err)
		}
		for _, item := range items {
			if len(out.Items) >= c.cfg.MaxItems {
				out.Truncated = true
				return out, nil
			}
			out.Items = append(out.Items, item)
		}

		// Stop on the first short page. Observium reports countpage when it
		// paginates, but not every endpoint sets it, and a short page is the
		// signal that holds either way.
		if len(items) < c.cfg.PageSize {
			return out, nil
		}
		if env.CountPage > 0 && pageNo >= env.CountPage {
			return out, nil
		}
		// A page that is full but reports no total would loop forever if the
		// endpoint ignores pageno. Bounded by MaxItems above, but an endpoint
		// returning the same full page each time would spin to that cap
		// making identical requests, so stop when nothing new can arrive.
		if env.Count > 0 && len(out.Items) >= env.Count {
			return out, nil
		}
	}
}

// decodeCollection turns Observium's keyed object into an ordered slice.
//
// This is the shape of the API most likely to surprise: a collection is a JSON
// *object* keyed by the entity's id, not an array.
//
//	{"status":"ok","count":2,"devices":{"277":{...},"278":{...}}}
//
// Go decodes that into a map, and a map has no order, so the same call would
// return devices in a different sequence each time and a model comparing two
// answers would see changes that did not happen. Sorting by the numeric key
// restores the order Observium's own UI shows.
//
// A single-entity endpoint answers under the same key with the same shape, so
// nothing special is needed for it.
func decodeCollection(raw json.RawMessage, key string) ([]map[string]any, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("response was not a JSON object: %w", err)
	}

	entities, ok := body[key]
	if !ok {
		// A successful response with no collection means an empty result on
		// some endpoints and a wrong key on others. Distinguishing them would
		// need a list of which is which; reporting empty is right for the
		// common case and the key is a constant at every call site, so a
		// wrong one is a bug caught the first time the tool is run.
		return nil, nil
	}

	// The empty case arrives as an array rather than an object on some
	// endpoints -- PHP encodes an empty associative array as []. Handling it
	// here rather than letting the object decode fail is the difference
	// between "no sensors" and an error.
	var empty []json.RawMessage
	if err := json.Unmarshal(entities, &empty); err == nil {
		out := make([]map[string]any, 0, len(empty))
		for _, item := range empty {
			var m map[string]any
			if err := json.Unmarshal(item, &m); err != nil {
				return nil, fmt.Errorf("entity in %s was not an object: %w", key, err)
			}
			out = append(out, m)
		}
		return out, nil
	}

	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(entities, &keyed); err != nil {
		return nil, fmt.Errorf("%s was neither an array nor an object: %w", key, err)
	}

	ids := make([]string, 0, len(keyed))
	for id := range keyed {
		ids = append(ids, id)
	}
	sortIDs(ids)

	out := make([]map[string]any, 0, len(keyed))
	for _, id := range ids {
		var m map[string]any
		if err := json.Unmarshal(keyed[id], &m); err != nil {
			return nil, fmt.Errorf("entity %s in %s was not an object: %w", id, key, err)
		}
		out = append(out, m)
	}
	return out, nil
}

// sortIDs orders entity ids numerically where they are numbers and
// lexically where they are not.
//
// Observium keys by database id, so they are numbers in practice -- but string
// ordering would put "10" before "9", and a device list that jumps around is
// one a person cannot scan.
func sortIDs(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		a, errA := strconv.Atoi(ids[i])
		b, errB := strconv.Atoi(ids[j])
		if errA == nil && errB == nil {
			return a < b
		}
		return ids[i] < ids[j]
	})
}

// do performs one request and returns the envelope alongside the raw body, so
// the caller can pull its own collection key out without a second decode.
func (c *Client) do(ctx context.Context, path string, params url.Values) (envelope, json.RawMessage, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return envelope{}, nil, fmt.Errorf("observium: waiting to call %s: %w", path, err)
	}

	target := c.root + apiPrefix + path
	if encoded := params.Encode(); encoded != "" {
		target += "?" + encoded
	}

	started := c.now()
	body, status, err := c.send(ctx, target)
	elapsed := c.now().Sub(started)

	if err != nil {
		c.observe("error", elapsed)
		return envelope{}, nil, err
	}
	if status != http.StatusOK {
		c.observe("error", elapsed)
		return envelope{}, nil, explainRequestFailure(status, path, body)
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		c.observe("error", elapsed)
		return envelope{}, nil, fmt.Errorf("observium: %s answered with something "+
			"that is not the API's JSON envelope: %s", path, summarise(status, body))
	}
	// A 200 that says it failed. Checked because treating it as success hands
	// the model an empty collection, which reads as "there are none" rather
	// than "the question was refused".
	if env.Status != "" && !strings.EqualFold(env.Status, "ok") {
		c.observe("error", elapsed)
		return envelope{}, nil, explainEnvelopeFailure(path, env.Message)
	}

	c.observe("ok", elapsed)
	// The upstream half of a tool call. Off by default and the first thing to
	// turn on when an assistant reports an answer that does not match what
	// somebody sees in Observium: it says what was asked and how much came
	// back, without the body, which is where the estate is.
	c.log.DebugContext(ctx, "observium API call",
		"path", path, "status", status, "count", env.Count,
		"page", env.PageNo, "took", elapsed)
	return env, body, nil
}

// send performs the HTTP round trip and reads a bounded body.
func (c *Client) send(ctx context.Context, target string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("observium: building a request for %s: %w",
			redactURL(target), err)
	}
	c.auth.apply(req)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("observium: could not reach %s: %w",
			redactURL(target), err)
	}
	defer resp.Body.Close()

	limit := int64(maxErrorBody)
	if resp.StatusCode == http.StatusOK {
		// A successful body is the answer, so it is not bounded by the error
		// budget. It is bounded by pagesize, which is what keeps it sane.
		limit = 64 << 20
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("observium: reading the response "+
			"from %s: %w", redactURL(target), err)
	}
	return body, resp.StatusCode, nil
}

// Root reports the configured web root, for the health message and for
// building graph URLs.
func (c *Client) Root() string { return c.root }

// Entity is one kind of thing Observium records.
//
// A name rather than a path, because the API does not name its endpoints and
// its response keys the same way -- /address answers under "addresses",
// /alert_log under "alert_log". One constant per thing, resolved through
// apiPaths, keeps that irregularity in one table instead of at every call
// site.
type Entity string

const (
	EntityDevices    Entity = "devices"
	EntityPorts      Entity = "ports"
	EntitySensors    Entity = "sensors"
	EntityAlerts     Entity = "alerts"
	EntityAlertLog   Entity = "alert_log"
	EntityStorage    Entity = "storage"
	EntityMempools   Entity = "mempools"
	EntityProcessors Entity = "processors"
	EntityInventory  Entity = "inventory"
	EntityNeighbours Entity = "neighbours"
	EntityAddresses  Entity = "addresses"
	EntityVLANs      Entity = "vlans"
)

// Filter names are the API's own, named here so a typo is a compile error
// rather than a filter that silently matches everything.
const (
	FilterDeviceID = "device_id"
	FilterHostname = "hostname"
	FilterStatus   = "status"
	FilterOS       = "os"
	FilterLocation = "location"
	FilterHardware = "hardware"
	FilterVendor   = "vendor"
	FilterGroup    = "group"
	FilterState    = "state"
	FilterErrors   = "errors"
	FilterAlerted  = "alerted"
	FilterIfAlias  = "ifAlias"
	FilterMetric   = "metric"
	FilterEvent    = "event"
	FilterMessage  = "message"
	FilterFrom     = "timestamp_from"
	FilterTo       = "timestamp_to"
	FilterModel    = "entPhysicalModelName"
	FilterSerial   = "entPhysicalSerialNum"
	// FilterID selects one entity by its own primary key. The API expresses
	// this as a path segment rather than a query parameter, which is why it is
	// named here rather than being one of the filters above.
	FilterID = "__id"
)

// apiPaths maps an entity onto the endpoint that serves it and the envelope
// key its rows arrive under.
//
// Observium names the key after the endpoint rather than uniformly, so both
// halves are recorded rather than one being derived from the other.
var apiPaths = map[Entity]struct{ path, key string }{
	EntityDevices:    {"/devices", "devices"},
	EntityPorts:      {"/ports", "ports"},
	EntitySensors:    {"/sensors", "sensors"},
	EntityAlerts:     {"/alerts", "alerts"},
	EntityAlertLog:   {"/alert_log", "alert_log"},
	EntityStorage:    {"/storage", "storage"},
	EntityMempools:   {"/mempools", "mempools"},
	EntityProcessors: {"/processors", "processors"},
	EntityInventory:  {"/inventory", "inventory"},
	EntityNeighbours: {"/neighbours", "neighbours"},
	EntityAddresses:  {"/address", "addresses"},
	EntityVLANs:      {"/vlans", "vlans"},
}

// Read fetches one entity collection.
func (c *Client) Read(ctx context.Context, entity Entity, filters url.Values, limit int) (Page, error) {
	route, ok := apiPaths[entity]
	if !ok {
		return Page{}, fmt.Errorf("observium: no API endpoint for %s", entity)
	}

	path := route.path
	params := url.Values{}
	for name, values := range filters {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		// The API selects one entity by path segment rather than by filter,
		// which is why FilterID is spelled differently from the rest.
		if name == FilterID {
			path += "/" + url.PathEscape(values[0])
			continue
		}
		params.Set(name, values[0])
	}

	client := c
	if limit > 0 && limit < c.cfg.MaxItems {
		narrowed := *c
		narrowed.cfg.MaxItems = limit
		client = &narrowed
	}
	return client.Get(ctx, path, route.key, params)
}

// Probe makes the cheapest authenticated call there is: one device, one page.
//
// It establishes that the address resolves, TLS works, the credential is
// accepted, and the response is the API's JSON rather than a login page --
// which is four things a wrong configuration could be, told apart at startup
// rather than inside the first tool call an assistant makes.
func (c *Client) Probe(ctx context.Context) error {
	probe := url.Values{}
	probe.Set("pagesize", "1")
	_, err := c.walk(ctx, "/devices", "devices", probe)
	return err
}

// Describe says where this instance reads from and what its read-only
// guarantee rests on, for the startup log and the health report.
func (c *Client) Describe() string {
	return "the API at " + redactURL(c.root) +
		", restricted to reads by a transport that refuses every method but GET"
}
