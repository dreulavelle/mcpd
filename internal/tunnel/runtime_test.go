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
	operator, _ := auth.BuiltinRole(auth.RoleOperator)
	return Config{
		Enabled:  true,
		TunnelID: "tunnel_0123456789abcdef0123456789abcdef",
		APIKey:   "sk-runtime-not-a-real-key",
		Principal: auth.Principal{
			ID: "svc:chatgpt", RoleID: operator.ID, RoleName: operator.Name,
			Permissions: operator.Permissions,
			Grants:      auth.GrantsAt([]string{auth.Wildcard}, auth.LevelWrite),
			TokenID:     "tunnel",
		},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Every tunnel binds in process: no port, no socket, no credential, and no
// local address for anything else to find.
//
// There was a second binding that pointed the tunnel at mcpd's own HTTP
// listener. It existed so protected-resource discovery could succeed, since
// that is a tunnel command the client can only run against a URL. mcpd is no
// longer an authorization server, so there is nothing to discover.
func TestInMemoryBindingIsTheOnlyOne(t *testing.T) {
	r, err := newRuntime(testConfig(), testServer(), t.Context(), discardLogger(), logWriter{log: discardLogger()}, nil)
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	if _, ok := r.(inMemoryRuntime); !ok {
		t.Fatalf("runtime = %T, want the in-memory binding", r)
	}
}

// Without the guard this is a nil dereference inside a goroutine, which takes
// the whole process down instead of just failing to start the tunnel.
func TestInMemoryBindingNeedsAServer(t *testing.T) {
	if _, err := newInMemoryRuntime(testConfig(), nil, t.Context(), discardLogger(), logWriter{log: discardLogger()}, nil); err == nil {
		t.Fatal("a missing MCP server must be an error, not a panic")
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
	if !strings.Contains(got, "no longer accepts") || !strings.Contains(got, "new runtime key") {
		t.Fatalf("diagnose = %q, want it to say the key was revoked and a new one is needed", got)
	}

	admin := diagnose("sk-admin-whatever", "")
	if !strings.Contains(admin, "admin key") {
		t.Fatalf("diagnose = %q, want it to name the admin-key mistake", admin)
	}
}
