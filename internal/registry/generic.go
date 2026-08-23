package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

// The Generic MCP Registry API, and the two catalogues that speak it.
//
// The official registry defines a wire shape -- a page of {server, _meta}
// rows under `servers`, a `metadata.nextCursor`, a server.json passed through
// verbatim -- and PulseMCP's v0.1 sub-registry implements the same one. So
// this file is the reader, and official.go and pulsemcp.go are the two
// configurations of it: a base URL, a path prefix, the headers a request
// carries, and the _meta key that catalogue writes its lifecycle facts under.
//
// Written as one reader rather than two because the alternative is a
// near-duplicate of three hundred lines whose copies drift. What actually
// differs between the two catalogues is four values, and four values is a
// parameter list rather than a second implementation.

// statusActive is the only lifecycle status this host offers.
//
// A deprecated or deleted entry is withheld rather than shown greyed out: the
// catalogue is a place to pick something to install, and the answer to "should
// I install the thing its author has withdrawn" is not a nuance worth
// rendering.
const statusActive = "active"

// requestTimeout bounds one call to a catalogue.
//
// Short on purpose. This runs inside an administrator's request, and a
// catalogue that is slow is one whose page should say so rather than one that
// holds a browser open.
const requestTimeout = 15 * time.Second

// defaultLimit is one page. The registry caps a page at a hundred; thirty is
// a screenful and keeps a single fetch small.
const defaultLimit = 30

// refuseRedirects is the CheckRedirect every catalogue client uses.
//
// None of them redirects today, and a catalogue that suddenly wants to send
// this host somewhere else is a change worth refusing rather than following.
// It matters more for the two that carry a credential: PulseMCP's tenant key
// travels on every request, and Go's own defence against handing it to a
// redirect target is only as good as the redirect never being followed.
func refuseRedirects(req *http.Request, _ []*http.Request) error {
	return fmt.Errorf("registry: refused a redirect to %s", req.URL.Redacted())
}

// catalogueClient builds the HTTP client a source uses when it was given none.
func catalogueClient(supplied *http.Client) *http.Client {
	if supplied != nil {
		return supplied
	}
	return &http.Client{Timeout: requestTimeout, CheckRedirect: refuseRedirects}
}

// userAgent names this host to a free service, which is ordinary manners.
func userAgent(supplied string) string {
	if supplied == "" {
		return "mcpd"
	}
	return supplied
}

// pageLimit bounds a client's configured page size.
func pageLimit(limit int) int {
	if limit <= 0 || limit > MaxEntriesPerPage {
		return defaultLimit
	}
	return limit
}

// fetchJSON performs one bounded, optionally conditional GET and decodes it.
//
// The Freshness it returns is meaningful on the ErrNotModified path too: that
// is how a 304 renews what the cache holds, and how a new ETag arriving with
// one is picked up.
//
// decorate is where a catalogue adds what only it needs -- PulseMCP's tenant
// and key headers. It runs after the common headers and before the call, so a
// source can override Accept but cannot skip the bound below it.
func fetchJSON(
	ctx context.Context,
	client *http.Client,
	source, url, agent string,
	v Validators,
	decorate func(*http.Request),
	out any,
) (Freshness, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Freshness{}, fmt.Errorf("registry: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", agent)
	applyValidators(req, v)
	if decorate != nil {
		decorate(req)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Freshness{}, fmt.Errorf("registry: %s could not be reached: %w", source, err)
	}
	defer resp.Body.Close()

	freshness := readFreshness(resp)
	switch resp.StatusCode {
	case http.StatusNotModified:
		return freshness, ErrNotModified
	case http.StatusNotFound:
		return freshness, ErrNotFound
	case http.StatusOK:
	default:
		// The body is a third party's error text, so it is drained and
		// discarded rather than passed through. The status is the fact.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return Freshness{}, describeStatus(source, resp)
	}

	data, err := readBounded(resp.Body, MaxResponseBytes)
	if err != nil {
		return Freshness{}, fmt.Errorf("registry: reading from %s: %w", source, err)
	}
	if data == nil {
		return Freshness{}, fmt.Errorf("registry: %s returned more than %d MiB in one page",
			source, MaxResponseBytes>>20)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return Freshness{}, fmt.Errorf("registry: %s returned something this host cannot read: %w",
			source, err)
	}
	return freshness, nil
}

// describeStatus turns a refusal into something an operator can act on.
//
// The status is the fact, and for a catalogue this host sends no credential to
// it is the whole of what can honestly be said. Rate limiting is named because
// "429" and "the far end is throttling us" are the same fact and only one of
// them reads as something to wait out.
//
// Deliberately says nothing about credentials. Three sources share this
// function and only one of them sends any, so a 401 here is a third party
// behaving unexpectedly rather than a key to go and check -- telling an
// operator to check a key they never configured for Smithery or the official
// registry would send them looking for a setting that does not exist. The one
// source that does authenticate adds that context itself; see
// PulseMCP.fetch.
func describeStatus(source string, resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("registry: %s is rate limiting this host (%s)", source, resp.Status)
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("registry: %s answered %s%w", source, resp.Status, errRefused)
	default:
		return fmt.Errorf("registry: %s answered %s", source, resp.Status)
	}
}

// errRefused marks a 401 or 403, so that a source which does send a credential
// can add what to check about it while one that sends none says nothing.
//
// Wrapped with an empty message: it exists to be matched, and the sentence an
// operator reads is composed by whoever caught it.
var errRefused = errors.New("")

// --- the Generic MCP Registry API wire format -------------------------------

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

// registryMeta is the lifecycle block a registry writes beside a document.
//
// Both catalogues that speak this API write the same fields; they write them
// under different keys, because _meta is a shared namespace and the key names
// the party making the claim.
type registryMeta struct {
	Status      string    `json:"status"`
	IsLatest    bool      `json:"isLatest"`
	PublishedAt time.Time `json:"publishedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// registryFacts reads the lifecycle block, which is absent on a malformed row.
func (c catalogueEntry) registryFacts(metaKey string) registryMeta {
	raw, ok := c.Meta[metaKey]
	if !ok {
		return registryMeta{}
	}
	var m registryMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return registryMeta{}
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
func (c catalogueEntry) entry(metaKey string) (Entry, bool) {
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
	facts := c.registryFacts(metaKey)
	transport, endpoint, addable, reason, auth := describe(c.Server)

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
		Auth:          auth,
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
func dedupe(rows []catalogueEntry, metaKey string) []catalogueEntry {
	type ranked struct {
		row   catalogueEntry
		facts registryMeta
		order int
	}
	best := make(map[string]ranked, len(rows))
	var names []string

	for i, row := range rows {
		if i >= MaxEntriesPerPage {
			break
		}
		facts := row.registryFacts(metaKey)
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

func better(a registryMeta, aOrder int, b registryMeta, bOrder int) bool {
	if a.IsLatest != b.IsLatest {
		return a.IsLatest
	}
	if !a.PublishedAt.Equal(b.PublishedAt) {
		return a.PublishedAt.After(b.PublishedAt)
	}
	return aOrder > bOrder
}
