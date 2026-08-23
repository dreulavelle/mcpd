package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/mcpservers"
)

// Smithery's registry.
//
// Terms: registry.smithery.ai/robots.txt publishes `User-agent: *`,
// `Allow: /`, and `Content-Signal: search=yes,ai-train=no,use=reference`.
// `search=yes` is the signal's own words for "building a search index and
// providing search results", which is exactly and only what this does -- an
// operator types a name, this host asks Smithery, and the answer is shown with
// a link back. Nothing here trains anything and nothing is retained beyond the
// cache. Recorded so the next reader does not have to derive it again.
const (
	// SmitheryBaseURL is Smithery's registry API.
	SmitheryBaseURL = "https://registry.smithery.ai"

	// SmitheryGatewayURL is where every server Smithery hosts is reachable.
	//
	// One address shape for all ten thousand of them --
	// server.smithery.ai/{qualifiedName}/mcp, streamable-http -- and one
	// credential: an operator's Smithery API key opens every one. Dialled
	// without an Authorization header it answers 401 invalid_token, which is
	// why every document composed here declares that header and marks the
	// value behind it secret.
	SmitheryGatewayURL = "https://server.smithery.ai"

	// smitherySource names the catalogue in responses and cache keys.
	smitherySource = "registry.smithery.ai"

	// smitheryKeyVariable is the placeholder the composed Authorization
	// header carries, and so the name of the settings field an operator fills
	// in. Spelled like an environment variable because that is the convention
	// server.json variables follow and the one Docker's entries already
	// produce.
	smitheryKeyVariable = "SMITHERY_API_KEY"
)

// smitheryNamespace prefixes the name of every server.json this source
// composes.
//
// Smithery names a server with a qualifiedName -- "brave",
// "onesignal/onesignal" -- and server.json requires reverse-DNS with exactly
// one slash. There is no published name to use, so one is derived, and it is
// derived to say where it came from. Deliberately not a namespace the official
// registry knows, for the same reason Docker's is not: a derived name that
// looked registered would invite being treated as one.
const smitheryNamespace = "ai.smithery"

// How far Smithery's own pagination goes, measured rather than assumed.
//
// The listing reports totalCount in the ten thousands and totalPages of five,
// and page six comes back empty whatever page size is asked for: at
// pageSize=100 totalPages is 5, at pageSize=3 it is 167, and 167*3 is the same
// five hundred. So the cap is on rows, not on pages, and it does not move.
//
// This is the whole reason browse and search are different questions here.
// Browsing reaches five hundred of ten and a half thousand; the rest are
// reachable only by asking Smithery a question, which is why q= is passed
// upstream rather than used to filter a page that was already truncated.
// Filtering locally would silently present the top five hundred as the
// catalogue, and a search for a server at position nine thousand would come
// back empty with nothing saying why.
const (
	smitheryPageSize  = 100
	smitheryMaxPages  = 5
	smitheryBrowseCap = smitheryPageSize * smitheryMaxPages
)

// smitheryBrowseNote is what the page says about the bound above.
//
// Said in the response rather than only in this file, because the operator who
// needs to know is the one looking at the last row of the list and wondering
// where the other ten thousand are.
var smitheryBrowseNote = fmt.Sprintf(
	"Smithery's listing stops after %d servers of the ten thousand it holds, "+
		"most used first; search to reach the rest, which asks Smithery "+
		"directly rather than filtering this list.", smitheryBrowseCap)

// Smithery reads Smithery's registry.
//
// It holds no credential. Browsing Smithery costs nothing and needs nobody's
// key, so an operator with no Smithery account still gets the catalogue, the
// descriptions and the search. What needs a key is *dialling* one of these
// servers, and that key is asked for at import, by the composed document, in
// the ordinary way -- see composeSmitheryDocument.
type Smithery struct {
	base    string
	gateway string
	client  *http.Client
	agent   string
	limit   int
}

// SmitheryOptions configures the client. Every field has a working default, so
// the zero value is usable and a test can replace exactly what it needs.
type SmitheryOptions struct {
	// BaseURL overrides the registry address. For tests.
	BaseURL string
	// GatewayURL overrides where composed documents are pointed. For tests.
	GatewayURL string
	// HTTPClient overrides the transport. The default refuses redirects and
	// bounds the call.
	HTTPClient *http.Client
	// UserAgent identifies this host.
	UserAgent string
	// Limit is the page size this source serves. Zero takes defaultLimit.
	Limit int
}

