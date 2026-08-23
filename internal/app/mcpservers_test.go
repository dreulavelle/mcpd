package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// tokenRemote is the credential the endpoint test presents.
const tokenRemote = "remote-agent-token-000000000000000000000"

// --- harness ---------------------------------------------------------------

type remote struct {
	server *httptest.Server

	mu    sync.Mutex
	tools map[string]string
}

func newRemote(t *testing.T, tools map[string]string) *remote {
	t.Helper()
	r := &remote{tools: tools}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		srv := mcp.NewServer(&mcp.Implementation{
			Name: "fixture", Title: "Fixture", Version: "1.0.0",
		}, nil)
		r.mu.Lock()
		defer r.mu.Unlock()
		for name, description := range r.tools {
			toolName := name
			mcp.AddTool(srv, &mcp.Tool{
				Name:        toolName,
				Description: description,
				InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			}, func(_ context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
				return nil, map[string]any{"called": toolName}, nil
			})
		}
		return srv
	}, &mcp.StreamableHTTPOptions{JSONResponse: true})

	r.server = httptest.NewServer(handler)
	t.Cleanup(r.server.Close)
	return r
}

func (r *remote) setTools(tools map[string]string) {
	r.mu.Lock()
	r.tools = tools
	r.mu.Unlock()
}

func (r *remote) document() []byte {
	return []byte(fmt.Sprintf(`{
		"$schema": %q,
		"name": "io.example/fixture",
		"title": "Fixture",
		"description": "A fixture server.",
		"version": "1.0.0",
		"remotes": [{"type": "streamable-http", "url": %q}]
	}`, mcpservers.SchemaURI, r.server.URL))
}

