package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/apikeys"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/config"
)

// An API key scoped to one plugin reaches one plugin, on every path, even when
// it belongs to a group that grants everything.
//
// This is the regression test for a live vulnerability. Grants used to be the
// union of a subject's own list and every group it belonged to, so a key saved
// as ["echo"] -- and displayed in the dashboard as ["echo"] -- that also
// belonged to a group granting ["*"] reached every plugin on the host. An audit
// against a running instance called Graylog and Textable tools with a key
// scoped to Bandwidth, and both reached their real upstreams using mcpd's
// stored credentials.
//
// The failure was fail-open and silent: nothing errored, nothing was logged,
// and the key looked correctly bounded everywhere an operator would think to
// look.
//
// Pinned at the HTTP boundary with a real key and a real group, because the
// only thing that made it reachable was the combination -- the auth package
// alone cannot show that the aggregate endpoint honours the answer.
func TestScopedAPIKey_IsNotWidenedByItsGroups(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "mcpd.db")
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Plugins = map[string]config.PluginConfig{
		"echo": {Enabled: true},
		"graylog": {Enabled: true, Settings: map[string]any{
			"base_url": "https://graylog.invalid", "token": "t"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })
	h := a.Handler()

	// A real database-backed key, not a static one from the configuration
	// file. That distinction is the whole point: a file token carries its
	// grants literally and was never affected, so a test written against one
	// passes against the vulnerable code and proves nothing. The first version
	// of this test did exactly that.
	ctx := context.Background()
	const actor = "user:admin@example.com"
	everything, err := a.groups.Create(ctx, actor, groups.CreateRequest{
		Name: "Everything", Plugins: []string{auth.Wildcard},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	key, scoped, err := a.keys.Create(ctx, actor, apikeys.CreateRequest{
		Name: "Echo Only", Role: auth.RoleAdmin, Plugins: []string{"echo"},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if err := a.groups.AddMember(ctx, actor, everything.ID, groups.Key(key.ID)); err != nil {
		t.Fatalf("add to group: %v", err)
	}

	// The aggregate endpoint, which is where the vulnerability was reachable.
	// Its catalogue must be the grant, not the estate.
	tools := toolNames(t, h, "/mcp", scoped)
	if len(tools) == 0 {
		t.Fatal("the scoped token was advertised no tools at all")
	}
	for _, name := range tools {
		if !strings.HasPrefix(name, "echo_") {
			t.Errorf("aggregate tools/list offered %q to a key scoped to echo", name)
		}
	}

	// And calling out of scope is refused rather than executed. The key's role
	// is admin deliberately: an administrator is not exempt from plugin scope,
	// and the audit's other hypothesis was that the role was the bypass. It is
	// not -- AuthorizeEndpoint checks the grant whatever the role.
	w := mcpRequest(t, h, "/mcp", scoped, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "graylog_list_streams", "arguments": map[string]any{},
		},
	})
	if body := w.Body.String(); !strings.Contains(body, "error") {
		t.Errorf("a key scoped to echo executed a graylog tool on /mcp:\n%s", body)
	}

	// The per-plugin path was already correct, and stays correct.
	w = mcpRequest(t, h, "/mcp/graylog", scoped, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": map[string]any{},
	})
	if w.Code == 200 && strings.Contains(w.Body.String(), "graylog_") {
		t.Errorf("a key scoped to echo listed graylog tools on /mcp/graylog:\n%s",
			w.Body.String())
	}
}

// toolNames lists the tool names an endpoint advertises to a token.
func toolNames(t *testing.T, h http.Handler, path, token string) []string {
	t.Helper()
	w := mcpRequest(t, h, path, token, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("%s tools/list: %d %s", path, w.Code, w.Body.String())
	}
	body := w.Body.String()
	i := strings.Index(body, `{"jsonrpc"`)
	if i < 0 {
		t.Fatalf("%s: no JSON-RPC envelope in %s", path, body)
	}
	var env struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body[i:]), &env); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	out := make([]string, 0, len(env.Result.Tools))
	for _, tl := range env.Result.Tools {
		out = append(out, tl.Name)
	}
	return out
}
