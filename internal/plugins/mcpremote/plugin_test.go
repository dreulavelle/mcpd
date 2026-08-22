package mcpremote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/plugins"
)

func testDeps() plugins.Deps {
	return plugins.Deps{
		Instance: "weather",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}
}

// fixtureServer is a real MCP server over streamable HTTP that records the
// headers it was sent, so a test can check what actually left the process.
type fixtureServer struct {
	*httptest.Server
	seen chan http.Header
}

func newFixtureServer(t *testing.T) *fixtureServer {
	t.Helper()
	fs := &fixtureServer{seen: make(chan http.Header, 16)}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{
			Name: "fixture", Title: "Fixture", Version: "1.0.0",
		}, nil)
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "getWeather",
			Description: "Reads the forecast.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`),
		}, func(_ context.Context, _ *mcp.CallToolRequest, in map[string]any) (*mcp.CallToolResult, any, error) {
			return nil, map[string]any{"city": in["city"], "sky": "clear"}, nil
		})
		return srv
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})

	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case fs.seen <- r.Header.Clone():
		default:
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(fs.Close)
	return fs
}

func documentFor(t *testing.T, url string, extra string) *mcpservers.Document {
	t.Helper()
	body := fmt.Sprintf(`{
		"$schema": %q,
		"name": "io.example/fixture",
		"title": "Fixture",
		"description": "A fixture server.",
		"version": "1.0.0",
		"remotes": [{"type": "streamable-http", "url": %q%s}]
	}`, mcpservers.SchemaURI, url, extra)
	doc, err := mcpservers.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func snapshotOfServer(t *testing.T, p *Plugin) []mcpservers.Tool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tools, err := p.Discover(ctx)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	return tools
}

