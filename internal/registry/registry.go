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
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spoked/mcpd/internal/mcpservers"
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
	// Addable reports that this host would accept the document. It is decided
	// by handing the document to the same parser the import endpoint uses, so
	// an entry offered here is one that can actually be imported.
	Addable bool `json:"addable"`
	// Reason says why not, when Addable is false. A server with only packages
	// and no remotes is the common case: this host does not run packaged
	// servers, and offering an Add button that cannot work is worse than
	// saying so.
	Reason string `json:"reason,omitempty"`
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
	Document json.RawMessage `json:"document"`
	// Source names the catalogue this came from.
	Source string `json:"source"`
	// Stale reports that the catalogue could not be reached and this is what
	// was last seen. It is never an error: a browsable stale list beats a
	// page that refuses to render because a third party is down.
	Stale bool `json:"stale"`
	// RetrievedAt is when the data was actually fetched, which is what makes
	// Stale readable rather than merely alarming.
	RetrievedAt time.Time `json:"retrieved_at"`
}

// Page is one page of a browse or search.
type Page struct {
	Source  string  `json:"source"`
	Entries []Entry `json:"entries"`
	// NextCursor is opaque and belongs to the catalogue. Empty means the end.
	NextCursor  string    `json:"next_cursor,omitempty"`
	Stale       bool      `json:"stale"`
	RetrievedAt time.Time `json:"retrieved_at"`
}

// Query is one browse or search request.
type Query struct {
	// Search matches server names as a substring. Empty browses everything.
	Search string
	// Cursor continues a previous page. Empty starts at the beginning.
	Cursor string
	// Limit bounds one page. Zero takes the client's default.
	Limit int
}

// Client is a catalogue of MCP servers.
//
// An interface so a second catalogue can be added without touching a caller.
// There is one implementation today and adding another is a decision nobody
// has made yet; what this buys now is that the cache in front of it and the
// handler behind it are written against a contract rather than against the
// official registry's quirks.
type Client interface {
	// List returns one page of servers.
	List(ctx context.Context, q Query) (Page, error)
	// Get returns one entry and the document that would be imported.
	Get(ctx context.Context, name string) (Detail, error)
	// Source names the catalogue, for the operator and for cache keys.
	Source() string
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
// The judgement is made by the parser the import endpoint uses, not by looking
// for a remotes array. Those differ in ways that matter: a document offering
// only sse, or plaintext http to a public host, or a credential written into
// its own text, parses as having remotes and is still refused at import. An
// entry offered as addable here has to be one that actually imports.
func describe(document []byte) (transport, url string, addable bool, reason string) {
	if len(document) > MaxDocumentBytes {
		return "", "", false, fmt.Sprintf(
			"the catalogue's document is %d KiB, and this host stores at most %d KiB",
			len(document)>>10, MaxDocumentBytes>>10)
	}
	doc, err := mcpservers.Parse(document)
	if err != nil {
		// The commonest refusal by far, and the one whose default message
		// reads worst in a list: roughly a fifth of the remote servers in the
		// official registry were published against an earlier server.json
		// format. Two full schema URLs per row says the same thing at ten
		// times the length, so the dates are what is shown. The pin itself is
		// not negotiable -- the fields this host depends on are exactly the
		// ones a format change moves, and guessing wrong means dialling
		// somewhere with a credential the operator did not intend to send.
		if errors.Is(err, mcpservers.ErrUnsupportedSchema) {
			return "", "", false, fmt.Sprintf(
				"published against the %s server.json format; this host reads %s",
				schemaLabel(schemaOf(document)), schemaLabel(mcpservers.SchemaURI))
		}
		return "", "", false, clean(strings.TrimPrefix(err.Error(), "mcpservers: "), maxReasonRunes)
	}
	remote, err := doc.Remote()
	if err != nil {
		return "", "", false, clean(strings.TrimPrefix(err.Error(), "mcpservers: "), maxReasonRunes)
	}
	return remote.Type, clean(remote.URL, maxURLRunes), true, ""
}

// schemaOf reads a document's declared $schema without judging it.
func schemaOf(document []byte) string {
	var probe struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(document, &probe); err != nil {
		return ""
	}
	return probe.Schema
}

// schemaLabel reduces a schema URL to the dated version in it, which is the
// only part that differs between two of them.
func schemaLabel(uri string) string {
	parts := strings.Split(strings.TrimSuffix(uri, "/server.schema.json"), "/")
	if label := parts[len(parts)-1]; label != "" && label != uri {
		return clean(label, 32)
	}
	return "an unrecognised"
}
