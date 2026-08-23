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
	if !strings.Contains(tools[0].Descriptor.Description, "[redacted]") {
		t.Errorf("expected the echoed value to be replaced, got %q",
			tools[0].Descriptor.Description)
	}
	// The fixture has to actually be echoing something, or this proves nothing.
	if !strings.Contains(tools[0].Descriptor.Title, "[redacted]") {
		t.Errorf("the fixture did not echo into the title: %q", tools[0].Descriptor.Title)
	}
}
