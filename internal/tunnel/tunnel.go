// Package tunnel connects mcpd to ChatGPT through OpenAI's Secure MCP Tunnel,
// in this process rather than through a sidecar.
//
// The tunnel dials outward and holds the connection open, so ChatGPT can reach
// mcpd with no inbound port, no public DNS, and no NAT rule. Embedding it
// removes the HTTP hop a sidecar would need: the tunnel talks to the MCP
// server over an in-memory transport, so requests never touch a socket and
// there is no local address for anything else to find.
//
// It also removes a credential. A sidecar has to authenticate to mcpd like any
// other client, which means a bearer token configured in two places. In
// process there is no request to authenticate, so the tunnel's identity comes
// from configuration instead -- see Config.Principal.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	tunnelclient "github.com/openai/tunnel-client"
	"github.com/spoked/mcpd/internal/auth"
)

// State is the tunnel's lifecycle position.
type State string

const (
	// StateDisabled means no tunnel is configured.
	StateDisabled State = "disabled"
	// StateStopped means it is configured but not running.
	StateStopped State = "stopped"
	// StateStarting means it is connecting to the control plane.
	StateStarting State = "starting"
	// StateConnected means ChatGPT can reach mcpd through it.
	StateConnected State = "connected"
	// StateFailed means it stopped with an error.
	StateFailed State = "failed"
)

// Config describes the tunnel.
type Config struct {
	// Enabled switches the tunnel on. It is separate from TunnelID so a
	// configured tunnel can be turned off without losing its settings.
	Enabled bool

	// TunnelID identifies the tunnel in OpenAI's control plane.
	TunnelID string
	// APIKey is a *runtime* key. An admin key is for creating and deleting
	// tunnels and must not be used for the long-running connection.
	APIKey string

	// Principal is the identity requests arriving through the tunnel act as.
	//
	// An in-memory transport carries no HTTP request and therefore no bearer
	// token, so there is nothing to authenticate. Authorization comes from
	// here instead: the tunnel is already authenticated to OpenAI by APIKey,
	// and this decides what that authenticated tunnel may reach.
	Principal auth.Principal

	// ControlPlaneBaseURL overrides the OpenAI endpoint. Empty uses the
	// default.
	ControlPlaneBaseURL string

	// LogLevel controls how much of the tunnel client's own output is kept.
	// Info is the default, because at Warn a rejected credential produces no
	// explanation at all.
	LogLevel slog.Level

	// ReadyTimeout is retained for compatibility and no longer gates startup.
	//
	// Readiness is now watched rather than waited on: the client reports ready
	// after its first completed control-plane poll, and an idle tunnel's first
	// poll waits out the poll timeout while being perfectly healthy. Treating
	// that as a failure tore down working tunnels.
	ReadyTimeout time.Duration
}

// Validate checks the configuration before anything connects.
func (c *Config) Validate() error {
	var problems []string

	if !tunnelIDPattern(c.TunnelID) {
		problems = append(problems,
			"tunnel_id must look like tunnel_ followed by 32 hexadecimal characters")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		problems = append(problems, "an API key is required")
	}
	if err := c.Principal.Validate(); err != nil {
		problems = append(problems, err.Error())
	}

	if len(problems) > 0 {
		return fmt.Errorf("tunnel: %s", strings.Join(problems, "; "))
	}
	return nil
}

