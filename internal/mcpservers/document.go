// Package mcpservers models a remote MCP server: the server.json document
// that describes one, and the snapshot of the tools it was last seen to offer.
//
// It is deliberately a leaf. Storage reads and writes these types, and the
// runtime that mounts a remote server builds from them, so anything either of
// those knows about -- SQL, settings, the plugin contract -- would make this
// package impossible to share.
package mcpservers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// SchemaURI is the current server.json format: the one a document should be
// published against today, and the one the dashboard offers.
//
// It is not the only one read. Four earlier formats are vendored beside it in
// schema.go and are translated into this model explicitly, version by version,
// because what a format changed is knowable from the published schemas and a
// refusal costs an operator a server they could have had. What has not moved
// is the pin itself: a document declaring a $schema this build has not
// vendored is refused rather than parsed optimistically. The fields this
// runtime depends on -- how a remote is addressed, which of its inputs are
// secret -- are exactly the fields a format change is likely to move, and
// guessing wrong means dialling somewhere with a credential the operator did
// not intend to send.
const SchemaURI = "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json"

// TransportStreamableHTTP is the only remote transport this increment serves.
const TransportStreamableHTTP = "streamable-http"

// Input formats a server.json may declare.
const (
	FormatString   = "string"
	FormatNumber   = "number"
	FormatBoolean  = "boolean"
	FormatFilepath = "filepath"
)

// Document is the subset of server.json this host acts on.
//
// Only the remote path is modelled. `packages` describes something to run
// locally, which this runtime does not do; it is kept as raw JSON so a
// re-export of the stored document is byte-faithful to what was imported, and
// is otherwise ignored.
type Document struct {
	Schema      string            `json:"$schema"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	Title       string            `json:"title,omitempty"`
	Remotes     []Remote          `json:"remotes,omitempty"`
	Packages    []json.RawMessage `json:"packages,omitempty"`
	Repository  json.RawMessage   `json:"repository,omitempty"`
	Icons       json.RawMessage   `json:"icons,omitempty"`
	Meta        json.RawMessage   `json:"_meta,omitempty"`
}

// Remote is one way to reach the server over the network.
type Remote struct {
	Type      string           `json:"type"`
	URL       string           `json:"url"`
	Headers   []KeyValueInput  `json:"headers,omitempty"`
	Variables map[string]Input `json:"variables,omitempty"`
}

// KeyValueInput is a header, which is an Input that also carries a name.
type KeyValueInput struct {
	Name string `json:"name"`
	Input
}

// Input is one configurable value in a server.json.
type Input struct {
	Description string           `json:"description,omitempty"`
	IsRequired  bool             `json:"isRequired,omitempty"`
	IsSecret    bool             `json:"isSecret,omitempty"`
	Format      string           `json:"format,omitempty"`
	Value       string           `json:"value,omitempty"`
	Default     string           `json:"default,omitempty"`
	Choices     []string         `json:"choices,omitempty"`
	Placeholder string           `json:"placeholder,omitempty"`
	Variables   map[string]Input `json:"variables,omitempty"`
}

// InputRole says what filling in an input actually configures.
type InputRole string

const (
	// RoleVariable substitutes into the URL template, or into a header's own
	// `value` template.
	RoleVariable InputRole = "variable"
	// RoleHeader is a header whose value the operator supplies whole, because
	// the document declared no `value` for it.
	RoleHeader InputRole = "header"
)

// ConfigInput is one thing an operator has to fill in before a server can be
// dialled, with the key it is stored under.
type ConfigInput struct {
	// Key is the bare settings field key. Derived from Name rather than equal
	// to it, because a settings key is lowercase and underscored while a
	// server.json variable is whatever its author typed.
	Key string
	// Name is the variable or header name as the document spells it.
	Name  string
	Role  InputRole
	Input Input
}

// ErrUnsupportedSchema reports a document in a format this build does not
// understand.
var ErrUnsupportedSchema = errors.New("mcpservers: unsupported server.json schema")

// ErrClientConfig reports an mcp.json handed to the server.json parser.
var ErrClientConfig = errors.New("mcpservers: wrong kind of file")

// headerNamePattern is RFC 9110's field-name token.
var headerNamePattern = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+\-.^_` + "`" + `|~]+$`)

// bracePattern finds a {variable} reference in a URL or header template.
var bracePattern = regexp.MustCompile(`\{([^{}]+)\}`)

