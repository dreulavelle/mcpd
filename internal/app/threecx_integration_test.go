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
	"github.com/spoked/mcpd/internal/settings"
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

// A customer added while the host is running is answerable on the next tool
// call, without a restart and without a client reconnecting.
//
// This is the question an operator actually has: they add a business on the
// Plugins page while a connector is live. The row write reconciles the
// instance -- the same path the dashboard's row endpoints trigger -- which
// rebuilds the plugin from its stored rows and remounts it. The tool list does
// not change, because the customer argument is a name rather than an enum of
// them, so a client that has already fetched the tools needs nothing.
//
// It reaches no phone system: list_customers reads configuration, so the
// addresses here need not exist.
func TestThreecx_ANewCustomerNeedsNoRestart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCPD_TOKEN_WILDCARD", tokenWildcard)
	key, err := settings.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCPD_SECRET_KEY", key)

	cfg := config.Default()
	cfg.SecretKeyRef = "env:MCPD_SECRET_KEY"
	cfg.Storage.Path = filepath.Join(dir, "mcpd.db")
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Legacy().Server.PublicURL = ptr("https://mcp.test.invalid")
	cfg.Plugins = map[string]config.PluginConfig{"pbx": {Enabled: true, Type: "threecx"}}
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{{
		ID: "wildcard", SecretRef: "env:MCPD_TOKEN_WILDCARD",
		Principal: "svc:wildcard", Role: "admin", Plugins: []string{"*"},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// One customer, stored as a row the way the dashboard stores one.
	rowsFor := func(a *App, name, host string) {
		t.Helper()
		if _, err := a.pluginRows.Create(ctx, "user:test", "pbx", "customers", name,
			map[string]any{"name": name, "host": host, "extension": "100"},
			map[string]string{"password": "pw"}); err != nil {
			t.Fatal(err)
		}
	}

	a, err := New(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.db.Close() })
	rowsFor(a, "Acme Dental", "acme.invalid")
	if err := a.reconcileInstance(ctx, "pbx"); err != nil {
		t.Fatal(err)
	}

	customers := func() []string {
		t.Helper()
		w := mcpRequest(t, a.Handler(), "/mcp/pbx", tokenWildcard, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "pbx_list_customers", "arguments": map[string]any{}},
		})
		if w.Code != http.StatusOK {
			t.Fatalf("list_customers: %d %s", w.Code, w.Body.String())
		}
		body := w.Body.String()
		var env struct {
			Result struct {
				IsError    bool `json:"isError"`
				Structured struct {
					Customers []struct {
						Name string `json:"name"`
					} `json:"customers"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(body[strings.Index(body, `{"jsonrpc"`):]), &env); err != nil {
			t.Fatal(err)
		}
		if env.Result.IsError {
			t.Fatalf("list_customers answered with an error: %s", body)
		}
		var names []string
		for _, c := range env.Result.Structured.Customers {
			names = append(names, c.Name)
		}
		return names
	}

	if got := customers(); len(got) != 1 || got[0] != "Acme Dental" {
		t.Fatalf("before adding: %v", got)
	}

	// Add a second business, and reconcile the way a row write does.
	rowsFor(a, "Globex Roofing", "globex.invalid")
	if err := a.reconcileInstance(ctx, "pbx"); err != nil {
		t.Fatal(err)
	}
	got := customers()
	if len(got) != 2 || got[1] != "Globex Roofing" {
		t.Fatalf("after adding, without a restart: %v", got)
	}

	// And the new customer resolves by name on a tool that would reach the
	// PBX: it fails to connect, which is the address being unreachable rather
	// than the customer being unknown.
	w := mcpRequest(t, a.Handler(), "/mcp/pbx", tokenWildcard, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "pbx_get_system_status",
			"arguments": map[string]any{"customer": "globex"},
		},
	})
	body := w.Body.String()
	if strings.Contains(body, "no customer here is called") {
		t.Errorf("the new customer should resolve by alias immediately: %s", body)
	}
}