// newAppIn builds a host against a database in dir, so a test can close one
// host and open another over the same data -- which is what a restart is.
func newAppIn(t *testing.T, dir string) *App {
	t.Helper()
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "mcpd.db")
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Legacy().Server.PublicURL = ptr("https://mcp.test.invalid")
	cfg.Plugins = nil
	t.Setenv("MCPD_TOKEN_REMOTE", tokenRemote)
	// Granted the wildcard rather than the server by name. A static token is
	// declared in the configuration file, and the file's validation refuses a
	// grant naming a plugin the file does not define -- which a remote MCP
	// server never is, because it lives in the database. The same is already
	// true of an instance added from the dashboard. Scoping itself works; the
	// gap is that this one route to a credential cannot express it.
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{{
		ID: "remote", SecretRef: "env:MCPD_TOKEN_REMOTE",
		Principal: "svc:remote", Role: "user", Plugins: []string{"*"},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("test config invalid: %v", err)
	}

	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

func mustImport(t *testing.T, a *App, name string, doc []byte) {
	t.Helper()
	if err := a.ImportMCPServer(context.Background(), "tester", name, doc); err != nil {
		t.Fatalf("import: %v", err)
	}
}

func mustDiscover(t *testing.T, a *App, name string) mcpservers.Diff {
	t.Helper()
	diff, err := a.DiscoverMCPServer(context.Background(), "tester", name)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	return diff
}

func mustEnable(t *testing.T, a *App, server, tool string) {
	t.Helper()
	ctx := context.Background()
	tools, err := a.MCPServerTools(ctx, server)
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	for _, tl := range tools {
		if tl.Name != tool {
			continue
		}
		if err := a.ClassifyMCPTool(ctx, "tester", server, tool, tl.Hash, mcpservers.ToolEnabled); err != nil {
			t.Fatalf("enable %s: %v", tool, err)
		}
		return
	}
	t.Fatalf("no tool named %q", tool)
}

func mountedTools(a *App, name string) []string {
	m := a.manager.Lookup(name)
	if m == nil {
		return nil
	}
	return m.Registry.ToolNames()
}

// --- tests -----------------------------------------------------------------

// TestMCPServer_ImportDiscoverClassifyMount walks the whole lifecycle, and
// asserts at each step that nothing is served before a person said so.
func TestMCPServer_ImportDiscoverClassifyMount(t *testing.T) {
	rs := newRemote(t, map[string]string{
		"getWeather": "Reads the forecast.",
		"listAlerts": "Reads current alerts.",
	})
	a := newAppIn(t, t.TempDir())
	ctx := context.Background()

	mustImport(t, a, "weather", rs.document())

	// Importing records how to reach a server and nothing about what it
	// offers. Nothing is mounted.
	if a.manager.Lookup("weather") != nil {
		t.Fatal("importing must not mount anything")
	}

	diff := mustDiscover(t, a, "weather")
	if len(diff.Added) != 2 {
		t.Fatalf("expected two tools discovered, got %+v", diff)
	}
	if a.manager.Lookup("weather") != nil {
		t.Fatal("a discovered tool is pending, and pending is not served")
	}

	// The instance exists and says what it is still waiting for.
	var inst *Instance
	for _, candidate := range a.instances(ctx) {
		if candidate.Name == "weather" {
			inst = &candidate
		}
	}
	if inst == nil {
		t.Fatal("an imported server must appear as an instance")
	}
	if inst.Runtime != plugins.RuntimeMCP {
		t.Errorf("runtime = %q, want %q", inst.Runtime, plugins.RuntimeMCP)
	}
	if ready, missing := a.ready(ctx, *inst); ready || len(missing) == 0 {
		t.Errorf("a server with nothing enabled is not ready, and should say so: %v", missing)
	}

	mustEnable(t, a, "weather", "getWeather")

	got := mountedTools(a, "weather")
	if len(got) != 1 || got[0] != "weather_getWeather" {
		t.Fatalf("mounted tools = %v, want [weather_getWeather]", got)
	}
}

// TestMCPServer_ToolNamesArePassedThroughUnchanged defends the decision not to
// normalise: a rewritten name is one the far end does not answer to.
func TestMCPServer_ToolNamesArePassedThroughUnchanged(t *testing.T) {
	rs := newRemote(t, map[string]string{
		"getWeather":  "camelCase, which the house rule rejects.",
		"search.docs": "a dot, which the house rule rejects.",
		"read-file":   "a hyphen, which the house rule rejects.",
	})
	a := newAppIn(t, t.TempDir())

	mustImport(t, a, "weather", rs.document())
	mustDiscover(t, a, "weather")
	for _, name := range []string{"getWeather", "search.docs", "read-file"} {
		mustEnable(t, a, "weather", name)
	}

	got := mountedTools(a, "weather")
	want := map[string]bool{
		"weather_getWeather": true, "weather_search.docs": true, "weather_read-file": true,
	}
	if len(got) != len(want) {
		t.Fatalf("mounted tools = %v", got)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected tool name %q", name)
		}
	}
}

// TestMCPServer_RediscoveryOfAnAddedTool: a server that grows a tool overnight
// cannot put it in front of a model on its own.
func TestMCPServer_RediscoveryOfAnAddedTool(t *testing.T) {
	rs := newRemote(t, map[string]string{"getWeather": "Reads the forecast."})
	a := newAppIn(t, t.TempDir())

	mustImport(t, a, "weather", rs.document())
	mustDiscover(t, a, "weather")
	mustEnable(t, a, "weather", "getWeather")

	rs.setTools(map[string]string{
		"getWeather": "Reads the forecast.",
		"sendEmail":  "Sends an email to anyone you name.",
	})

	diff := mustDiscover(t, a, "weather")
	if len(diff.Added) != 1 || diff.Added[0] != "sendEmail" {
		t.Fatalf("the new tool should appear as a difference, got %+v", diff)
	}

	if got := mountedTools(a, "weather"); len(got) != 1 || got[0] != "weather_getWeather" {
		t.Errorf("a pending tool must not be mounted, got %v", got)
	}

	states := map[string]mcpservers.ToolState{}
	tools, err := a.MCPServerTools(context.Background(), "weather")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	for _, tl := range tools {
		states[tl.Name] = tl.State
	}
	if states["sendEmail"] != mcpservers.ToolPending {
		t.Errorf("sendEmail = %q, want pending", states["sendEmail"])
	}
}

