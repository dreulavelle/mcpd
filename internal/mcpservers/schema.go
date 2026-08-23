package mcpservers

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The server.json schemas, vendored rather than fetched.
//
// Fetching one at runtime would make importing a document depend on a third
// party being up, and would let the rules a document is judged by change
// without anyone reviewing the change. Every version this build reads has its
// own file here: moving to a newer format means adding the next file and
// reading the diff, which is the point.
//
//go:embed schema/server-2025-12-11.schema.json
var schemaJSON []byte

//go:embed schema/server-2025-10-17.schema.json
var schema20251017JSON []byte

//go:embed schema/server-2025-09-29.schema.json
var schema20250929JSON []byte

//go:embed schema/server-2025-09-16.schema.json
var schema20250916JSON []byte

//go:embed schema/server-2025-07-09.schema.json
var schema20250709JSON []byte

// schemaVersion is one server.json format this build reads, and what differs
// about it in the fields this host actually acts on.
//
// The differences are recorded as data rather than discovered by sniffing the
// document, because sniffing is how a host ends up reading a field the format
// never defined and dialling somewhere on the strength of it. What a version
// does not declare, this host does not read.
type schemaVersion struct {
	// uri is the exact $schema a document must declare. The pin is by URI and
	// not by the date inside it: a document naming the right date at the wrong
	// host is not a document published against this format.
	uri string
	// label is the dated version, which is the only part two of these differ
	// by and the only part worth putting in a message.
	label string
	// document is the vendored copy.
	document []byte

	// remoteVariables reports that this format defines remotes[].variables --
	// the map that says what a {placeholder} in a remote's url means.
	//
	// Only 2025-12-11 does; it added RemoteTransport for exactly this. In
	// every earlier format a remote is a StreamableHttpTransport or an
	// SseTransport and neither has the field, so a {placeholder} in an
	// earlier document's url refers to nothing the format can describe.
	// Reading a variables map that happens to be present anyway would be
	// this host inventing the meaning of somebody else's field and then
	// dialling the address it produced.
	remoteVariables bool

	// legacyInputFlags reports that this format spells an input's two flags
	// with underscores: is_required and is_secret, renamed to isRequired and
	// isSecret in 2025-09-16.
	//
	// Only 2025-07-09. Both spellings are read for such a document and the
	// results are OR-ed, which is the safe direction and not a symmetric
	// choice: isSecret governs whether a credential written into the document
	// is refused and whether the operator's value is encrypted at rest, so
	// reading it where the format did not promise it can only add protection,
	// while failing to read it removes protection from a real credential.
	// Every 2025-07-09 document in the live registry that sets the flag at all
	// in fact uses the newer spelling.
	legacyInputFlags bool
}

// schemaVersions is every format this build reads, newest first.
//
// The five that exist. static.modelcontextprotocol.io publishes no other
// dated schema, and one this build has not vendored is refused rather than
// guessed at.
var schemaVersions = []schemaVersion{
	{
		uri:             "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
		label:           "2025-12-11",
		document:        schemaJSON,
		remoteVariables: true,
	},
	{
		uri:      "https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json",
		label:    "2025-10-17",
		document: schema20251017JSON,
	},
	{
		uri:      "https://static.modelcontextprotocol.io/schemas/2025-09-29/server.schema.json",
		label:    "2025-09-29",
		document: schema20250929JSON,
	},
	{
		uri:      "https://static.modelcontextprotocol.io/schemas/2025-09-16/server.schema.json",
		label:    "2025-09-16",
		document: schema20250916JSON,
	},
	{
		uri:              "https://static.modelcontextprotocol.io/schemas/2025-07-09/server.schema.json",
		label:            "2025-07-09",
		document:         schema20250709JSON,
		legacyInputFlags: true,
	},
}

// lookupSchema finds the format a document declares.
func lookupSchema(uri string) (schemaVersion, bool) {
	for _, v := range schemaVersions {
		if v.uri == uri {
			return v, true
		}
	}
	return schemaVersion{}, false
}

// SupportedSchemaURIs lists every server.json format this build reads, newest
// first. For the dashboard, and for anyone asking what would be accepted.
func SupportedSchemaURIs() []string {
	out := make([]string, 0, len(schemaVersions))
	for _, v := range schemaVersions {
		out = append(out, v.uri)
	}
	return out
}

// SchemaLabel reduces a schema URI to the dated version in it, which is the
// only part two of them differ by. An unrecognised URI has no label, and the
// caller gets the empty string rather than a guess.
func SchemaLabel(uri string) string {
	if v, ok := lookupSchema(uri); ok {
		return v.label
	}
	return ""
}

// SupportedSchemaLabels lists the dated versions, newest first, for a message
// that has to name them all.
func SupportedSchemaLabels() []string {
	out := make([]string, 0, len(schemaVersions))
	for _, v := range schemaVersions {
		out = append(out, v.label)
	}
	return out
}

// SchemaDocument returns the vendored schema for the current format, for the
// dashboard and for anyone wanting to see what a document is checked against.
func SchemaDocument() []byte {
	out := make([]byte, len(schemaJSON))
	copy(out, schemaJSON)
	return out
}

// SchemaDocumentFor returns the vendored copy of one format. The second result
// is false for a format this build does not read.
func SchemaDocumentFor(uri string) ([]byte, bool) {
	v, ok := lookupSchema(uri)
	if !ok {
		return nil, false
	}
	out := make([]byte, len(v.document))
	copy(out, v.document)
	return out, true
}

// schemaIDs reads the $id out of every vendored copy, keyed by the URI this
// build has it filed under.
//
// It exists so that a test can assert the vendored files and the table above
// still agree. Dropping in a schema without moving its entry would mean
// accepting documents in one format and reading them by the rules of another.
func schemaIDs() (map[string]string, error) {
	out := make(map[string]string, len(schemaVersions))
	for _, v := range schemaVersions {
		var envelope struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(v.document, &envelope); err != nil {
			return nil, fmt.Errorf("mcpservers: the vendored %s schema is not JSON: %w", v.label, err)
		}
		out[v.uri] = envelope.ID
	}
	return out, nil
}

// supportedSchemaList renders the formats for an error message.
func supportedSchemaList() string {
	labels := SupportedSchemaLabels()
	sorted := append([]string(nil), labels...)
	sort.Sort(sort.Reverse(sort.StringSlice(sorted)))
	return strings.Join(sorted, ", ")
}
