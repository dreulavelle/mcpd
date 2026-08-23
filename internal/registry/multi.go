package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Multi serves one catalogue built from several.
//
// The handler behind it and the import path in front of it know nothing about
// how many catalogues there are; a source is added or turned off by changing
// what is passed here. Sources are listed in preference order, most preferred
// first, and that order decides two things: which copy of a server appearing
// in two catalogues survives deduplication, and which catalogue answers a Get
// for a name both of them have.
//
// A source's failure is that source's. One catalogue being unreachable
// produces a shorter list that says which one is missing, not a page that
// refuses to render; a page is an error only when nothing answered. That is
// the same judgement Cached makes about staleness, one level up: the operator
// looking at a partial catalogue can still see what is there and still decide
// to import one, and the fault is not in this deployment.
type Multi struct {
	sources []Client
	budget  time.Duration
}

// DefaultFanOutBudget bounds how long one page waits on every catalogue
// together.
//
// Shorter than the fifteen seconds a single source's own HTTP client allows,
// and deliberately: that timeout is what one request may take, this is what a
// person waiting for a page may be asked to spend. A source slower than this
// is dropped from the page it was too slow for -- its own request runs on and
// fills the cache, so the next page has it.
const DefaultFanOutBudget = 8 * time.Second

// NewMulti composes catalogues in preference order, most preferred first.
func NewMulti(sources ...Client) *Multi {
	kept := make([]Client, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			kept = append(kept, s)
		}
	}
	return &Multi{sources: kept, budget: DefaultFanOutBudget}
}

// WithBudget replaces how long a page waits on its sources. For tests, and for
// a deployment whose catalogues are behind a slow link.
func (m *Multi) WithBudget(budget time.Duration) *Multi {
	if budget > 0 {
		m.budget = budget
	}
	return m
}

