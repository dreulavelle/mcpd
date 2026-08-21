package tunnel

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	tunnelclient "github.com/openai/tunnel-client"
	tcapp "github.com/openai/tunnel-client/pkg/app"
	tcconfig "github.com/openai/tunnel-client/pkg/config"
	tccontrolplane "github.com/openai/tunnel-client/pkg/controlplane"
	tcruntimeconfig "github.com/openai/tunnel-client/pkg/runtimeconfig"
	"github.com/openai/tunnel-client/pkg/tlsconfig"
	tctypes "github.com/openai/tunnel-client/pkg/types"
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
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
// With no MCPServerURL the tunnel talks to an MCP server running in this
// process over an in-memory transport: no port, no socket, no credential.
// That is the better arrangement whenever it is sufficient.
//
// It is not sufficient for OAuth. Protected-resource discovery is a *tunnel
// command*: the control plane asks the client to go and fetch the metadata,
// and the client can only do that against an HTTP URL. An in-memory binding
// has no URL, so the client answers "missing MCP server URL", the control
// plane concludes the server does not implement OAuth, and ChatGPT refuses to
// create the connector. Pointing the tunnel at mcpd's own HTTP listener is
// what makes discovery possible.
func newRuntime(cfg Config, server *mcp.Server, runCtx context.Context, log *slog.Logger, out logWriter) (runtime, error) {
	if cfg.MCPServerURL == "" {
		return newInMemoryRuntime(cfg, server, runCtx, log, out)
	}
	return newHTTPRuntime(cfg, log, out)
}

// inMemoryRuntime is the embedded SDK, driving an MCP server in this process.
type inMemoryRuntime struct {
	client *tunnelclient.Client
	// served closes when the MCP server goroutine has returned, so Stop can
	// wait for it rather than leaving it running past shutdown.
	served chan struct{}
}

