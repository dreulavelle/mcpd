package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OfficialBaseURL is the official MCP registry. Free, unauthenticated, and the
// one catalogue this build reads.
const OfficialBaseURL = "https://registry.modelcontextprotocol.io"

// officialSource names the catalogue in responses and cache keys.
const officialSource = "registry.modelcontextprotocol.io"

// statusActive is the only lifecycle status this host offers.
//
// A deprecated or deleted entry is withheld rather than shown greyed out: the
// catalogue is a place to pick something to install, and the answer to "should
// I install the thing its author has withdrawn" is not a nuance worth
// rendering.
const statusActive = "active"

// defaultLimit is one page. The registry caps a page at a hundred; thirty is
// a screenful and keeps a single fetch small.
const defaultLimit = 30

// requestTimeout bounds one call to the catalogue.
//
// Short on purpose. This runs inside an administrator's request, and a
// catalogue that is slow is one whose page should say so rather than one that
// holds a browser open.
const requestTimeout = 15 * time.Second

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
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: requestTimeout,
			// The registry does not redirect, and a catalogue that suddenly
			// wants to send this host somewhere else is a change worth
			// refusing rather than following. There is no credential to
			// leak here, which is why this is a plain refusal rather than
			// the origin pin remote servers get.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("registry: refused a redirect to %s", req.URL.Redacted())
			},
		}
	}
	agent := opts.UserAgent
	if agent == "" {
		agent = "mcpd"
	}
	limit := opts.Limit
	if limit <= 0 || limit > MaxEntriesPerPage {
		limit = defaultLimit
	}
	return &Official{base: base, client: client, agent: agent, limit: limit}
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
	if err := o.fetch(ctx, "/v0/servers?"+values.Encode(), &body); err != nil {
		return Page{}, err
	}

	kept := dedupe(body.Servers)
	entries := make([]Entry, 0, len(kept))
	for _, raw := range kept {
		entry, ok := raw.entry()
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return Page{
		Source:      o.Source(),
		Entries:     entries,
		NextCursor:  opaque(body.Metadata.NextCursor, maxCursorRunes),
		RetrievedAt: time.Now().UTC(),
	}, nil
}

// Get returns one entry and its document.
func (o *Official) Get(ctx context.Context, name string) (Detail, error) {
	trimmed := clean(name, maxNameRunes)
	if trimmed == "" {
		return Detail{}, ErrNotFound
	}
	var raw catalogueEntry
	path := "/v0/servers/" + url.PathEscape(trimmed) + "/versions/latest"
	if err := o.fetch(ctx, path, &raw); err != nil {
		return Detail{}, err
	}
	// The same filter the list applies. An entry the catalogue does not show
	// must not be reachable by typing its name: a withdrawn server is
	// withheld, not merely hidden.
	if raw.registryFacts().Status != statusActive {
		return Detail{}, ErrNotFound
	}
	entry, ok := raw.entry()
	if !ok {
		return Detail{}, ErrNotFound
	}
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
		Source:      o.Source(),
		RetrievedAt: time.Now().UTC(),
	}, nil
}

// fetch performs one bounded GET and decodes it.
func (o *Official) fetch(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base+path, nil)
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", o.agent)

	resp, err := o.client.Do(req)
	if err != nil {
		return fmt.Errorf("registry: %s could not be reached: %w", o.Source(), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		// The body is a third party's error text, so it is drained and
		// discarded rather than passed through. The status is the fact.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("registry: %s answered %s", o.Source(), resp.Status)
	}

	// Bounded before decoding, not after. A JSON decoder reading an unbounded
	// body from a third party is a memory limit set by somebody else.
	limited := io.LimitReader(resp.Body, MaxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("registry: reading from %s: %w", o.Source(), err)
	}
	if len(data) > MaxResponseBytes {
		return fmt.Errorf("registry: %s returned more than %d MiB in one page",
			o.Source(), MaxResponseBytes>>20)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("registry: %s returned something this host cannot read: %w",
			o.Source(), err)
	}
	return nil
}

// --- the registry's wire format --------------------------------------------

// officialMetaKey is where the registry puts an entry's lifecycle facts. The
// key is a URI on purpose: _meta is a shared namespace, and a registry is one
// of several parties that may write into it.
const officialMetaKey = "io.modelcontextprotocol.registry/official"

