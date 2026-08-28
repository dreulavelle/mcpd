package mcpservers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ToolState is where a discovered tool stands.
//
// Three states rather than a boolean, because "not enabled" has two very
// different meanings: nobody has looked at it yet, and somebody looked and
// said no. Collapsing them would make a re-discovery that re-offers a rejected
// tool indistinguishable from one offering something new.
type ToolState string

const (
	// ToolPending has been seen and not classified. It is not mounted.
	ToolPending ToolState = "pending"
	// ToolEnabled is mounted, and is the only state that is.
	ToolEnabled ToolState = "enabled"
	// ToolDisabled was refused, withdrawn by the server, or is unusable.
	ToolDisabled ToolState = "disabled"
)

// Valid reports whether s is a state this host stores.
func (s ToolState) Valid() bool {
	switch s {
	case ToolPending, ToolEnabled, ToolDisabled:
		return true
	}
	return false
}

// MaxQualifiedToolName bounds the name a model actually sees, which is the
// plugin prefix plus the upstream name. The MCP specification's own limit.
const MaxQualifiedToolName = 128

// toolNamePattern is the specification's charset for a tool name.
//
// Deliberately not the host's own `^[a-z][a-z0-9_]{1,47}$`. That rule was
// written for names a plugin author chooses, and a remote server's names are
// not ours to choose: getWeather, search.docs and read-file are all valid, and
// rejecting them would reject most of the ecosystem. Normalising them instead
// would be worse -- the model would be shown a name the far end does not
// answer to.
var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Descriptor is a remote tool as it was last described to us.
//
// Snapshotted rather than re-read, because Register runs at boot and boot must
// not depend on a third party being reachable.
type Descriptor struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	// Annotations are the server's own claims about its tool -- readOnlyHint
	// and friends. The MCP specification says plainly that clients must not
	// rely on them from an untrusted server, so they are kept for what they
	// are worth as a default in the classification form and are never read as
	// authority. Nothing in this host branches on them.
	Annotations json.RawMessage `json:"annotations,omitempty"`
}

