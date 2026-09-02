package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	tunnelclient "github.com/openai/tunnel-client"
)

// runtime is the part of the tunnel client the manager actually drives.
//
// Two implementations exist because the tunnel can reach mcpd two ways, and
// the choice has consequences the manager should not have to know about. See
// newRuntime.
type runtime interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	// Ready closes after the first completed control-plane poll.
	Ready() <-chan struct{}
}

// newRuntime builds the tunnel client for a configuration.
//
// The tunnel talks to an MCP server running in this process over an in-memory
// transport: no port, no socket, no credential, and no local address for
// anything else to find.
//
// There used to be a second arrangement that pointed the tunnel at mcpd's own
// HTTP listener, and it existed for one reason: protected-resource discovery
// is a tunnel command, and the client can only fetch metadata against a URL.
// mcpd stopped being an authorization server, so there is no metadata to
// fetch and nothing left that a URL binding could do.
func newRuntime(cfg Config, server *mcp.Server, runCtx context.Context, log *slog.Logger, out logWriter, observe func()) (runtime, error) {
	return newInMemoryRuntime(cfg, server, runCtx, log, out, observe)
}

// observedTransport wraps the server's side of the in-memory pair and reports
// every message that arrives from the tunnel. That is the one positive sign
// of life a tunnel gives: the client says ready once and then nothing, so
// what ChatGPT actually sends is the only proof the connector is in use.
type observedTransport struct {
	inner   mcp.Transport
	observe func()
}

func (t observedTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return observedConnection{Connection: conn, observe: t.observe}, nil
}

type observedConnection struct {
	mcp.Connection
	observe func()
}

func (c observedConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if err == nil && c.observe != nil {
		c.observe()
	}
	return msg, err
}

// inMemoryRuntime is the embedded SDK, driving an MCP server in this process.
type inMemoryRuntime struct {
	client *tunnelclient.Client
	// served closes when the MCP server goroutine has returned, so Stop can
	// wait for it rather than leaving it running past shutdown.
	served chan struct{}
}

func newInMemoryRuntime(cfg Config, server *mcp.Server, runCtx context.Context, log *slog.Logger, out logWriter, observe func()) (runtime, error) {
	if server == nil {
		// Reachable only from a caller that got the binding wrong; without the
		// guard it is a nil dereference inside a goroutine, which takes down
		// the process rather than the tunnel.
		return nil, fmt.Errorf("tunnel: an in-memory binding needs an MCP server")
	}
	serverSide, tunnelTransport := mcp.NewInMemoryTransports()
	var serverTransport mcp.Transport = serverSide
	if observe != nil {
		serverTransport = observedTransport{inner: serverSide, observe: observe}
	}

	client, err := tunnelclient.New(tunnelclient.Config{
		TunnelID:            cfg.TunnelID,
		APIKey:              cfg.APIKey,
		ControlPlaneBaseURL: cfg.ControlPlaneBaseURL,
		LogLevel:            cfg.LogLevel,
		LogWriter:           out,
	}, tunnelTransport)
	if err != nil {
		return nil, err
	}

	r := inMemoryRuntime{client: client, served: make(chan struct{})}
	go func() {
		defer close(r.served)
		if err := server.Run(runCtx, serverTransport); err != nil && runCtx.Err() == nil {
			log.Error("tunnel MCP server stopped", "error", err)
		}
	}()
	return r, nil
}

func (r inMemoryRuntime) Start(ctx context.Context) error { return r.client.Start(ctx) }
func (r inMemoryRuntime) Ready() <-chan struct{}          { return r.client.Ready() }

func (r inMemoryRuntime) Stop(ctx context.Context) error {
	err := r.client.Stop(ctx)
	// The server returns once the run context is cancelled, which the manager
	// does before stopping. Waiting for it here keeps "stopped" meaning the
	// whole tunnel is stopped.
	select {
	case <-r.served:
	case <-ctx.Done():
	}
	return err
}

// These match the embedded SDK's own defaults.
const (
	maxInFlightRequests   = 20
	maxConcurrentRequests = 10
	connectionMaxTTL      = 10 * time.Minute
)
