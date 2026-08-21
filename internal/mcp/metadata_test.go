package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Each MCP endpoint is its own protected resource. Advertising one identifier
// for all of them breaks audience binding, and makes two tunnels serving
// different endpoints indistinguishable to anything reading their metadata --
// which is how one connector ends up standing in for both.
func TestResourceMetadataNamesTheEndpointAsked(t *testing.T) {
	h := &Host{opts: Options{PublicURL: "https://mcpd.test:9080", AuthorizationServer: "https://mcpd.test:9080"}}

	for path, want := range map[string]string{
		"":          "https://mcpd.test:9080",
		"mcp":       "https://mcpd.test:9080/mcp",
		"mcp/echo":  "https://mcpd.test:9080/mcp/echo",
		"/mcp/echo": "https://mcpd.test:9080/mcp/echo",
	} {
		r := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
		r.SetPathValue("path", path)
		w := httptest.NewRecorder()

		h.handleResourceMetadata(w, r)

		var got protectedResourceMetadata
		if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
			t.Fatalf("path %q: %v", path, err)
		}
		if got.Resource != want {
			t.Errorf("path %q: resource = %q, want %q", path, got.Resource, want)
		}
	}
}

// A client following the pointer and a client deriving the address itself must
// reach the same document, or discovery depends on which one it did.
func TestChallengePointsAtTheEndpointsOwnMetadata(t *testing.T) {
	h := &Host{opts: Options{PublicURL: "https://mcpd.test:9080"}}

	for path, want := range map[string]string{
		"/mcp":      "https://mcpd.test:9080/.well-known/oauth-protected-resource/mcp",
		"/mcp/echo": "https://mcpd.test:9080/.well-known/oauth-protected-resource/mcp/echo",
	} {
		w := httptest.NewRecorder()
		h.challenge(w, httptest.NewRequest(http.MethodPost, path, nil))

		got := w.Header().Get("WWW-Authenticate")
		if !strings.Contains(got, `resource_metadata="`+want+`"`) {
			t.Errorf("%s challenge = %q, want it to point at %q", path, got, want)
		}
	}
}
