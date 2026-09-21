package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openai/tunnel-client/pkg/mcpclient"
	"github.com/openai/tunnel-client/pkg/tunnelctx"
)

type waitArgs struct {
	Tag string `json:"tag"`
	MS  int    `json:"ms"`
}

// waitServer answers wait with the tag it was given, after the delay it was
// given, and reports on cancelled any call whose context ended first.
func waitServer(cancelled chan<- string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "wait"}, func(ctx context.Context, _ *mcp.CallToolRequest, in waitArgs) (*mcp.CallToolResult, any, error) {
		select {
		case <-time.After(time.Duration(in.MS) * time.Millisecond):
		case <-ctx.Done():
			if cancelled != nil {
				cancelled <- in.Tag
			}
			return nil, nil, ctx.Err()
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: in.Tag}}}, nil, nil
	})
	return s
}

// dispatcher drives the transport the way the tunnel client does: the
// injected transport wrapped in its shared connection, every command
// connecting to it and then reading until an answer arrives.
func dispatcher(t *testing.T, transport mcp.Transport) mcpclient.ForwardingTransport {
	t.Helper()
	return mcpclient.NewForwardingTransport(mcpclient.NewSharedConnectionTransport(transport))
}

// waitCall is one command as ChatGPT sends it: self-contained, and numbered 0
// like every other.
func waitCall(t *testing.T, tag string, ms int) *jsonrpc.Request {
	t.Helper()
	id, err := jsonrpc.MakeID(float64(0))
	if err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(map[string]any{
		"name":      "wait",
		"arguments": waitArgs{Tag: tag, MS: ms},
		"_meta": map[string]any{
			mcp.MetaKeyProtocolVersion:    "2026-07-28",
			mcp.MetaKeyClientCapabilities: map[string]any{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &jsonrpc.Request{ID: id, Method: "tools/call", Params: params}
}

// command forwards one request under its own command id and returns the text
// of the answer it read.
func command(ctx context.Context, fwd mcpclient.ForwardingTransport, key string, req *jsonrpc.Request) (string, error) {
	ctx = tunnelctx.ContextWithRequestID(ctx, key)
	conn, err := fwd.Connect(ctx)
	if err != nil {
		return "", err
	}
	if _, err := conn.Write(ctx, nil, req); err != nil {
		return "", err
	}
	for {
		msg, err := conn.Read(ctx)
		if err != nil {
			_ = conn.Close()
			return "", err
		}
		resp, ok := msg.(*jsonrpc.Response)
		if !ok {
			continue
		}
		if resp.Error != nil {
			return "", resp.Error
		}
		var result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		}
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return "", err
		}
		if result.IsError || len(result.Content) == 0 {
			return "", fmt.Errorf("tool error: %s", resp.Result)
		}
		return result.Content[0].Text, nil
	}
}

// Two tool calls in flight together each get their own answer.
//
// The bug: the tunnel client shares one connection between its workers and
// ChatGPT numbers every request 0, so over a single in-memory pipe the slow
// call's worker could read the fast call's result and return it as its own,
// and the fast call's worker was left waiting for an answer already taken.
func TestConcurrentCommandsGetTheirOwnAnswers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fwd := dispatcher(t, newCommandSessions(ctx, waitServer(nil), nil))

	const n = 8
	var wg sync.WaitGroup
	got := make([]string, n)
	errs := make([]error, n)
	for i := range n {
		// Later commands finish first, so every answer arrives while earlier
		// commands are still waiting for theirs.
		req := waitCall(t, fmt.Sprintf("call-%d", i), (n-i)*40)
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = command(ctx, fwd, fmt.Sprintf("cmd_%d", i), req)
		}()
		time.Sleep(5 * time.Millisecond)
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Errorf("call-%d: %v", i, errs[i])
			continue
		}
		if want := fmt.Sprintf("call-%d", i); got[i] != want {
			t.Errorf("command for %s was answered with %s's result", want, got[i])
		}
	}
}

