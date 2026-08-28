package mcpservers

import "testing"

const silentDoc = `{
  "$schema": "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
  "name": "com.example/thing", "description": "A thing.", "version": "1.0.0",
  "remotes": [{"type": "streamable-http", "url": "https://example.test/mcp"}]
}`

// TestWithHeadersAsksForWhatTheDocumentDidNot defends the operator's half of
// the credential story.
//
// It exists for a bug: a host that could only send headers a published
// document declared could never reach the roughly one in three remote servers
// whose documents declare none and which answer 401 -- there was nowhere to
// put the key, so the server was addable and unusable.
func TestWithHeadersAsksForWhatTheDocumentDidNot(t *testing.T) {
	doc, err := Parse([]byte(silentDoc))
	if err != nil {
		t.Fatal(err)
	}
	if in, err := doc.Inputs(); err != nil || len(in) != 0 {
		t.Fatalf("the fixture must declare no inputs; got %d (%v)", len(in), err)
	}

	merged := doc.WithHeaders([]KeyValueInput{{
		Name:  "X-Syncro-API-Key",
		Input: Input{IsSecret: true, IsRequired: true},
	}})

	inputs, err := merged.Inputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %d, want 1", len(inputs))
	}
	if !inputs[0].Input.IsSecret {
		t.Error("an added credential must be secret, or it renders in a form field")
	}

	// The document an operator re-exports has to be the one they imported.
	if remote, err := doc.Remote(); err != nil || len(remote.Headers) != 0 {
		t.Errorf("WithHeaders wrote back into the stored document: %d headers, %v",
			len(remote.Headers), err)
	}
}

// TestWithHeadersLetsThePublisherWin keeps one name from deriving two fields.
func TestWithHeadersLetsThePublisherWin(t *testing.T) {
	const declared = `{
	  "$schema": "https://static.modelcontextprotocol.io/schemas/2025-12-11/server.schema.json",
	  "name": "com.example/thing", "description": "A thing.", "version": "1.0.0",
	  "remotes": [{"type": "streamable-http", "url": "https://example.test/mcp",
	    "headers": [{"name": "Authorization", "isSecret": true, "description": "the publisher's"}]}]
	}`
	doc, err := Parse([]byte(declared))
	if err != nil {
		t.Fatal(err)
	}
	// Same header, different case: a second Authorization would be a duplicate
	// field whose winner depended on slice order.
	merged := doc.WithHeaders([]KeyValueInput{{
		Name:  "authorization",
		Input: Input{IsSecret: true, Description: "the operator's"},
	}})
	inputs, err := merged.Inputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %d, want 1", len(inputs))
	}
	if inputs[0].Input.Description != "the publisher's" {
		t.Errorf("description = %q, want the published one to win", inputs[0].Input.Description)
	}
}
