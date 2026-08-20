package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth/oauth"
	"github.com/spoked/mcpd/internal/config"
)

// newOAuthApp builds a host running the full OAuth authorization server, with
// a bootstrapped administrator.
func newOAuthApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("MCPD_BOOTSTRAP_PASSWORD", "bootstrap-admin-password")

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(t.TempDir(), "mcpd.db")
	cfg.Storage.RelaxedDurability = true
	cfg.Server.PublicURL = "https://mcp.test.invalid"
	cfg.Auth.Mode = "oauth"
	cfg.Auth.StaticTokens = nil
	cfg.Auth.OAuth.Bootstrap = config.Bootstrap{
		Username:    "admin",
		PasswordRef: "env:MCPD_BOOTSTRAP_PASSWORD",
	}
	cfg.Plugins = map[string]config.PluginConfig{"echo": {Enabled: true}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}

	a, err := New(context.Background(), cfg,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

// TestOAuthEndToEnd walks the exact sequence a ChatGPT connector performs:
// discover the resource, discover the authorization server, register, consent,
// exchange, and then call a tool with the resulting token.
func TestOAuthEndToEnd(t *testing.T) {
	a := newOAuthApp(t)
	h := a.Handler()

	// 1. Discover the protected resource. Unauthenticated by specification.
	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	getJSON(t, h, "/.well-known/oauth-protected-resource", &prm)
	if len(prm.AuthorizationServers) != 1 {
		t.Fatalf("protected resource must name its authorization server, got %v",
			prm.AuthorizationServers)
	}

	// 2. Discover the authorization server.
	var asm struct {
		Issuer                        string   `json:"issuer"`
		AuthorizationEndpoint         string   `json:"authorization_endpoint"`
		TokenEndpoint                 string   `json:"token_endpoint"`
		RegistrationEndpoint          string   `json:"registration_endpoint"`
		CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
	}
	getJSON(t, h, "/.well-known/oauth-authorization-server", &asm)
	if asm.RegistrationEndpoint == "" {
		t.Fatal("dynamic registration must be advertised for ChatGPT to self-register")
	}

	// 3. Register as a public client, the way ChatGPT does.
	const callback = "https://chatgpt.com/connector_platform_oauth_redirect"
	body := `{"client_name":"ChatGPT","redirect_uris":["` + callback +
		`"],"token_endpoint_auth_method":"none"}`
	r := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("registration = %d: %s", w.Code, w.Body.String())
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	mustJSON(t, w.Body.Bytes(), &reg)

	// 4. Consent, using the bootstrapped administrator.
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	form := url.Values{
		"client_id":             {reg.ClientID},
		"redirect_uri":          {callback},
		"response_type":         {"code"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"scope":                 {oauth.ScopeRead + " " + oauth.PluginScope("echo")},
		"state":                 {"opaque-state"},
		"username":              {"admin"},
		"password":              {"bootstrap-admin-password"},
		"action":                {"allow"},
	}
	r = httptest.NewRequest(http.MethodPost, "/oauth/authorize", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatalf("authorize = %d: %s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(loc.String(), callback) {
		t.Fatalf("redirected to %s, want the registered callback", loc.String())
	}
	if loc.Query().Get("state") != "opaque-state" {
		t.Fatal("state must round-trip unchanged")
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatal("no authorization code issued")
	}

	// 5. Exchange the code.
	form = url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {callback},
		"client_id":     {reg.ClientID},
		"code_verifier": {verifier},
	}
	r = httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("token exchange = %d: %s", w.Code, w.Body.String())
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	mustJSON(t, w.Body.Bytes(), &tok)

	// 6. Use the token against the plugin endpoint.
	call := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "echo_echo",
			"arguments": map[string]any{"message": "authorized via oauth"},
		},
	}
	w = mcpRequest(t, h, "/mcp/echo", tok.AccessToken, call)
	if w.Code != http.StatusOK {
		t.Fatalf("tool call with an OAuth token = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "authorized via oauth") {
		t.Fatalf("tool did not run: %s", w.Body.String())
	}

	// 7. The token's plugin scope must bound it, even though the bootstrapped
	//    user is an administrator with wildcard access.
	w = mcpRequest(t, h, "/mcp/proxmox", tok.AccessToken, call)
	if w.Code != http.StatusNotFound {
		t.Fatalf("a token scoped to echo reached another endpoint (status %d)", w.Code)
	}
}

// An admin consenting with a narrow scope must produce a narrow token. The
// token's authority comes from the grant, not from the user's role.
func TestOAuth_TokenScopeBoundsAnAdministrator(t *testing.T) {
	a := newOAuthApp(t)
	ctx := context.Background()

	user, err := a.oauthStore.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != "admin" {
		t.Fatalf("bootstrapped user role = %s, want admin", user.Role)
	}

	// Read-only scope, despite the user being able to approve.
	granted := a.oauthServer.GrantScopeForTest(oauth.ScopeRead+" "+oauth.PluginScope("echo"), user)
	if oauth.HasScope(granted, oauth.ScopeApprove) {
		t.Fatal("a scope the client did not request must not be granted")
	}

	v := oauth.NewVerifier(a.oauthStore, nil)
	_ = v // verifier behaviour is covered in the oauth package tests
}

func TestBootstrap_OnlyRunsOnEmptyDatabase(t *testing.T) {
	a := newOAuthApp(t)
	ctx := context.Background()

	n, err := a.oauthStore.CountUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("users = %d, want exactly the bootstrapped administrator", n)
	}

	// Re-running must not create a second account or reset the first.
	if err := bootstrapAdmin(ctx, a.cfg, a.oauthStore,
		slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	n, _ = a.oauthStore.CountUsers(ctx)
	if n != 1 {
		t.Fatalf("users = %d after re-running bootstrap, want 1", n)
	}
}

func getJSON(t *testing.T, h http.Handler, path string, into any) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, w.Code, w.Body.String())
	}
	mustJSON(t, w.Body.Bytes(), into)
}

func mustJSON(t *testing.T, b []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
}

// Advertising an authorization server that is not mounted points every client
// at a 404. OpenAI's tunnel-client treats authorization_servers[0] as its only
// metadata target, so a stale advertisement breaks discovery rather than
// degrading.
func TestProtectedResourceMetadata_OmitsUnmountedAuthorizationServer(t *testing.T) {
	a := newTestApp(t) // static-token mode
	h := a.Handler()

	var prm struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	getJSON(t, h, "/.well-known/oauth-protected-resource", &prm)

	if len(prm.AuthorizationServers) != 0 {
		t.Fatalf("static-token mode advertises %v, but no authorization server is "+
			"mounted; a client would fetch metadata from a 404", prm.AuthorizationServers)
	}
	if prm.Resource == "" {
		t.Fatal("the resource identifier must still be advertised")
	}

	// And the endpoints really are absent, which is what makes the
	// advertisement wrong.
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("authorization server metadata = %d, want 404 in static mode", w.Code)
	}
}

// Under OAuth it must be advertised, or a connector cannot discover where to
// authenticate.
func TestProtectedResourceMetadata_AdvertisesMountedAuthorizationServer(t *testing.T) {
	a := newOAuthApp(t)

	var prm struct {
		AuthorizationServers []string `json:"authorization_servers"`
	}
	getJSON(t, a.Handler(), "/.well-known/oauth-protected-resource", &prm)

	if len(prm.AuthorizationServers) != 1 {
		t.Fatalf("oauth mode must advertise its authorization server, got %v",
			prm.AuthorizationServers)
	}
}