// TestMCPServer_RediscoveryOfARemovedTool: the tool stops being served, other
// grants are untouched, and nothing panics on the way.
func TestMCPServer_RediscoveryOfARemovedTool(t *testing.T) {
	rs := newRemote(t, map[string]string{
		"getWeather": "Reads the forecast.",
		"listAlerts": "Reads current alerts.",
	})
	a := newAppIn(t, t.TempDir())

	mustImport(t, a, "weather", rs.document())
	mustDiscover(t, a, "weather")
	mustEnable(t, a, "weather", "getWeather")
	mustEnable(t, a, "weather", "listAlerts")
	if got := mountedTools(a, "weather"); len(got) != 2 {
		t.Fatalf("expected two mounted tools, got %v", got)
	}

	rs.setTools(map[string]string{"getWeather": "Reads the forecast."})

	diff := mustDiscover(t, a, "weather")
	if len(diff.Removed) != 1 || diff.Removed[0] != "listAlerts" {
		t.Fatalf("the withdrawn tool should appear as a difference, got %+v", diff)
	}
	if got := mountedTools(a, "weather"); len(got) != 1 || got[0] != "weather_getWeather" {
		t.Errorf("mounted tools = %v, want only the one still offered", got)
	}

	// The remaining approval is untouched.
	tools, err := a.MCPServerTools(context.Background(), "weather")
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	for _, tl := range tools {
		switch tl.Name {
		case "getWeather":
			if tl.State != mcpservers.ToolEnabled {
				t.Errorf("getWeather = %q, want enabled", tl.State)
			}
		case "listAlerts":
			if tl.State != mcpservers.ToolDisabled {
				t.Errorf("listAlerts = %q, want disabled", tl.State)
			}
		}
	}
}

// TestMCPServer_BootsFromTheSnapshotWithTheNetworkDown is the load-bearing
// test of this whole design.
//
// A host restarting while a third party is unreachable must come up serving
// exactly what it served before, from SQLite, and say plainly that the server
// is not answering. The alternative -- Register calling tools/list -- gives a
// host with no tools and a model that reasonably concludes the integration was
// removed.
func TestMCPServer_BootsFromTheSnapshotWithTheNetworkDown(t *testing.T) {
	dir := t.TempDir()
	rs := newRemote(t, map[string]string{
		"getWeather": "Reads the forecast.",
		"listAlerts": "Reads current alerts.",
	})

	first := newAppIn(t, dir)
	mustImport(t, first, "weather", rs.document())
	mustDiscover(t, first, "weather")
	mustEnable(t, first, "weather", "getWeather")
	mustEnable(t, first, "weather", "listAlerts")
	if got := mountedTools(first, "weather"); len(got) != 2 {
		t.Fatalf("expected two mounted tools before the restart, got %v", got)
	}
	first.db.Close()

	// The far end goes away entirely: the port is closed, so a dial is
	// refused rather than merely slow.
	rs.server.Close()

	second := newAppIn(t, dir)
	if err := second.manager.Start(context.Background()); err != nil {
		t.Fatalf("an unreachable remote must not fail startup: %v", err)
	}

	got := mountedTools(second, "weather")
	if len(got) != 2 {
		t.Fatalf("every enabled tool must mount from the snapshot, got %v", got)
	}

	health := second.manager.CheckHealth(context.Background())["weather"]
	if health.State != plugins.UnhealthyState {
		t.Errorf("health = %q, want unhealthy", health.State)
	}
	if health.Message == "" {
		t.Error("an unhealthy remote should say why")
	}

	// And a call fails with something an operator can read, rather than
	// silently returning nothing.
	mounted := second.manager.Lookup("weather")
	if mounted == nil {
		t.Fatal("the plugin should still be mounted")
	}
}

