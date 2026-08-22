package mcpservers

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// The server.json schema, vendored rather than fetched.
//
// Fetching it at runtime would make importing a document depend on a third
// party being up, and would let the rules a document is judged by change
// without anyone reviewing the change. The copy here is pinned to SchemaURI:
// moving to a newer format means adding the next file and reading the diff,
// which is the point.
//
//go:embed schema/server-2025-12-11.schema.json
var schemaJSON []byte

// SchemaDocument returns the vendored schema, for the dashboard and for
// anyone wanting to see what a document is checked against.
func SchemaDocument() []byte {
	out := make([]byte, len(schemaJSON))
	copy(out, schemaJSON)
	return out
}

// schemaID reads the $id out of the vendored copy.
//
// It exists so that a test can assert the vendored file and SchemaURI still
// agree. Dropping in a newer schema without moving the constant would mean
// accepting documents in one format and reading them by the rules of another.
func schemaID() (string, error) {
	var envelope struct {
		ID string `json:"$id"`
	}
	if err := json.Unmarshal(schemaJSON, &envelope); err != nil {
		return "", fmt.Errorf("mcpservers: the vendored schema is not JSON: %w", err)
	}
	return envelope.ID, nil
}