// tunnelIDPattern reports whether an ID has the documented shape. Checking it
// locally turns a typo into a startup error rather than an opaque 401 from the
// control plane.
func tunnelIDPattern(id string) bool {
	const prefix = "tunnel_"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	hex := id[len(prefix):]
	if len(hex) != 32 {
		return false
	}
	for _, r := range hex {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// Status is what the dashboard shows.
type Status struct {
	State State `json:"state"`
	// TunnelID is safe to display; the API key never is.
	TunnelID string `json:"tunnel_id,omitempty"`
	// Principal names the identity requests act as, so an operator can see
	// what the tunnel is allowed to do.
	Principal string   `json:"principal,omitempty"`
	Role      string   `json:"role,omitempty"`
	Plugins   []string `json:"plugins,omitempty"`
	// Message explains a failure in terms an operator can act on. It never
	// quotes a credential.
	Message     string     `json:"message,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
}

// ServerFactory builds the MCP server the tunnel exposes.
//
// It is a function rather than a value because the server depends on which
// plugins the tunnel's principal may reach, and plugins are mounted after the
// tunnel is constructed.
type ServerFactory func(principal *auth.Principal) (*mcp.Server, error)

// Manager owns the tunnel's lifecycle.
//
// The tunnel is startable and stoppable at runtime so it can be managed from
// the dashboard: an operator who has just pasted a tunnel ID should not have
// to restart mcpd to find out whether it works.
type Manager struct {
	cfg     Config
	factory ServerFactory
	log     *slog.Logger

	mu          sync.RWMutex
	state       State
	message     string
	connectedAt *time.Time
	cancel      context.CancelFunc
	running     sync.WaitGroup
}

// NewManager builds a tunnel manager. A zero TunnelID leaves it disabled.
func NewManager(cfg Config, factory ServerFactory, log *slog.Logger) *Manager {
	state := StateStopped
	if cfg.TunnelID == "" {
		state = StateDisabled
	}
	return &Manager{cfg: cfg, factory: factory, log: log, state: state}
}

// Status reports the current state.
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s := Status{
		State:       m.state,
		Message:     m.message,
		ConnectedAt: m.connectedAt,
	}
	if m.state != StateDisabled {
		s.TunnelID = m.cfg.TunnelID
		s.Principal = m.cfg.Principal.ID
		s.Role = m.cfg.Principal.Role.String()
		s.Plugins = m.cfg.Principal.Plugins
	}
	return s
}

// Start connects the tunnel. It returns once the connection is established or
// has failed, so a caller acting on an operator's request can report the
// outcome rather than leaving them to poll.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.state == StateDisabled {
		m.mu.Unlock()
		return errors.New("tunnel: no tunnel is configured")
	}
	if m.state == StateStarting || m.state == StateConnected {
		m.mu.Unlock()
		return nil
	}
	m.state = StateStarting
	m.message = ""
	m.mu.Unlock()

	if err := m.cfg.Validate(); err != nil {
		m.fail(err)
		return err
	}

	principal := m.cfg.Principal
	server, err := m.factory(&principal)
	if err != nil {
		m.fail(fmt.Errorf("tunnel: could not build the MCP server: %w", err))
		return err
	}

	// The server side of the pair runs the MCP server; the client side goes to
	// the tunnel. Nothing binds a port and nothing crosses a socket.
	serverTransport, tunnelTransport := mcp.NewInMemoryTransports()

	client, err := tunnelclient.New(tunnelclient.Config{
		TunnelID:            m.cfg.TunnelID,
		APIKey:              m.cfg.APIKey,
		ControlPlaneBaseURL: m.cfg.ControlPlaneBaseURL,
		// The client's own output is the only place a 401 or a permission
		// problem is visible; without it a failure is just "deadline
		// exceeded", which says nothing an operator can act on.
		LogLevel:  m.cfg.LogLevel,
		LogWriter: logWriter{log: m.log},
	}, tunnelTransport)
	if err != nil {
		m.fail(fmt.Errorf("tunnel: %w", redactKey(err, m.cfg.APIKey)))
		return err
	}

	// A context detached from the caller's: the tunnel outlives the request
	// that started it and is stopped explicitly.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	m.running.Add(1)
	go func() {
		defer m.running.Done()
		if err := server.Run(runCtx, serverTransport); err != nil && runCtx.Err() == nil {
			m.log.Error("tunnel MCP server stopped", "error", err)
		}
	}()

	if err := client.Start(runCtx); err != nil {
		cancel()
		m.fail(fmt.Errorf("tunnel: %w", redactKey(err, m.cfg.APIKey)))
		return err
	}

	m.running.Add(1)
	go func() {
		defer m.running.Done()
		<-runCtx.Done()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := client.Stop(stopCtx); err != nil {
			m.log.Warn("tunnel did not stop cleanly", "error", err)
		}
	}()

	// Readiness is watched rather than waited on.
	//
	// The client reports ready after its first *completed* control-plane poll,
	// and an empty poll waits out the poll timeout before completing. A tunnel
	// with nothing to do is therefore slow to report ready while being
	// perfectly healthy -- so blocking on it, and tearing the client down when
	// it does not arrive, destroys a working tunnel for no reason.
	//
	// Start returns as soon as the client is running. The state moves from
	// starting to connected on its own.
	m.running.Add(1)
	go func() {
		defer m.running.Done()
		select {
		case <-client.Ready():
			now := time.Now()
			m.mu.Lock()
			// Only claim connected if nothing stopped us in the meantime.
			if m.state == StateStarting {
				m.state = StateConnected
				m.connectedAt = &now
				m.message = ""
			}
			m.mu.Unlock()
			m.log.Info("tunnel connected",
				"tunnel_id", m.cfg.TunnelID,
				"principal", m.cfg.Principal.ID,
				"plugins", m.cfg.Principal.Plugins)
		case <-runCtx.Done():
		}
	}()

	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	m.log.Info("tunnel started; waiting for the first control-plane poll",
		"tunnel_id", m.cfg.TunnelID,
		"principal", m.cfg.Principal.ID,
		"plugins", m.cfg.Principal.Plugins)
	return nil
}

// Stop disconnects the tunnel and waits for its goroutines.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	if m.state != StateDisabled {
		m.state = StateStopped
	}
	m.connectedAt = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		m.running.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Reconfigure replaces the configuration and restarts if it is enabled.
//
// This is what makes the dashboard mean anything: a tunnel id pasted into a
// form has to reach the running tunnel, not just the database. It stops first
// so a changed key or id never keeps serving under the old one.
func (m *Manager) Reconfigure(ctx context.Context, cfg Config) error {
	if err := m.Stop(ctx); err != nil {
		m.log.Warn("previous tunnel did not stop cleanly before reconfiguring", "error", err)
	}

	m.mu.Lock()
	m.cfg = cfg
	m.message = ""
	m.connectedAt = nil
	if cfg.TunnelID == "" || !cfg.Enabled {
		m.state = StateDisabled
		if cfg.TunnelID != "" {
			m.state = StateStopped
		}
		m.mu.Unlock()
		return nil
	}
	m.state = StateStopped
	m.mu.Unlock()

	return m.Start(ctx)
}

// Enabled reports whether a tunnel is configured.
func (m *Manager) Enabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state != StateDisabled
}

func (m *Manager) fail(err error) {
	m.mu.Lock()
	m.state = StateFailed
	m.message = err.Error()
	m.connectedAt = nil
	m.mu.Unlock()
	m.log.Error("tunnel failed", "error", err)
}

// redactKey removes the API key from an error before it reaches a log line or
// the dashboard. Transport errors quote request details freely.
func redactKey(err error, key string) error {
	if err == nil || key == "" {
		return err
	}
	msg := err.Error()
	if !strings.Contains(msg, key) {
		return err
	}
	return errors.New(strings.ReplaceAll(msg, key, "[REDACTED]"))
}

// logWriter routes the tunnel client's own output into mcpd's logger, so a
// single log stream carries everything rather than half of it going to stderr.
type logWriter struct{ log *slog.Logger }

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Errors from the client matter more than its routine chatter, and an
		// operator scanning for a cause should not have to read past the rest.
		if strings.Contains(line, "level=ERROR") || strings.Contains(line, `"level":"ERROR"`) {
			w.log.Error("tunnel-client", "line", line)
			continue
		}
		w.log.Info("tunnel-client", "line", line)
	}
	return len(p), nil
}

// diagnose names the likeliest cause of a failure to connect.
//
// The two credentials involved look alike and are obtained from adjacent pages,
// so the mistake is easy to make and nearly impossible to spot from a 401. An
// admin key is recognisable by its prefix, which turns a guess into a
// statement.
func diagnose(apiKey string) string {
	if strings.HasPrefix(apiKey, "sk-admin-") {
		return "That looks like an admin key (it starts with sk-admin-). Admin keys " +
			"can only create and delete tunnels, not run one. Use a runtime API key " +
			"from Settings, Organization, API keys instead"
	}
	return "Check the tunnel ID exists, and that the key is a runtime API key whose " +
		"principal has Tunnels Read and Use -- an admin key will not work"
}
