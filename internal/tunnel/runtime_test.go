package tunnel

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
)

func testServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
}

func testConfig() Config {
	return Config{
		Enabled:  true,
		TunnelID: "tunnel_0123456789abcdef0123456789abcdef",
		APIKey:   "sk-runtime-not-a-real-key",
		Principal: auth.Principal{
			ID: "svc:chatgpt", Role: auth.RoleOperator,
			Plugins: []string{auth.Wildcard}, TokenID: "tunnel",
		},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The bug this guards: with an in-memory binding the tunnel client answers
// "missing MCP server URL" to every protected-resource discovery command, so
// the control plane concludes mcpd has no OAuth and ChatGPT refuses to create
// the connector. An HTTP binding is the only way discovery can succeed.
func TestHTTPBindingChosenWhenServerURLIsSet(t *testing.T) {
	cfg := testConfig()
	cfg.MCPServerURL = "http://192.168.1.10:9080/mcp"

	r, err := newRuntime(cfg, testServer(), t.Context(), discardLogger(), logWriter{log: discardLogger()})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if _, ok := r.(*httpRuntime); !ok {
		t.Fatalf("runtime = %T, want the HTTP binding", r)
	}
}

// And the reverse: without one, nothing binds a port and no credential is
// involved, which is the better arrangement whenever OAuth is not in play.
func TestInMemoryBindingIsTheDefault(t *testing.T) {
	r, err := newRuntime(testConfig(), testServer(), t.Context(), discardLogger(), logWriter{log: discardLogger()})
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if _, ok := r.(inMemoryRuntime); !ok {
		t.Fatalf("runtime = %T, want the in-memory binding", r)
	}
}

func TestHTTPBindingRejectsAnUnusableURL(t *testing.T) {
	cfg := testConfig()
	cfg.MCPServerURL = "192.168.1.10:9080/mcp" // no scheme

	if _, err := newRuntime(cfg, testServer(), t.Context(), discardLogger(), logWriter{log: discardLogger()}); err == nil {
		t.Fatal("an address with no scheme must be refused, not dialled")
	}
}

// Without the guard this is a nil dereference inside a goroutine, which takes
// the whole process down instead of just failing to start the tunnel.
func TestInMemoryBindingNeedsAServer(t *testing.T) {
	if _, err := newInMemoryRuntime(testConfig(), nil, t.Context(), discardLogger(), logWriter{log: discardLogger()}); err == nil {
		t.Fatal("a missing MCP server must be an error, not a panic")
	}
}

// A plain-HTTP deployment is the expected case for a tunnel -- a host that
// already had TLS did not need one -- so Harpoon has to accept it, or the
// authorization server's token endpoint is unreachable from outside.
func TestPlaintextIsAllowedForAnHTTPDeployment(t *testing.T) {
	cfg := testConfig()
	cfg.MCPServerURL = "http://192.168.1.10:9080/mcp"

	if _, err := newHTTPRuntime(cfg, discardLogger(), logWriter{log: discardLogger()}); err != nil {
		t.Fatalf("newHTTPRuntime: %v", err)
	}
}

func TestValidateRejectsAnUnusableMCPAddress(t *testing.T) {
	cfg := testConfig()
	cfg.MCPServerURL = "not a url"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected a validation failure")
	}
	if !strings.Contains(err.Error(), "full URL") {
		t.Fatalf("error = %q, want it to say what a usable address looks like", err)
	}
}

// The client treats a 401 as retryable and backs off for ever, so a revoked
// key left the dashboard reporting "starting" indefinitely with the only
// explanation buried in a log line. These recognise the two verdicts that will
// never come good on a retry.
func TestCredentialRejectionIsRecognised(t *testing.T) {
	for _, line := range []string{
		`level=WARN msg="poll failed; backing off" error_code=invalid_api_key`,
		`{"msg":"poll failed","error_code":"token_invalidated"}`,
	} {
		if credentialRejection(line) == "" {
			t.Errorf("a rejected key must be recognised in: %s", line)
		}
	}
}

// A blip or a 5xx is worth backing off for; tearing the tunnel down over one
// would be worse than the problem.
func TestTransientFailuresAreNotTreatedAsRejection(t *testing.T) {
	for _, line := range []string{
		`level=WARN msg="poll failed; backing off" status_code=503`,
		`level=INFO msg="tunnel metadata fetched"`,
		`error_code=rate_limit_exceeded`,
	} {
		if code := credentialRejection(line); code != "" {
			t.Errorf("credentialRejection(%q) = %q, want none", line, code)
		}
	}
}

// A rotated key is the likeliest reason a tunnel that used to work stops, and
// "invalidated" is the only word that tells the operator to go and make a new
// one rather than re-checking the id.
func TestDiagnoseExplainsARevokedKey(t *testing.T) {
	got := diagnose("sk-proj-whatever", "token_invalidated")
	if !strings.Contains(got, "invalidated") || !strings.Contains(got, "new runtime key") {
		t.Fatalf("diagnose = %q, want it to say the key was revoked and a new one is needed", got)
	}

	admin := diagnose("sk-admin-whatever", "")
	if !strings.Contains(admin, "admin key") {
		t.Fatalf("diagnose = %q, want it to name the admin-key mistake", admin)
	}
}