// TestPlugin_DiscoverThenCall exercises the real transport end to end: dial,
// initialize, tools/list, then a call against a snapshot mounted from what
// came back.
func TestPlugin_DiscoverThenCall(t *testing.T) {
	fs := newFixtureServer(t)
	doc := documentFor(t, fs.URL, "")

	probe, err := New(Options{Instance: "weather", Document: doc, Deps: testDeps()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	tools := snapshotOfServer(t, probe)
	if err := probe.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	if len(tools) != 1 || tools[0].Name != "getWeather" {
		t.Fatalf("discovered = %+v", tools)
	}
	if tools[0].Problem != "" {
		t.Fatalf("a well-formed tool should have no problem: %q", tools[0].Problem)
	}
	if tools[0].Hash == "" {
		t.Error("a discovered tool must carry a descriptor hash")
	}

	p, err := New(Options{
		Instance: "weather", Document: doc, Tools: tools, Deps: testDeps(),
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	out, err := p.call(context.Background(), "getWeather", map[string]any{"city": "Leeds"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	result, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want a structured object", out)
	}
	if result["city"] != "Leeds" || result["sky"] != "clear" {
		t.Errorf("result = %#v", result)
	}
}

// TestPlugin_SendsTheConfiguredHeaders checks that a credential resolved out of
// the settings store is what actually goes on the wire -- not the document's
// own value, and not nothing.
func TestPlugin_SendsTheConfiguredHeaders(t *testing.T) {
	fs := newFixtureServer(t)
	doc := documentFor(t, fs.URL,
		`,"headers":[{"name":"Authorization","isSecret":true,"isRequired":true}]`)

	p, err := New(Options{
		Instance: "weather", Document: doc, Deps: testDeps(),
		Values: map[string]string{"header_authorization": "Bearer sk_live_fixture_token"},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	if _, err := p.Discover(context.Background()); err != nil {
		t.Fatalf("discover: %v", err)
	}

	select {
	case h := <-fs.seen:
		if got := h.Get("Authorization"); got != "Bearer sk_live_fixture_token" {
			t.Errorf("Authorization = %q", got)
		}
	default:
		t.Fatal("the server saw no request")
	}
}

// TestPlugin_RegisterReadsOnlyTheSnapshot is the load-bearing property, at the
// level it is implemented: Register never touches the network, so it works
// against an address nothing is listening on.
func TestPlugin_RegisterReadsOnlyTheSnapshot(t *testing.T) {
	doc := documentFor(t, "http://127.0.0.1:1/mcp", "")
	descriptor := mcpservers.Descriptor{
		Name:        "getWeather",
		Description: "Reads the forecast.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
	hash, err := mcpservers.HashDescriptor(descriptor)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	p, err := New(Options{
		Instance: "weather", Document: doc, Deps: testDeps(),
		Tools: []mcpservers.Tool{{Name: "getWeather", Descriptor: descriptor, Hash: hash}},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	m := plugins.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), "test",
		func(context.Context, string, auth.Capability) error { return nil }, nil, nil)
	if err := m.Register(context.Background(), p, "weather", false); err != nil {
		t.Fatalf("register must not need the network: %v", err)
	}
	if names := m.Lookup("weather").Registry.ToolNames(); len(names) != 1 {
		t.Fatalf("tools = %v", names)
	}
}

// TestPlugin_StartDoesNotFailOnAnUnreachableServer records a deliberate
// departure from how a compiled-in plugin behaves.
//
// For one of those, a Start that fails usually means a wrong credential, and
// refusing to take up new settings while leaving the working ones in place is
// the kind thing to do. A remote MCP server being down is not a configuration
// error, and treating it as one would tell an operator correcting a header
// while the far end is unreachable that their change "did not start" -- with
// the old value silently still in force.
func TestPlugin_StartDoesNotFailOnAnUnreachableServer(t *testing.T) {
	doc := documentFor(t, "http://127.0.0.1:1/mcp", "")
	p, err := New(Options{Instance: "weather", Document: doc, Deps: testDeps()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start must not fail because the far end is down: %v", err)
	}
	if got := p.Check(context.Background()); got.State != plugins.UnhealthyState {
		t.Errorf("health = %q, want unhealthy", got.State)
	}
}

// TestPlugin_HealthDoesNotEchoACredential defends the readiness endpoint,
// which is unauthenticated: a failed dial naturally wants to quote the address
// it failed on, and a variable may have put a token in it.
func TestPlugin_HealthDoesNotEchoACredential(t *testing.T) {
	const token = "sk_live_do_not_leak_this"
	doc := documentFor(t, "http://127.0.0.1:1/mcp?key={api_key}",
		`,"variables":{"api_key":{"isSecret":true,"isRequired":true}}`)

	p, err := New(Options{
		Instance: "weather", Document: doc, Deps: testDeps(),
		Values: map[string]string{"var_api_key": token},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if !strings.Contains(p.Endpoint(), token) {
		t.Fatalf("the fixture is not exercising the case: %q", p.Endpoint())
	}

	health := p.Check(context.Background())
	if health.State != plugins.UnhealthyState {
		t.Fatalf("health = %q, want unhealthy", health.State)
	}
	if strings.Contains(health.Message, token) {
		t.Errorf("the credential reached the health message: %q", health.Message)
	}

	// And on the tool-call path, which is where a model would see it.
	_, err = p.call(context.Background(), "getWeather", nil)
	if err == nil {
		t.Fatal("expected the call to fail")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("the credential reached the tool error: %v", err)
	}
}

// TestPlugin_RegisterRefusesWithNothingEnabled: a server whose tools are all
// still pending is the ordinary state right after an import, and the honest
// report is that there is nothing to serve rather than an empty mount.
func TestPlugin_RegisterRefusesWithNothingEnabled(t *testing.T) {
	doc := documentFor(t, "http://127.0.0.1:1/mcp", "")
	p, err := New(Options{Instance: "weather", Document: doc, Deps: testDeps()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	m := plugins.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), "test",
		func(context.Context, string, auth.Capability) error { return nil }, nil, nil)
	err = m.Register(context.Background(), p, "weather", false)
	if err == nil || !strings.Contains(err.Error(), "discover them") {
		t.Fatalf("expected a report naming the next step, got %v", err)
	}
}

// TestBudget_BoundsTheWholeServer records why the host's per-tool limiter is
// not enough here: thirty tools behind one address are one upstream.
func TestBudget_BoundsTheWholeServer(t *testing.T) {
	b := newBudget(2)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Burst of one, so the first call is free and the second must wait a full
	// half second -- longer than this context lives.
	if err := b.wait(ctx); err != nil {
		t.Fatalf("the first call should not wait: %v", err)
	}
	if err := b.wait(ctx); err == nil {
		t.Error("a second call inside the same budget window must be made to wait")
	}

	if err := newBudget(0).wait(context.Background()); err != nil {
		t.Errorf("zero must mean unbounded: %v", err)
	}
}