// reservedHeaders belong to the MCP transport and to HTTP itself.
var reservedHeaders = map[string]bool{
	"accept":               true,
	"content-length":       true,
	"content-type":         true,
	"host":                 true,
	"mcp-protocol-version": true,
	"mcp-session-id":       true,
	"last-event-id":        true,
	"transfer-encoding":    true,
}

// Parse decodes and checks a server.json document.
//
// Everything refused here is refused with a reason an operator can act on. The
// alternative -- importing whatever parses and failing at dial time -- puts the
// diagnosis several steps away from the paste that caused it.
func Parse(raw []byte) (*Document, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("mcpservers: this is not a JSON document: %w", err)
	}

	version, known := lookupSchema(strings.TrimSpace(doc.Schema))
	switch {
	// Named before the schema is mentioned. A document with no $schema is
	// usually not a server.json that forgot one -- it is an mcp.json, the
	// file an editor keeps and the one people reach for first. Answering that
	// with a list of schema dates describes a format they were not trying to
	// use and does not say what to do next.
	case looksLikeClientConfigDoc(raw):
		return nil, fmt.Errorf("%w: this is an MCP client configuration (an "+
			"mcpServers file), not a server.json. Paste it into Add server and "+
			"this host will convert the servers in it that can be reached over "+
			"HTTP", ErrClientConfig)
	case strings.TrimSpace(doc.Schema) == "":
		return nil, fmt.Errorf("%w: the document declares no $schema; this host reads %s",
			ErrUnsupportedSchema, supportedSchemaList())
	case !known:
		return nil, fmt.Errorf("%w: the document declares %s, and this host reads %s",
			ErrUnsupportedSchema, doc.Schema, supportedSchemaList())
	}

	// Everything the declared format spells differently is moved into this
	// model here, once, so that nothing downstream has to know which format a
	// document came from. What the format does not define is removed for the
	// same reason: a field left in place is a field something later will read.
	if err := doc.translate(version, raw); err != nil {
		return nil, err
	}

	for _, required := range []struct{ field, value string }{
		{"name", doc.Name}, {"description", doc.Description}, {"version", doc.Version},
	} {
		if strings.TrimSpace(required.value) == "" {
			return nil, fmt.Errorf("mcpservers: server.json is missing %q", required.field)
		}
	}

	if _, err := doc.Remote(); err != nil {
		return nil, err
	}
	if _, err := doc.Inputs(); err != nil {
		return nil, err
	}
	return &doc, nil
}

// translate moves an earlier format's spelling of the fields this host reads
// into the model above, and removes what the format does not define.
//
// Everything the five vendored formats agree on -- name, description, version,
// packages, repository, _meta, a remote's type and url and headers, and the
// whole of Input and KeyValueInput and InputWithVariables from 2025-09-16
// onward -- is decoded by the struct tags and needs nothing here. What is left
// is small, and is listed rather than inferred.
//
// Two things move, and one is deliberately not carried across:
//
//   - 2025-07-09 spelled an input's flags is_required and is_secret. Both
//     spellings are read for such a document and OR-ed together; see
//     legacyInputFlags for why that direction and not the other.
//
//   - remotes[].variables arrived with 2025-12-11's RemoteTransport. An
//     earlier document's remote has no such field, so one present anyway is
//     dropped: it is not part of the format the publisher declared, and its
//     meaning is this host's to invent. Remote() then refuses a url template
//     in an earlier document rather than resolving one out of nothing.
//
// Anything else an earlier format carries that this build does not model --
// 2025-07-09's website_url and its snake-cased package fields, 2025-09-16's
// server-level status -- is not read on any path and so is not translated.
// The stored bytes stay exactly as published; this shapes only the decoded
// value, and re-parsing the stored document reproduces it.
func (d *Document) translate(version schemaVersion, raw []byte) error {
	if version.legacyInputFlags {
		if err := d.applyLegacyInputFlags(raw); err != nil {
			return err
		}
	}
	if !version.remoteVariables {
		for i := range d.Remotes {
			d.Remotes[i].Variables = nil
		}
	}
	return nil
}

// legacyDocument is the 2025-07-09 spelling of the two flags, at every depth
// they can appear: a remote's headers, a header's own variables, and those
// variables' variables. `variables` on a remote is absent from that format and
// so is absent here.
type legacyDocument struct {
	Remotes []struct {
		Headers []struct {
			Name string `json:"name"`
			legacyInput
		} `json:"headers"`
	} `json:"remotes"`
}

