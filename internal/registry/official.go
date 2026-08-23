package registry

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OfficialBaseURL is the official MCP registry. Free, unauthenticated, and the
// one catalogue this build reads.
const OfficialBaseURL = "https://registry.modelcontextprotocol.io"

// officialSource names the catalogue in responses and cache keys.
const officialSource = "registry.modelcontextprotocol.io"

// Official reads the official MCP registry.
type Official struct {
	base   string
	client *http.Client
	agent  string
	limit  int
}

// OfficialOptions configures the client. Every field has a working default, so
// the zero value is usable and a test can replace exactly what it needs.
type OfficialOptions struct {
	// BaseURL overrides the registry address. For tests; a second registry is
	// a second Client, not a different URL in this one.
	BaseURL string
	// HTTPClient overrides the transport. The default refuses redirects and
	// bounds the call.
	HTTPClient *http.Client
	// UserAgent identifies this host to the registry. Naming the caller is
	// ordinary manners toward a free service.
	UserAgent string
	// Limit is the page size. Zero takes defaultLimit.
	Limit int
}

// NewOfficial builds a client for the official registry.
func NewOfficial(opts OfficialOptions) *Official {
	base := strings.TrimSuffix(opts.BaseURL, "/")
	if base == "" {
		base = OfficialBaseURL
	}
	return &Official{
		base:   base,
		client: catalogueClient(opts.HTTPClient),
		agent:  userAgent(opts.UserAgent),
		limit:  pageLimit(opts.Limit),
	}
}

// Source names the catalogue.
func (o *Official) Source() string { return officialSource }

// List returns one page of servers.
//
// version=latest asks the registry for one row per name. It is asked for and
// then not relied on: the deduplication below runs regardless, because "the
// far end promises one row per name" is exactly the kind of promise that turns
// a catalogue page into a list of the same server four times.
func (o *Official) List(ctx context.Context, q Query) (Page, error) {
	return o.ListIfChanged(ctx, q, Validators{})
}

// ListIfChanged is List with the previous answer's validators.
//
// The official registry sends none today -- no ETag, no Last-Modified, no
// Cache-Control -- so this makes an unconditional request and reports a zero
// Freshness, which leaves the cache on its configured default. It is written
// anyway because the cost is a header the far end ignores, and because a
// registry that starts sending one should not need a code change to be
// believed.
func (o *Official) ListIfChanged(ctx context.Context, q Query, v Validators) (Page, error) {
	// Normalised again rather than assumed. The cache in front applies this so
	// its keys match the requests they stand for, and this client is also
	// usable on its own; the operation is idempotent, so doing it twice costs
	// nothing and doing it never is a bound that was not applied.
	q = q.Normalised()
	limit := q.Limit
	if limit <= 0 {
		limit = o.limit
	}
	values := url.Values{}
	values.Set("version", "latest")
	values.Set("limit", strconv.Itoa(limit))
	if q.Search != "" {
		values.Set("search", q.Search)
	}
	if q.Cursor != "" {
		values.Set("cursor", q.Cursor)
	}

	var body listResponse
	freshness, err := o.fetch(ctx, "/v0/servers?"+values.Encode(), v, &body)
	if err != nil {
		return Page{Freshness: freshness}, err
	}

	kept := dedupe(body.Servers, officialMetaKey)
	entries := make([]Entry, 0, len(kept))
	for _, raw := range kept {
		entry, ok := raw.entry(officialMetaKey)
		if !ok {
			continue
		}
		entry.Source = o.Source()
		entries = append(entries, entry)
	}
	page := Page{
		Source:      o.Source(),
		Entries:     entries,
		NextCursor:  opaque(body.Metadata.NextCursor, maxCursorRunes),
		RetrievedAt: time.Now().UTC(),
		Freshness:   freshness,
	}
	page.Sources = []SourceStatus{{
		Source: o.Source(), OK: true,
		RetrievedAt: page.RetrievedAt, Entries: len(entries),
	}}
	return page, nil
}

// Get returns one entry and its document.
func (o *Official) Get(ctx context.Context, name string) (Detail, error) {
	return o.GetIfChanged(ctx, name, Validators{})
}

// GetIfChanged is Get with the previous answer's validators. See ListIfChanged
// for why it is written for a registry that offers none.
func (o *Official) GetIfChanged(ctx context.Context, name string, v Validators) (Detail, error) {
	trimmed := clean(name, maxNameRunes)
	if trimmed == "" {
		return Detail{}, ErrNotFound
	}
	var raw catalogueEntry
	path := "/v0/servers/" + url.PathEscape(trimmed) + "/versions/latest"
	freshness, err := o.fetch(ctx, path, v, &raw)
	if err != nil {
		return Detail{Freshness: freshness}, err
	}
	// The same filter the list applies. An entry the catalogue does not show
	// must not be reachable by typing its name: a withdrawn server is
	// withheld, not merely hidden.
	if raw.registryFacts(officialMetaKey).Status != statusActive {
		return Detail{}, ErrNotFound
	}
	entry, ok := raw.entry(officialMetaKey)
	if !ok {
		return Detail{}, ErrNotFound
	}
	entry.Source = o.Source()
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

// fetch performs one bounded, optionally conditional GET and decodes it.
//
// Nothing to decorate: the official registry is free and unauthenticated, so
// a request carries the common headers and nothing else.
func (o *Official) fetch(ctx context.Context, path string, v Validators, out any) (Freshness, error) {
	return fetchJSON(ctx, o.client, o.Source(), o.base+path, o.agent, v, nil, out)
}

// officialMetaKey is where the registry puts an entry's lifecycle facts. The
// key is a URI on purpose: _meta is a shared namespace, and a registry is one
// of several parties that may write into it.
const officialMetaKey = "io.modelcontextprotocol.registry/official"

// compile-time check that the official registry satisfies the contract the
// cache and the handler are written against, including the conditional half
// the cache uses wherever a catalogue offers a validator.
var _ Revalidating = (*Official)(nil)