// Close releases every source that holds something to release -- the caches,
// and the background refreshes they own. Called on the way out.
func (m *Multi) Close() error {
	var errs []error
	for _, s := range m.sources {
		if closer, ok := s.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Sources names the catalogues, in preference order.
func (m *Multi) Sources() []string {
	out := make([]string, 0, len(m.sources))
	for _, s := range m.sources {
		out = append(out, s.Source())
	}
	return out
}

// Source names the composite, for a caller that wants one string.
func (m *Multi) Source() string {
	names := m.Sources()
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names, ", ")
}

// List returns one page from every source, merged and deduplicated.
//
// Each source contributes at most one page of its own, so the merged page is
// at most one page per source. MaxEntriesPerPage is per source by its own
// terms -- it caps what "one page may contribute" -- and re-truncating the
// merge would drop entries whose cursors had already advanced past them.
//
// Deduplication is over the merged page, which is sufficient rather than
// approximate for the case it is for. A search asks every source the same
// question and every source answers with its matches at once, so the two
// copies of a server land side by side and one of them is removed -- which is
// the only situation in which a person can see that there are two. Browsing
// the official registry's twenty-four thousand entries thirty at a time is not
// that situation: its page of ai.* names and Docker's page of bare slugs have
// no reason to hold the same server, and reconciling them would mean holding
// both catalogues whole. That is the same judgement the official registry's own
// page-local dedupe already makes, one level up.
func (m *Multi) List(ctx context.Context, q Query) (Page, error) {
	if len(m.sources) == 0 {
		return Page{}, errors.New("registry: no catalogue is configured")
	}

	// A cursor that decoded to nothing usable -- garbage, or one written by a
	// host configured with different sources -- restarts the listing rather
	// than paging nothing. Restarting is a state the caller already handles;
	// an empty page with no explanation is not.
	cursors := decodeMultiCursor(q.Cursor, m.Sources())
	resuming := len(cursors) > 0
	results := make([]Page, len(m.sources))
	failures := make([]error, len(m.sources))

	// Concurrent, and bounded as a whole.
	//
	// Concurrent because a search across four catalogues must not cost four
	// round trips one after another -- with a cold cache that is the
	// difference between a page and a wait. Bounded because the slowest
	// catalogue must not decide how long a page takes: past the budget this
	// host stops waiting, serves what arrived, and says which source did not
	// answer in time. The alternative is a dashboard whose speed is set by
	// whichever third party is having the worst day.
	//
	// Results come back over a buffered channel rather than into the slice
	// directly, because a goroutine still running when the budget expires
	// would otherwise be writing to something already being read. The buffer
	// is one slot per source, so a late answer is delivered to nobody and the
	// goroutine ends rather than blocking.
	type sourceResult struct {
		index int
		page  Page
		err   error
	}
	arrivals := make(chan sourceResult, len(m.sources))
	// asked and arrived are tracked separately from the results, because
	// "returned nothing" and "has not answered" look identical in a Page and
	// mean opposite things to somebody reading the list.
	asked := make([]bool, len(m.sources))
	arrived := make([]bool, len(m.sources))
	launched := 0
	for i, source := range m.sources {
		name := source.Source()
		// A source that reported no further pages last time is not asked
		// again: asking would restart it from the beginning and repeat its
		// first page under every subsequent cursor.
		if resuming && !cursors.more(name) {
			continue
		}
		asked[i] = true
		launched++
		go func(i int, source Client, cursor string) {
			page, err := source.List(ctx, Query{
				Search: q.Search, Cursor: cursor, Limit: q.Limit,
			})
			arrivals <- sourceResult{index: i, page: page, err: err}
		}(i, source, cursors.get(name))
	}

	budget := time.NewTimer(m.budget)
	defer budget.Stop()
collect:
	for received := 0; received < launched; received++ {
		select {
		case r := <-arrivals:
			results[r.index], failures[r.index] = r.page, r.err
			arrived[r.index] = true
		case <-budget.C:
			// Whatever has not arrived is not going to be waited for.
			break collect
		case <-ctx.Done():
			// The caller gave up, which is different from a catalogue being
			// slow: there is nobody left to serve a partial page to.
			return Page{}, ctx.Err()
		}
	}
	for i := range m.sources {
		if asked[i] && !arrived[i] {
			failures[i] = fmt.Errorf("registry: %s did not answer within %s",
				m.sources[i].Source(), m.budget)
		}
	}

	page := Page{Source: m.Source(), Entries: []Entry{}}
	next := multiCursor{}
	answered := 0
	// Identity is remembered across sources so that the more preferred
	// source's copy is the one kept. Sources are visited in preference order
	// and the first claim on an identity wins.
	seen := map[string]bool{}
	oldest := time.Time{}

	for i, source := range m.sources {
		name := source.Source()
		if err := failures[i]; err != nil {
			page.Sources = append(page.Sources, SourceStatus{
				Source: name,
				Error:  clean(err.Error(), maxReasonRunes),
			})
			continue
		}
		if resuming && !cursors.more(name) {
			// Exhausted on an earlier page. Reported so that the response
			// still accounts for every configured source rather than looking
			// as though one had vanished.
			page.Sources = append(page.Sources, SourceStatus{Source: name, OK: true})
			continue
		}
		answered++
		contributed := 0
		for _, entry := range results[i].Entries {
			if entry.Source == "" {
				entry.Source = name
			}
			key := identity(entry)
			if seen[key] {
				continue
			}
			seen[key] = true
			page.Entries = append(page.Entries, entry)
			contributed++
		}
		if results[i].NextCursor != "" {
			next.set(name, results[i].NextCursor)
		}
		page.Stale = page.Stale || results[i].Stale
		if !results[i].RetrievedAt.IsZero() &&
			(oldest.IsZero() || results[i].RetrievedAt.Before(oldest)) {
			oldest = results[i].RetrievedAt
		}
		page.Sources = append(page.Sources, SourceStatus{
			Source:      name,
			OK:          true,
			Stale:       results[i].Stale,
			RetrievedAt: results[i].RetrievedAt,
			Entries:     contributed,
		})
	}

	if answered == 0 {
		// Nothing answered, so there is no partial page to serve honestly.
		// Every failure is named rather than only the first, so an operator
		// does not fix one catalogue and find the other still broken.
		return Page{}, joinSourceErrors(failures)
	}

	page.NextCursor = next.encode()
	page.RetrievedAt = oldest
	if page.RetrievedAt.IsZero() {
		page.RetrievedAt = time.Now().UTC()
	}
	return page, nil
}

// Get returns one entry, from the first source that has it.
//
// Preference order, so a name both catalogues know is answered by the more
// trusted one -- the same rule the merge applies, applied to the lookup, so
// that clicking an entry cannot hand back a different catalogue's copy of it.
func (m *Multi) Get(ctx context.Context, name string) (Detail, error) {
	var failures []error
	for _, source := range m.sources {
		detail, err := source.Get(ctx, name)
		switch {
		case err == nil:
			if detail.Entry.Source == "" {
				detail.Entry.Source = source.Source()
			}
			return detail, nil
		case errors.Is(err, ErrNotFound):
			// Not this catalogue's; ask the next.
			continue
		default:
			// A source that is down must not turn into "no such server",
			// which would read as the entry having been withdrawn. It is kept
			// and reported only if no other source has the name.
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return Detail{}, joinSourceErrors(failures)
	}
	return Detail{}, ErrNotFound
}

// identity is what makes two entries from two catalogues the same server.
//
// The endpoint, when there is one. A name cannot do this job: the official
// registry names a server in reverse-DNS with the publisher's domain
// ("app.linear/linear") and Docker names it with a bare slug ("linear"), so
// the same server has two names that no rule turns into each other. The
// address it is dialled at is the same string in both catalogues for thirty-two
// of the entries they share, and it is also the thing that actually matters:
// two entries that resolve to one endpoint are one server however they are
// named, and importing both would mount the same upstream twice.
//
// An entry with no endpoint -- a package-only server, a Docker container, a
// document this host refused -- falls back to the source and name, which makes
// it unique to its own catalogue. That is right rather than a compromise:
// nothing can establish that two unreachable entries are the same server, and
// merging them on a name that happened to match would hide one of them.
func identity(e Entry) string {
	if endpoint := normaliseEndpoint(e.URL); endpoint != "" {
		return "url\x00" + endpoint
	}
	return "name\x00" + e.Source + "\x00" + strings.ToLower(e.Name)
}

// normaliseEndpoint reduces a URL to the parts that decide whether two of them
// address the same server: scheme, host, port and path.
//
// Case and a trailing slash are noise -- "https://MCP.Linear.app/mcp/" and
// "https://mcp.linear.app/mcp" are one address. A query string is not noise
// and is kept, because it can select a tenant. A URL that will not parse is
// not normalised into something that might collide with a real one; it is
// dropped, and the entry falls back to its name.
func normaliseEndpoint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !defaultPort(u.Scheme, port) {
		host += ":" + port
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	out := strings.ToLower(u.Scheme) + "://" + host + path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

func defaultPort(scheme, port string) bool {
	return (strings.EqualFold(scheme, "https") && port == "443") ||
		(strings.EqualFold(scheme, "http") && port == "80")
}

// joinSourceErrors reports every catalogue that failed, in one message.
func joinSourceErrors(failures []error) error {
	var reasons []string
	for _, err := range failures {
		if err != nil {
			reasons = append(reasons, err.Error())
		}
	}
	if len(reasons) == 0 {
		return errors.New("registry: no catalogue answered")
	}
	sort.Strings(reasons)
	return errors.New(strings.Join(reasons, "; "))
}

// --- the composite cursor ---------------------------------------------------

// multiCursor carries one upstream cursor per source.
//
// Opaque to everyone outside this file, and it has to be: a source's cursor
// belongs to that source and means nothing here. What this adds is which
// source each one came from, so that a page continues each catalogue where it
// left off rather than restarting one of them.
//
// A source absent from the map has no further pages. That is why the encoding
// is a map rather than a list: adding or removing a source between two
// requests changes the list's positions and would silently hand one source's
// cursor to another.
type multiCursor map[string]string

func (c multiCursor) get(source string) string { return c[source] }

func (c multiCursor) more(source string) bool { return c[source] != "" }

func (c multiCursor) set(source, cursor string) { c[source] = cursor }

// encode renders the cursor for the wire. Empty when nothing has more pages,
// which is how a caller learns the listing has ended.
func (c multiCursor) encode() string {
	if len(c) == 0 {
		return ""
	}
	raw, err := json.Marshal(map[string]string(c))
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeMultiCursor reads a composite cursor.
//
// A cursor that will not decode, or that names a source this host is not
// configured with, yields an empty one: the listing restarts rather than
// paging a catalogue with a token from somewhere else. Restarting is a state
// the caller already handles; handing an arbitrary string to a third party's
// pagination is not.
func decodeMultiCursor(encoded string, sources []string) multiCursor {
	out := multiCursor{}
	if opaque(encoded, maxCursorRunes) == "" {
		return out
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) > maxCursorRunes*len(sources)+len(sources)*64+64 {
		return out
	}
	var decoded map[string]string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return out
	}
	known := make(map[string]bool, len(sources))
	for _, s := range sources {
		known[s] = true
	}
	for source, cursor := range decoded {
		if !known[source] {
			continue
		}
		if bounded := opaque(cursor, maxCursorRunes); bounded != "" {
			out[source] = bounded
		}
	}
	return out
}

// compile-time check that the multiplexer is itself a catalogue, which is what
// lets it be composed and cached like any other.
var _ Client = (*Multi)(nil)