// NewSmithery builds a client for Smithery's registry. It fetches nothing.
func NewSmithery(opts SmitheryOptions) *Smithery {
	base := strings.TrimSuffix(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = SmitheryBaseURL
	}
	gateway := strings.TrimSuffix(strings.TrimSpace(opts.GatewayURL), "/")
	if gateway == "" {
		gateway = SmitheryGatewayURL
	}
	return &Smithery{
		base:    base,
		gateway: gateway,
		client:  catalogueClient(opts.HTTPClient),
		agent:   userAgent(opts.UserAgent),
		limit:   pageLimit(opts.Limit),
	}
}

// Source names the catalogue.
func (s *Smithery) Source() string { return smitherySource }

// List returns one page of Smithery's registry.
func (s *Smithery) List(ctx context.Context, q Query) (Page, error) {
	return s.ListIfChanged(ctx, q, Validators{})
}

// ListIfChanged is List with the previous answer's validators.
//
// Smithery sends no ETag and no Last-Modified, so the conditional request is
// written for a catalogue that ignores it -- the same judgement official.go
// makes, for the same reason: the cost is a header the far end drops, and a
// registry that starts sending a validator should not need a code change to be
// believed. What Smithery does send is a policy, `max-age=60, s-maxage=14400,
// stale-while-revalidate=86400`, and the cache reads it: four hours fresh for
// a shared cache rather than the browser's sixty seconds, and a stale window
// clamped to the six-hour ceiling.
func (s *Smithery) ListIfChanged(ctx context.Context, q Query, v Validators) (Page, error) {
	// Normalised here as well as at the cache, so the client is bounded
	// whether or not something cached it.
	q = q.Normalised()
	rows, total, freshness, err := s.fetchWindow(ctx, q.Search, v)
	if err != nil {
		return Page{Freshness: freshness}, err
	}

	ranked := make([]rankedServer, 0, len(rows))
	for _, raw := range rows {
		entry, _, ok := s.translate(raw)
		if !ok {
			continue
		}
		ranked = append(ranked, rankedServer{entry: entry, key: smitheryRankKey(raw)})
	}
	// Most used first. Smithery's own page order is by popularity but is not
	// a total order -- it repeats rows across pages, which is why the window
	// is deduplicated before anything is paged out of it -- so the order is
	// rebuilt here from the numbers it publishes, and rebuilt as a total one
	// so that the cursor below is a resume point rather than an offset into a
	// list that reshuffles.
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].key < ranked[j].key })

	// Measured over the whole window rather than the page, because the window
	// is what makes it a sample worth quoting: five hundred rows deduplicated
	// to a few hundred distinct servers, every one of them already put through
	// describe() to get here. See estimateAddable for what is done with it,
	// including why the sample being Smithery's most popular matters.
	judged := len(ranked)
	addable := 0
	for _, r := range ranked {
		if r.entry.Addable {
			addable++
		}
	}

	limit := q.Limit
	if limit <= 0 {
		limit = s.limit
	}
	if after := q.Cursor; after != "" {
		i := sort.Search(len(ranked), func(i int) bool { return ranked[i].key > after })
		ranked = ranked[i:]
	}
	next := ""
	if len(ranked) > limit {
		next = ranked[limit-1].key
		ranked = ranked[:limit]
	}
	entries := make([]Entry, 0, len(ranked))
	for _, r := range ranked {
		entries = append(entries, r.entry)
	}

	page := Page{
		Source:      s.Source(),
		Entries:     entries,
		NextCursor:  next,
		RetrievedAt: time.Now().UTC(),
		Freshness:   freshness,
	}
	status := SourceStatus{
		Source: s.Source(), OK: true,
		RetrievedAt: page.RetrievedAt, Entries: len(page.Entries),
		Judged: judged, Addable: addable, Total: total,
	}
	// The note is attached to a browse and not to a search, because it is only
	// true of a browse. A search was answered by Smithery over the whole
	// catalogue, so there is no truncation to warn about.
	if q.Search == "" {
		status.Note = smitheryBrowseNote
	}
	page.Sources = []SourceStatus{status}
	return page, nil
}

// Get returns one entry and the server.json composed from it.
func (s *Smithery) Get(ctx context.Context, name string) (Detail, error) {
	return s.GetIfChanged(ctx, name, Validators{})
}

