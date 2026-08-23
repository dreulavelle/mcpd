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
	"time"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/operations"
)

const approverToken = "approver-token-000000000000000000000000"

// newApprovalApp builds a host whose principal can both propose and approve,
// so a single test can drive the whole lifecycle.
func newApprovalApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("MCPD_TOKEN_APPROVER", approverToken)

	cfg := config.Default()
	cfg.Storage.Path = filepath.Join(t.TempDir(), "mcpd.db")
	cfg.Legacy().Storage.RelaxedDurability = ptr(true)
	cfg.Legacy().Server.PublicURL = ptr("https://mcp.test.invalid")
	cfg.Plugins = map[string]config.PluginConfig{"echo": {Enabled: true}}
	cfg.Auth.StaticTokens = []config.StaticTokenConfig{{
		ID: "user", SecretRef: "env:MCPD_TOKEN_APPROVER",
		Principal: "svc:approver", Role: "user", Plugins: []string{"echo"},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}

	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { a.db.Close() })
	return a
}

// callTool invokes an MCP tool and returns its structured result.
func callTool(t *testing.T, h http.Handler, token, tool string, args map[string]any) map[string]any {
	t.Helper()
	w := mcpRequest(t, h, "/mcp/echo", token, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("%s = HTTP %d: %s", tool, w.Code, w.Body.String())
	}
	return decodeToolResult(t, tool, w.Body.String())
}

// decodeToolResult pulls structuredContent out of an SSE or JSON response.
func decodeToolResult(t *testing.T, tool, body string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var env struct {
			Result struct {
				StructuredContent map[string]any `json:"structuredContent"`
				IsError           bool           `json:"isError"`
				Content           []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		if env.Error != nil {
			t.Fatalf("%s returned a protocol error: %s", tool, env.Error.Message)
		}
		if env.Result.IsError {
			detail := ""
			if len(env.Result.Content) > 0 {
				detail = env.Result.Content[0].Text
			}
			t.Fatalf("%s returned a tool error: %s", tool, detail)
		}
		if env.Result.StructuredContent != nil {
			return env.Result.StructuredContent
		}
	}
	t.Fatalf("could not decode a result for %s from: %s", tool, body)
	return nil
}

// TestApprovalLifecycle walks propose -> approve -> execute -> verify and
// asserts that nothing changes until approval lands.
func TestApprovalLifecycle(t *testing.T) {
	a := newApprovalApp(t) // separation of duties off; one token does both
	h := a.Handler()
	ctx := context.Background()

	// Start the outbox drain so an approval reaches the executor.
	workerCtx, stop := context.WithCancel(ctx)
	defer stop()
	go a.publisher.Run(workerCtx)

	before := callTool(t, h, approverToken, "echo_status", map[string]any{})
	if before["label"] != "default" {
		t.Fatalf("initial label = %v, want default", before["label"])
	}

	// 1. Propose. This must change nothing.
	proposal := callTool(t, h, approverToken, "echo_label_set",
		map[string]any{"label": "production"})

	if proposal["state"] != string(operations.StatePendingApproval) {
		t.Fatalf("state = %v, want pending_approval", proposal["state"])
	}
	opID, _ := proposal["operation_id"].(string)
	if opID == "" {
		t.Fatal("no operation_id returned")
	}
	// The note is what stops a model reading "proposed" as "done".
	if note, _ := proposal["note"].(string); !strings.Contains(note, "NOTHING HAS CHANGED") {
		t.Fatalf("proposal note should state plainly that nothing changed, got %q", note)
	}

	stillDefault := callTool(t, h, approverToken, "echo_status", map[string]any{})
	if stillDefault["label"] != "default" {
		t.Fatalf("label changed at proposal time: %v -- a proposal must not mutate anything",
			stillDefault["label"])
	}

	// 2. The proposal must be listed as pending.
	listed := callTool(t, h, approverToken, "echo_list_operations",
		map[string]any{"state": "pending_approval"})
	if count, _ := listed["count"].(float64); count != 1 {
		t.Fatalf("pending operations = %v, want 1", listed["count"])
	}

	// 3. Approve, which authorises execution.
	approved := callTool(t, h, approverToken, "echo_approve_operation",
		map[string]any{"operation_id": opID, "reason": "looks fine"})
	if approved["state"] != string(operations.StateApproved) {
		t.Fatalf("state after approval = %v, want approved", approved["state"])
	}

	// 4. Execution happens asynchronously; wait for it to settle.
	final := waitForState(t, a, opID, operations.StateSucceeded, 5*time.Second)
	if final.OutcomeVerified == nil || !*final.OutcomeVerified {
		t.Fatal("a succeeded operation must be verified by observation")
	}

	// 5. And the change is real.
	after := callTool(t, h, approverToken, "echo_status", map[string]any{})
	if after["label"] != "production" {
		t.Fatalf("label = %v after execution, want production", after["label"])
	}
}

// A rejected proposal must never take effect.
func TestApprovalLifecycle_RejectionChangesNothing(t *testing.T) {
	a := newApprovalApp(t)
	h := a.Handler()

	proposal := callTool(t, h, approverToken, "echo_label_set",
		map[string]any{"label": "should-never-apply"})
	opID := proposal["operation_id"].(string)

	rejected := callTool(t, h, approverToken, "echo_reject_operation",
		map[string]any{"operation_id": opID, "reason": "no"})
	if rejected["state"] != string(operations.StateRejected) {
		t.Fatalf("state = %v, want rejected", rejected["state"])
	}

	status := callTool(t, h, approverToken, "echo_status", map[string]any{})
	if status["label"] != "default" {
		t.Fatalf("label = %v after rejection, want it untouched", status["label"])
	}
}

func TestApproval_RequiresAnExistingOperation(t *testing.T) {
	a := newApprovalApp(t)
	h := a.Handler()

	w := mcpRequest(t, h, "/mcp/echo", approverToken, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "echo_approve_operation",
			"arguments": map[string]any{"operation_id": "op_does_not_exist"},
		},
	})
	if !strings.Contains(w.Body.String(), "not found") {
		t.Fatalf("approving a nonexistent operation should fail: %s", w.Body.String())
	}
}

// The propose tool must advertise itself honestly.
func TestProposeTool_IsAdvertisedAsNonDestructive(t *testing.T) {
	a := newApprovalApp(t)
	w := mcpRequest(t, a.Handler(), "/mcp/echo", approverToken, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{},
	})
	body := w.Body.String()

	for _, want := range []string{
		"echo_label_set",
		"echo_approve_operation",
		"echo_reject_operation",
		"echo_list_operations",
		"echo_get_operation",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tools/list omits %s", want)
		}
	}
	if !strings.Contains(body, "Nothing changes until a human") {
		t.Error("the propose tool description must say that nothing changes until approval")
	}
}

// waitForState polls until an operation reaches the expected state.
func waitForState(t *testing.T, a *App, opID string, want operations.OperationState, budget time.Duration) *operations.Operation {
	t.Helper()
	deadline := time.Now().Add(budget)
	var last *operations.Operation
	for time.Now().Before(deadline) {
		op, err := a.ops.Get(context.Background(), opID)
		if err != nil {
			t.Fatal(err)
		}
		last = op
		if op.State == want {
			return op
		}
		if op.State.IsTerminal() {
			t.Fatalf("operation reached terminal state %s, want %s (error: %s %s)",
				op.State, want, op.ErrorCode, op.ErrorDetail)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("operation stayed in %s, never reached %s", last.State, want)
	return nil
}