type legacyInput struct {
	IsRequired bool                   `json:"is_required"`
	IsSecret   bool                   `json:"is_secret"`
	Variables  map[string]legacyInput `json:"variables"`
}

// applyLegacyInputFlags ORs the underscored flags onto an already-decoded
// document.
//
// A second targeted decode rather than an UnmarshalJSON on Input, because this
// belongs to one format and putting it on the type would apply it to all five
// -- reading a field a document's own format does not define is the thing this
// package refuses to do elsewhere, and it should not do it here by accident.
func (d *Document) applyLegacyInputFlags(raw []byte) error {
	var legacy legacyDocument
	if err := json.Unmarshal(raw, &legacy); err != nil {
		// The document decoded once already, so a failure here is a shape
		// mismatch -- headers as an object, say -- rather than bad JSON. It
		// is a refusal because the alternative is importing a document whose
		// secrets this host could not read.
		return fmt.Errorf("mcpservers: this document declares the 2025-07-09 format "+
			"but does not have its shape: %w", err)
	}
	for i := range legacy.Remotes {
		if i >= len(d.Remotes) {
			break
		}
		for j := range legacy.Remotes[i].Headers {
			if j >= len(d.Remotes[i].Headers) {
				break
			}
			mergeLegacyInput(&d.Remotes[i].Headers[j].Input, legacy.Remotes[i].Headers[j].legacyInput)
		}
	}
	return nil
}

func mergeLegacyInput(in *Input, legacy legacyInput) {
	in.IsRequired = in.IsRequired || legacy.IsRequired
	in.IsSecret = in.IsSecret || legacy.IsSecret
	for name, nested := range legacy.Variables {
		v, ok := in.Variables[name]
		if !ok {
			continue
		}
		mergeLegacyInput(&v, nested)
		in.Variables[name] = v
	}
}

// Remote returns the transport this host will dial.
//
// The first streamable-http remote wins. An sse-only document is refused
// rather than half-supported: sse is a different transport with a different
// failure model, and pretending otherwise produces a server that imports
// cleanly and never connects.
func (d *Document) Remote() (Remote, error) {
	if len(d.Remotes) == 0 {
		return Remote{}, fmt.Errorf("mcpservers: %s declares no remotes, so there is "+
			"nothing to connect to; this host does not run packaged servers", d.Name)
	}
	for _, r := range d.Remotes {
		if r.Type != TransportStreamableHTTP {
			continue
		}
		if err := checkURL(r.URL); err != nil {
			return Remote{}, err
		}
		if err := d.checkURLTemplate(r.URL); err != nil {
			return Remote{}, err
		}
		for _, h := range r.Headers {
			if !headerNamePattern.MatchString(h.Name) {
				return Remote{}, fmt.Errorf("mcpservers: %q is not a usable HTTP header name", h.Name)
			}
			// Checked here rather than in Inputs, because a header that
			// carries its own value never becomes a question and so would
			// never reach the input checks at all -- which is exactly the
			// shape a credential pasted into a document takes.
			if err := checkSecretLiteral(h.Name, h.Input); err != nil {
				return Remote{}, err
			}
			if reservedHeaders[strings.ToLower(h.Name)] {
				// These are the transport's, not the document's. Letting a
				// server.json set Mcp-Session-Id or Content-Type would let it
				// break the protocol underneath the client, and the failure
				// would look like a bug in this host.
				return Remote{}, fmt.Errorf("mcpservers: %s is set by the transport "+
					"and cannot be supplied by a server.json", h.Name)
			}
		}
		return r, nil
	}

	kinds := make([]string, 0, len(d.Remotes))
	for _, r := range d.Remotes {
		kinds = append(kinds, r.Type)
	}
	return Remote{}, fmt.Errorf("mcpservers: %s offers %s, and this host connects over %s",
		d.Name, strings.Join(kinds, ", "), TransportStreamableHTTP)
}