func newInMemoryRuntime(cfg Config, server *mcp.Server, runCtx context.Context, log *slog.Logger, out logWriter) (runtime, error) {
	if server == nil {
		// Reachable only from a caller that got the binding wrong; without the
		// guard it is a nil dereference inside a goroutine, which takes down
		// the process rather than the tunnel.
		return nil, fmt.Errorf("tunnel: an in-memory binding needs an MCP server")
	}
	serverTransport, tunnelTransport := mcp.NewInMemoryTransports()

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

// httpRuntime points the tunnel client at mcpd's own MCP listener.
//
// The embedded SDK cannot do this -- tunnelclient.New hard-codes an in-memory
// binding and its Config has no field for a server URL -- so the runtime is
// assembled from the same exported packages the SDK itself uses.
//
// Requests now arrive at mcpd as ordinary HTTP carrying whatever Authorization
// header the connector sent, which is the point: identity comes from the
// caller's OAuth token rather than from a principal named in configuration.
type httpRuntime struct {
	app   *fx.App
	ready chan struct{}
	once  sync.Once
}

func newHTTPRuntime(cfg Config, log *slog.Logger, out logWriter) (runtime, error) {
	serverURL, err := url.Parse(cfg.MCPServerURL)
	if err != nil || serverURL.Scheme == "" || serverURL.Host == "" {
		return nil, fmt.Errorf("tunnel: %q is not a usable MCP server URL", cfg.MCPServerURL)
	}
	controlPlane, err := controlPlaneURL(cfg.ControlPlaneBaseURL)
	if err != nil {
		return nil, err
	}
	urlPath, err := tcconfig.NormalizeControlPlaneURLPath("")
	if err != nil {
		return nil, fmt.Errorf("tunnel: %w", err)
	}

	// mcpd's own certificate is not in any system trust store, so the client
	// is told about it explicitly. LoadBundle extends the system roots rather
	// than replacing them, which keeps api.openai.com verifiable.
	var trust *tlsconfig.Bundle
	if cfg.TrustedCAFile != "" {
		trust, err = tlsconfig.LoadBundle(cfg.TrustedCAFile)
		if err != nil {
			return nil, fmt.Errorf("tunnel: trust mcpd's certificate: %w", err)
		}
	}

	r := &httpRuntime{ready: make(chan struct{})}

	tcCfg := &tcconfig.Config{
		TLS: trust,
		ControlPlane: tcconfig.ControlPlaneConfig{
			BaseURL:             controlPlane,
			URLPath:             urlPath,
			TunnelID:            tctypes.TunnelID(cfg.TunnelID),
			APIKey:              cfg.APIKey,
			MaxInFlightRequests: maxInFlightRequests,
		},
		Logging: tcconfig.LoggingConfig{
			Level:  cfg.LogLevel,
			Format: tcconfig.LogFormatStructText,
		},
		MCP: tcconfig.MCPConfig{
			ServerURL:             serverURL,
			TransportKind:         tcconfig.MCPTransportHTTPStreamable,
			ConnectionMaxTTL:      connectionMaxTTL,
			MaxConcurrentRequests: maxConcurrentRequests,
			ChannelBindings: []tcconfig.MCPChannelBinding{{
				Channel:       tctypes.DefaultChannel,
				TransportKind: tcconfig.MCPTransportHTTPStreamable,
				ServerURL:     serverURL,
			}},
		},
		// Harpoon is how the authorization server's POST endpoints -- token,
		// registration, revocation -- are reached from outside the network.
		// They are auto-registered from the protected-resource metadata, and
		// only when they share an origin with the MCP server URL above, which
		// is why mcpd derives both from the same public address.
		//
		// Browser authorization is never tunnelled; the person approving the
		// connection reaches /oauth/authorize directly, which is fine because
		// they are the one sitting on the network mcpd is on.
		Harpoon: tcconfig.HarpoonConfig{
			// A private deployment on a plain-HTTP address is the expected
			// case here, and refusing it would leave OAuth working only for
			// hosts that already have TLS -- which are the hosts that did not
			// need a tunnel.
			AllowPlaintextHTTP: serverURL.Scheme == "http",
			MaxResponseBytes:   tcruntimeconfig.DefaultHarpoonMaxResponseBytes,
			MaxRedirects:       tcruntimeconfig.DefaultHarpoonMaxRedirects,
			HostClassifier: tcruntimeconfig.HarpoonHostClassifierConfig{
				IncludeLoopback: true,
				IncludePrivate:  true,
			},
		},
	}

	r.app = tcapp.NewWithRuntime(tcCfg,
		tcapp.RuntimeOptions{DisableHealthAdmin: true},
		fx.Provide(func() io.Writer { return out }),
		// The client reports no readiness of its own once it is assembled this
		// way, so it is taken from the same place the SDK takes it: the first
		// control-plane poll that completes without an error.
		fx.Decorate(func(f tccontrolplane.Fetcher) tccontrolplane.Fetcher {
			return &readyFetcher{delegate: f, ready: r.markReady}
		}),
		fx.WithLogger(func(*slog.Logger) fxevent.Logger { return fxevent.NopLogger }),
	)
	if err := r.app.Err(); err != nil {
		return nil, fmt.Errorf("tunnel: build client: %w", err)
	}
	return r, nil
}

func (r *httpRuntime) Start(ctx context.Context) error { return r.app.Start(ctx) }
func (r *httpRuntime) Stop(ctx context.Context) error  { return r.app.Stop(ctx) }
func (r *httpRuntime) Ready() <-chan struct{}          { return r.ready }

func (r *httpRuntime) markReady() { r.once.Do(func() { close(r.ready) }) }

// readyFetcher reports readiness on the first poll that returns without error.
type readyFetcher struct {
	delegate tccontrolplane.Fetcher
	ready    func()
}

func (f *readyFetcher) Poll(ctx context.Context, limit int) ([]tccontrolplane.PolledCommand, tctypes.TunnelServiceRequestID, error) {
	commands, requestID, err := f.delegate.Poll(ctx, limit)
	if err == nil && f.ready != nil {
		f.ready()
	}
	return commands, requestID, err
}

// These match the embedded SDK's own defaults, so the two runtimes behave the
// same in every respect except how they reach the MCP server.
const (
	maxInFlightRequests   = 20
	maxConcurrentRequests = 10
	connectionMaxTTL      = 10 * time.Minute
)

func controlPlaneURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = tunnelclient.DefaultControlPlaneBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("tunnel: %q is not a usable control-plane URL", raw)
	}
	return parsed, nil
}