// GetIfChanged is Get with the previous answer's validators.
//
// The entry route is used rather than a scan of the listing, and it has to be:
// the listing reaches five hundred servers and a search reaches the other ten
// thousand, so a name an operator found by searching is very often one no
// listing page holds.
func (s *Smithery) GetIfChanged(ctx context.Context, name string, v Validators) (Detail, error) {
	wanted := opaque(name, maxNameRunes)
	if wanted == "" {
		return Detail{}, ErrNotFound
	}
	var raw smitheryDetail
	freshness, err := fetchJSON(ctx, s.client, s.Source(),
		s.base+"/servers/"+url.PathEscape(wanted), s.agent, v, nil, &raw)
	if err != nil {
		return Detail{Freshness: freshness}, err
	}
	// The entry route answers for a name the listing never showed, so the
	// name is taken from the answer rather than from the request: a catalogue
	// that redirected one name onto another would otherwise have this host
	// compose a document under the name that was asked for.
	if opaque(raw.QualifiedName, maxNameRunes) == "" {
		return Detail{}, ErrNotFound
	}
	entry, document, ok := s.translate(raw.listing())
	if !ok {
		return Detail{}, ErrNotFound
	}
	return Detail{
		Entry:       entry,
		Document:    document,
		RetrievedAt: time.Now().UTC(),
		Freshness:   freshness,
	}, nil
}

// fetchWindow reads as much of Smithery's answer as Smithery will give.
//
// Page one is fetched first because it is the only way to learn how many there
// are; the rest are fetched together, because five requests one after another
// inside an eight-second fan-out budget is how a catalogue gets dropped from
// the page it was too slow for. A failure on any page after the first is
// fatal to the fetch rather than silently short: half a window presented as a
// whole one is the truncation this source exists to be honest about.
//
// Duplicates are removed here, and they are not hypothetical. Measured against
// the live API, the five hundred rows of the browse window hold two hundred
// and sixty-nine distinct servers -- page one and page two alone share
// thirty-nine. Smithery orders by popularity, and the order is not a total one,
// so a row can land on two pages. Page one is stable when refetched, so this
// is not jitter that a retry would fix.
func (s *Smithery) fetchWindow(ctx context.Context, search string, v Validators) ([]smitheryServer, int, Freshness, error) {
	first, freshness, err := s.fetchPage(ctx, search, 1, v)
	if err != nil {
		return nil, 0, freshness, err
	}
	// What Smithery says it holds, which on a browse is the whole catalogue
	// and on a search is the number of matches. The only one of the four
	// sources that answers the question at all; see estimateAddable.
	total := first.Pagination.TotalCount
	if total < 0 {
		total = 0
	}

	pages := first.Pagination.TotalPages
	if pages > smitheryMaxPages {
		pages = smitheryMaxPages
	}
	rows := first.Servers
	if pages > 1 && len(first.Servers) > 0 {
		type result struct {
			page smitheryPage
			err  error
		}
		results := make([]result, pages+1)
		var wg sync.WaitGroup
		for p := 2; p <= pages; p++ {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				// Validators are deliberately not resent on pages after the
				// first. They belong to whatever the cache last held, which
				// is the whole window; a 304 on page three would mean this
				// host had no page three, not that it could reuse one.
				page, _, err := s.fetchPage(ctx, search, p, Validators{})
				results[p] = result{page: page, err: err}
			}(p)
		}
		wg.Wait()
		for p := 2; p <= pages; p++ {
			if err := results[p].err; err != nil {
				return nil, 0, Freshness{}, err
			}
			rows = append(rows, results[p].page.Servers...)
		}
	}

	seen := make(map[string]bool, len(rows))
	out := make([]smitheryServer, 0, len(rows))
	for _, row := range rows {
		if len(out) >= MaxCatalogEntries {
			break
		}
		name := opaque(row.QualifiedName, maxNameRunes)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, row)
	}
	return out, total, freshness, nil
}

// fetchPage performs one bounded, optionally conditional GET of one page.
func (s *Smithery) fetchPage(ctx context.Context, search string, page int, v Validators) (smitheryPage, Freshness, error) {
	values := url.Values{}
	values.Set("pageSize", strconv.Itoa(smitheryPageSize))
	values.Set("page", strconv.Itoa(page))
	if search != "" {
		// Upstream, not a local filter. Smithery answers q= over the whole
		// catalogue, which is the only route to the ten thousand servers the
		// listing will not page to.
		values.Set("q", search)
	}
	var out smitheryPage
	freshness, err := fetchJSON(ctx, s.client, s.Source(),
		s.base+"/servers?"+values.Encode(), s.agent, v, nil, &out)
	if err != nil {
		return smitheryPage{}, freshness, err
	}
	if len(out.Servers) > MaxEntriesPerPage {
		out.Servers = out.Servers[:MaxEntriesPerPage]
	}
	return out, freshness, nil
}