// Tool is one row of a server's tool snapshot.
type Tool struct {
	Name       string     `json:"name"`
	Descriptor Descriptor `json:"descriptor"`
	// Hash identifies the descriptor, and is the guard on every state change.
	Hash    string    `json:"descriptor_hash"`
	State   ToolState `json:"state"`
	Problem string    `json:"problem,omitempty"`

	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// Server is one imported remote MCP server.
type Server struct {
	Name string `json:"name"`
	// Document is the imported server.json, verbatim.
	Document json.RawMessage `json:"document"`
	// Parsed is the decoded document. Nil on a row this build can no longer
	// read, which is reported rather than hidden.
	Parsed        *Document `json:"-"`
	SchemaVersion string    `json:"schema_version"`
	Transport     string    `json:"transport"`
	// URL is the template, braces intact. Substitution happens at dial time,
	// from resolved settings.
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	// ExtraHeaders are headers an operator added because the published
	// document did not declare them. They are merged onto Parsed on the way to
	// building a client or a settings form -- never into Document, which stays
	// as it was imported. See Document.WithHeaders.
	ExtraHeaders []KeyValueInput `json:"extra_headers,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// Effective is the document as this host acts on it: what the publisher wrote,
// with the headers an operator added merged on.
//
// Every path that builds a client or renders a settings form goes through
// here, so a header an operator added is asked for, sent, and cleaned up on
// removal by exactly the code that already does those things for a declared
// one. Nil when the document is one this build cannot read.
func (s Server) Effective() *Document {
	return s.Parsed.WithHeaders(s.ExtraHeaders)
}

// Diff is what one discovery changed.
type Diff struct {
	Added     []string `json:"added,omitempty"`
	Changed   []string `json:"changed,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	Unchanged []string `json:"unchanged,omitempty"`
}

// HashDescriptor returns a stable identifier for a descriptor.
//
// The input schema is re-marshalled through `any` before hashing, so that a
// server which reorders its JSON keys between calls does not read as a changed
// tool -- Go marshals a map with its keys sorted.
func HashDescriptor(d Descriptor) (string, error) {
	canonical := struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		InputSchema any    `json:"inputSchema"`
		Annotations any    `json:"annotations"`
	}{Name: d.Name, Title: d.Title, Description: d.Description}

	for _, part := range []struct {
		raw  json.RawMessage
		into *any
	}{{d.InputSchema, &canonical.InputSchema}, {d.Annotations, &canonical.Annotations}} {
		if len(part.raw) == 0 {
			continue
		}
		if err := json.Unmarshal(part.raw, part.into); err != nil {
			return "", fmt.Errorf("mcpservers: tool %q has undecodable JSON: %w", d.Name, err)
		}
	}

	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// Inspect reports why a descriptor cannot be mounted, or "" when it can.
//
// The name rules come from the specification. The schema rule does not: the
// host's out-of-process adapter quietly substitutes {"type":"object"} for
// anything it cannot use, which is defensible for a binary an operator dropped
// in and indefensible for a third party's server -- it would throw away the
// only argument validation between a model and someone else's endpoint. So an
// unusable schema disqualifies the tool and says so.
func Inspect(prefix string, d Descriptor) string {
	switch {
	case d.Name == "":
		return "the server offered a tool with no name"
	case !toolNamePattern.MatchString(d.Name):
		return fmt.Sprintf("the name %q is outside the character set the MCP "+
			"specification allows for a tool (letters, digits, _ . and -)", d.Name)
	case len(prefix)+1+len(d.Name) > MaxQualifiedToolName:
		return fmt.Sprintf("%s_%s is %d characters, past the %d a tool name may be",
			prefix, d.Name, len(prefix)+1+len(d.Name), MaxQualifiedToolName)
	}

	if len(d.InputSchema) == 0 {
		return "the tool publishes no input schema, so nothing here could check " +
			"the arguments a model sends it"
	}
	var schema map[string]any
	if err := json.Unmarshal(d.InputSchema, &schema); err != nil {
		return "the tool's input schema is not a JSON object: " + err.Error()
	}
	if t, _ := schema["type"].(string); t != "object" {
		return fmt.Sprintf("the tool's input schema declares type %q; MCP tool "+
			"arguments are an object, and substituting a permissive one would "+
			"throw away the only validation there is", orUnset(t))
	}
	return ""
}

func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

// QualifiedName is the name a model sees: the instance prefix and the
// upstream name, joined and otherwise untouched.
func QualifiedName(prefix, name string) string {
	return prefix + "_" + name
}

// Redactor blanks known credentials out of text that is about to be shown.
//
// Health messages reach the unauthenticated readiness endpoint, and a failed
// dial naturally wants to quote the address it failed on -- which for a remote
// MCP server may carry a token in a query string or a header echoed back in an
// error. Blanking is done on the resolved values themselves, so it does not
// depend on guessing which part of a message is sensitive.
type Redactor struct {
	secrets []string
}

// NewRedactor returns a redactor for the given resolved secret values.
func NewRedactor(secrets []string) *Redactor {
	kept := make([]string, 0, len(secrets))
	for _, s := range secrets {
		// A very short "secret" would blank half of every message. Anything
		// that short is not protecting much anyway.
		if len(s) >= 6 {
			kept = append(kept, s)
		}
	}
	return &Redactor{secrets: kept}
}

// String returns msg with every known secret replaced.
func (r *Redactor) String(msg string) string {
	if r == nil {
		return msg
	}
	for _, s := range r.secrets {
		msg = strings.ReplaceAll(msg, s, "[redacted]")
	}
	return msg
}

// Found reports whether any known value appears in s.
//
// The question the descriptor path asks, which is not the question String
// answers. A server echoing a credential back into its tool catalogue is not
// something to tidy up and store; it is something an operator has to see, so
// the caller records it as a reason the tool cannot be mounted and leaves the
// text alone.
func (r *Redactor) Found(s string) bool {
	if r == nil {
		return false
	}
	for _, secret := range r.secrets {
		if strings.Contains(s, secret) {
			return true
		}
	}
	return false
}

// Error returns err's message with every known secret replaced.
func (r *Redactor) Error(err error) string {
	if err == nil {
		return ""
	}
	return r.String(err.Error())
}
