package mcpservers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// A client config is the `mcpServers` file Claude Desktop, Cursor, Cline and
// VS Code keep, and which the MCP Inspector reads. It is a convention rather
// than a specification: the protocol spec defines no configuration file, and
// modelcontextprotocol.io documents the shape only as "the familiar MCP client
// config shape" that its own tooling parses across four clients.
//
// So this reads it the way a tolerant client does, and says so. Two thirds of
// the entries published to GitHub declare no `type` at all, and those that do
// spell it six ways -- `http`, `stdio`, `streamableHttp`, `streamable-http`,
// `local`, `remote`. Transport is therefore inferred from which of `url` and
// `command` is present, and the declared type is only a tie-breaker.
//
// Only the remote entries convert. A `command` entry runs a local process,
// which this host does not do: it has no node, python or package manager, its
// filesystem is read-only, and its own plugin rule forbids executing anything
// outside a plugin's directory. That is roughly three quarters of the entries
// in the wild, so the refusal names the command and says why rather than
// failing at a dial nobody connected to the paste.

// clientConfig is the file's outer shape. Both root keys are read: Claude
// Desktop and most clients write `mcpServers`, VS Code writes `servers`.
type clientConfig struct {
	MCPServers map[string]clientEntry `json:"mcpServers"`
	Servers    map[string]clientEntry `json:"servers"`
}

// clientEntry is one server. Every field beyond these is a client's own
// addition -- `disabled`, `autoApprove`, `timeout`, `cwd` and a dozen more
// appear in under five per cent of published files each -- and none of them
// changes how this host would reach the server, so they are read and dropped.
type clientEntry struct {
	Type        string            `json:"type"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	Command     json.RawMessage   `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	Description string            `json:"description"`
}

// ClientConfigEntry is one server from a client config, converted or refused.
type ClientConfigEntry struct {
	// Name is the key it was filed under, which is what an operator recognises.
	Name string
	// Document is a server.json this host can import, or nil when Reason says
	// why it cannot.
	Document json.RawMessage
	// Reason is why this entry cannot be imported. Empty when Document is set.
	Reason string
	// Suggestion is a way forward for an entry this host refused -- a stdio
	// shim whose environment names the remote it proxies can be reached
	// directly. Empty when there is nothing useful to say.
	Suggestion string
}

// stripJSONC makes a client config readable by encoding/json.
//
// VS Code's mcp.json is JSONC by design -- comments and trailing commas are
// part of the format there, and people hand-edit these files -- so a paste
// that a client accepts must not be refused here for a comma. About one in
// thirty published files needs this.
//
// Deliberately not applied to a server.json. That is a published artifact
// judged against a schema, and loosening what counts as one would mean this
// host accepting documents the registry that served them would not.
//
// String-aware, because a // inside a URL is not a comment and neither is the
// brace in "{}". Anything it cannot make sense of it leaves alone: the caller
// gets a JSON error about the original text rather than about a mangled copy.
func stripJSONC(raw []byte) []byte {
	// A BOM is not whitespace to encoding/json, and an editor on Windows will
	// happily write one.
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})

	out := make([]byte, 0, len(raw))
	var inString, escaped bool
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(raw) && raw[i+1] == '/':
			for i < len(raw) && raw[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(raw) && raw[i+1] == '*':
			i += 2
			for i+1 < len(raw) && !(raw[i] == '*' && raw[i+1] == '/') {
				i++
			}
			i++
		case c == ',':
			// A trailing comma is one followed only by whitespace and a close.
			j := i + 1
			for j < len(raw) && (raw[j] == ' ' || raw[j] == '\t' ||
				raw[j] == '\n' || raw[j] == '\r') {
				j++
			}
			if j < len(raw) && (raw[j] == '}' || raw[j] == ']') {
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// ErrNotClientConfig reports a document that is not an mcp.json at all.
var ErrNotClientConfig = fmt.Errorf("mcpservers: not an MCP client configuration")

// LooksLikeClientConfig reports whether raw is an mcp.json rather than a
// server.json. It is deliberately cheap and structural: it exists so a paste
// that is one can be told apart from a paste that is neither, and an operator
// who pasted the wrong file is told which file they pasted.
func LooksLikeClientConfig(raw []byte) bool {
	var probe struct {
		MCPServers json.RawMessage `json:"mcpServers"`
		Servers    json.RawMessage `json:"servers"`
	}
	if err := json.Unmarshal(stripJSONC(raw), &probe); err != nil {
		return false
	}
	return len(probe.MCPServers) > 0 || len(probe.Servers) > 0
}

// ParseClientConfig converts every server in an mcp.json.
//
// Entries are returned in name order, converted and refused alike: an operator
// pasting a file with four servers in it needs to see all four and what became
// of each, not silently receive the one that happened to work.
func ParseClientConfig(raw []byte) ([]ClientConfigEntry, error) {
	var cfg clientConfig
	if err := json.Unmarshal(stripJSONC(raw), &cfg); err != nil {
		return nil, ErrNotClientConfig
	}
	servers := cfg.MCPServers
	if len(servers) == 0 {
		servers = cfg.Servers
	}
	if len(servers) == 0 {
		return nil, ErrNotClientConfig
	}

	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]ClientConfigEntry, 0, len(names))
	for _, name := range names {
		out = append(out, convertClientEntry(name, servers[name]))
	}
	return out, nil
}