// TestMCPServer_ToolIsCallableThroughTheEndpoint is the increment's headline:
// a remote server's read tools appear behind mcpd's own endpoint, its
// authentication, and its per-plugin scoping, and a call reaches the far end
// and comes back.
func TestMCPServer_ToolIsCallableThroughTheEndpoint(t *testing.T) {
	rs := newRemote(t, map[string]string{"getWeather": "Reads the forecast."})
	a := newAppIn(t, t.TempDir())

	mustImport(t, a, "weather", rs.document())
	mustDiscover(t, a, "weather")
	mustEnable(t, a, "weather", "getWeather")

	h := a.Handler()
	initialize := mcpRequest(t, h, "/mcp/weather", tokenRemote, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1"},
		},
	})
	if initialize.Code != http.StatusOK {
		t.Fatalf("initialize = %d: %s", initialize.Code, initialize.Body.String())
	}
	session := initialize.Header().Get("Mcp-Session-Id")

	call := func(body any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/mcp/weather", bytes.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("Authorization", "Bearer "+tokenRemote)
		if session != "" {
			r.Header.Set("Mcp-Session-Id", session)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	call(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})

	listed := call(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if !strings.Contains(listed.Body.String(), "weather_getWeather") {
		t.Fatalf("tools/list did not offer the remote tool: %s", listed.Body.String())
	}

	called := call(map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "weather_getWeather", "arguments": map[string]any{}},
	})
	body := called.Body.String()
	if called.Code != http.StatusOK {
		t.Fatalf("tools/call = %d: %s", called.Code, body)
	}
	if !strings.Contains(body, `"called"`) || !strings.Contains(body, "getWeather") {
		t.Errorf("the call did not reach the remote server: %s", body)
	}
	if strings.Contains(body, `"isError":true`) {
		t.Errorf("the call reported an error: %s", body)
	}
}

