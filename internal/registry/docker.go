package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/spoked/mcpd/internal/mcpservers"
)

// Docker's MCP catalogue.
//
// The content is built from github.com/docker/mcp-registry, which is MIT
// licensed:
//
//	MIT License
//	Copyright (c) 2025 Docker
//
// Everything this file derives from it -- the entries it lists, the
// server.json documents it composes, and the fixtures under testdata/docker --
// carries that notice, reproduced in full in testdata/docker/LICENSE. Nothing
// here is fetched at build time and nothing is fetched at construction; the
// vendored copies are test fixtures, and the running host reads the live
// catalogue over HTTP.
const (
	// DockerCatalogURL is Docker's built catalogue: one document holding every
	// entry, which is the form Docker Desktop itself reads.
	//
	// The alternative was the git repository, 328 separate YAML files behind
	// either 328 requests or a tarball this host would have to unpack. One
	// bounded GET of a document somebody else already assembled is the same
	// content with none of that, and the remote block -- the only part this
	// host acts on -- is byte-identical between the two.
	DockerCatalogURL = "https://desktop.docker.com/mcp/catalog/v3/catalog.yaml"

	// dockerSource names the catalogue in responses and cache keys.
	dockerSource = "docker/mcp-registry"

	// MaxCatalogBytes caps a whole-catalogue response.
	//
	// Separate from MaxResponseBytes, and larger, because it bounds a
	// different thing: that one bounds a page of a hundred entries, this one
	// bounds every entry there is. Docker's catalogue measured 567 KiB at
	// three hundred and seventeen entries, so this is fourteen times the
	// working size. Past it the source fails and says so, which leaves the
	// official registry browsable -- the failure is legible and costs one
	// catalogue, which is why the bound is a bound and not a truncation.
	MaxCatalogBytes = 8 << 20

	// MaxCatalogEntries caps how many entries one catalogue document may
	// contribute, whatever it contains. The same bound MaxEntriesPerPage puts
	// on a page, at the scale a whole catalogue is read.
	MaxCatalogEntries = 5000
)

// Docker entry types. Only one of them describes something this host can
// reach.
const (
	dockerTypeRemote = "remote"
	dockerTypeServer = "server"
	dockerTypePOCI   = "poci"
)

// dockerNamespace prefixes the name of every server.json this source composes.
//
// Docker names a server with a bare key -- "context7", "linear" -- and
// server.json requires reverse-DNS with exactly one slash. There is no
// published name to use, so one is derived, and it is derived to say where it
// came from. It is deliberately not a namespace the official registry knows:
// nothing in this build treats the two catalogues' names as the same
// namespace, and a derived name that looked like a registered one would invite
// exactly that.
const dockerNamespace = "com.docker.mcp-registry"

// dockerEnvReference matches Docker's header templating, ${SOME_ENV_NAME}.
//
// Anchored to an environment-variable-shaped name on purpose. The value is a
// third party's string and it becomes a server.json {placeholder}, so a name
// that is not a plain identifier is not translated -- it is refused.
var dockerEnvReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Docker reads Docker's MCP catalogue.
//
// The whole catalogue arrives in one document, so unlike the official registry
// there is nothing to page through upstream. Paging is done here, over the
// entries sorted by name, and the cursor is the name to resume after.
type Docker struct {
	url    string
	client *http.Client
	agent  string
	limit  int
}

// DockerOptions configures the client. Every field has a working default, so
// the zero value is usable and a test can replace exactly what it needs.
type DockerOptions struct {
	// CatalogURL overrides the catalogue address. For tests.
	CatalogURL string
	// HTTPClient overrides the transport. The default refuses redirects and
	// bounds the call.
	HTTPClient *http.Client
	// UserAgent identifies this host. Naming the caller is ordinary manners
	// toward a free service.
	UserAgent string
	// Limit is the page size. Zero takes defaultLimit.
	Limit int
}