// rankedServer is one entry with the sort key that decides where it lands.
type rankedServer struct {
	entry Entry
	key   string
}

// smitheryRankKey orders Smithery's window: most used first.
//
// This is the one real quality signal any of the four catalogues publishes.
// Smithery counts calls to each server it hosts and reports the count on every
// listing row, and the numbers are not close -- the head of the catalogue is
// in the tens of thousands and the tail is at zero. Ordering by it is the
// difference between "here are ten servers people use" and "here are ten
// servers", and the second is what the front page used to be.
//
// Verified is a tiebreak and deliberately not an override: it is Smithery
// vouching for a listing, which is worth something between two servers nobody
// has called yet and worth nothing against a server with fifty thousand calls
// behind it. Name is the final discriminator, which is what makes the ordering
// total -- and a total order is what lets the key below be a cursor.
//
// The key is a string because that is what a cursor is. Fixed-width fields in
// descending-as-ascending form, so that plain string comparison is the
// ordering and sort.Search can resume from one: twenty digits of
// MaxInt64-useCount, then one digit of verified, then the name. A name
// containing the separator is harmless, since everything before the name is
// fixed width.
//
// Deliberately not applied to the other three sources. None of them publishes
// a usage figure, and PulseMCP's visitor estimates -- weekly uniques on a
// listing page -- are not the same measurement as a call count and would not
// survive being added to one. A cross-source score composed out of those would
// be a normalisation this host invented, presented as a ranking. Each source
// is ordered by the best signal it actually has, and Multi interleaves them.
func smitheryRankKey(raw smitheryServer) string {
	uses := raw.UseCount
	if uses < 0 {
		uses = 0
	}
	verified := 1
	if raw.Verified {
		verified = 0
	}
	return fmt.Sprintf("%020d|%d|%s", math.MaxInt64-uses, verified, raw.QualifiedName)
}

// --- Smithery's wire format -------------------------------------------------

type smitheryPage struct {
	Servers    []smitheryServer `json:"servers"`
	Pagination struct {
		CurrentPage int `json:"currentPage"`
		PageSize    int `json:"pageSize"`
		TotalPages  int `json:"totalPages"`
		TotalCount  int `json:"totalCount"`
	} `json:"pagination"`
}

// smitheryServer is one row of the listing. Only the parts this host acts on
// are modelled; use counts, icons, ownership and scores are Smithery's own
// business.
type smitheryServer struct {
	QualifiedName string `json:"qualifiedName"`
	DisplayName   string `json:"displayName"`
	Description   string `json:"description"`
	// Remote says Smithery hosts it rather than describing something to run
	// locally; IsDeployed says the hosted copy is actually up. Both, because
	// they are different claims and only the pair means "there is an address
	// behind this right now".
	//
	// In the live catalogue `remote` is the flag that discriminates: across
	// 2,175 distinct servers sampled on 2026-08-23, none had one without the
	// other, while 178 had `remote: false` with `isDeployed: true`. Both are
	// checked anyway, because they are two claims and the second is the one
	// that would stop an Add button dialling a server Smithery has listed but
	// not stood up.
	Remote     bool   `json:"remote"`
	IsDeployed bool   `json:"isDeployed"`
	CreatedAt  string `json:"createdAt"`
	// IconURL is Smithery's own icon for the server, which it serves for most
	// of them. Sometimes a favicon service's URL rather than the project's
	// own image; either way it is a third party's address bound for an
	// <img src>, so it is validated rather than relayed.
	IconURL string `json:"iconUrl"`
	// UseCount is how many times Smithery has been asked to call this server,
	// and Verified is Smithery vouching for it. They are the only usage
	// signal any of the four catalogues publishes, and they are what orders
	// this source's listing. See smitheryRankKey.
	UseCount int64 `json:"useCount"`
	Verified bool  `json:"verified"`
}