// remoteTypes are the spellings seen in published files for a server reached
// over HTTP. `sse` is deliberately absent: it names a transport this host does
// not serve, and is refused by name below rather than quietly treated as one
// it does.
var remoteTypes = map[string]bool{
	"http": true, "https": true, "streamablehttp": true,
	"streamable-http": true, "streamable_http": true, "remote": true,
}

func convertClientEntry(name string, e clientEntry) ClientConfigEntry {
	out := ClientConfigEntry{Name: name}
	kind := strings.ToLower(strings.TrimSpace(e.Type))

	// A command is decisive whichever way the type is spelled: an entry with
	// one runs a process, and this host does not.
	if command := clientCommand(e.Command); command != "" {
		out.Reason = fmt.Sprintf("runs the local command %q, and this host has no "+
			"runtime to run it in: no node, python or package manager, a read-only "+
			"filesystem, and a plugin rule that forbids executing anything outside "+
			"a plugin's own directory", clientCommandLine(command, e.Args))
		out.Suggestion = shimSuggestion(e)
		return out
	}
	if kind == "sse" {
		out.Reason = "is offered over SSE, and this host serves streamable-http only"
		return out
	}
	if e.URL == "" {
		out.Reason = "names neither a URL to reach nor a command to run, so there " +
			"is nothing here to connect to"
		return out
	}
	if kind != "" && !remoteTypes[kind] {
		// Read anyway rather than refused: a spelling nobody has seen before,
		// on an entry that carries a URL, is far more likely to be a client's
		// own word for HTTP than a transport this host cannot speak.
		kind = ""
	}

	doc, reason := clientDocument(name, e)
	if reason != "" {
		out.Reason = reason
		return out
	}
	out.Document = doc
	return out
}

// clientCommand reads `command`, which is a string in every published file but
// is occasionally written as an array. Both are read; neither is run.
func clientCommand(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err == nil && len(parts) > 0 {
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	return ""
}

func clientCommandLine(command string, args []string) string {
	line := command
	if len(args) > 0 {
		line += " " + strings.Join(args, " ")
	}
	const max = 120
	if len([]rune(line)) > max {
		return string([]rune(line)[:max]) + "…"
	}
	return line
}

// urlPattern finds the address a stdio shim proxies to.
var urlPattern = regexp.MustCompile(`^https?://`)

// shimSuggestion points at the remote behind a local proxy.
//
// A minority of stdio entries -- about three in a hundred -- are a wrapper in
// front of an HTTP server, with the real address in the environment. Those can
// be reached directly, so the address is offered rather than left for somebody
// to spot. It is a suggestion and not a conversion: this host cannot know that
// the address speaks MCP itself rather than something the wrapper translates.
func shimSuggestion(e clientEntry) string {
	keys := make([]string, 0, len(e.Env))
	for k := range e.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := e.Env[k]; urlPattern.MatchString(v) {
			return fmt.Sprintf("its environment sets %s to %s — if that address "+
				"speaks MCP itself, add it as a remote server and put the "+
				"credential in a header", k, v)
		}
	}
	return ""
}