// NewDocker builds a client for Docker's catalogue. It fetches nothing.
func NewDocker(opts DockerOptions) *Docker {
	target := strings.TrimSpace(opts.CatalogURL)
	if target == "" {
		target = DockerCatalogURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: requestTimeout,
			// Same refusal as the official registry's client, for the same
			// reason: there is no credential to leak, and a catalogue that
			// suddenly wants to send this host somewhere else is a change
			// worth refusing rather than following.
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
	return &Docker{url: target, client: client, agent: agent, limit: limit}
}

// Source names the catalogue.
func (d *Docker) Source() string { return dockerSource }

// List returns one page of Docker's catalogue.
func (d *Docker) List(ctx context.Context, q Query) (Page, error) {
	return d.ListIfChanged(ctx, q, Validators{})
}

// ListIfChanged is List with the previous answer's validators.
//
// This is where revalidation earns its place. Docker's catalogue is one 567
// KiB document served from a CDN that sends both an ETag and a Last-Modified,
// so a refresh that finds nothing changed costs a few hundred bytes of headers
// instead of the whole catalogue again. It sends no Cache-Control, so how long
// to hold it is still the configured default.
func (d *Docker) ListIfChanged(ctx context.Context, q Query, v Validators) (Page, error) {
	// Normalised here as well as at the cache, so that the client is bounded
	// whether or not something cached it -- the bounds are the client's rule
	// and not the cache's favour.
	q = q.Normalised()
	catalog, freshness, err := d.fetch(ctx, v)
	if err != nil {
		return Page{Freshness: freshness}, err
	}
	entries := catalog.entries()

	limit := q.Limit
	if limit <= 0 {
		limit = d.limit
	}
	// The search the official registry performs is a substring of the name.
	// The same rule here, widened to the title, because a Docker name is a
	// bare slug where an official name carries the publisher's domain -- so
	// the words an operator would type are as likely to be in one as the
	// other.
	if needle := strings.ToLower(q.Search); needle != "" {
		kept := entries[:0:0]
		for _, e := range entries {
			if strings.Contains(strings.ToLower(e.Name), needle) ||
				strings.Contains(strings.ToLower(e.Title), needle) {
				kept = append(kept, e)
			}
		}
		entries = kept
	}

	// The cursor is the name the previous page ended on. Entries are sorted by
	// name, so resuming is a search rather than an offset -- an offset would
	// silently skip or repeat entries when the catalogue changed between two
	// pages, and the catalogue changes on Docker's schedule.
	if after := q.Cursor; after != "" {
		i := sort.Search(len(entries), func(i int) bool { return entries[i].Name > after })
		entries = entries[i:]
	}

	next := ""
	if len(entries) > limit {
		next = entries[limit-1].Name
		entries = entries[:limit]
	}
	page := Page{
		Source:      d.Source(),
		Entries:     append([]Entry(nil), entries...),
		NextCursor:  next,
		RetrievedAt: time.Now().UTC(),
		Freshness:   freshness,
	}
	page.Sources = []SourceStatus{{
		Source: d.Source(), OK: true,
		RetrievedAt: page.RetrievedAt, Entries: len(page.Entries),
	}}
	return page, nil
}

// Get returns one entry and the server.json composed from it.
func (d *Docker) Get(ctx context.Context, name string) (Detail, error) {
	return d.GetIfChanged(ctx, name, Validators{})
}

// GetIfChanged is Get with the previous answer's validators.
//
// One document holds every entry, so the validator is the whole catalogue's
// and a 304 confirms this entry along with the other three hundred. That is
// why detail is cached separately: it is keyed by a stable name and outlives
// several listings.
func (d *Docker) GetIfChanged(ctx context.Context, name string, v Validators) (Detail, error) {
	wanted := opaque(name, maxNameRunes)
	if wanted == "" {
		return Detail{}, ErrNotFound
	}
	catalog, freshness, err := d.fetch(ctx, v)
	if err != nil {
		return Detail{Freshness: freshness}, err
	}
	raw, ok := catalog.Registry[wanted]
	if !ok {
		return Detail{}, ErrNotFound
	}
	entry, document, ok := translateDockerEntry(wanted, raw)
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

// entries turns every row into an Entry, sorted by name so that paging over
// the catalogue is stable.
func (c dockerCatalog) entries() []Entry {
	names := make([]string, 0, len(c.Registry))
	for name := range c.Registry {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > MaxCatalogEntries {
		names = names[:MaxCatalogEntries]
	}

	out := make([]Entry, 0, len(names))
	for _, name := range names {
		entry, _, ok := translateDockerEntry(name, c.Registry[name])
		if !ok {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// fetch performs one bounded, optionally conditional GET of the catalogue and
// decodes it.
func (d *Docker) fetch(ctx context.Context, v Validators) (dockerCatalog, Freshness, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.url, nil)
	if err != nil {
		return dockerCatalog{}, Freshness{}, fmt.Errorf("registry: %w", err)
	}
	req.Header.Set("Accept", "application/yaml, text/yaml, */*")
	req.Header.Set("User-Agent", d.agent)
	applyValidators(req, v)

	resp, err := d.client.Do(req)
	if err != nil {
		return dockerCatalog{}, Freshness{},
			fmt.Errorf("registry: %s could not be reached: %w", d.Source(), err)
	}
	defer resp.Body.Close()

	freshness := readFreshness(resp)
	switch resp.StatusCode {
	case http.StatusNotModified:
		return dockerCatalog{}, freshness, ErrNotModified
	case http.StatusOK:
	default:
		return dockerCatalog{}, Freshness{},
			fmt.Errorf("registry: %s answered %s", d.Source(), resp.Status)
	}

	// Bounded before decoding, not after. A YAML decoder reading an unbounded
	// body from a third party is a memory limit set by somebody else.
	data, err := readBounded(resp.Body, MaxCatalogBytes)
	if err != nil {
		return dockerCatalog{}, Freshness{},
			fmt.Errorf("registry: reading from %s: %w", d.Source(), err)
	}
	if data == nil {
		return dockerCatalog{}, Freshness{}, fmt.Errorf("registry: %s returned more than %d MiB",
			d.Source(), MaxCatalogBytes>>20)
	}
	var catalog dockerCatalog
	if err := yaml.Unmarshal(data, &catalog); err != nil {
		return dockerCatalog{}, Freshness{}, fmt.Errorf(
			"registry: %s returned something this host cannot read: %w", d.Source(), err)
	}
	return catalog, freshness, nil
}

// --- Docker's wire format ---------------------------------------------------

// dockerCatalog is the built catalogue document. Only the parts this host acts
// on are modelled; everything else -- pull counts, readme links, the tool
// names Docker last saw -- is somebody else's business.
type dockerCatalog struct {
	Registry map[string]dockerEntry `yaml:"registry"`
}

// dockerEntry is one row of Docker's catalogue.
type dockerEntry struct {
	// Type is "server", "remote" or "poci". Only "remote" describes something
	// this host can reach; see translateDockerEntry.
	Type        string       `yaml:"type"`
	Title       string       `yaml:"title"`
	Description string       `yaml:"description"`
	DateAdded   string       `yaml:"dateAdded"`
	Remote      dockerRemote `yaml:"remote"`
	// OAuth is present when Docker's gateway obtains the credential through an
	// OAuth flow of its own.
	OAuth dockerOAuth `yaml:"oauth"`
	// Secrets names the credentials a header template refers to, which is
	// where the description of an input comes from.
	Secrets []dockerSecret `yaml:"secrets"`
}

type dockerRemote struct {
	TransportType string `yaml:"transport_type"`
	URL           string `yaml:"url"`
	// Headers are name to value, where a value may embed ${SOME_ENV_NAME}.
	Headers map[string]string `yaml:"headers"`
}

type dockerOAuth struct {
	Providers []struct {
		Provider string `yaml:"provider"`
	} `yaml:"providers"`
}

type dockerSecret struct {
	Name        string `yaml:"name"`
	Env         string `yaml:"env"`
	Description string `yaml:"description"`
	Example     string `yaml:"example"`
}

// --- translation ------------------------------------------------------------

// translateDockerEntry turns one Docker row into the same Entry the official
// registry produces, and into the server.json that would be imported.
//
// The document is composed rather than passed through, because Docker's format
// is not server.json and there is no second import route: what leaves here
// goes to the same endpoint a pasted document goes to, is validated by the
// same parser, and is stored verbatim as this host composed it. That the
// composed document is judged addable by mcpservers.Parse -- the import
// endpoint's own parser -- is what keeps "offered" and "imports" the same set.
//
// The second result is the document; the third is false for a row with nothing
// to show, which is a row nothing can be done with.
func translateDockerEntry(name string, raw dockerEntry) (Entry, json.RawMessage, bool) {
	// Not cleaned: Name is the identifier the dashboard sends back to the
	// entry route, so a truncated or rewritten one is a row that 404s when
	// somebody clicks it. The same rule the official registry follows -- it
	// survives unchanged or the row is dropped.
	name = opaque(name, maxNameRunes)
	if name == "" {
		return Entry{}, nil, false
	}
	title := clean(raw.Title, maxTitleRunes)
	if title == "" {
		title = SuggestName(name)
	}
	entry := Entry{
		Name:          name,
		SuggestedName: SuggestName(name),
		Title:         title,
		Description:   clean(raw.Description, maxDescriptionRunes),
		// Deliberately empty. Docker's catalogue versions an image, not a
		// remote entry, so there is no version of the *server* to show and a
		// placeholder rendered as one would be a claim nobody made. The
		// composed document below carries the placeholder server.json insists
		// on, and says in its own field that it is one.
		Version:   "",
		UpdatedAt: dockerTimestamp(raw.DateAdded),
		Source:    dockerSource,
	}

	// The address is filled in for every remote entry, addable or not.
	//
	// A Docker entry names exactly one remote, so unlike the official
	// registry's multi-remote documents there is no question of which one this
	// host would dial -- and the reasons a remote entry is refused here are
	// about the credential or the transport, never about not knowing where the
	// server is. Carrying it matters twice: an operator can see what they are
	// being refused, and cross-source deduplication has something to match on.
	// The address is the only identity the two catalogues share, since one
	// names this server app.linear/linear and the other names it linear.
	if raw.Type == dockerTypeRemote {
		entry.Transport = clean(raw.Remote.TransportType, maxVersionRunes)
		entry.URL = clean(raw.Remote.URL, maxURLRunes)
	}

	document, reason := composeDockerDocument(name, title, raw)
	if reason != "" {
		entry.Reason = clean(reason, maxReasonRunes)
		return entry, nil, true
	}

	// Addability is decided the same way it is for the official registry: by
	// handing the document to the parser the import endpoint uses. Composing
	// it here and judging it by a different rule would let this source offer
	// something the import path refuses.
	transport, url, addable, describeReason := describe(document)
	entry.Addable = addable
	entry.Reason = clean(describeReason, maxReasonRunes)
	if !addable {
		return entry, nil, true
	}
	// On the addable path the document is the authority, since it is what
	// would actually be dialled.
	entry.Transport, entry.URL = transport, url
	return entry, document, true
}

// composeDockerDocument builds the server.json for one Docker row, or says why
// there is none to build.
//
// The reasons returned here are the ones that cannot be expressed as a
// server.json at all. Everything a server.json *can* say -- an sse remote, a
// url this host will not dial -- is left to mcpservers.Parse, so that the
// refusal an operator reads is the import path's own words rather than this
// file's guess at them.
func composeDockerDocument(name, title string, raw dockerEntry) (json.RawMessage, string) {
	switch raw.Type {
	case dockerTypeRemote:
	case dockerTypeServer:
		return nil, "a container Docker runs locally; this host connects to remote " +
			"MCP servers over the network and does not run packaged servers"
	case dockerTypePOCI:
		return nil, "a command Docker runs locally; this host connects to remote " +
			"MCP servers over the network and does not run packaged servers"
	default:
		return nil, fmt.Sprintf("the catalogue calls this a %q entry, "+
			"which this host does not know how to reach", clean(raw.Type, 64))
	}

	if strings.TrimSpace(raw.Remote.URL) == "" {
		return nil, "the catalogue gives no address for it"
	}

	headers, err := dockerHeaders(raw)
	if err != nil {
		return nil, err.Error()
	}
	// An entry whose only credential comes from a flow Docker's own gateway
	// performs cannot be reached from here. This host sends headers an
	// operator configured; it does not hold an OAuth client, and the entry
	// does not say which header the token it would obtain belongs in. Offering
	// an Add button that produces a server answering 401 is worse than saying
	// so -- and it is said here rather than left to a dial failure, because
	// the failure would arrive after the import with nothing naming the cause.
	if len(headers) == 0 && len(raw.OAuth.Providers) > 0 {
		return nil, fmt.Sprintf("reached through an OAuth flow Docker's gateway "+
			"performs with %s; this host sends a credential you configure, and the "+
			"catalogue does not say which header carries one",
			clean(dockerProviderNames(raw.OAuth), maxTitleRunes))
	}

	remote := map[string]any{
		"type": clean(raw.Remote.TransportType, maxVersionRunes),
		"url":  clean(raw.Remote.URL, maxURLRunes),
	}
	if len(headers) > 0 {
		// Absent rather than empty. An empty headers array is a claim that the
		// entry was examined and found to need none, which is true here -- but
		// it is also a field this host wrote into somebody else's description,
		// and the shorter document is the one that says less.
		remote["headers"] = headers
	}

	doc := map[string]any{
		"$schema": mcpservers.SchemaURI,
		// Derived, not published. See dockerNamespace.
		"name":  dockerNamespace + "/" + dockerDocumentName(name),
		"title": title,
		// server.json caps a description at a hundred characters and Docker's
		// run to eight hundred, so the document carries the format's length
		// and the Entry above carries the whole of it. A document that
		// declares a format and then breaks it is not a document to hand an
		// import endpoint.
		"description": dockerDescription(raw.Description, title),
		// server.json requires a version and Docker's catalogue does not
		// version a remote entry. 0.0.0 is the honest unversioned value: it
		// sorts below every real release and claims nothing about upstream.
		"version": "0.0.0",
		"remotes": []any{remote},
		"_meta": map[string]any{
			// Provenance travels with the document, so a server imported six
			// months ago can still say where its description came from and
			// under what licence. The key is reverse-DNS because _meta is a
			// shared namespace.
			"io.mcpd/catalogue-source": map[string]any{
				"source":  dockerSource,
				"name":    name,
				"licence": "MIT, Copyright (c) 2025 Docker",
				"origin":  "https://github.com/docker/mcp-registry",
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

// dockerHeaders translates Docker's header map into server.json headers.
//
// Docker writes a header value with ${SOME_ENV_NAME} where the operator's
// credential goes; server.json writes the same thing as {SOME_ENV_NAME} with a
// `variables` entry saying what it is. So the translation is the braces, and
// the variable the braces now refer to:
//
//	Authorization: "Bearer ${APIFY_API_KEY}"
//
//	{"name": "Authorization", "value": "Bearer {APIFY_API_KEY}",
//	 "variables": {"APIFY_API_KEY": {"isSecret": true, "isRequired": true}}}
//
// The environment variable's name is kept as the variable's name rather than
// renamed, because it is the only name the catalogue gives the value and it is
// what the entry's own `secrets` block refers to. Every variable is marked
// secret: a value substituted into a header is a credential whatever the
// catalogue calls it, which is the same judgement Document.CredentialValues
// makes on the way out.
//
// A header whose value carries no reference is a constant, and is passed
// through as one.
func dockerHeaders(raw dockerEntry) ([]any, error) {
	names := make([]string, 0, len(raw.Remote.Headers))
	for name := range raw.Remote.Headers {
		names = append(names, name)
	}
	// Sorted so that the composed document is the same bytes every time. The
	// import path hashes what it stores, and a map's iteration order would
	// make the same catalogue entry two different documents.
	sort.Strings(names)

	described := dockerSecretDescriptions(raw.Secrets)
	out := make([]any, 0, len(names))
	for _, name := range names {
		value := raw.Remote.Headers[name]
		// A brace already in the value would be read as a server.json
		// placeholder by everything downstream, and this host did not put it
		// there. Checked with Docker's own references removed, so that what is
		// left is only braces nobody here accounted for. Refusing is the only
		// option that does not change what the header means.
		if strings.ContainsAny(dockerEnvReference.ReplaceAllString(value, ""), "{}") {
			return nil, fmt.Errorf("the catalogue's %s header contains a brace this "+
				"host cannot translate", clean(name, maxTitleRunes))
		}
		header := map[string]any{"name": clean(name, maxTitleRunes)}

		variables := map[string]any{}
		substituted := dockerEnvReference.ReplaceAllStringFunc(value, func(match string) string {
			env := dockerEnvReference.FindStringSubmatch(match)[1]
			input := map[string]any{"isSecret": true, "isRequired": true}
			if d := described[env]; d != "" {
				input["description"] = clean(d, maxDescriptionRunes)
			}
			variables[env] = input
			return "{" + env + "}"
		})
		// Anything left over is a $ or a brace-less reference Docker meant as
		// a substitution and this host would ship literally -- a credential
		// that is the string "${API KEY}" is not a credential.
		if strings.Contains(substituted, "${") {
			return nil, fmt.Errorf("the catalogue's %s header refers to a value by a "+
				"name this host cannot translate", clean(name, maxTitleRunes))
		}
		header["value"] = clean(substituted, maxURLRunes)
		if len(variables) > 0 {
			header["variables"] = variables
		}
		out = append(out, header)
	}
	return out, nil
}

// dockerSecretDescriptions maps an environment variable name to the prose the
// catalogue offers for it, so an operator filling in the form sees Docker's
// own words rather than a bare variable name.
func dockerSecretDescriptions(secrets []dockerSecret) map[string]string {
	out := make(map[string]string, len(secrets))
	for _, s := range secrets {
		env := strings.TrimSpace(s.Env)
		if env == "" {
			continue
		}
		switch {
		case strings.TrimSpace(s.Description) != "":
			out[env] = s.Description
		case strings.TrimSpace(s.Name) != "":
			out[env] = "Docker's catalogue calls this " + s.Name
		}
	}
	return out
}

func dockerProviderNames(oauth dockerOAuth) string {
	names := make([]string, 0, len(oauth.Providers))
	for _, p := range oauth.Providers {
		if n := strings.TrimSpace(p.Provider); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "an identity provider"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// dockerDocumentName reduces a catalogue key to server.json's name charset:
// letters, digits, dot, underscore and dash.
func dockerDocumentName(name string) string {
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

// dockerDescriptionMax is server.json's own limit on a description. Every
// vendored schema states it.
const dockerDescriptionMax = 100

// dockerDescription fits Docker's prose into the format's length, falling back
// to the title when there is no prose at all -- server.json requires a
// description, and a document refused for having none would list a server
// nobody could add for a reason that is this host's own doing.
func dockerDescription(description, title string) string {
	out := clean(description, dockerDescriptionMax)
	if out == "" {
		out = clean(title, dockerDescriptionMax)
	}
	if out == "" {
		out = "An MCP server listed in Docker's catalogue."
	}
	return out
}

// dockerTimestamp reads the catalogue's dateAdded, which is RFC 3339. A row
// with an unreadable one keeps the zero time rather than today's date, because
// "we do not know when this changed" and "it changed just now" are different
// facts and the second is the one that misleads.
func dockerTimestamp(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

var _ Revalidating = (*Docker)(nil)
