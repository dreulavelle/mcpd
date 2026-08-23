package mcpremote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/plugins"
)

// testImpl is the client identity these tests dial with.
func testImpl() *mcp.Implementation {
	return &mcp.Implementation{Name: "mcpd", Title: "mcpd", Version: "test"}
}

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

// TestNew_ConstructionErrorDoesNotEchoACredential covers the path the other
// redaction test misses.
//
// Resolve substitutes variables into the URL and then re-checks the result,
// quoting the whole address in its refusals. A secret variable holding a
// scheme-less host produces exactly that refusal -- and it is the error the
// host records as the reason a plugin will not mount, which reaches the
// Plugins page, the reconcile log line, and the /discover response body.
func TestNew_ConstructionErrorDoesNotEchoACredential(t *testing.T) {
	const token = "mcp.example.com/abc123SECRET"
	doc := documentFor(t, "https://{base_url}/mcp",
		`,"variables":{"base_url":{"isSecret":true,"isRequired":true}}`)

	// The substituted URL is https://mcp.example.com/abc123SECRET/mcp, which
	// parses -- so force the refusal with a value that does not.
	_, err := New(Options{
		Instance: "weather", Document: doc, Deps: testDeps(),
		Values: map[string]string{"var_base_url": "not a host " + token},
	})
	if err == nil {
		t.Fatal("expected construction to fail on an unusable address")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("the credential reached the construction error: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("expected the value to be replaced, got %v", err)
	}
}

// TestPlugin_HealthDoesNotEchoACredential defends every message a resolved
// credential could ride out on: a failed dial naturally wants to quote the
// address it failed on, and a variable may have put a token in it.
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
	if !strings.Contains(p.endpoint, token) {
		t.Fatalf("the fixture is not exercising the case: %q", p.endpoint)
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

// TestCheck_ReturnsWithinItsDeadline is the readiness probe's guarantee.
//
// The probe is served unauthenticated and runs every plugin's check in turn on
// one shared two-second budget. Before this, a dial ran on a context detached
// from the caller with its own twenty-second timeout, while holding the
// connection mutex -- so a server that accepts a connection and then says
// nothing cost the whole probe twenty seconds per server, and an orchestrator
// restarted a host that was serving perfectly well.
func TestCheck_ReturnsWithinItsDeadline(t *testing.T) {
	// A listener that accepts into the kernel backlog and never reads. The TCP
	// connect succeeds, so this is not a refused dial -- it is the case that
	// used to hang.
	blackhole, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = blackhole.Close() })

	doc := documentFor(t, "http://"+blackhole.Addr().String()+"/mcp", "")
	p, err := New(Options{Instance: "weather", Document: doc, Deps: testDeps()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	const budget = 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	start := time.Now()
	health := p.Check(ctx)
	elapsed := time.Since(start)

	if elapsed > budget*3 {
		t.Errorf("Check took %s against a %s budget; the probe's deadline is not "+
			"reaching the dial", elapsed, budget)
	}
	if health.State == plugins.HealthyState {
		t.Errorf("a server that never answers is not healthy, got %+v", health)
	}
}

// TestCheck_DoesNotDialOnAnExhaustedBudget: spending what is left of a shared
// probe budget on a handshake nobody will wait for takes it from the checks
// that could have answered.
func TestCheck_DoesNotDialOnAnExhaustedBudget(t *testing.T) {
	doc := documentFor(t, "http://127.0.0.1:1/mcp", "")
	p, err := New(Options{Instance: "weather", Document: doc, Deps: testDeps()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	start := time.Now()
	health := p.Check(ctx)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("Check took %s on an exhausted budget; it should have reported "+
			"what it last observed", elapsed)
	}
	// Which, before anything has connected, is the state New recorded.
	if health.State != plugins.DegradedState {
		t.Errorf("state = %q, want the recorded %q", health.State, plugins.DegradedState)
	}
}

// TestShutdown_DoesNotWaitOutAnInFlightDial: Shutdown runs on a bounded
// context, and an unreachable server is exactly when a dial is still running.
func TestShutdown_DoesNotWaitOutAnInFlightDial(t *testing.T) {
	blackhole, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = blackhole.Close() })

	doc := documentFor(t, "http://"+blackhole.Addr().String()+"/mcp", "")
	p, err := New(Options{Instance: "weather", Document: doc, Deps: testDeps()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Start a dial and leave it in flight.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_ = p.Check(ctx)
	cancel()

	done := make(chan error, 1)
	go func() { done <- p.Shutdown(context.Background()) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown queued behind an in-flight dial")
	}
}

// TestPlugin_HealthIsWhatStartObserved: the host reads this at boot instead of
// dialling the same unreachable address a second time.
func TestPlugin_HealthIsWhatStartObserved(t *testing.T) {
	doc := documentFor(t, "http://127.0.0.1:1/mcp", "")
	p, err := New(Options{Instance: "weather", Document: doc, Deps: testDeps()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := p.Health(); got.State != plugins.UnhealthyState {
		t.Errorf("Health() = %q, want what Start observed (%q)",
			got.State, plugins.UnhealthyState)
	}

	var reporter plugins.HealthReporter = p
	if reporter.Health().State != plugins.UnhealthyState {
		t.Error("the plugin must satisfy HealthReporter, which is how the " +
			"manager avoids a second dial at startup")
	}
}

// TestSnapshotOf_BoundsWhatAServerCanMakeUsStore.
//
// A discovery is written in one transaction against the single SQLite writer,
// and a description goes into the tool catalogue a model chooses from.
func TestSnapshotOf_BoundsWhatAServerCanMakeUsStore(t *testing.T) {
	huge := strings.Repeat("x", maxDescription*2)
	tool := &mcp.Tool{
		Name:        "getWeather",
		Title:       strings.Repeat("t", maxTitle*2),
		Description: huge,
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}
	got, size, err := snapshotOf("weather", tool, mcpservers.NewRedactor(nil))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(got.Descriptor.Description) > maxDescription+64 {
		t.Errorf("description is %d bytes, want it truncated", len(got.Descriptor.Description))
	}
	if len(got.Descriptor.Title) > maxTitle+64 {
		t.Errorf("title is %d bytes, want it truncated", len(got.Descriptor.Title))
	}
	if got.Problem != "" {
		t.Errorf("truncating text should not disqualify the tool: %q", got.Problem)
	}
	if size <= 0 {
		t.Error("the caller needs a size to bound the whole discovery by")
	}

	// A schema is different: it cannot be shortened without changing what it
	// validates, so the tool is kept with the reason and can never be enabled.
	oversized := map[string]any{"type": "object", "description": strings.Repeat("s", maxInputSchema)}
	got, _, err = snapshotOf("weather", &mcp.Tool{
		Name: "getWeather", InputSchema: oversized,
	}, mcpservers.NewRedactor(nil))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if got.Problem == "" {
		t.Error("an oversized schema must disqualify the tool")
	}
	if len(got.Descriptor.InputSchema) != 0 {
		t.Error("half a schema is not a schema; it must not be stored truncated")
	}
}

// TestTruncate_DoesNotSplitARune: byte-slicing third-party text produces
// invalid UTF-8, which then travels into JSON and into the tool catalogue.
func TestTruncate_DoesNotSplitARune(t *testing.T) {
	// Three bytes per rune, so a byte cut lands mid-rune for two cuts in three.
	text := strings.Repeat("\u3042", 100)
	for n := 10; n < 20; n++ {
		got := truncate(text, n)
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(..., %d) produced invalid UTF-8: %q", n, got)
		}
	}
	if got := truncate("short", 100); got != "short" {
		t.Errorf("text inside the limit must be untouched, got %q", got)
	}
}