// credentialHeader matches a header name whose value is a credential.
//
// Erring towards secret is deliberate and the errors are not symmetrical: a
// plain header treated as a credential costs an operator one retype, and a
// credential treated as plain writes a live token into a stored document,
// where it is neither encrypted nor redacted. An mcp.json is exactly the file
// people paste with real keys still in it.
var credentialHeader = regexp.MustCompile(`(?i)auth|key|token|secret|password|credential|bearer|cookie|signature`)

// clientDocument composes a server.json from one remote entry.
func clientDocument(name string, e clientEntry) (json.RawMessage, string) {
	if err := checkURL(e.URL); err != nil {
		return nil, strings.TrimPrefix(err.Error(), "mcpservers: ")
	}

	headers := make([]any, 0, len(e.Headers))
	names := make([]string, 0, len(e.Headers))
	for h := range e.Headers {
		names = append(names, h)
	}
	sort.Strings(names)
	for _, h := range names {
		if err := CheckHeaderName(h); err != nil {
			return nil, fmt.Sprintf("declares %q as a header, which is not a usable "+
				"HTTP header name", h)
		}
		// Dropped rather than refused. A client config that pins Content-Type
		// is describing what its own HTTP layer sends, not asking this host to
		// override the transport -- and refusing the whole entry over a header
		// the transport sets for itself would lose a server that is otherwise
		// perfectly reachable.
		if reservedHeaders[strings.ToLower(h)] {
			continue
		}
		header := map[string]any{"name": h}
		if credentialHeader.MatchString(h) {
			// The value is dropped rather than carried. It came out of a file
			// that holds live credentials, and the settings store is where one
			// belongs -- encrypted, and withheld when read back.
			header["isSecret"] = true
			header["isRequired"] = true
			header["description"] = "Carried a value in the client config, which was " +
				"not copied here. Credentials belong in the settings store."
		} else if v := e.Headers[h]; v != "" {
			// Not a credential, so the value is kept as something the operator
			// can see and change -- as a default in the settings store rather
			// than a literal in the document, so one place holds it.
			header["default"] = v
		}
		headers = append(headers, header)
	}

	remote := map[string]any{
		"type": TransportStreamableHTTP,
		"url":  e.URL,
	}
	if len(headers) > 0 {
		remote["headers"] = headers
	}

	description := strings.TrimSpace(e.Description)
	if description == "" {
		description = "Imported from an MCP client configuration."
	}
	if len([]rune(description)) > 100 {
		description = string([]rune(description)[:99]) + "…"
	}

	doc := map[string]any{
		"$schema": SchemaURI,
		// Derived rather than published: a client config carries no
		// reverse-DNS name, and inventing one under this host's own namespace
		// says where it came from instead of impersonating a publisher.
		"name":        clientConfigNamespace + "/" + clientDocumentName(name),
		"description": description,
		// A client config does not version a server, and 0.0.0 is the honest
		// unversioned value: it claims nothing about what is at the far end.
		"version": "0.0.0",
		"remotes": []any{remote},
		"_meta": map[string]any{
			"io.mcpd/imported-from": map[string]any{
				"source": "mcp client configuration",
				"key":    name,
			},
		},
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, "this host could not compose a server.json from that entry"
	}
	return encoded, ""
}

const clientConfigNamespace = "local.mcp-client-config"

// clientDocumentName reduces a config key to server.json's name charset.
func clientDocumentName(name string) string {
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

// looksLikeClientConfigDoc is LooksLikeClientConfig for the parser's use.
//
// Separate only to keep Parse's dependency one way round: the parser names the
// file it was handed, and does not convert it.
func looksLikeClientConfigDoc(raw []byte) bool { return LooksLikeClientConfig(raw) }
