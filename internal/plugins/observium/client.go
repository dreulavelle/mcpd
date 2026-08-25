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
	// Raw rather than a string, so that a collection landing on this field
	// cannot fail the decode.
	//
	// The envelope is built in PHP as $out['status'] = 'ok' followed by
	// $out[$entity] = $rows, so an endpoint whose entity is literally
	// "status" would overwrite the verdict. Measured against a live
	// installation /status does not: it answers with both, "status":"ok"
	// beside "statuses":{...}. This stays as defence for a version that
	// does collide, not as a description of one that does.
	Status    json.RawMessage `json:"status"`
	Message   string          `json:"message"`
	Count     flexInt         `json:"count"`
	PageSize  flexInt         `json:"pagesize"`
	PageNo    flexInt         `json:"pageno"`
	CountPage flexInt         `json:"countpage"`
}

// verdict returns the envelope's own status, and whether there was one.
//
// Absent means the field was overwritten by a collection of the same name --
// see the comment on Status. That is not a failure and must not be read as
// one: a response with a collection in it is a response that worked.
func (e envelope) verdict() (string, bool) {
	if len(e.Status) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(e.Status, &s); err != nil {
		return "", false
	}
	return s, true
}

// flexInt is a number Observium may send quoted.
//
// A real /devices answer opens:
//
//	{"count":"85","pagesize":250,"pageno":1,"countpage":85,"status":"ok",...}
//
// One field quoted and the next not, in the same envelope. Decoding that into
// an int fails on the quoted one, and the failure surfaces as "answered with
// something that is not the API's JSON envelope" -- so a correct token against
// a healthy installation reads as a broken one, which is the least useful
// place to be wrong. The entity ids have the same inconsistency and the tests
// already cope with it; the envelope did not.
//
// Quoted or bare is not worth distinguishing, so this does not record which
// arrived: nothing downstream would do anything differently, and a caller that
// cannot tell cannot come to depend on it.
type flexInt int

func (f *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))

	// Absent, null and "" all mean "not reported". Zero is right for every
	// one of them: the walk treats a zero count as "no total given" already,
	// and an error here would reject the whole response over a field that is
	// optional in the first place.
	if s == "null" || s == "" {
		*f = 0
		return nil
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		unquoted, err := strconv.Unquote(s)
		if err != nil {
			return fmt.Errorf("not a quoted number: %s", s)
		}
		s = strings.TrimSpace(unquoted)
		if s == "" {
			*f = 0
			return nil
		}
	}

	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt(n)
		return nil
	}
	// A count arriving as 85.0 is still a count. Accepted rather than
	// refused, for the same reason the quoting is: the alternative is
	// discarding a whole page over the spelling of a number.
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("not a number: %s", s)
	}
	*f = flexInt(n)
	return nil
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
	// Fields are the field names that survived the view, sorted.
	Fields []string
	// FieldsDropped is how many fields the widest item lost to it. Surfaced
	// for the same reason Truncated is: a narrowed row that does not say it
	// was narrowed is one a model will treat as the whole record.
	FieldsDropped int
}