// A command that outlives its deadline takes nothing down with it: the next
// command on the same tunnel is answered, and the abandoned tool call is
// cancelled rather than left running for nobody.
//
// The bug: at the deadline the client closed the shared connection and
// connected again, and an in-memory pair cannot be reconnected, so every later
// call on the tunnel failed with a closed pipe until the tunnel was rebuilt.
func TestATimedOutCommandDoesNotBreakTheNext(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cancelled := make(chan string, 1)
	fwd := dispatcher(t, newCommandSessions(ctx, waitServer(cancelled), nil))

	deadline, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err := command(deadline, fwd, "cmd_slow", waitCall(t, "slow", 5000))
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow command: err = %v, want its deadline", err)
	}

	select {
	case tag := <-cancelled:
		if tag != "slow" {
			t.Errorf("cancelled %q, want slow", tag)
		}
	case <-time.After(2 * time.Second):
		t.Error("the timed-out tool call was left running")
	}

	for i := range 3 {
		got, err := command(ctx, fwd, fmt.Sprintf("cmd_next_%d", i), waitCall(t, "next", 0))
		if err != nil {
			t.Fatalf("command %d after a timeout: %v", i, err)
		}
		if got != "next" {
			t.Fatalf("command %d after a timeout answered %q", i, got)
		}
	}
}

// A command that fails closes the shared connection, and that must not end
// the commands still waiting beside it.
func TestClosingTheSharedConnectionSparesOtherCommands(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	fwd := dispatcher(t, newCommandSessions(ctx, waitServer(nil), nil))

	result := make(chan error, 1)
	req := waitCall(t, "waiting", 200)
	go func() {
		got, err := command(ctx, fwd, "cmd_waiting", req)
		if err == nil && got != "waiting" {
			err = fmt.Errorf("answered %q", got)
		}
		result <- err
	}()
	time.Sleep(20 * time.Millisecond)

	failing := tunnelctx.ContextWithRequestID(ctx, "cmd_failing")
	conn, err := fwd.Connect(failing)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	if err := <-result; err != nil {
		t.Fatalf("a command in flight when another closed the connection: %v", err)
	}
}

// Stopping the tunnel ends every session, including one mid-call, and says
// so; nothing new is opened afterwards.
func TestStoppingEndsEverySession(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancelled := make(chan string, 1)
	sessions := newCommandSessions(ctx, waitServer(cancelled), nil)
	fwd := dispatcher(t, sessions)

	req := waitCall(t, "long", 5000)
	go func() { _, _ = command(context.WithoutCancel(ctx), fwd, "cmd_long", req) }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-sessions.done:
	case <-time.After(2 * time.Second):
		t.Fatal("sessions did not end after the tunnel stopped")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Error("the call in flight was not cancelled when the tunnel stopped")
	}
	if _, err := sessions.sessionFor("cmd_after"); err == nil {
		t.Error("a session opened after the tunnel stopped")
	}
}

// A session nobody finished is closed once it is older than any command could
// be, so a worker that leaves without reading cannot hold one for ever.
func TestAbandonedSessionsAreSwept(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	sessions := newCommandSessions(ctx, waitServer(nil), nil)

	if _, err := sessions.sessionFor("cmd_abandoned"); err != nil {
		t.Fatal(err)
	}
	sessions.mu.Lock()
	sessions.open["cmd_abandoned"].opened = time.Now().Add(-staleSession - time.Second)
	sessions.mu.Unlock()

	if _, err := sessions.sessionFor("cmd_fresh"); err != nil {
		t.Fatal(err)
	}
	if sessions.lookup("cmd_abandoned") != nil {
		t.Error("an abandoned session survived the sweep")
	}
	if sessions.lookup("cmd_fresh") == nil {
		t.Error("the sweep closed a fresh session")
	}
}