// checkURLTemplate refuses a {placeholder} the declared format cannot explain.
//
// remotes[].variables -- the map that says what a placeholder in a remote's
// url means, and whether the value behind it is a secret -- arrived with
// 2025-12-11. An earlier document may still contain a placeholder, and eight
// in the live registry do; what it may not contain is anything that defines
// one. Substituting from a variables map the format never had would be this
// host inventing a meaning and then dialling the address it produced, which is
// the case the $schema pin exists for. Resolving it from nothing instead gives
// an import that succeeds and a server that can never start.
//
// So it is refused here, at the moment of paste, with the version named. It is
// the document that is refused and not the format: everything else published
// against 2025-07-09 through 2025-10-17 translates faithfully, and a publisher
// who wants the placeholder read has to say what it means, which means
// republishing against 2025-12-11.
func (d *Document) checkURLTemplate(raw string) error {
	version, known := lookupSchema(strings.TrimSpace(d.Schema))
	if !known || version.remoteVariables {
		return nil
	}
	found := bracePattern.FindAllStringSubmatch(raw, -1)
	if len(found) == 0 {
		return nil
	}
	names := make([]string, 0, len(found))
	for _, m := range found {
		names = append(names, m[1])
	}
	sort.Strings(names)
	return fmt.Errorf("mcpservers: the url carries %s, and the %s server.json format "+
		"has no way to say what a url placeholder means; ask its publisher to "+
		"republish against %s, which does",
		strings.Join(names, ", "), version.label, SchemaLabel(SchemaURI))
}