// smitheryDetail is the entry route's answer, which is shaped differently from
// a listing row.
type smitheryDetail struct {
	QualifiedName string `json:"qualifiedName"`
	DisplayName   string `json:"displayName"`
	Description   string `json:"description"`
	Remote        bool   `json:"remote"`
	IconURL       string `json:"iconUrl"`
	UseCount      int64  `json:"useCount"`
	Verified      bool   `json:"verified"`
	// DeploymentURL is non-empty exactly when the listing would have said
	// isDeployed: it is the address Smithery has stood up. It is read as that
	// flag and not used as the endpoint -- see composeSmitheryDocument for why
	// the gateway is dialled instead.
	DeploymentURL string `json:"deploymentUrl"`
}

// listing turns an entry-route answer into the row shape, so that one
// translation serves both routes and the two cannot compose different
// documents for one server.
//
// CreatedAt is left empty because the entry route does not carry it, so an
// entry fetched by name reports a zero UpdatedAt where the same entry in a
// listing reports a date. That is the honest gap rather than a bug to paper
// over: substituting the time of the fetch would say the server changed just
// now, which is the reading that misleads.
func (d smitheryDetail) listing() smitheryServer {
	return smitheryServer{
		QualifiedName: d.QualifiedName,
		DisplayName:   d.DisplayName,
		Description:   d.Description,
		Remote:        d.Remote,
		IsDeployed:    strings.TrimSpace(d.DeploymentURL) != "",
		IconURL:       d.IconURL,
		UseCount:      d.UseCount,
		Verified:      d.Verified,
	}
}

// --- translation ------------------------------------------------------------

// translate turns one Smithery row into an Entry and the server.json that
// would be imported.
//
// The third result is false for a row with nothing to show, which is a row
// nothing can be done with.
func (s *Smithery) translate(raw smitheryServer) (Entry, json.RawMessage, bool) {
	// Not cleaned: QualifiedName is the identifier the dashboard sends back to
	// the entry route, so a truncated or rewritten one is a row that 404s when
	// somebody clicks it. The same rule the other two sources follow.
	name := opaque(raw.QualifiedName, maxNameRunes)
	if name == "" {
		return Entry{}, nil, false
	}
	title := clean(raw.DisplayName, maxTitleRunes)
	if title == "" {
		title = SuggestName(name)
	}
	entry := Entry{
		Name:          name,
		SuggestedName: SuggestName(name),
		Title:         title,
		Description:   clean(raw.Description, maxDescriptionRunes),
		// Deliberately empty. Smithery versions a deployment, not a published
		// release, and there is no version of the *server* to show; a
		// placeholder rendered as one would be a claim nobody made. The
		// composed document carries the placeholder server.json insists on and
		// says in its own field that it is one. The same judgement docker.go
		// makes for the same reason.
		Version:   "",
		Icon:      safeIconURL(raw.IconURL),
		UpdatedAt: smitheryTimestamp(raw.CreatedAt),
		Source:    smitherySource,
	}

	// The address is filled in for every hosted row, addable or not, so that
	// an operator can see what they are being refused and so cross-source
	// deduplication has something to match on.
	if raw.Remote {
		entry.Transport = mcpservers.TransportStreamableHTTP
		entry.URL = s.endpoint(name)
	}

	document, reason := s.composeSmitheryDocument(name, title, raw)
	if reason != "" {
		entry.Reason = clean(reason, maxReasonRunes)
		return entry, nil, true
	}

	// Addability is decided by handing the composed document to the parser the
	// import endpoint uses, exactly as the other sources do. Composing here
	// and judging by a different rule would let this source offer something
	// the import path refuses.
	transport, endpoint, addable, describeReason, auth := describe(document)
	entry.Addable = addable
	entry.Reason = clean(describeReason, maxReasonRunes)
	entry.Auth = auth
	if !addable {
		return entry, nil, true
	}
	entry.Transport, entry.URL = transport, endpoint
	return entry, document, true
}

// endpoint is where a Smithery-hosted server is dialled.
//
// Every one of them, at one address shape. The qualifiedName is path-escaped
// because it may carry a slash -- "onesignal/onesignal" -- and the gateway
// takes it either way; escaping is what keeps a name with a stranger character
// in it from becoming a different path.
func (s *Smithery) endpoint(qualifiedName string) string {
	var parts []string
	for _, segment := range strings.Split(qualifiedName, "/") {
		parts = append(parts, url.PathEscape(segment))
	}
	return s.gateway + "/" + strings.Join(parts, "/") + "/mcp"
}

