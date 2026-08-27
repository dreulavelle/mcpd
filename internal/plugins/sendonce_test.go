package plugins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
)

// mountEcho serves one read tool through a real client, and returns the
// session so a caller can inspect what actually arrived on the wire.
func mountEcho(t *testing.T, payload string) *mcp.ClientSession {
	t.Helper()
	r := newRegistry(Descriptor{Name: "bench", Version: "1.0.0", Title: "Bench"})
	Tool(r, ToolSpec{
		Name: "get_payload", Title: "Get a payload",
		Description: "Returns a fixed payload.", Idempotent: true,
	}, func(_ context.Context, _ benchInput) (benchOutput, error) {
		return benchOutput{Filler: payload}, nil
	})
	if err := r.err(); err != nil {
		t.Fatalf("registration: %v", err)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	r.tools[0].attach(srv, func(context.Context, string, auth.Capability) error { return nil }, noObserver{})

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// TestAModernClientIsSentTheAnswerOnce is the saving.
//
// The specification has a result carried as structured content and again as a
// text copy, and the copy is half of what reaches a model's context. A client
// that negotiated 2025-06-18 or later is required to read structuredContent, so
// the copy is dead weight it should not be charged for.
func TestAModernClientIsSentTheAnswerOnce(t *testing.T) {
	payload := strings.Repeat("x", 4096)
	cs := mountEcho(t, payload)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "bench_get_payload", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}

	// The answer is there, in full, exactly once. Re-encoded because a client
	// receives structured content as a decoded value rather than raw bytes.
	if res.StructuredContent == nil {
		t.Fatal("no structured content: the answer did not arrive at all")
	}
	structured, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(structured), payload) {
		t.Fatal("structured content does not carry the payload")
	}
	for _, c := range res.Content {
		text, ok := c.(*mcp.TextContent)
		if !ok {
			continue
		}
		if strings.Contains(text.Text, payload) {
			t.Fatalf("the payload was sent twice: %d bytes duplicated in a "+
				"text block", len(text.Text))
		}
	}
}

// TestSendOnceIsConservativeWithoutAVersion keeps the default compatible.
//
// A caller whose negotiated version cannot be established is a caller that
// might predate structuredContent, and the cost of guessing wrong is an
// assistant that receives an empty answer and reports the integration broken.
// Sending twice is the wasteful answer; sending nothing readable is the wrong
// one.
func TestSendOnceIsConservativeWithoutAVersion(t *testing.T) {
	if got := sendOnce(nil); got != nil {
		t.Errorf("a nil request suppressed the copy: %+v", got)
	}
	if got := sendOnce(&mcp.CallToolRequest{}); got != nil {
		t.Errorf("a request with no session suppressed the copy: %+v", got)
	}
}

// TestStructuredContentVersionIsTheSpecifiedOne guards the constant.
//
// Raising it silently would send every modern client the copy again and cost
// half of every answer; lowering it would send an empty result to a client that
// cannot read structured content.
func TestStructuredContentVersionIsTheSpecifiedOne(t *testing.T) {
	if structuredContentVersion != "2025-06-18" {
		t.Fatalf("structuredContentVersion is %q; structuredContent arrived in 2025-06-18",
			structuredContentVersion)
	}
	// Lexicographic ordering is what the comparison relies on.
	for _, older := range []string{"2024-11-05", "2025-03-26"} {
		if !(older < structuredContentVersion) {
			t.Errorf("%q does not sort before %q", older, structuredContentVersion)
		}
	}
	for _, newer := range []string{"2025-06-18", "2025-11-25", "2026-07-28"} {
		if newer < structuredContentVersion {
			t.Errorf("%q sorts before %q", newer, structuredContentVersion)
		}
	}
}
