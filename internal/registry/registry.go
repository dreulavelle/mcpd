// Package registry browses public catalogues of MCP servers, so an operator
// can pick one instead of hand-authoring a server.json.
//
// What it produces is a server.json document and nothing else. Whether to
// import one, how it is validated, what settings it asks for and how it mounts
// all belong to the paths that already do those things for a pasted document.
// A catalogue is a place to find a document, not a second way to install one.
//
// Everything here treats the far end as untrusted text. A registry entry is
// somebody else's prose and somebody else's URL, arriving over the network in
// whatever quantity they choose to send, so every field is bounded and every
// response is capped before any of it is stored or returned.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/plugins/mcpremote"
	"github.com/spoked/mcpd/internal/settings"
)

// ErrNotFound reports a name no catalogue has.
var ErrNotFound = errors.New("registry: no such server")

// Bounds on what a catalogue may hand back. They are generous next to any
// honest entry and far short of what would make one a problem to hold.
const (
	// MaxResponseBytes caps one HTTP response. A hundred entries of the
	// official registry run to roughly 90 KiB, so this is twenty times the
	// working size and still a bounded read.
	MaxResponseBytes = 2 << 20
	// MaxEntriesPerPage caps how many entries one page may contribute,
	// whatever the far end returns for the limit it was asked for.
	MaxEntriesPerPage = 100
	// MaxDocumentBytes caps one stored server.json. An entry whose document
	// is larger is listed as unavailable rather than truncated: a truncated
	// document is not a document.
	MaxDocumentBytes = 64 << 10

	maxNameRunes        = 256
	maxTitleRunes       = 256
	maxVersionRunes     = 64
	maxURLRunes         = 2048
	maxDescriptionRunes = 2048
	maxReasonRunes      = 512
	maxQueryRunes       = 128
	maxCursorRunes      = 512
	maxSchemaLabelRunes = 96
)