// composeSmitheryDocument builds the server.json for one Smithery row, or says
// why there is none to build.
//
// The credential is a header with a placeholder behind it, marked secret --
// the same shape docker.go composes for `Authorization: "Bearer ${...}"`, and
// deliberately so. It is what makes the operator's Smithery key land where
// every other credential lands: typed into the dashboard, encrypted at rest,
// resolved store-then-file-then-default, and never written into the stored
// document. The document says a key is needed and names it; it never holds one.
//
// One key opens every Smithery server, which is a real argument for holding it
// once rather than once per import -- and it is not the shape taken, because
// the alternatives are worse. A key held beside the catalogue would have to be
// substituted into the document to be used, which is a credential in a stored,
// hashed document; or resolved at dial time from a store the plugin does not
// belong to, which is a second credential path beside the one every other
// plugin uses and a per-plugin scoping rule with a hole in it. What is bought
// by neither is worth less than what is lost: an operator importing four
// Smithery servers pastes the same key four times, into four fields that each
// say what they are for. That is the honest cost and it is the one taken.
//
// Byte-stable, because the import path hashes what it stores: the same row
// composes the same bytes every time, so a re-import is recognisably the same
// document rather than a new one.
func (s *Smithery) composeSmitheryDocument(name, title string, raw smitheryServer) (json.RawMessage, string) {
	switch {
	case !raw.Remote:
		return nil, "Smithery lists this as a server to run yourself rather than " +
			"one it hosts; this host connects to remote MCP servers over the " +
			"network and does not run packaged servers"
	case !raw.IsDeployed:
		return nil, "Smithery lists this server as hosted but has not deployed it, " +
			"so there is no address to connect to yet"
	}

	doc := map[string]any{
		"$schema": mcpservers.SchemaURI,
		// Derived, not published. See smitheryNamespace.
		"name":  smitheryNamespace + "/" + smitheryDocumentName(name),
		"title": title,
		// server.json caps a description at a hundred characters and
		// Smithery's run to several hundred, so the document carries the
		// format's length and the Entry above carries the whole of it.
		"description": smitheryDescription(raw.Description, title),
		// server.json requires a version and Smithery does not version a
		// hosted server. 0.0.0 is the honest unversioned value: it sorts below
		// every real release and claims nothing about upstream.
		"version": "0.0.0",
		"remotes": []any{map[string]any{
			"type": mcpservers.TransportStreamableHTTP,
			"url":  s.endpoint(name),
			"headers": []any{map[string]any{
				"name":  "Authorization",
				"value": "Bearer {" + smitheryKeyVariable + "}",
				"variables": map[string]any{
					smitheryKeyVariable: map[string]any{
						"isSecret":   true,
						"isRequired": true,
						"description": "Your Smithery API key, from " +
							"smithery.ai/account/api-keys. One key reaches every " +
							"server Smithery hosts.",
					},
				},
			}},
		}},
		"_meta": map[string]any{
			// Provenance travels with the document, so a server imported six
			// months ago can still say where its description came from. The
			// key is reverse-DNS because _meta is a shared namespace.
			"io.mcpd/catalogue-source": map[string]any{
				"source": smitherySource,
				"name":   name,
				"origin": "https://smithery.ai/server/" + name,
			},
		},
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, "this host could not compose a server.json from the catalogue's entry"
	}
	if len(encoded) > MaxDocumentBytes {
		return nil, fmt.Sprintf(
			"the composed document is %d KiB, and this host stores at most %d KiB",
			len(encoded)>>10, MaxDocumentBytes>>10)
	}
	return encoded, ""
}

// smitheryDocumentName reduces a qualifiedName to server.json's name charset:
// letters, digits, dot, underscore and dash. A qualifiedName's own slash is
// one of the characters that has to go, since the document's name already has
// its one slash between the namespace and this.
func smitheryDocumentName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "server"
	}
	return out
}

// smitheryDescription fits Smithery's prose into server.json's hundred
// characters, falling back to the title when there is no prose -- the format
// requires a description, and a document refused for having none would list a
// server nobody could add for a reason that is this host's own doing.
func smitheryDescription(description, title string) string {
	out := clean(description, dockerDescriptionMax)
	if out == "" {
		out = clean(title, dockerDescriptionMax)
	}
	if out == "" {
		out = "An MCP server hosted by Smithery."
	}
	return out
}

// smitheryTimestamp reads a row's createdAt, which is RFC 3339. A row with an
// unreadable one keeps the zero time rather than today's date, because "we do
// not know when this changed" and "it changed just now" are different facts
// and the second is the one that misleads.
func smitheryTimestamp(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

var _ Revalidating = (*Smithery)(nil)