// checkURL rejects an address this host will not dial.
//
// https everywhere except loopback. A header carrying a bearer token over
// plaintext to another host is a credential given away, and the document's own
// pattern permits http -- so the refusal has to happen here.
func checkURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("mcpservers: the remote declares no url")
	}
	// The template is checked with its braces stripped: {var} is not valid in
	// a host or a path segment, and substitution happens later from settings.
	probe := bracePattern.ReplaceAllString(raw, "x")
	u, err := url.Parse(probe)
	if err != nil {
		return fmt.Errorf("mcpservers: %q is not a usable url: %w", raw, err)
	}
	if u.Host == "" {
		return fmt.Errorf("mcpservers: %q has no host", raw)
	}
	if u.User != nil {
		return fmt.Errorf("mcpservers: %q carries credentials in the url; "+
			"put them in a header instead, where they can be stored encrypted", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("mcpservers: %q is plaintext http to a remote host; "+
			"anything sent with it, credentials included, travels in the clear", raw)
	default:
		return fmt.Errorf("mcpservers: %q uses scheme %q, and this host dials http(s)", raw, u.Scheme)
	}
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// Inputs returns everything an operator must fill in, in a stable order.
//
// Two sources, one namespace: the remote's own variables, which substitute
// into the URL, and the headers whose value the document left for someone to
// supply. A header that declares a `value` is a template, not a question, so
// it does not appear here -- its braces resolve from the variables that do.
func (d *Document) Inputs() ([]ConfigInput, error) {
	remote, err := d.Remote()
	if err != nil {
		return nil, err
	}

	byKey := map[string]ConfigInput{}
	add := func(name string, role InputRole, in Input) error {
		if err := checkInput(name, in); err != nil {
			return err
		}
		key := SettingKey(name, role)
		if existing, dup := byKey[key]; dup {
			return fmt.Errorf("mcpservers: %q and %q both map to the setting %q; "+
				"this host cannot tell them apart", existing.Name, name, key)
		}
		byKey[key] = ConfigInput{Key: key, Name: name, Role: role, Input: in}
		return nil
	}

	for name, in := range remote.Variables {
		if err := add(name, RoleVariable, in); err != nil {
			return nil, err
		}
	}
	for _, h := range remote.Headers {
		if h.Value == "" {
			if err := add(h.Name, RoleHeader, h.Input); err != nil {
				return nil, err
			}
			continue
		}
		// A header template resolves from its own variables, which are just
		// more things to ask for.
		for name, in := range h.Variables {
			if err := add(name, RoleVariable, in); err != nil {
				return nil, err
			}
		}
	}

	out := make([]ConfigInput, 0, len(byKey))
	for _, in := range byKey {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// checkInput refuses an input this host cannot honestly render or resolve.
func checkInput(name string, in Input) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("mcpservers: an input has no name")
	}
	if slug(name) == "" {
		return fmt.Errorf("mcpservers: %q has no letters or digits to build a setting key from", name)
	}
	switch in.Format {
	case "", FormatString, FormatNumber, FormatBoolean:
	case FormatFilepath:
		// A path is a path on the machine running the server, and this server
		// runs somewhere else. Offering the field would invite an operator to
		// type a path that means nothing at the far end -- or one that means
		// something here, which is worse.
		return fmt.Errorf("mcpservers: %q asks for a file path, which is meaningless "+
			"for a server running somewhere else", name)
	default:
		return fmt.Errorf("mcpservers: %q declares unknown format %q", name, in.Format)
	}
	return checkSecretLiteral(name, in)
}

// checkSecretLiteral refuses a credential written into the document.
//
// Both fields, because both are read as a value. `value` is the one an author
// reaches for, and `default` is the one that slips past: Resolve falls back to
// it when nothing is configured, so a default-sourced credential is sent on
// the wire -- and Secrets() reads only what the settings store resolved, so it
// is not even in the redactor. A secret in a document is a secret in a
// database column either way.
func checkSecretLiteral(name string, in Input) error {
	if !in.IsSecret {
		return nil
	}
	for _, literal := range []struct{ field, value string }{
		{"value", in.Value}, {"default", in.Default},
	} {
		// A template is not a literal: its braces resolve from variables the
		// operator fills in, which is the supported way to compose one.
		if literal.value == "" || bracePattern.MatchString(literal.value) {
			continue
		}
		return fmt.Errorf("mcpservers: %q is marked secret and carries its %s in the "+
			"document; a credential belongs in the settings store, encrypted",
			name, literal.field)
	}
	return nil
}

// SettingKey builds the bare settings field key for one input.
//
// Prefixed by role because a variable and a header may share a name and mean
// different things, and a settings key that collided would silently give one
// of them the other's value.
func SettingKey(name string, role InputRole) string {
	prefix := "var_"
	if role == RoleHeader {
		prefix = "header_"
	}
	return prefix + slug(name)
}

// slug turns an arbitrary input name into something a settings key accepts:
// lowercase letters, digits and underscores.
func slug(name string) string {
	var b strings.Builder
	prevUnderscore := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevUnderscore = false
		case !prevUnderscore && b.Len() > 0:
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	return strings.TrimSuffix(b.String(), "_")
}

// Resolve builds the address to dial and the headers to send, from values the
// host resolved out of the settings store.
//
// values is keyed by the bare setting key, which is what ties a form field
// back to the {variable} it fills. Nothing is read from the document's own
// `value` for a secret, and nothing is read from the environment: the host
// resolves settings, and a plugin receives resolved values.
func (d *Document) Resolve(values map[string]string) (endpoint string, headers map[string]string, err error) {
	remote, err := d.Remote()
	if err != nil {
		return "", nil, err
	}

	substitute := func(template string, extra map[string]Input) (string, error) {
		var missing []string
		out := bracePattern.ReplaceAllStringFunc(template, func(match string) string {
			name := match[1 : len(match)-1]
			if v, ok := values[SettingKey(name, RoleVariable)]; ok && v != "" {
				return v
			}
			if in, ok := extra[name]; ok && in.Default != "" {
				return in.Default
			}
			missing = append(missing, name)
			return match
		})
		if len(missing) > 0 {
			sort.Strings(missing)
			return "", fmt.Errorf("mcpservers: nothing is configured for %s",
				strings.Join(missing, ", "))
		}
		return out, nil
	}

	endpoint, err = substitute(remote.URL, remote.Variables)
	if err != nil {
		return "", nil, err
	}
	if err := checkURL(endpoint); err != nil {
		return "", nil, err
	}

	headers = make(map[string]string, len(remote.Headers))
	for _, h := range remote.Headers {
		if h.Value != "" {
			v, subErr := substitute(h.Value, h.Variables)
			if subErr != nil {
				return "", nil, subErr
			}
			headers[h.Name] = v
			continue
		}
		v := values[SettingKey(h.Name, RoleHeader)]
		if v == "" {
			v = h.Default
		}
		if v == "" {
			if h.IsRequired {
				return "", nil, fmt.Errorf("mcpservers: header %s is required and has no value", h.Name)
			}
			continue
		}
		headers[h.Name] = v
	}
	return endpoint, headers, nil
}

// SensitiveValues returns every resolved value that must never appear in a log
// line, a health message, or an error.
//
// Every one, not the ones the document called secret. `isSecret` is a field in
// a third party's document, and letting it decide this would put the party
// being defended against in charge of the defence: a server declaring its
// Authorization header `isSecret: false` would keep the operator's pasted key
// out of the redactor entirely, and every error path would then print it.
//
// So `isSecret` governs the two things it can honestly govern -- how the field
// is rendered, and whether the value is encrypted at rest -- and governs
// nothing here. The cost is that a diagnostic message may blank a value that
// was not really a credential, such as a region. That is a message an operator
// can still read: they know what they configured, and what an error has to
// tell them is what went wrong, not what they typed.
func (d *Document) SensitiveValues(values map[string]string) []string {
	inputs, err := d.Inputs()
	if err != nil {
		// The document no longer parses, so which values belong to it cannot
		// be established. Everything resolved for it is treated as sensitive,
		// because guessing the other way is the one that leaks.
		out := make([]string, 0, len(values))
		for _, v := range values {
			if v != "" {
				out = append(out, v)
			}
		}
		sort.Strings(out)
		return out
	}
	var out []string
	for _, in := range inputs {
		if v := values[in.Key]; v != "" {
			out = append(out, v)
		}
	}
	return out
}

// CredentialValues returns the resolved values that are credential-shaped.
//
// A narrower set than SensitiveValues, and for a different job. That one
// decides what to blank out of transient text -- an error, a log line, a
// health message -- where over-blanking costs a little readability and
// under-blanking leaks. This one decides whether a remote server has echoed
// something we sent it back into its own tool catalogue, which is a judgement
// about the server rather than a formatting choice, and which must not fire on
// an operator's region or tenant slug appearing in a tool description by
// coincidence.
//
// Two sources, both genuinely credential-shaped:
//
//   - every value injected as an HTTP header, whatever the document says about
//     it, because a header is where authorization lives and because that claim
//     is the third party's to make;
//   - every value the document itself declared secret, since a server echoing
//     back its own declared secret has done something worth stopping for.
//
// A plain URL variable is deliberately absent. `region=us-east` is not a
// credential, and treating it as one would disable a legitimate tool for
// mentioning a region.
func (d *Document) CredentialValues(values map[string]string) []string {
	inputs, err := d.Inputs()
	if err != nil {
		return nil
	}
	var out []string
	for _, in := range inputs {
		if in.Role != RoleHeader && !in.Input.IsSecret {
			continue
		}
		if v := values[in.Key]; v != "" {
			out = append(out, v)
		}
	}
	return out
}

// DisplayTitle is what an operator sees. server.json makes `title` optional,
// and the reverse-DNS name is a poor label on its own.
func (d *Document) DisplayTitle() string {
	if strings.TrimSpace(d.Title) != "" {
		return d.Title
	}
	if _, after, found := strings.Cut(d.Name, "/"); found && after != "" {
		return after
	}
	return d.Name
}

// CheckHeaderName reports whether a name can be sent as an HTTP header.
//
// Exported because the operator-supplied headers on a server are checked
// before they are stored, and a name that cannot be sent must be refused where
// somebody can still fix it rather than at the next dial.
func CheckHeaderName(name string) error {
	if !headerNamePattern.MatchString(name) {
		return fmt.Errorf("mcpservers: %q is not a usable HTTP header name", name)
	}
	return nil
}

// WithHeaders returns a copy of the document with extra headers merged onto
// its streamable-http remote.
//
// It is a copy on purpose. The stored document is the operator's own file and
// a re-export has to be byte-faithful to what they imported, so what an
// operator adds afterwards is merged on the way to a client or a settings
// form -- and is never written back into somebody else's published document.
//
// A published header wins over an added one of the same name. The document is
// the publisher's statement about their own server; an operator adding a
// header the document already declares has duplicated a field rather than
// overridden it, and the resolved value would otherwise depend on the order
// two slices happened to be concatenated in.
func (d *Document) WithHeaders(extra []KeyValueInput) *Document {
	if d == nil || len(extra) == 0 {
		return d
	}
	out := *d
	out.Remotes = make([]Remote, len(d.Remotes))
	copy(out.Remotes, d.Remotes)

	for i, r := range out.Remotes {
		if r.Type != TransportStreamableHTTP {
			continue
		}
		declared := make(map[string]bool, len(r.Headers))
		for _, h := range r.Headers {
			declared[strings.ToLower(h.Name)] = true
		}
		merged := make([]KeyValueInput, len(r.Headers), len(r.Headers)+len(extra))
		copy(merged, r.Headers)
		for _, h := range extra {
			if declared[strings.ToLower(h.Name)] {
				continue
			}
			merged = append(merged, h)
		}
		out.Remotes[i].Headers = merged
	}
	return &out
}
