package plugins

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/observability"
)

// What a tool call costs this host, with no upstream anywhere in it.
//
// Every latency number mcpd records includes the plugin's own upstream work,
// so "is mcpd slow" has never been separable from "is cnMaestro slow". These
// benchmarks answer the first question on its own: a tool that returns a fixed
// string, called through a real MCP client over an in-memory transport, so the
// only thing being timed is the host path -- the gate, the rate limiter, the
// dispatch, the marshalling, and the specification's double-send.
//
// Run them with:
//
//	go test ./internal/plugins/ -bench 'ToolCall' -benchmem -run '^$'

type benchInput struct{}

type benchOutput struct {
	Filler string `json:"filler"`
}

// mountBenchTool serves one read tool returning a payload of the given size.
func mountBenchTool(b *testing.B, size int, obs ToolObserver) *mcp.ClientSession {
	b.Helper()

	r := newRegistry(Descriptor{Name: "bench", Version: "1.0.0", Title: "Bench"})
	filler := strings.Repeat("x", size)
	Tool(r, ToolSpec{
		Name:        "get_payload",
		Title:       "Get a payload",
		Description: "Returns a fixed payload, for measuring the host.",
		Idempotent:  true,
	}, func(_ context.Context, _ benchInput) (benchOutput, error) {
		return benchOutput{Filler: filler}, nil
	})
	if err := r.err(); err != nil {
		b.Fatalf("registration: %v", err)
	}

	srv := mcp.NewServer(&mcp.Implementation{Name: "bench", Version: "0"}, nil)
	gate := func(context.Context, string, auth.Capability) error { return nil }
	r.tools[0].attach(srv, gate, obs)

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		b.Fatalf("connect server: %v", err)
	}
	b.Cleanup(func() { _ = ss.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "bench", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		b.Fatalf("connect client: %v", err)
	}
	b.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callBench(b *testing.B, cs *mcp.ClientSession) {
	b.Helper()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
			Name: "bench_get_payload", Arguments: map[string]any{},
		}); err != nil {
			b.Fatalf("call: %v", err)
		}
	}
}

// BenchmarkToolCallBySize is the headline: what one call costs at each size the
// result-size histogram has a boundary for.
//
// The bytes-per-op figure is the one worth watching. It should scale with the
// payload at roughly twice the rate the payload grows, because the
// specification has a result carried as structured content and again as text.
func BenchmarkToolCallBySize(b *testing.B) {
	for _, size := range []int{512, 8_000, 20_000, MaxResultBytes} {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			callBench(b, mountBenchTool(b, size, noObserver{}))
		})
	}
}

// BenchmarkToolCallObserved is what measuring costs.
//
// Recording a result's size means marshalling it a second time, on a value the
// SDK is about to marshal anyway. That was justified as a fraction of a call
// that takes hundreds of milliseconds against a real upstream -- a claim worth
// checking rather than repeating, because against an upstream this fast it is
// the whole call.
//
// Compare against BenchmarkToolCallBySize at the same size: the difference is
// the price of the mcpd_tool_result_bytes series.
func BenchmarkToolCallObserved(b *testing.B) {
	for _, size := range []int{512, 20_000, MaxResultBytes} {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			callBench(b, mountBenchTool(b, size, observability.NewMetrics()))
		})
	}
}

// BenchmarkMarshalledSize isolates the measurement from everything around it.
func BenchmarkMarshalledSize(b *testing.B) {
	for _, size := range []int{512, 20_000, MaxResultBytes} {
		b.Run(fmt.Sprintf("%dB", size), func(b *testing.B) {
			out := benchOutput{Filler: strings.Repeat("x", size)}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if marshalledSize(out) < 0 {
					b.Fatal("unmeasurable")
				}
			}
		})
	}
}

// BenchmarkDoubleSend measures what the specification's text duplicate costs.
//
// The SDK adds a TextContent copy of the whole result only when the handler
// left Content nil -- which mcpd's tools all do, because they return the typed
// value and let the SDK build the result. Handing back a result whose Content
// is set, even to an empty slice, skips the copy for an object output.
//
// The saving is not free: the duplicate exists so a pre-SEP-2106 client can
// recover the payload from unstructured content. Suppressing it is a
// compatibility decision, and this benchmark exists so it can be made against
// a number rather than an intuition.
func BenchmarkDoubleSend(b *testing.B) {
	mount := func(b *testing.B, size int, suppress bool) *mcp.ClientSession {
		b.Helper()
		filler := strings.Repeat("x", size)
		srv := mcp.NewServer(&mcp.Implementation{Name: "bench", Version: "0"}, nil)
		mcp.AddTool(srv, &mcp.Tool{Name: "payload", Description: "bench"},
			func(_ context.Context, _ *mcp.CallToolRequest, _ benchInput) (*mcp.CallToolResult, benchOutput, error) {
				out := benchOutput{Filler: filler}
				if suppress {
					// Non-nil and empty: the structured content still carries
					// the whole answer, and nothing is sent twice.
					return &mcp.CallToolResult{Content: []mcp.Content{}}, out, nil
				}
				return nil, out, nil
			})

		ctx := context.Background()
		clientTransport, serverTransport := mcp.NewInMemoryTransports()
		ss, err := srv.Connect(ctx, serverTransport, nil)
		if err != nil {
			b.Fatalf("connect server: %v", err)
		}
		b.Cleanup(func() { _ = ss.Close() })
		client := mcp.NewClient(&mcp.Implementation{Name: "bench", Version: "0"}, nil)
		cs, err := client.Connect(ctx, clientTransport, nil)
		if err != nil {
			b.Fatalf("connect client: %v", err)
		}
		b.Cleanup(func() { _ = cs.Close() })
		return cs
	}

	call := func(b *testing.B, cs *mcp.ClientSession) {
		ctx := context.Background()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := cs.CallTool(ctx, &mcp.CallToolParams{
				Name: "payload", Arguments: map[string]any{},
			}); err != nil {
				b.Fatalf("call: %v", err)
			}
		}
	}

	for _, size := range []int{20_000, MaxResultBytes} {
		b.Run(fmt.Sprintf("%dB/sent_twice", size), func(b *testing.B) {
			call(b, mount(b, size, false))
		})
		b.Run(fmt.Sprintf("%dB/sent_once", size), func(b *testing.B) {
			call(b, mount(b, size, true))
		})
	}
}