type listResponse struct {
	Servers  []catalogueEntry `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

// catalogueEntry is one row: the server.json, and what the registry knows
// about it that the document itself does not say.
type catalogueEntry struct {
	Server json.RawMessage            `json:"server"`
	Meta   map[string]json.RawMessage `json:"_meta"`
}

type officialMeta struct {
	Status      string    `json:"status"`
	IsLatest    bool      `json:"isLatest"`
	PublishedAt time.Time `json:"publishedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// registryFacts reads the lifecycle block, which is absent on a malformed row.
func (c catalogueEntry) registryFacts() officialMeta {
	raw, ok := c.Meta[officialMetaKey]
	if !ok {
		return officialMeta{}
	}
	var m officialMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return officialMeta{}
	}
	return m
}

// documentFields is the handful of server.json fields an entry displays. The
// document is parsed properly by describe(); this is only what is shown when
// parsing fails and there is still a name to render.
type documentFields struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

// entry turns one registry row into what the dashboard shows. The second
// result is false for a row with no usable name, which is a row nothing can be
// done with.
func (c catalogueEntry) entry() (Entry, bool) {
	var fields documentFields
	if err := json.Unmarshal(c.Server, &fields); err != nil {
		return Entry{}, false
	}
	// Not cleaned: Name is the identifier the dashboard sends back to the
	// entry route, so a truncated or rewritten one is a row that 404s when
	// somebody clicks it. It survives unchanged or the row is dropped, and a
	// row that is absent is better than one that is dead.
	name := opaque(fields.Name, maxNameRunes)
	if name == "" {
		return Entry{}, false
	}
	facts := c.registryFacts()
	transport, endpoint, addable, reason := describe(c.Server)

	title := clean(fields.Title, maxTitleRunes)
	if title == "" {
		title = SuggestName(name)
	}
	return Entry{
		Name:          name,
		SuggestedName: SuggestName(name),
		Title:         title,
		Description:   clean(fields.Description, maxDescriptionRunes),
		Version:       clean(fields.Version, maxVersionRunes),
		Transport:     transport,
		URL:           endpoint,
		UpdatedAt:     facts.UpdatedAt.UTC(),
		Addable:       addable,
		Reason:        reason,
	}, true
}

// dedupe keeps one row per name: the active one the registry calls latest.
//
// The registry stores every version of every server and returns them all
// unless asked otherwise. Without this, a page of the catalogue shows the same
// server four times with four version numbers, which reads as a broken list
// rather than as a version history.
//
// Ranking, in order: an entry the registry marks isLatest wins; failing that
// the one published most recently; failing that the one that came later in the
// page, since the registry orders by name then version.
//
// This is page-local, and that is sufficient rather than approximate: rows for
// one name are adjacent in the registry's ordering, and the query asks for
// version=latest so there is normally one of each. What it defends against is
// that promise not being kept.
func dedupe(rows []catalogueEntry) []catalogueEntry {
	type ranked struct {
		row   catalogueEntry
		facts officialMeta
		order int
	}
	best := make(map[string]ranked, len(rows))
	var names []string

	for i, row := range rows {
		if i >= MaxEntriesPerPage {
			break
		}
		facts := row.registryFacts()
		if facts.Status != statusActive {
			continue
		}
		var fields documentFields
		if err := json.Unmarshal(row.Server, &fields); err != nil {
			continue
		}
		// The same bound entry() applies, so the two cannot disagree about
		// which rows exist.
		name := opaque(fields.Name, maxNameRunes)
		if name == "" {
			continue
		}
		candidate := ranked{row: row, facts: facts, order: i}
		current, seen := best[name]
		if !seen {
			best[name] = candidate
			names = append(names, name)
			continue
		}
		if better(candidate.facts, candidate.order, current.facts, current.order) {
			best[name] = candidate
		}
	}

	// Sorted by name so a page is stable, which is what makes the cursor the
	// registry hands back mean the same thing on the way out as on the way in.
	sort.Strings(names)
	out := make([]catalogueEntry, 0, len(names))
	for _, name := range names {
		out = append(out, best[name].row)
	}
	return out
}

func better(a officialMeta, aOrder int, b officialMeta, bOrder int) bool {
	if a.IsLatest != b.IsLatest {
		return a.IsLatest
	}
	if !a.PublishedAt.Equal(b.PublishedAt) {
		return a.PublishedAt.After(b.PublishedAt)
	}
	return aOrder > bOrder
}

// compile-time check that the official registry satisfies the contract the
// cache and the handler are written against.
var _ Client = (*Official)(nil)
