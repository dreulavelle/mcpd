package mcpservers

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSchemaURIMatchesTheVendoredCopy stops the two drifting.
//
// The vendored schema is the pinned reference an import is judged by, and
// SchemaURI is the version gate that decides which documents are read at all.
// Dropping in a newer schema without moving the constant -- or the reverse --
// would mean accepting documents in one format and reading them by the rules
// of another.
func TestSchemaURIMatchesTheVendoredCopy(t *testing.T) {
	id, err := schemaID()
	if err != nil {
		t.Fatalf("read vendored schema: %v", err)
	}
	if id != SchemaURI {
		t.Errorf("the vendored schema declares $id %q but SchemaURI is %q", id, SchemaURI)
	}
	if len(SchemaDocument()) == 0 {
		t.Error("the vendored schema is empty")
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// TestParse_PublishedDocuments feeds real documents from the public registry.
//
// Hand-written fixtures would test the parser against the parser's own
// assumptions. These were published by other people, and are the shapes the
// import path actually meets.
func TestParse_PublishedDocuments(t *testing.T) {
	tests := []struct {
		file      string
		wantURL   string
		wantTitle string
		// wantInputs is the settings key of every input the document asks for,
		// in the order Inputs returns them.
		wantInputs []string
		wantSecret []string
	}{
		{
			file:       "adadvisor.json",
			wantURL:    "https://api.adadvisor.ai/mcp",
			wantTitle:  "AdAdvisor MCP Server",
			wantInputs: []string{"header_authorization"},
			wantSecret: []string{"header_authorization"},
		},
		{
			file:       "autorfp.json",
			wantURL:    "https://{api_host}/mcp",
			wantTitle:  "AutoRFP.ai",
			wantInputs: []string{"var_api_host"},
		},
		{
			file:      "contabo.json",
			wantURL:   "https://contabo.run.mcp.com.ai/mcp",
			wantTitle: "Contabo (VPS) MCP Server",
			// Sorted by key, so the optional trace header comes first.
			wantInputs: []string{"header_authorization", "header_x_request_id"},
			wantSecret: []string{"header_authorization"},
		},
		{
			// Two remotes: streamable-http first, sse second. The sse one is
			// not this host's business, and its variables must not turn up in
			// the form.
			file:       "biel.json",
			wantURL:    "https://mcp.biel.ai/v2/{project_slug}/mcp",
			wantTitle:  "Biel.ai",
			wantInputs: []string{"var_project_slug"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			doc, err := Parse(fixture(t, tc.file))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			remote, err := doc.Remote()
			if err != nil {
				t.Fatalf("remote: %v", err)
			}
			if remote.Type != TransportStreamableHTTP {
				t.Errorf("transport = %q, want %q", remote.Type, TransportStreamableHTTP)
			}
			if remote.URL != tc.wantURL {
				t.Errorf("url = %q, want %q", remote.URL, tc.wantURL)
			}
			if got := doc.DisplayTitle(); got != tc.wantTitle {
				t.Errorf("title = %q, want %q", got, tc.wantTitle)
			}

			inputs, err := doc.Inputs()
			if err != nil {
				t.Fatalf("inputs: %v", err)
			}
			var keys, secrets []string
			for _, in := range inputs {
				keys = append(keys, in.Key)
				if in.Input.IsSecret {
					secrets = append(secrets, in.Key)
				}
			}
			if strings.Join(keys, ",") != strings.Join(tc.wantInputs, ",") {
				t.Errorf("inputs = %v, want %v", keys, tc.wantInputs)
			}
			if strings.Join(secrets, ",") != strings.Join(tc.wantSecret, ",") {
				t.Errorf("secrets = %v, want %v", secrets, tc.wantSecret)
			}
		})
	}
}

func TestResolve_SubstitutesVariablesAndBuildsHeaders(t *testing.T) {
	doc, err := Parse(fixture(t, "autorfp.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	endpoint, headers, err := doc.Resolve(map[string]string{"var_api_host": "api.eu.autorfp.ai"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if endpoint != "https://api.eu.autorfp.ai/mcp" {
		t.Errorf("endpoint = %q", endpoint)
	}
	if len(headers) != 0 {
		t.Errorf("headers = %v, want none", headers)
	}

	doc, err = Parse(fixture(t, "contabo.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, headers, err = doc.Resolve(map[string]string{
		"header_authorization": "Bearer sekrit-token-value",
		"header_x_request_id":  "abc",
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if headers["Authorization"] != "Bearer sekrit-token-value" {
		t.Errorf("Authorization = %q", headers["Authorization"])
	}
	if headers["x-request-id"] != "abc" {
		t.Errorf("x-request-id = %q", headers["x-request-id"])
	}
}

func TestResolve_SaysWhatIsMissing(t *testing.T) {
	doc, err := Parse(fixture(t, "autorfp.json"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, _, err := doc.Resolve(nil); err == nil ||
		!strings.Contains(err.Error(), "api_host") {
		t.Fatalf("expected the missing variable to be named, got %v", err)
	}
}

func TestParse_Refusals(t *testing.T) {
	base := func(body string) []byte {
		return []byte(`{"$schema":"` + SchemaURI + `","name":"io.example/x",` +
			`"description":"d","version":"1","remotes":[{` + body + `}]}`)
	}

	tests := []struct {
		name     string
		document []byte
		wantIn   string
	}{
		{
			name:     "no schema declared",
			document: []byte(`{"name":"io.example/x","description":"d","version":"1"}`),
			wantIn:   "declares no $schema",
		},
		{
			name: "a schema this build does not read",
			document: []byte(`{"$schema":"https://static.modelcontextprotocol.io/schemas/2099-01-01/server.schema.json",` +
				`"name":"io.example/x","description":"d","version":"1"}`),
			wantIn: "2099-01-01",
		},
		{
			name:     "no remote to connect to",
			document: []byte(`{"$schema":"` + SchemaURI + `","name":"io.example/x","description":"d","version":"1"}`),
			wantIn:   "declares no remotes",
		},
		{
			name:     "only sse",
			document: base(`"type":"sse","url":"https://x.test/sse"`),
			wantIn:   "connects over streamable-http",
		},
		{
			name:     "plaintext to a remote host",
			document: base(`"type":"streamable-http","url":"http://x.test/mcp"`),
			wantIn:   "travels in the clear",
		},
		{
			name:     "credentials in the url",
			document: base(`"type":"streamable-http","url":"https://user:pw@x.test/mcp"`),
			wantIn:   "carries credentials in the url",
		},
		{
			name: "a file path for a server somewhere else",
			document: base(`"type":"streamable-http","url":"https://x.test/mcp",` +
				`"variables":{"cert":{"format":"filepath"}}`),
			wantIn: "meaningless for a server running somewhere else",
		},
		{
			name: "a secret sitting in the document",
			document: base(`"type":"streamable-http","url":"https://x.test/mcp",` +
				`"headers":[{"name":"Authorization","isSecret":true,"value":"Bearer hunter2"}]`),
			wantIn: "belongs in the settings store",
		},
		{
			name: "a header the transport owns",
			document: base(`"type":"streamable-http","url":"https://x.test/mcp",` +
				`"headers":[{"name":"Mcp-Session-Id"}]`),
			wantIn: "set by the transport",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.document)
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", err, tc.wantIn)
			}
		})
	}
}

func TestParse_UnsupportedSchemaIsMatchable(t *testing.T) {
	_, err := Parse([]byte(`{"$schema":"https://example.test/other.json","name":"a/b",` +
		`"description":"d","version":"1"}`))
	if !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("expected ErrUnsupportedSchema, got %v", err)
	}
}

// TestParse_LoopbackHTTPIsAllowed keeps local development possible without
// making plaintext to a third party possible.
func TestParse_LoopbackHTTPIsAllowed(t *testing.T) {
	_, err := Parse([]byte(`{"$schema":"` + SchemaURI + `","name":"io.example/x",` +
		`"description":"d","version":"1","remotes":[{"type":"streamable-http",` +
		`"url":"http://127.0.0.1:9000/mcp"}]}`))
	if err != nil {
		t.Fatalf("loopback http should be allowed: %v", err)
	}
}

func TestSlug(t *testing.T) {
	tests := map[string]string{
		"Authorization": "authorization",
		"x-request-id":  "x_request_id",
		"API_KEY":       "api_key",
		"api.host":      "api_host",
		"  spaced  ":    "spaced",
	}
	for in, want := range tests {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactor_BlanksResolvedSecrets(t *testing.T) {
	r := NewRedactor([]string{"Bearer sk_live_abcdef", "short"})
	msg := r.String("dial https://x.test/mcp failed: sent Bearer sk_live_abcdef")
	if strings.Contains(msg, "sk_live_abcdef") {
		t.Errorf("the secret survived redaction: %q", msg)
	}
	if !strings.Contains(msg, "[redacted]") {
		t.Errorf("expected a replacement marker, got %q", msg)
	}
	// A very short value would blank half of every message and is not
	// protecting much anyway.
	if !strings.Contains(r.String("the short way"), "short") {
		t.Error("a very short secret should not be treated as one")
	}
}