// Get fetches a single entity or a collection under one key.
//
// The key is the envelope field the entities live under, and it is not derived
// from the path: /devices answers under "devices", /storage under "storages",
// and /processors, /mempools, /inventory, /neighbours, /vlans and /alert_log
// all under the generic "entries". Passing it in survives that. Guessing it
// from the endpoint does not, and did not -- seven of the twelve routes here
// were named after their path and read nothing for it.
func (c *Client) Get(ctx context.Context, path, key string, params url.Values) (Page, error) {
	if c.cache == nil {
		return c.walk(ctx, path, key, params)
	}
	// c.cfg.MaxItems is the effective ceiling: Read narrows a copy of the
	// client when a caller asks for fewer, so by here it is this call's own
	// limit rather than the instance-wide setting.
	got, err := c.cache.reuse(ctx, path, params, c.cfg.MaxItems, func(ctx context.Context) (any, error) {
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
		if int(env.Count) > out.Total {
			out.Total = int(env.Count)
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
			// Before the item is retained anywhere, including the read cache.
			// A community string that is never stored cannot be served out of
			// storage by a later change to how views work.
			for _, name := range alwaysRemoved {
				delete(item, name)
			}
			out.Items = append(out.Items, item)
		}

		// Stop on the first short page. Observium reports countpage when it
		// paginates, but not every endpoint sets it, and a short page is the
		// signal that holds either way.
		if len(items) < c.cfg.PageSize {
			return out, nil
		}
		if env.CountPage > 0 && pageNo >= int(env.CountPage) {
			return out, nil
		}
		// A page that is full but reports no total would loop forever if the
		// endpoint ignores pageno. Bounded by MaxItems above, but an endpoint
		// returning the same full page each time would spin to that cap
		// making identical requests, so stop when nothing new can arrive.
		if env.Count > 0 && len(out.Items) >= int(env.Count) {
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
		// A wrong key used to be indistinguishable from an empty estate: this
		// returned no items and no error, and the tool above reported that
		// Observium held nothing. Seven of the twelve routes were wrong and
		// nobody could tell -- capacity answered "no processors" against a
		// host with thirty-seven.
		//
		// So an absent key is now reported, and the message names what the
		// response did carry, because that is the fix. An estate that really
		// is empty answers with the key present and empty, or with no
		// collection field at all, and both are handled below.
		if others := collectionKeys(body); len(others) > 0 {
			return nil, fmt.Errorf(
				"answered under %s, not %q -- the route table has the wrong "+
					"envelope key for this endpoint", quoteAll(others), key)
		}
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

// collectionKeys names the fields of a response that could hold entities --
// everything that is not part of the envelope itself.
//
// entity_cache is envelope furniture rather than a collection: /alert_log
// returns it beside the entries as a lookup table of names, and offering it as
// a candidate would point a wrong-key report at the wrong field.
func collectionKeys(body map[string]json.RawMessage) []string {
	meta := map[string]bool{
		"status": true, "count": true, "pagesize": true, "pageno": true,
		"countpage": true, "message": true, "entity_cache": true,
	}
	var out []string
	for name := range body {
		if !meta[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func quoteAll(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = strconv.Quote(name)
	}
	return strings.Join(quoted, ", ")
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
	if verdict, ok := env.verdict(); ok && !strings.EqualFold(verdict, "ok") {
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
	// Status entries are a device's enumerated conditions -- a power supply
	// that is present or absent, a fan that is ok or failed. Sensors carry the
	// readings that are numbers; these carry the ones that are states, and a
	// health question wants both.
	EntityStatus Entity = "status"
	// Maintenance windows explain why an estate with something visibly wrong
	// is reporting no alerts.
	EntityMaintenance Entity = "maintenance"
	// Groups are how an operator has organised the estate, and several filters
	// take a group name -- which is unusable without a way to learn them.
	EntityGroups Entity = "groups"
	// Alert checks are what is being watched, which is the question behind
	// "why did nobody get told".
	EntityAlertChecks Entity = "alert_checks"

	// Metered and per-device readings no other tool reaches: traffic and power
	// bills with their allowances, arbitrary counters, printer consumables and
	// response-time probes.
	EntityBills         Entity = "bills"
	EntityPowerBills    Entity = "power_bills"
	EntityCounters      Entity = "counters"
	EntityProbes        Entity = "probes"
	EntityPrinterSupply Entity = "printersupplies"
)

// Filter names are the API's own, named here so a typo is a compile error
// rather than a filter that silently matches everything.
const (
	FilterDeviceID   = "device_id"
	FilterHostname   = "hostname"
	FilterStatus     = "status"
	FilterOS         = "os"
	FilterLocation   = "location"
	FilterHardware   = "hardware"
	FilterVendor     = "vendor"
	FilterGroup      = "group"
	FilterState      = "state"
	FilterErrors     = "errors"
	FilterAlerted    = "alerted"
	FilterIfAlias    = "ifAlias"
	FilterMetric     = "metric"
	FilterEvent      = "event"
	FilterMessage    = "message"
	FilterFrom       = "timestamp_from"
	FilterTo         = "timestamp_to"
	FilterModel      = "entPhysicalModelName"
	FilterSerial     = "entPhysicalSerialNum"
	FilterType       = "type"
	FilterGroupID    = "group_id"
	FilterDeviceGrp  = "device_group"
	FilterIfDescr    = "ifDescr"
	FilterSensorType = "sensor_type"
	FilterEntityType = "entity_type"
	FilterEntityID   = "entity_id"
	FilterClass      = "class"
	FilterAF         = "af"
	FilterActive     = "active"
	FilterUpcoming   = "upcoming"
	FilterMembers    = "include_members"
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
var apiPaths = map[Entity]route{
	EntityDevices:    {"/devices", "devices", "device"},
	EntityPorts:      {"/ports", "ports", "port"},
	EntitySensors:    {"/sensors", "sensors", "sensor"},
	EntityAlerts:     {"/alerts", "alerts", "alert"},
	EntityAlertLog:   {"/alert_log/", "entries", "entries"},
	EntityStorage:    {"/storage", "storages", "storage"},
	EntityMempools:   {"/mempools", "entries", "mempool"},
	EntityProcessors: {"/processors", "entries", "processor"},
	EntityInventory:  {"/inventory", "entries", "inventory"},
	EntityNeighbours: {"/neighbours", "entries", "neighbour"},
	EntityAddresses:  {"/address", "addresses", "addresses"},
	EntityVLANs:      {"/vlans", "entries", "vlan"},

	EntityBills:         {"/bills", "bills", "bill"},
	EntityPowerBills:    {"/power_bills", "power_bills", "power_bill"},
	EntityCounters:      {"/counters", "counters", "counter"},
	EntityGroups:        {"/groups", "groups", "group"},
	EntityStatus:        {"/status", "statuses", "status_entry"},
	EntityProbes:        {"/probes", "probes", "probe"},
	EntityPrinterSupply: {"/printersupplies", "printersupplies", "printersupply"},
	EntityAlertChecks:   {"/alert_checks", "alert_checks", "alert_check"},
	EntityMaintenance:   {"/maintenance", "maintenance", "maintenance"},
}

// route is how one entity is reached and how its answer is shaped.
//
// key and idKey are both needed because Observium answers a collection and a
// single entity under different names: /devices under "devices" and
// /devices/491 under "device", /status under "statuses" and /status/12 under
// "status_entry". Selecting one entity is a path segment rather than a filter,
// so the same route serves both and has to know both names. Getting this wrong
// is silent -- it reads as "no such device" -- which is how observium_device
// came to be broken without anybody noticing.
type route struct {
	path  string
	key   string
	idKey string
}

// Read fetches one entity collection, narrowed to the view.
//
// The view is applied here rather than inside walk because walk's results are
// what the read cache holds: narrowing before the cache would let a summary
// read be served to a caller that asked for full detail. So the cache holds
// whole rows and each call narrows its own copy -- a copy, because the maps in
// a cached Page are shared with every later reader of it, and trimming them in
// place would empty the cache into whichever view happened to be asked for
// first.
//
// Credentials are the exception and are gone before this point: walk removes
// them as it decodes, so they are never in the cache to be copied out of.
func (c *Client) Read(ctx context.Context, entity Entity, filters url.Values, limit int, v view) (Page, error) {
	route, ok := apiPaths[entity]
	if !ok {
		return Page{}, fmt.Errorf("observium: no API endpoint for %s", entity)
	}

	path := route.path
	key := route.key
	params := url.Values{}
	for name, values := range filters {
		if len(values) == 0 || values[0] == "" {
			continue
		}
		// The API selects one entity by path segment rather than by filter,
		// which is why FilterID is spelled differently from the rest -- and
		// answers it under a different envelope key, which is why the key
		// changes with it.
		if name == FilterID {
			path += "/" + url.PathEscape(values[0])
			key = route.idKey
			continue
		}
		params.Set(name, values[0])
	}

	client := c
	if limit > 0 && limit < c.cfg.MaxItems {
		capped := *c
		capped.cfg.MaxItems = limit
		client = &capped
	}
	page, err := client.Get(ctx, path, key, params)
	if err != nil {
		return Page{}, err
	}

	page.Items = copyItems(page.Items)
	page.Fields, page.FieldsDropped = narrow(page.Items, entity, v)
	return page, nil
}

// copyItems gives the caller maps it may modify.
//
// Page.Items from a cache hit are the cache's own maps. Without this, one
// tool call narrowing its result would narrow the cached entry every later
// call reads.
func copyItems(in []map[string]any) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, item := range in {
		dup := make(map[string]any, len(item))
		for k, v := range item {
			dup[k] = v
		}
		out[i] = dup
	}
	return out
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
