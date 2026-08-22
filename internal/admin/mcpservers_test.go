package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/mcpservers"
)

// roleVerifier hands back a principal with a fixed role, so a test can check
// what each capability may reach without standing up accounts.
type roleVerifier struct{ role auth.Role }

func (roleVerifier) Scheme() string { return "test" }

func (v roleVerifier) Verify(context.Context, string, *http.Request) (*auth.Principal, error) {
	return &auth.Principal{
		ID: "svc:test", DisplayName: "test", Role: v.role,
		Plugins: []string{auth.Wildcard}, TokenID: "test",
	}, nil
}

type recordedCall struct {
	name, tool, hash string
	state            mcpservers.ToolState
	document         []byte
}

func newMCPDashboard(t *testing.T, role auth.Role, calls *recordedCall) *Server {
	t.Helper()
	return NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier: roleVerifier{role: role},
		MCPServers: MCPServerAPI{
			List: func(context.Context) (any, error) {
				return []map[string]any{{"name": "weather"}}, nil
			},
			Tools: func(_ context.Context, name string) ([]mcpservers.Tool, error) {
				calls.name = name
				return []mcpservers.Tool{{
					Name: "getWeather", Hash: "abc", State: mcpservers.ToolPending,
				}}, nil
			},
			Import: func(_ context.Context, _, name string, document []byte) error {
				calls.name, calls.document = name, document
				return nil
			},
			Discover: func(_ context.Context, _, name string) (mcpservers.Diff, error) {
				calls.name = name
				return mcpservers.Diff{Added: []string{"getWeather"}}, nil
			},
			Classify: func(_ context.Context, _, server, tool, hash string, state mcpservers.ToolState) error {
				calls.name, calls.tool, calls.hash, calls.state = server, tool, hash, state
				return nil
			},
			Schema: mcpservers.SchemaDocument,
		},
	})
}

func request(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(payload))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer test")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// TestMCPServerRoutes_CapabilityGating is the check that matters most on these
// routes: importing a server, connecting to it, and deciding which of its
// tools an assistant may reach all change what leaves this deployment, so they
// are an administrator's and not an operator's.
func TestMCPServerRoutes_CapabilityGating(t *testing.T) {
	tests := []struct {
		method, path string
		body         any
		adminOnly    bool
	}{
		{method: http.MethodGet, path: "/api/mcp-servers"},
		{method: http.MethodGet, path: "/api/mcp-servers/schema"},
		{method: http.MethodGet, path: "/api/mcp-servers/weather/tools"},
		{
			method: http.MethodPost, path: "/api/mcp-servers", adminOnly: true,
			body: map[string]any{"name": "weather", "document": map[string]any{"a": 1}},
		},
		{
			method: http.MethodPost, path: "/api/mcp-servers/weather/discover",
			adminOnly: true,
		},
		{
			method: http.MethodPatch, path: "/api/mcp-servers/weather/tools/getWeather",
			adminOnly: true,
			body:      map[string]any{"state": "enabled", "descriptor_hash": "abc"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var calls recordedCall

			asUser := request(t, newMCPDashboard(t, auth.RoleUser, &calls), tc.method, tc.path, tc.body)
			if tc.adminOnly && asUser.Code != http.StatusForbidden {
				t.Errorf("a user reached %s and should not have: %d", tc.path, asUser.Code)
			}
			if !tc.adminOnly && asUser.Code != http.StatusOK {
				t.Errorf("a user could not read %s: %d %s", tc.path, asUser.Code, asUser.Body)
			}

			asAdmin := request(t, newMCPDashboard(t, auth.RoleAdmin, &calls), tc.method, tc.path, tc.body)
			if asAdmin.Code >= http.StatusBadRequest {
				t.Errorf("an admin was refused %s: %d %s", tc.path, asAdmin.Code, asAdmin.Body)
			}
		})
	}
}

// TestClassifyTool_PassesTheDescriptorHashThrough: the hash is the guard, so a
// handler that dropped it would turn a guarded decision into an unguarded one.
func TestClassifyTool_PassesTheDescriptorHashThrough(t *testing.T) {
	var calls recordedCall
	s := newMCPDashboard(t, auth.RoleAdmin, &calls)

	w := request(t, s, http.MethodPatch, "/api/mcp-servers/weather/tools/getWeather",
		map[string]any{"state": "enabled", "descriptor_hash": "deadbeef"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if calls.hash != "deadbeef" {
		t.Errorf("descriptor hash = %q, want deadbeef", calls.hash)
	}
	if calls.state != mcpservers.ToolEnabled || calls.tool != "getWeather" || calls.name != "weather" {
		t.Errorf("classified %+v", calls)
	}
}

// TestImportMCPServer_PassesTheDocumentVerbatim: the document is the
// operator's, and re-encoding it on the way in would quietly drop anything
// this build does not model.
func TestImportMCPServer_PassesTheDocumentVerbatim(t *testing.T) {
	var calls recordedCall
	s := newMCPDashboard(t, auth.RoleAdmin, &calls)

	document := json.RawMessage(`{"$schema":"x","somethingUnmodelled":true}`)
	w := request(t, s, http.MethodPost, "/api/mcp-servers",
		map[string]any{"name": "weather", "document": document})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if !bytes.Contains(calls.document, []byte("somethingUnmodelled")) {
		t.Errorf("document = %s", calls.document)
	}
}

func TestImportMCPServer_RequiresADocument(t *testing.T) {
	var calls recordedCall
	s := newMCPDashboard(t, auth.RoleAdmin, &calls)

	w := request(t, s, http.MethodPost, "/api/mcp-servers", map[string]any{"name": "weather"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// The schema an import is judged by is a public document, and showing an
// operator what their paste will be measured against is the difference between
// a refusal they can act on and one they argue with.
func TestMCPSchema_IsServedAsTheVendoredCopy(t *testing.T) {
	var calls recordedCall
	s := newMCPDashboard(t, auth.RoleUser, &calls)

	w := request(t, s, http.MethodGet, "/api/mcp-servers/schema", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), mcpservers.SchemaURI) {
		t.Error("the served schema should be the one SchemaURI names")
	}
}
