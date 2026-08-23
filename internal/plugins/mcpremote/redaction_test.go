package mcpremote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/mcpservers"
)

// TestRedactor_DoesNotTrustTheDocumentsClaim is C8.
//
// isSecret is a field in a third party's document. Letting it decide what the
// redactor holds puts the party being defended against in charge of the
// defence: a server declaring its Authorization header isSecret:false keeps
// the operator's pasted key out of the redactor, and every error path then
// prints it verbatim.
func TestRedactor_DoesNotTrustTheDocumentsClaim(t *testing.T) {
	const key = "Bearer sk_live_the_operators_actual_key"

	// Note what this document says about its own credential.
	doc := documentFor(t, "https://x.test/mcp",
		`,"headers":[{"name":"Authorization","isSecret":false,"isRequired":true}]`)

	values := map[string]string{"header_authorization": key}
	sensitive := doc.SensitiveValues(values)

	var found bool
	for _, v := range sensitive {
		if v == key {
			found = true
		}
	}
	if !found {
		t.Fatalf("a resolved header value must be redactable whatever the "+
			"document claims about it; got %v", sensitive)
	}

	redacted := mcpservers.NewRedactor(sensitive).
		String("dial failed, sent " + key)
	if strings.Contains(redacted, "sk_live") {
		t.Errorf("the credential survived: %q", redacted)
	}
}

// A URL variable is the same story: it is substituted into the address, and
// the address is quoted in refusals.
func TestRedactor_CoversURLVariablesToo(t *testing.T) {
	const slug = "tenant-abcdef-private-slug"
	doc := documentFor(t, "https://x.test/{tenant}/mcp",
		`,"variables":{"tenant":{"isRequired":true}}`)

	sensitive := doc.SensitiveValues(map[string]string{"var_tenant": slug})
	if len(sensitive) == 0 || sensitive[0] != slug {
		t.Fatalf("a resolved variable must be redactable, got %v", sensitive)
	}
}

// TestDiscover_RedactsWhatTheServerEchoesBack is C9.
//
// The remote holds our credential -- it receives it on every request -- so it
// can hand it back in a name, a description or a schema. Those are persisted
// to SQLite and read by people and by models, so they are blanked on the way
// in rather than at each of the places they would later come out.
func TestDiscover_RedactsWhatTheServerEchoesBack(t *testing.T) {
	const key = "Bearer sk_live_echoed_straight_back"

	echo := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		seen := r.Header.Get("Authorization")
		srv := mcp.NewServer(&mcp.Implementation{
			Name: "echo", Title: "Echo", Version: "1.0.0",
		}, nil)
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "getWeather",
			Title:       "Title carrying " + seen,
			Description: "This description contains " + seen + " on purpose.",
			InputSchema: json.RawMessage(fmt.Sprintf(
				`{"type":"object","description":%q,"properties":{}}`, seen)),
		}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return nil, map[string]any{}, nil
		})
		return srv
	}, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	t.Cleanup(echo.Close)

	doc := documentFor(t, echo.URL,
		`,"headers":[{"name":"Authorization","isSecret":true,"isRequired":true}]`)
	p, err := New(Options{
		Instance: "weather", Document: doc, Deps: testDeps(),
		Values: map[string]string{"header_authorization": key},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	tools, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got %d", len(tools))
	}

	// Everything that will be written to the database.
	stored, err := json.Marshal(tools[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "sk_live_echoed_straight_back") {
		t.Errorf("the credential was about to be persisted: %s", stored)
	}
	// Not merely scrubbed: refused. A server echoing a credential back into
	// its own catalogue is something an operator has to see, so the tool
	// carries the reason and can never be enabled.
	if !strings.Contains(tools[0].Problem, "echoed back a value configured for it") {
		t.Errorf("expected the echo to disqualify the tool, got problem %q", tools[0].Problem)
	}
	// The fixture has to actually be echoing, or this proves nothing.
	if !strings.Contains(tools[0].Descriptor.Title, "[redacted]") {
		t.Errorf("the fixture did not echo into the title: %q", tools[0].Descriptor.Title)
	}
	if tools[0].Descriptor.InputSchema != nil {
		t.Errorf("a schema that carried a credential must not be stored: %s",
			tools[0].Descriptor.InputSchema)
	}
}

// TestSnapshot_HashDoesNotDependOnSettings is the regression for the bug that
// redacting descriptors introduced.
//
// descriptor_hash is the guard on every classification. If the stored
// descriptor were rewritten using anything resolved from settings, the hash
// would become a function of the operator's configuration as well as the
// server's output -- and editing an unrelated non-secret field would change
// the hashes on the next discovery, trip the demotion in Snapshot, and
// silently un-approve every affected tool.
func TestSnapshot_HashDoesNotDependOnSettings(t *testing.T) {
	// A description that mentions a value an operator might plausibly have
	// configured, which is exactly the coincidence that used to mangle it.
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		s := mcp.NewServer(&mcp.Implementation{
			Name: "fixture", Title: "Fixture", Version: "1.0.0",
		}, nil)
		mcp.AddTool(s, &mcp.Tool{
			Name:        "getWeather",
			Title:       "Weather for us-east",
			Description: "Reads the forecast for the us-east region in english.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		}, func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, any, error) {
			return nil, map[string]any{}, nil
		})
		return s
	}, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	t.Cleanup(srv.Close)

	doc := documentFor(t, srv.URL+"/{region}/mcp",
		`,"variables":{"region":{"isRequired":true}}`)

	discover := func(region string) mcpservers.Tool {
		t.Helper()
		p, err := New(Options{
			Instance: "weather", Document: doc, Deps: testDeps(),
			Values: map[string]string{"var_region": region},
		})
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		defer func() { _ = p.Shutdown(context.Background()) }()

		tools, err := p.Discover(context.Background())
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		if len(tools) != 1 {
			t.Fatalf("expected one tool, got %d", len(tools))
		}
		return tools[0]
	}

	// The first value appears verbatim in the tool's title and description.
	withMatch := discover("us-east")
	withoutMatch := discover("eu-west")

	if withMatch.Hash != withoutMatch.Hash {
		t.Errorf("the descriptor hash moved when an unrelated setting changed:\n"+
			"  us-east: %s\n  eu-west: %s\nEvery approved tool would have been "+
			"silently demoted to pending.", withMatch.Hash, withoutMatch.Hash)
	}
	if withMatch.Problem != "" {
		t.Errorf("a benign setting appearing in a description must not disqualify "+
			"the tool: %q", withMatch.Problem)
	}
	if !strings.Contains(withMatch.Descriptor.Description, "us-east") {
		t.Errorf("catalogue text must reach the model as published, got %q",
			withMatch.Descriptor.Description)
	}
	if withMatch.Descriptor.Name != "getWeather" {
		t.Errorf("the tool name must be as published, got %q", withMatch.Descriptor.Name)
	}
}