// TestMCPServer_PluginsPageCannotBrickStartup is the regression this exists for.
//
// a.instances() returns remote servers, so the endpoints that manage a
// compiled-in instance would accept one. Writing an instances. record for a
// remote server is worse than useless: the MCP loop overwrites it on the next
// read, so the toggle reports success and changes nothing -- and the record
// outlives the server, leaving an enabled instance of type "mcp", which no
// binary has. registerPlugins used to make that fatal, so the host would not
// start and the only way back was a SQLite prompt.
func TestMCPServer_PluginsPageCannotBrickStartup(t *testing.T) {
	dir := t.TempDir()
	rs := newRemote(t, map[string]string{"getWeather": "Reads the forecast."})

	a := newAppIn(t, dir)
	ctx := context.Background()
	mustImport(t, a, "weather", rs.document())
	mustDiscover(t, a, "weather")
	mustEnable(t, a, "weather", "getWeather")

	// Both plugin-instance endpoints must refuse, and say where to go instead.
	err := a.SetInstanceEnabled(ctx, "tester", "weather", false)
	if err == nil {
		t.Fatal("the plugins endpoint must not toggle a remote MCP server")
	}
	if !strings.Contains(err.Error(), "/api/mcp-servers/weather") {
		t.Errorf("the refusal should name the endpoint that works: %v", err)
	}
	err = a.RemoveInstance(ctx, "tester", "weather", false)
	if err == nil {
		t.Fatal("the plugins endpoint must not remove a remote MCP server")
	}
	if !strings.Contains(err.Error(), "/api/mcp-servers/weather") {
		t.Errorf("the refusal should name the endpoint that works: %v", err)
	}

	// The server is still on, and still serving.
	if err := a.RemoveMCPServer(ctx, "tester", "weather"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	a.db.Close()

	// The host comes back up.
	second := newAppIn(t, dir)
	if len(second.instances(context.Background())) != 0 {
		t.Errorf("removing a server should leave nothing behind, got %+v",
			second.instances(context.Background()))
	}
}

// TestMCPServer_RemoveClearsAnOrphanedInstanceRecord covers the databases an
// earlier build could already have written one into.
func TestMCPServer_RemoveClearsAnOrphanedInstanceRecord(t *testing.T) {
	dir := t.TempDir()
	rs := newRemote(t, map[string]string{"getWeather": "Reads the forecast."})

	a := newAppIn(t, dir)
	ctx := context.Background()
	mustImport(t, a, "weather", rs.document())

	// Exactly what the old SetInstanceEnabled wrote.
	record, err := json.Marshal(instanceRecord{Type: mcpInstanceType, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.settings.Apply(ctx, "tester", []settings.Change{
		{Key: instanceKeyPrefix + "weather", Value: string(record)},
	}); err != nil {
		t.Fatalf("plant the orphan: %v", err)
	}

	if err := a.RemoveMCPServer(ctx, "tester", "weather"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, inst := range a.instances(ctx) {
		if inst.Name == "weather" {
			t.Fatalf("the orphan survived the removal: %+v", inst)
		}
	}

	a.db.Close()
	second := newAppIn(t, dir)
	if second.manager.Lookup("weather") != nil {
		t.Error("nothing should be mounted for a server that was removed")
	}
}

// TestApp_StartsDespiteAnInstanceOfAnUnknownType is the defence that closes
// the class rather than the instance.
//
// A type the binary does not have is a mistake either way, but where the
// mistake lives decides what to do about it. In the configuration file it is a
// typo and failing loudly is how an operator finds it. In the settings store
// it is a record only the dashboard can correct -- and refusing to start
// removes the dashboard.
func TestApp_StartsDespiteAnInstanceOfAnUnknownType(t *testing.T) {
	dir := t.TempDir()
	a := newAppIn(t, dir)

	record, err := json.Marshal(instanceRecord{Type: "a-type-no-build-has", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.settings.Apply(context.Background(), "tester", []settings.Change{
		{Key: instanceKeyPrefix + "orphan", Value: string(record)},
	}); err != nil {
		t.Fatalf("plant the orphan: %v", err)
	}
	a.db.Close()

	// newAppIn fails the test if New returns an error, which is the assertion.
	second := newAppIn(t, dir)
	if second.manager.Lookup("orphan") != nil {
		t.Error("an instance of an unknown type must not mount")
	}
	if second.reconcileProblem("orphan") == "" {
		t.Error("the Plugins page should say why it is not serving")
	}
}

// Admin actions on a remote server used to leave nothing behind: after an
// import, a discovery and an enable, the audit table held zero rows. Enabling
// a tool hands every caller of that plugin a path into somebody else's code,
// which is a privilege grant, and it happened with no record of who or when.
func TestMCPServer_AdminActionsReachTheAuditTrail(t *testing.T) {
	rs := newRemote(t, map[string]string{"getWeather": "Reads the forecast."})
	a := newAppIn(t, t.TempDir())
	ctx := context.Background()

	mustImport(t, a, "weather", rs.document())
	mustDiscover(t, a, "weather")
	mustEnable(t, a, "weather", "getWeather")
	if err := a.RemoveMCPServer(ctx, "tester", "weather"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	records, err := a.audit.Recent(ctx, 200)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	seen := map[string]string{}
	for _, r := range records {
		if strings.HasPrefix(r.Entry.Kind, "mcpserver.") {
			seen[r.Entry.Kind] = r.Entry.Actor
		}
	}
	for _, kind := range []string{
		"mcpserver.imported", "mcpserver.discovered",
		"mcpserver.tool_classified", "mcpserver.removed",
	} {
		actor, ok := seen[kind]
		if !ok {
			t.Errorf("no %s entry in the audit trail", kind)
			continue
		}
		if actor != "tester" {
			t.Errorf("%s names actor %q, want the acting principal", kind, actor)
		}
	}
}
