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

// An API key's reach is the union of its own grants and every group it
// belongs to, at the higher level per plugin -- never less than its own
// grant, and never less than a group's either.
//
// This replaces a test that pinned the opposite rule: a key's own grant list
// used to be the ceiling on what it reached, so a key granted echo alone
// stayed at echo whatever group it joined. That rule and the group
// "ceiling" it partnered with were both removed in the same change (see
// internal/auth/groups/groups.go's package doc): grants add up now, and
// nothing subtracts. A key scoped to echo at write that joins a group
// granting every plugin at read reaches echo at write (its own grant is
// higher) and graylog at read (only the group grants it) -- the union, per
// plugin, at whichever side named the higher level.
func TestScopedAPIKey_ReachesUnionOfOwnAndGroupGrants(t *testing.T) {
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
	// file: a file token carries its grants literally and never resolves
	// through a group, so a test written against one would prove nothing
	// about Resolve's union.
	ctx := context.Background()
	const actor = "user:admin@example.com"
	everyoneAtRead, err := a.groups.Create(ctx, actor, groups.CreateRequest{
		Name:   "Everything At Read",
		Grants: auth.Grants{{Plugin: auth.Wildcard, Level: auth.LevelRead}},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	// The role is administrator deliberately: an administrator's grants are
	// not exempt from plugin scope, and AuthorizeEndpoint checks Reaches
	// whatever the role holds.
	key, scoped, err := a.keys.Create(ctx, actor, apikeys.CreateRequest{
		Name:   "Echo Writer",
		RoleID: auth.RoleAdministrator,
		Grants: auth.Grants{{Plugin: "echo", Level: auth.LevelWrite}},
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if err := a.groups.AddMember(ctx, actor, everyoneAtRead.ID, groups.Key(key.ID)); err != nil {
		t.Fatalf("add to group: %v", err)
	}

	// The aggregate endpoint advertises both: echo at write (the key's own,
	// higher grant) and graylog at read (only the group grants it).
	tools := toolNames(t, h, "/mcp", scoped)
	var sawEcho, sawEchoPropose, sawGraylog bool
	for _, name := range tools {
		switch {
		case name == "echo_label_set":
			sawEchoPropose = true
		case strings.HasPrefix(name, "echo_"):
			sawEcho = true
		case strings.HasPrefix(name, "graylog_"):
			sawGraylog = true
		}
	}
	if !sawEcho || !sawEchoPropose {
		t.Errorf("expected echo read and propose tools (own grant is write), got %v", tools)
	}
	if !sawGraylog {
		t.Errorf("expected graylog read tools (group grants %q at read), got %v", auth.Wildcard, tools)
	}

	// Proposing the echo change succeeds: the key's own write grant, not the
	// group's read, is what a plugin it names itself resolves to.
	w := mcpRequest(t, h, "/mcp", scoped, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "echo_label_set", "arguments": map[string]any{"label": "union-reaches-write"},
		},
	})
	if body := w.Body.String(); strings.Contains(body, `"error"`) && !strings.Contains(body, `"isError":true`) {
		// A JSON-RPC transport error (bad auth, bad routing) is a real
		// failure; a tool-level isError is not what this test checks.
		t.Errorf("proposing echo_label_set failed at the transport: %s", body)
	}

	// Reading graylog succeeds too: the group's grant, not the key's own
	// (empty) one, is what lets a plugin only the group names resolve at all.
	w = mcpRequest(t, h, "/mcp", scoped, map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{
			"name": "graylog_list_streams", "arguments": map[string]any{},
		},
	})
	if body := w.Body.String(); strings.Contains(body, "not authorized") {
		t.Errorf("a key in a group granting graylog at read was refused graylog_list_streams: %s", body)
	}

	// The per-plugin path resolves the same union: graylog is reachable
	// there too, on the strength of the group grant alone.
	w = mcpRequest(t, h, "/mcp/graylog", scoped, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/list", "params": map[string]any{},
	})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "graylog_") {
		t.Errorf("a key in a group granting graylog at read could not list graylog tools on /mcp/graylog:\n%s",
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
