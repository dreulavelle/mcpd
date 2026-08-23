package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PulseMCP's sub-registry.
//
// Terms: pulsemcp.com/robots.txt publishes `User-agent: *`, `Allow: /`, and
// `Content-Signal: search=yes,ai-train=no,use=reference` -- the same
// declaration Smithery makes. `search=yes` is the signal's own words for
// "building a search index and providing search results", which is exactly and
// only what this does. Nothing here trains anything. Recorded so the next
// reader does not have to derive it again.
//
// Two things about this source are not what they were, and both are load
// bearing.
//
// **It speaks the Generic MCP Registry API, so it is barely a source at all.**
// v0.1 returns the same shape the official registry does -- {server, _meta}
// rows under `servers`, a `metadata.nextCursor`, a server.json passed through
// verbatim rather than composed. So this file is a base URL, two headers and a
// _meta key; everything that reads the wire is generic.go, shared with
// official.go. There is no translation step here because there is nothing to
// translate: PulseMCP hands back the publisher's own document.
//
// **The unauthenticated v0beta API is being switched off.** It is the one the
// obvious integration would have been written against -- `count_per_page`,
// `offset`, a `remotes[]` carrying `url_direct` and `cost` -- and PulseMCP is
// deliberately failing a rising share of its requests: 1% from January 2026,
// 10% from April, 50% from June, and 100% from September 2026. Measured on
// 2026-08-23, three of six requests came back `410 API_SUNSET`. A source built
// on it would have shipped already broken and been entirely dead within weeks,
// so it is not built on it. v0.1 is what this reads.
//
// The cost of that is a credential: v0.1 wants `X-API-Key` and `X-Tenant-ID`,
// and PulseMCP issues them by email rather than self-service. That is why this
// source is the only one of the four that is off by default -- see
// config.Catalog. Without a key every request is a 401, so an operator who
// switched it on and got nothing would be reading a page of errors rather than
// a catalogue.
const (
	// PulseMCPBaseURL is PulseMCP's sub-registry API.
	PulseMCPBaseURL = "https://api.pulsemcp.com"

	// pulseMCPSource names the catalogue in responses and cache keys.
	pulseMCPSource = "api.pulsemcp.com"

	// pulseMCPMetaKey is where PulseMCP puts an entry's lifecycle facts. The
	// official registry writes the same fields under its own key; _meta is a
	// shared namespace and the key names the party making the claim.
	pulseMCPMetaKey = "com.pulsemcp/server-version"
)

// PulseMCP reads PulseMCP's v0.1 sub-registry.
type PulseMCP struct {
	base   string
	client *http.Client
	agent  string
	limit  int

	// apiKey and tenant are this deployment's credentials for the catalogue,
	// resolved from a secret reference at construction. They are never logged,
	// never returned, and never written into a composed document -- there is
	// no composed document here, and they authenticate this host to PulseMCP
	// rather than this host to a server it found there.
	apiKey string
	tenant string
}

// PulseMCPOptions configures the client. Every field has a working default, so
// the zero value is usable and a test can replace exactly what it needs.
type PulseMCPOptions struct {
	// BaseURL overrides the registry address. For tests.
	BaseURL string
	// HTTPClient overrides the transport. The default refuses redirects and
	// bounds the call.
	HTTPClient *http.Client
	// UserAgent identifies this host.
	UserAgent string
	// Limit is the page size. Zero takes defaultLimit.
	Limit int
	// APIKey and Tenant are what v0.1 authenticates with. Both are required;
	// see ErrPulseMCPUnconfigured.
	APIKey string
	Tenant string
}

// ErrPulseMCPUnconfigured reports a source switched on with no credentials.
//
// Returned from every call rather than from construction, and returned rather
// than logged once at boot, because the rule this host follows everywhere is
// that missing configuration is reported where somebody can see it and act on
// it. A catalogue page naming the source and saying what it lacks is that
// place; a line in a log from three weeks ago is not. It also keeps the
// promise nothing about the catalogue is on a startup path.
var ErrPulseMCPUnconfigured = errors.New(
	"registry: api.pulsemcp.com needs an api key and a tenant id, and none are " +
		"configured; set catalog.pulsemcp_api_key_ref and catalog.pulsemcp_tenant, " +
		"or turn the source off")

// NewPulseMCP builds a client for PulseMCP's sub-registry. It fetches nothing.
func NewPulseMCP(opts PulseMCPOptions) *PulseMCP {
	base := strings.TrimSuffix(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = PulseMCPBaseURL
	}
	return &PulseMCP{
		base:   base,
		client: catalogueClient(opts.HTTPClient),
		agent:  userAgent(opts.UserAgent),
		limit:  pageLimit(opts.Limit),
		apiKey: strings.TrimSpace(opts.APIKey),
		tenant: strings.TrimSpace(opts.Tenant),
	}
}

// Source names the catalogue.
func (p *PulseMCP) Source() string { return pulseMCPSource }

// configured reports that there is a credential to call with.
func (p *PulseMCP) configured() bool { return p.apiKey != "" && p.tenant != "" }

// List returns one page of servers.
func (p *PulseMCP) List(ctx context.Context, q Query) (Page, error) {
	return p.ListIfChanged(ctx, q, Validators{})
}

