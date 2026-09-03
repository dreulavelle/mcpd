package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/config"
)

// The 3CX plugin through the whole host: settings resolved from the file
// form of the customers table, the instance mounted, and a tool called over
// the MCP endpoint with the customer named. Skipped unless a PBX is supplied;
// see internal/plugins/threecx/integration_test.go for the variables.
func TestThreecx_ThroughTheHost(t *testing.T) {
	host := os.Getenv("THREECX_TEST_HOST")
	ext := os.Getenv("THREECX_TEST_EXTENSION")
	pass := os.Getenv("THREECX_TEST_PASSWORD")
	if host == "" || ext == "" || pass == "" {
		t.Skip("set THREECX_TEST_HOST, THREECX_TEST_EXTENSION and THREECX_TEST_PASSWORD to run against a real 3CX")
	}

	dir := t.TempDir()
	t.Setenv("MCPD_TOKEN_WILDCARD", tokenWildcard)
	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(dir, "mcpd.db")
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Legacy().Server.PublicURL = ptr("https://mcp.test.invalid")
	cfg.Plugins = map[string]config.PluginConfig{
		"pbx": {Enabled: true, Type: "threecx", Settings: map[string]any{
			"customers": []any{map[string]any{
				"name": "Trial", "aliases": []any{"trial pbx"}, "host": host, "extension": ext, "password": pass,
			}},
		}},
	}
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{{
		ID: "wildcard", SecretRef: "env:MCPD_TOKEN_WILDCARD",
		Principal: "svc:wildcard", Role: "admin", Plugins: []string{"*"},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	h := a.Handler()

	call := func(tool string, args map[string]any) map[string]any {
		w := mcpRequest(t, h, "/mcp/pbx", tokenWildcard, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": args},
		})
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tool, w.Code, w.Body.String())
		}
		body := w.Body.String()
		i := strings.Index(body, `{"jsonrpc"`)
		var env struct {
			Result struct {
				IsError    bool           `json:"isError"`
				Structured map[string]any `json:"structuredContent"`
				Content    []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(body[i:]), &env); err != nil {
			t.Fatalf("%s: %v in %s", tool, err, body)
		}
		if env.Result.IsError {
			t.Fatalf("%s answered with an error: %+v", tool, env.Result.Content)
		}
		return env.Result.Structured
	}

	customers := call("pbx_list_customers", map[string]any{})
	if n, _ := customers["count"].(float64); n != 1 {
		t.Errorf("list_customers: %+v", customers)
	}
	status := call("pbx_get_system_status", map[string]any{"customer": "trial pbx"})
	if status["customer"] != "Trial" || status["version"] == "" {
		t.Errorf("get_system_status by alias: %+v", status)
	}
	if _, ok := status["license_key"]; ok {
		t.Error("the licence key must never reach a result")
	}
}