// Entry is one server a catalogue offers, as the dashboard sees it.
type Entry struct {
	// Name is the catalogue's own identifier, reverse-DNS with a path segment
	// -- "io.github.example/weather". It is not a legal plugin name here,
	// which is what SuggestedName is for.
	Name string `json:"name"`
	// SuggestedName is a local instance name derived from Name, ready to
	// prefill the import form. The operator may change it; nothing depends on
	// it being what the catalogue said.
	SuggestedName string `json:"suggested_name"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Version       string `json:"version"`
	// Transport and URL come from the document this host would actually dial,
	// not from whatever the catalogue listed first. Empty when the document
	// offers nothing this host can reach.
	Transport string `json:"transport,omitempty"`
	URL       string `json:"url,omitempty"`
	// UpdatedAt is when the catalogue last changed the entry.
	UpdatedAt time.Time `json:"updated_at"`
	// Uses is how many times this entry's own catalogue has been asked to
	// call the server. Absent where the catalogue publishes no such figure,
	// which is every source but PulseMCP -- including an operator's own list,
	// which is a list rather than a service and counts nothing.
	//
	// A pointer, and that is the point of it. Absent and zero are different
	// facts -- "nobody has told us" against "nobody has called it" -- and a
	// plain int64 collapses them into a number that sorts with the servers
	// nobody uses and renders as a count somebody measured. Neither is true.
	//
	// It is a count of calls, and only a count of calls. A catalogue that
	// publishes some other popularity figure -- PulseMCP's weekly unique
	// visitors to a listing page is the one that exists -- must leave this
	// empty rather than fill it in: two different measurements under one
	// field is a cross-source ranking invented at the point of assignment,
	// and the number is rendered to an operator as a fact they can check.
	Uses *int64 `json:"uses,omitempty"`
	// Addable reports that this host would accept the document. It is decided
	// by handing the document to the same parser the import endpoint uses, so
	// an entry offered here is one that can actually be imported.
	Addable bool `json:"addable"`
	// Reason says why not, when Addable is false. A server with only packages
	// and no remotes is the common case: this host does not run packaged
	// servers, and offering an Add button that cannot work is worse than
	// saying so.
	Reason string `json:"reason,omitempty"`
	// Auth says whether importing this entry will ask the operator for a
	// credential: AuthNone, AuthAPIKey or AuthUnknown, and empty where there
	// is nothing to say because the entry cannot be imported at all.
	//
	// It is derived from the document rather than from anything a catalogue
	// claims about the server, so it means the same thing on all four sources
	// -- and it means the thing an operator actually needs to know before
	// clicking Add, which is whether they need to go and find a key first.
	// See authFromDocument.
	Auth string `json:"auth,omitempty"`
	// Source names the catalogue this entry came from. On a page merged from
	// more than one it is the only thing that distinguishes them, and it is
	// set on every entry rather than only on merged pages so that a consumer
	// never has to know which kind of page it is holding.
	Source string `json:"source"`
}

// Detail is one entry together with the document that would be imported.
//
// The document is the catalogue's bytes, re-encoded from what was decoded and
// nothing more: it is handed to the ordinary import endpoint, which stores what
// it is given verbatim and validates it there.
type Detail struct {
	Entry `json:",inline"`
	// Document is the server.json itself, as an object, so a dashboard can
	// post it to the import endpoint without re-encoding it.
	//
	// The catalogue this came from is Entry.Source, promoted through the
	// embedding. There is deliberately no second Source field here: two
	// fields encoding to one JSON key would let them disagree, and the one
	// that reached the wire would be decided by Go's shadowing rules rather
	// than by anything a reader could see.
	Document json.RawMessage `json:"document"`
	// Stale reports that the catalogue could not be reached and this is what
	// was last seen. It is never an error: a browsable stale list beats a
	// page that refuses to render because a third party is down.
	Stale bool `json:"stale"`
	// RetrievedAt is when the data was actually fetched, which is what makes
	// Stale readable rather than merely alarming.
	RetrievedAt time.Time `json:"retrieved_at"`
	// Freshness is what the catalogue said about reusing this answer. Read by
	// the cache and not by the dashboard, which is told about staleness by the
	// two fields above.
	Freshness Freshness `json:"-"`
}

// Page is one page of a browse or search.
type Page struct {
	Source  string  `json:"source"`
	Entries []Entry `json:"entries"`
	// NextCursor is opaque and belongs to the catalogue. Empty means the end.
	NextCursor  string    `json:"next_cursor,omitempty"`
	Stale       bool      `json:"stale"`
	RetrievedAt time.Time `json:"retrieved_at"`
	// Freshness is what the catalogue said about reusing this answer. Not part
	// of the wire shape; see Detail.Freshness.
	Freshness Freshness `json:"-"`
	// Addable is how many servers across these catalogues this host would
	// accept an import of. Counted, not sampled: every document behind it was
	// walked and handed to the same parser the import endpoint uses.
	//
	// It exists because the size of the catalogue is the question a page of
	// ten cannot answer, and getting it wrong is what made an operator looking
	// at thirty rows conclude there were ninety servers in the world. It was
	// an extrapolation from one window until the index started holding every
	// catalogue locally; the name went with the estimate.
	//
	// Zero means nothing could be said, which is different from zero servers
	// and is why the field is omitted rather than sent as 0.
	Addable int `json:"addable,omitempty"`
	// Sources says which catalogues this page was assembled from and how each
	// of them fared.
	//
	// A page built from more than one catalogue is not honest without it. One
	// source being down while another answers produces a shorter list, not an
	// error, and a shorter list that does not say a source is missing reads as
	// "there is nothing else" rather than as "we could not ask". A single
	// source reports itself here too, so a consumer has one shape to read.
	Sources []SourceStatus `json:"sources"`
}

// SourceStatus is how one catalogue fared on one request.
type SourceStatus struct {
	Source string `json:"source"`
	// OK is false when the catalogue could not be reached and nothing was
	// held for it. An OK false source contributed no entries.
	OK bool `json:"ok"`
	// Stale reports that the catalogue could not be reached and what it last
	// said was served instead.
	Stale bool `json:"stale"`
	// RetrievedAt is when this source's data was actually fetched.
	RetrievedAt time.Time `json:"retrieved_at,omitempty"`
	// Entries is how many this source contributed after deduplication, which
	// is what makes "this source answered" a checkable claim rather than a
	// flag.
	Entries int `json:"entries"`
	// Judged is how many of this source's documents this host actually parsed
	// while producing this answer, and Addable is how many of those it would
	// accept. Together they are the measured counts behind Page.Addable, and
	// they are reported so that the estimate is a claim somebody can check
	// rather than a number that appears.
	//
	// Their scope is whatever the source materialised. Docker holds its whole
	// catalogue in one document and Smithery fetches its whole browse window,
	// so for those two Judged covers everything they can offer or a stable
	// sample of it. The two Generic-API sources page an opaque cursor, so
	// Judged is one page of theirs and nothing more.
	Judged  int `json:"judged,omitempty"`
	Addable int `json:"addable,omitempty"`
	// Uses reports a catalogue that publishes how often each of its servers
	// is called, which is what makes sort=most-used possible over it.
	//
	// It is on the response so that a dashboard can offer that order for the
	// catalogues that support it and name them, rather than hard-coding which
	// ones do. A source scoped away by the caller is not in this
	// list at all, so a consumer that wants the full set reads it from an
	// unscoped page.
	Uses bool `json:"uses,omitempty"`
	// Total is how many servers this source says it holds altogether, when it
	// says. Absent where it does not, and absent is not zero.
	//
	// It exists because a page of ten out of twelve thousand looks exactly
	// like a catalogue of ten, and an operator who reads the second is
	// deciding against a thing that is not true. Few sources can answer it --
	// Smithery reports a totalCount, Docker's whole catalogue is held in one
	// document so its length is the count, and an operator's own list is read
	// whole for the same reason -- which is why what the dashboard renders
	// from these is a floor and says so.
	Total int `json:"total,omitempty"`
	// Error says what went wrong, when OK is false. It is a third party's
	// failure, not this host's, and the operator deciding whether to wait
	// needs to see which.
	Error string `json:"error,omitempty"`
	// Note is what a source has to say about a page it *did* answer.
	//
	// Not an error and not a warning about this deployment: it is a fact about
	// the answer that the answer itself cannot show. Smithery is the reason it
	// exists -- its listing stops at five hundred of ten thousand servers, and
	// a page whose last row is the five hundredth looks exactly like the end
	// of a catalogue. Saying so is the difference between a bound and a lie by
	// omission.
	Note string `json:"note,omitempty"`
}

// Query is one browse or search request.
type Query struct {
	// Search matches server names as a substring. Empty browses everything.
	Search string
	// Cursor continues a previous page. Empty starts at the beginning.
	Cursor string
	// Limit bounds one page. Zero takes the client's default.
	//
	// It bounds the page a caller receives, not what each catalogue is asked
	// for. Multi asks its sources for more than this and pages the merged,
	// deduplicated, filtered result down to it, because a limit that is
	// applied per source is a limit that means nothing: three sources each
	// honouring "ten" produced thirty rows and a listing that could not say
	// how large the catalogue was.
	Limit int
	// IncludeUnaddable keeps entries this host could not import in the
	// listing. Off by default, because roughly half of what the catalogues
	// publish only runs locally and a page of ten that spends five rows
	// saying so is a page of five.
	//
	// Read by Multi, which is what pages and therefore what filters. A source
	// always returns everything it has, with Addable and Reason on every row:
	// the machinery that decides addability is not weakened by a listing
	// choosing not to show its refusals, and GET /api/catalog/{name} still
	// explains one.
	IncludeUnaddable bool
	// Source scopes the listing to one catalogue by name. Empty covers them
	// all.
	//
	// The four differ in kind rather than only in contents -- Smithery hosts
	// and keys what it lists, the official registry is where a publisher
	// registers their own, Docker's is curated -- so which one an entry came
	// from is a real thing to filter on, and it is one this host can answer
	// completely rather than estimate.
	//
	// Read by Multi, like Sort. A source knows nothing about the others and
	// is never asked to filter itself out, so neither field reaches a
	// catalogue's own client or its cache key.
	Source string
	// Sort is the order the assembled page is put in. See Sort.
	Sort Sort
}

// Normalised returns the query as it will actually be asked, with every bound
// already applied.
//
// It exists so that a cache key and the request it stands for cannot disagree.
// A query arrives from a URL, so it is whatever somebody typed: "weather " and
// "weather" are one upstream search, a cursor past the bound is dropped rather
// than sent, and a limit outside the permitted range becomes the client's
// default. Keying on the raw form would hold several entries for one answer,
// and -- worse -- would file page one under the key of a page it is not.
//
// It is also what bounds the key itself. The cache caps how many entries it
// holds; without this it would cap nothing about how large one of them is.
//
// Idempotent, so a client that applies it again gets the same query.
func (q Query) Normalised() Query {
	if q.Limit < 0 || q.Limit > MaxEntriesPerPage {
		// Out of range means "no usable preference", which is the same thing
		// zero means: take whatever the client's default is.
		q.Limit = 0
	}
	return Query{
		Search:           clean(q.Search, maxQueryRunes),
		Cursor:           opaque(q.Cursor, maxCursorRunes),
		Limit:            q.Limit,
		IncludeUnaddable: q.IncludeUnaddable,
		// A source name is matched against this host's own configured names,
		// so an unrecognisable one is refused rather than dropped -- see
		// Multi.scopeFor. Bounded here all the same, because what arrives is
		// a caller's text and the refusal quotes it back.
		Source: clean(q.Source, maxNameRunes),
		Sort:   q.Sort.normalise(),
	}
}

// Client is a catalogue of MCP servers.
//
// An interface so a catalogue can be added without touching a caller. There
// are two real ones -- the official registry and Docker's -- plus the TTL
// cache and the multiplexer, which are Clients over Clients: the cache goes in
// front of each source so that one being down is one source's staleness rather
// than the page's, and Multi goes in front of the caches so that the handler
// still talks to a single catalogue.
type Client interface {
	// List returns one page of servers.
	List(ctx context.Context, q Query) (Page, error)
	// Get returns one entry and the document that would be imported.
	Get(ctx context.Context, name string) (Detail, error)
	// Source names the catalogue, for the operator and for cache keys.
	Source() string
}

// readBounded reads at most max bytes from a catalogue's response.
//
// Bounded before decoding, not after: a decoder reading an unbounded body from
// a third party is a memory limit set by somebody else. A body that exceeds
// the bound returns nil rather than what fitted, because half a catalogue is
// not a catalogue and the caller has to be able to tell the difference.
func readBounded(body io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, nil
	}
	return data, nil
}

// --- bounding untrusted text -----------------------------------------------

// clean bounds one field of a catalogue's prose.
//
// Control characters go first and are replaced rather than dropped, so that a
// name smuggling a newline or a bidirectional override cannot be rendered as
// something other than what it is. The length bound is in runes and truncates
// on a rune boundary, because a byte-sliced UTF-8 string is a string no
// consumer can decode.
func clean(s string, maxRunes int) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			b.WriteRune('�')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) <= maxRunes {
		return out
	}
	runes := []rune(out)
	return strings.TrimSpace(string(runes[:maxRunes])) + "…"
}

// opaque bounds a value that must survive unchanged or not at all.
//
// A pagination cursor belongs to the catalogue and means nothing here, so the
// two things that can be done with a strange one are pass it and drop it.
// Cleaning it the way prose is cleaned would mangle a working cursor into a
// broken one, and the failure would present as pagination that stops halfway
// with no explanation. Dropping it ends the listing, which is at least a state
// the caller already handles.
func opaque(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) > maxRunes {
		return ""
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			return ""
		}
	}
	return s
}

// SuggestName derives a local plugin name from a catalogue name.
//
// A catalogue name is reverse-DNS with a path segment -- "io.github.foo/bar"
// -- and none of that is a legal plugin name here, which must be lowercase
// letters, digits, dashes or underscores, two to thirty-two characters,
// starting with a letter. The last path segment is the part a person would
// call it, so that is what this keeps.
//
// The result is a suggestion the operator can overwrite. It is not checked for
// collisions with what is already installed; the import endpoint does that and
// says so plainly, which is the right place for it.
func SuggestName(catalogueName string) string {
	base := catalogueName
	if _, after, found := strings.Cut(catalogueName, "/"); found && after != "" {
		base = after
	}
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(base) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case b.Len() > 0 && !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	// A name must start with a letter: "3d-tools" is a reasonable thing for
	// somebody to publish and not a legal instance name, so it is prefixed
	// rather than refused.
	if name == "" {
		return "server"
	}
	if name[0] < 'a' || name[0] > 'z' {
		name = "s-" + name
	}
	if len(name) < 2 {
		name += "-server"
	}
	if len(name) > 32 {
		name = strings.TrimRight(name[:32], "-_")
	}
	return name
}

// describe turns a decoded document into the parts of an Entry that come from
// it, and says whether this host would accept it.
//
// The judgement is made by the calls the import endpoint makes, not by looking
// for a remotes array. Those differ in ways that matter: a document offering
// only sse, or plaintext http to a public host, or a credential written into
// its own text, parses as having remotes and is still refused at import. An
// entry offered as addable here has to be one that actually imports.
//
// Both calls, because import makes both. Parse checks the document; Fields
// derives the form an operator would fill in, and refuses documents Parse
// accepts -- an input declaring choices with a default that is not one of
// them, or a field the settings catalogue will not take. Checking only the
// first is how this offers an Add button that fails, which is the one thing
// it exists to prevent.
// The fifth result is Auth, and it comes from here rather than from a second
// pass because Fields has already been called by the time it is known: the
// same list that decides whether an operator *could* fill the form in says
// whether any of it is a secret. Working it out again outside would be a
// second parse of every row on the page to learn what this one already holds.
//
// How an entry is authenticated, as far as the document can say.
//
// A closed vocabulary rather than a catalogue's own words, because the point
// is that a row from Smithery and a row from the official registry answer the
// same question the same way. Absent when the entry is not addable: an entry
// nobody can import has no credential story worth reporting, and "none" there
// would read as "free to add".
//
// Deliberately not read from what a catalogue claims about the server. Two of
// the public ones have a field for it, they spell it differently, and one of
// those two fills it in only for a paying tenant -- so a value taken from upstream
// would mean three different things across a merged page and nothing at all on
// most rows. A fact this host works out for itself is the one that is there
// for every row and means one thing.
const (
	// AuthNone is an entry this host can dial with no credential at all.
	AuthNone = "none"
	// AuthAPIKey is an entry whose import will ask for a secret -- a token in
	// a header, typed into the dashboard and encrypted at rest.
	AuthAPIKey = "api_key"
	// AuthUnknown is an entry whose document says nothing about credentials.
	//
	// It is not AuthNone. A document that lists no headers and no variables
	// has not said the server is open; it has said nothing, and most of the
	// ones that say nothing answer 401. Reporting that silence as "none"
	// promises an operator a server they can add and use, and the promise is
	// broken at the first dial -- after the import, with nothing naming the
	// cause. The two facts are different and are kept apart, for the same
	// reason a missing precondition snapshot is not a drift check that passed.
	AuthUnknown = "unknown"
)

func describe(document []byte) (transport, url string, addable bool, reason, auth string) {
	if len(document) > MaxDocumentBytes {
		return "", "", false, fmt.Sprintf(
			"the catalogue's document is %d KiB, and this host stores at most %d KiB",
			len(document)>>10, MaxDocumentBytes>>10), ""
	}
	doc, err := mcpservers.Parse(document)
	if err != nil {
		// Every dated server.json format published to date is read, so what
		// reaches here is a $schema that is not one of them: a typo, a
		// bundle URL, a registry's own endpoint standing in for the format,
		// or a version newer than this build. Two full schema URLs per row
		// says the same thing at ten times the length, so the declared value
		// is shown short and the supported versions by date. The pin itself
		// is not negotiable -- the fields this host depends on are exactly
		// the ones a format change moves, and guessing wrong means dialling
		// somewhere with a credential the operator did not intend to send.
		if errors.Is(err, mcpservers.ErrUnsupportedSchema) {
			return "", "", false, fmt.Sprintf(
				"declares the server.json format %s, which this host does not read; "+
					"it reads %s", declaredSchema(document),
				strings.Join(mcpservers.SupportedSchemaLabels(), ", ")), ""
		}
		return "", "", false, clean(strings.TrimPrefix(err.Error(), "mcpservers: "), maxReasonRunes), ""
	}
	remote, err := doc.Remote()
	if err != nil {
		return "", "", false, clean(strings.TrimPrefix(err.Error(), "mcpservers: "), maxReasonRunes), ""
	}
	fields, err := mcpremote.Fields(doc)
	if err != nil {
		return "", "", false, clean(strings.TrimPrefix(err.Error(), "mcpremote: "), maxReasonRunes), ""
	}
	return remote.Type, clean(remote.URL, maxURLRunes), true, "", authFor(remote, fields)
}

// authFor works out what an operator has to go and find before clicking Add.
//
// A secret field is the certain case: the document named a credential, so the
// import will ask for it. The distinction that matters is between the other
// two. A document that declares headers or variables has been examined and
// found to need nothing secret; one that declares neither has not been
// examined at all, because there was nothing there to examine. Only the first
// of those is AuthNone.
func authFor(remote mcpservers.Remote, fields []settings.Field) string {
	for _, f := range fields {
		if f.Kind == settings.KindSecret {
			return AuthAPIKey
		}
	}
	// Fields always carries this host's own calls-per-second setting, so its
	// length says nothing about the document. The document's own inputs are
	// the question, and they live on the remote.
	if len(remote.Headers) > 0 || len(remote.Variables) > 0 {
		return AuthNone
	}
	return AuthUnknown
}

// countAddable is how many of these entries this host would accept an import
// of. Reported alongside how many were judged, because a ratio is only worth
// anything next to the size of the sample it came from.
func countAddable(entries []Entry) int {
	n := 0
	for _, e := range entries {
		if e.Addable {
			n++
		}
	}
	return n
}

// declaredSchema renders a document's $schema for a one-line reason.
//
// It is a third party's string, so it is cleaned and kept short. The trailing
// filename is dropped because every one of them ends in the same thing, and
// what a reader needs is the part that differs.
func declaredSchema(document []byte) string {
	var probe struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(document, &probe); err != nil || strings.TrimSpace(probe.Schema) == "" {
		return "nothing"
	}
	short := strings.TrimSuffix(probe.Schema, "/server.schema.json")
	short = strings.TrimPrefix(short, "https://static.modelcontextprotocol.io/schemas/")
	return strconv.Quote(clean(short, maxSchemaLabelRunes))
}