// ListIfChanged is List with the previous answer's validators.
//
// PulseMCP sends `cache-control: no-cache` and no validator, which the cache
// already handles and does not need a special case for: readFreshness turns
// `no-cache` with nothing to revalidate against into noCacheTTL, a minute --
// short enough that the catalogue's wish is substantially met, long enough
// that typing into a search box does not become one upstream request per
// keystroke. Confirmed against the live API rather than assumed; the header is
// on the v0beta responses and on every v0.1 error this host has seen.
//
// The conditional request is written anyway, for the same reason official.go
// writes one: it costs a header the far end ignores, and a registry that
// starts sending an ETag should not need a code change to be believed.
func (p *PulseMCP) ListIfChanged(ctx context.Context, q Query, v Validators) (Page, error) {
	if !p.configured() {
		return Page{}, ErrPulseMCPUnconfigured
	}
	// Normalised again rather than assumed; the operation is idempotent, so
	// doing it twice costs nothing and doing it never is a bound not applied.
	q = q.Normalised()
	limit := q.Limit
	if limit <= 0 {
		limit = p.limit
	}
	values := url.Values{}
	// version=latest asks for one row per name. It is asked for and then not
	// relied on -- dedupe below runs regardless -- for the reason official.go
	// gives: "the far end promises one row per name" is exactly the kind of
	// promise whose failure shows up as a page listing one server four times.
	values.Set("version", "latest")
	values.Set("limit", strconv.Itoa(limit))
	if q.Search != "" {
		values.Set("search", q.Search)
	}
	if q.Cursor != "" {
		values.Set("cursor", q.Cursor)
	}

	var body listResponse
	freshness, err := p.fetch(ctx, "/v0.1/servers?"+values.Encode(), v, &body)
	if err != nil {
		return Page{Freshness: freshness}, err
	}

	kept := dedupe(body.Servers, pulseMCPMetaKey)
	entries := make([]Entry, 0, len(kept))
	for _, raw := range kept {
		entry, ok := raw.entry(pulseMCPMetaKey)
		if !ok {
			continue
		}
		entry.Source = p.Source()
		entries = append(entries, entry)
	}
	page := Page{
		Source:      p.Source(),
		Entries:     entries,
		NextCursor:  opaque(body.Metadata.NextCursor, maxCursorRunes),
		RetrievedAt: time.Now().UTC(),
		Freshness:   freshness,
	}
	page.Sources = []SourceStatus{{
		Source: p.Source(), OK: true,
		RetrievedAt: page.RetrievedAt, Entries: len(entries),
		// One page's worth, because one page is all this catalogue will show
		// at a time -- it pages an opaque cursor and reports no total, so
		// there is no larger sample to measure and no size to scale it
		// against. See estimateAddable for what is done with that.
		Judged: len(entries), Addable: countAddable(entries),
	}}
	return page, nil
}

// Get returns one entry and its document.
func (p *PulseMCP) Get(ctx context.Context, name string) (Detail, error) {
	return p.GetIfChanged(ctx, name, Validators{})
}

// GetIfChanged is Get with the previous answer's validators.
func (p *PulseMCP) GetIfChanged(ctx context.Context, name string, v Validators) (Detail, error) {
	if !p.configured() {
		return Detail{}, ErrPulseMCPUnconfigured
	}
	trimmed := clean(name, maxNameRunes)
	if trimmed == "" {
		return Detail{}, ErrNotFound
	}
	var raw catalogueEntry
	path := "/v0.1/servers/" + url.PathEscape(trimmed) + "/versions/latest"
	freshness, err := p.fetch(ctx, path, v, &raw)
	if err != nil {
		return Detail{Freshness: freshness}, err
	}
	// The same filter the list applies. An entry the catalogue does not show
	// must not be reachable by typing its name: a withdrawn server is
	// withheld, not merely hidden.
	if raw.registryFacts(pulseMCPMetaKey).Status != statusActive {
		return Detail{}, ErrNotFound
	}
	entry, ok := raw.entry(pulseMCPMetaKey)
	if !ok {
		return Detail{}, ErrNotFound
	}
	entry.Source = p.Source()
	document := raw.Server
	if len(document) > MaxDocumentBytes {
		// describe() already refused it as addable; the document itself is
		// withheld rather than truncated, because half a server.json is not
		// something to hand an import form.
		document = nil
	}
	return Detail{
		Entry:       entry,
		Document:    document,
		RetrievedAt: time.Now().UTC(),
		Freshness:   freshness,
	}, nil
}

// fetch performs one bounded, optionally conditional, authenticated GET.
//
// The two credential headers are set here, in the decorator, rather than on a
// RoundTripper. A transport would attach them to whatever address it was
// handed, including one arrived at by redirect; setting them per request
// against a URL built from the configured base, with redirects refused
// outright, keeps them going to exactly one place.
func (p *PulseMCP) fetch(ctx context.Context, path string, v Validators, out any) (Freshness, error) {
	freshness, err := fetchJSON(ctx, p.client, p.Source(), p.base+path, p.agent, v,
		func(req *http.Request) {
			req.Header.Set("X-API-Key", p.apiKey)
			req.Header.Set("X-Tenant-ID", p.tenant)
		}, out)
	// This is the one source that sends a credential, so it is the one source
	// for which a 401 is this deployment's configuration rather than a third
	// party behaving oddly. An operator reading "answered 401 Unauthorized" on
	// a catalogue page has no reason to connect it to a key set months ago in
	// a config file, so the thing to check is named here -- and only here,
	// because for the other three there is no key to check.
	if errors.Is(err, errRefused) {
		return freshness, fmt.Errorf(
			"%w; check catalog.pulsemcp_api_key_ref and catalog.pulsemcp_tenant", err)
	}
	return freshness, err
}

// compile-time check that PulseMCP satisfies the contract the cache and the
// handler are written against, including the conditional half.
var _ Revalidating = (*PulseMCP)(nil)
