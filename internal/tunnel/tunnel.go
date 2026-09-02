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

	// Plugin names the single plugin this tunnel serves. Empty serves the
	// aggregate endpoint: everything the caller is granted.
	//
	// It is here rather than derived from MCPServerURL because it is what an
	// operator chose, and what the dashboard has to name the tunnel by. The
	// URL is built from it.
	Plugin string

	// TunnelID identifies the tunnel in OpenAI's control plane.
	TunnelID string

	// AccountID and AccountName name the ChatGPT account this tunnel connects
	// with. They decide nothing -- the credential and the identity are already
	// resolved into APIKey and Principal by the time a Config reaches here --
	// and exist so that a log line, a status and an error can say which
	// workspace a tunnel belongs to. With one account that is noise; with
	// several it is the first thing anybody needs to know.
	AccountID   string
	AccountName string
	// APIKey is a *runtime* key. An admin key is for creating and deleting
	// tunnels and must not be used for the long-running connection.
	APIKey string

	// Principal is the identity requests arriving through the tunnel act as,
	// and applies only to the in-memory binding.
	//
	// An in-memory transport carries no HTTP request and therefore no bearer
	// token, so there is nothing to authenticate. Authorization comes from
	// here instead: the tunnel is already authenticated to OpenAI by APIKey,
	// and this decides what that authenticated tunnel may reach.
	//
	// With MCPServerURL set the connector's own Authorization header arrives
	// with every request, so identity comes from the caller and this is
	// ignored -- which is the better arrangement, because one shared identity
	// cannot tell two people apart and so cannot enforce a second approver.
	Principal auth.Principal

	// Debug turns on the tunnel client's verbose and raw-HTTP logging.
	//
	// It logs full requests and responses, headers and bodies included, which
	// is the only way to see what the control plane actually sent and what
	// mcpd actually answered. That also means credentials in the clear, so it
	// is off by default, said out loud when it is on, and meant to be turned
	// back off.
	Debug bool

	// DiagnosticsAddr binds the tunnel client's own health and admin listener.
	//
	// Empty leaves it off, which is the right default: it is a second HTTP
	// surface in the process and mcpd already reports the tunnel's state. It
	// earns its place when a connector is being refused for reasons only the
	// client can see -- /readyz distinguishes "still discovering" from
	// "discovery failed", and /api/oauth reports what was actually discovered,
	// neither of which is visible from mcpd's side.
	//
	// Bind it to loopback. It is unauthenticated.
	DiagnosticsAddr string

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
	// Plugin names the system this tunnel serves, empty for all of them.
	Plugin string `json:"plugin"`
	// Message explains a failure in terms an operator can act on. It never
	// quotes a credential.
	Message     string     `json:"message,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`

	// Requests counts what ChatGPT has actually sent through this tunnel
	// since it last connected, and LastRequestAt is when the latest arrived.
	// "Connected" is decided once, on the first completed poll, and says
	// nothing about the hours after it; these are what say something.
	Requests      int64      `json:"requests"`
	LastRequestAt *time.Time `json:"last_request_at,omitempty"`

	// Trouble is the last error the tunnel client reported, and TroubleAt is
	// when. The client backs off on its own and never tells mcpd its poll is
	// failing; its error lines are the only sign, so they are kept.
	Trouble   string     `json:"trouble,omitempty"`
	TroubleAt *time.Time `json:"trouble_at,omitempty"`
	// Degraded is a connected tunnel whose client has been reporting errors
	// for a while with nothing served since. Not failed -- the client is
	// still trying -- but not something to trust either, and the watchdog
	// restarts it if it goes on long enough.
	Degraded bool `json:"degraded"`

	// Attempts counts restarts since the tunnel last worked, and NextRetryAt
	// is when the next one is due. A failure the supervisor will not retry --
	// a rejected credential, a configuration that cannot start -- leaves
	// NextRetryAt empty with the state at failed, which is the signal that a
	// person is needed.
	Attempts    int        `json:"attempts,omitempty"`
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`

	// Activity is how many requests arrived in each of the last
	// activityHours hours, oldest first, the current hour last. In memory
	// only and per process, which is enough for the question it answers --
	// "is this connector in use, and did it stop" -- and it survives a
	// restart of the tunnel because the history is carried to the manager
	// that replaces one.
	Activity []int64 `json:"activity"`
	// Errors is the client's error lines per hour over the same window, so
	// a chart can show a connector that stopped serving at the hour its
	// errors began.
	Errors []int64 `json:"errors"`

	// Upstream reports whether OpenAI still has this tunnel: "present",
	// "missing", or "" when nothing has checked -- there is no admin key, or
	// the check has not run yet. A tunnel deleted in OpenAI's dashboard polls
	// for ever and is never told.
	Upstream          string     `json:"upstream,omitempty"`
	UpstreamCheckedAt *time.Time `json:"upstream_checked_at,omitempty"`
}

// The supervisor's timings. Variables so a test can shorten them.
var (
	// retryBase is the first delay after a retryable failure; each further
	// attempt doubles it up to retryCap.
	retryBase = 2 * time.Second
	retryCap  = 5 * time.Minute
	// stillDownAfter is the attempt at which the operator is told again: the
	// first notice said mcpd was retrying, and this one says it has not
	// worked. About ten minutes of continuous failure at the timings above.
	stillDownAfter = 8
	// degradedAfter is how long the client has to keep reporting errors, with
	// nothing served in between, before a connected tunnel is called
	// degraded; restartAfter is how long before the watchdog restarts it.
	// Generous on purpose: a tunnel that logs a processing error while
	// serving requests is working, and must not be flapped.
	degradedAfter = 2 * time.Minute
	restartAfter  = 10 * time.Minute
	// troubleWindow is how long a client error counts as current.
	troubleWindow = 5 * time.Minute
	// watchdogEvery is how often the watchdog looks.
	watchdogEvery = 30 * time.Second
)

// activityHours is how far back the per-tunnel request history reaches.
const activityHours = 12

// activity is a ring of hourly request counts.
type activity struct {
	// counts[i] is the hour i; head is the index of the current hour, and
	// hour is which hour of the epoch it holds, so a gap of idle hours is
	// zeroed rather than left holding a stale count.
	counts [activityHours]int64
	head   int
	hour   int64
}

// note records one request at the given time.
func (a *activity) note(now time.Time) {
	a.advance(now)
	a.counts[a.head]++
}

// advance moves the ring to now, zeroing every hour skipped.
func (a *activity) advance(now time.Time) {
	h := now.Unix() / 3600
	if a.hour == 0 {
		a.hour = h
		return
	}
	for a.hour < h {
		a.hour++
		a.head = (a.head + 1) % activityHours
		a.counts[a.head] = 0
	}
}

// series returns the counts oldest first, ending with the current hour.
func (a *activity) series(now time.Time) []int64 {
	a.advance(now)
	out := make([]int64, activityHours)
	for i := 0; i < activityHours; i++ {
		out[i] = a.counts[(a.head+1+i)%activityHours]
	}
	return out
}

// backoff is the delay before the nth retry, starting at 1.
func backoff(attempt int) time.Duration {
	d := retryBase
	for i := 1; i < attempt && d < retryCap; i++ {
		d *= 2
	}
	if d > retryCap {
		d = retryCap
	}
	return d
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
	factory ServerFactory
	log     *slog.Logger

	// onFailure is told when this tunnel stops serving, so that a person can
	// be reached. retrying says whether the supervisor will keep trying on
	// its own: a rejected credential will not fix itself, a control plane
	// that is briefly unreachable will. Set by the group from the
	// composition root, and nil wherever nobody is listening.
	onFailure func(plugin, tunnelID, reason string, retrying bool)
	// onRecovered is told when a tunnel that had failed is serving again, so
	// the person told about the failure is told it is over.
	onRecovered func(plugin, tunnelID string)
	// now is the clock, replaceable by a test.
	now func() time.Time

	// ops serialises the lifecycle transitions -- Start, Stop, Reconfigure --
	// end to end.
	//
	// mu is not enough and never was. It guards the fields, so a read and a
	// write of cfg cannot tear; it does not stop two changes arriving together
	// from interleaving one's Stop with the other's Start, which leaves a
	// tunnel running a configuration nobody asked for and no error anywhere
	// saying so. The dashboard's settings watcher spawns a goroutine per
	// change, so two saves in quick succession is the ordinary case rather
	// than a contrived one.
	//
	// It is a plain Mutex rather than part of mu because the two protect
	// different things: mu protects the fields for the length of a field
	// access, and this protects the *sequence* for the length of a restart,
	// which includes waiting on goroutines and a network round trip. Holding a
	// field lock across either of those would block Status on the dashboard
	// for the whole of a reconnect.
	ops sync.Mutex

	mu sync.RWMutex
	// cfg is guarded by mu. Start snapshots it under the lock and every read
	// after that is of the snapshot, because the goroutines Start leaves
	// behind outlive it and Reconfigure may have replaced it by the time they
	// run.
	cfg         Config
	state       State
	message     string
	connectedAt *time.Time
	cancel      context.CancelFunc
	running     sync.WaitGroup

	// Liveness, all guarded by mu.
	requests     int64
	lastRequest  *time.Time
	history      activity
	troubles     activity
	trouble      string
	troubleAt    *time.Time
	troubleSince *time.Time

	// The supervisor, guarded by mu. attempts counts restarts since the
	// tunnel last reached connected; retry is the pending restart, nil when
	// none is due; failedBefore remembers that somebody was told, so they
	// are told when it is over.
	attempts     int
	retry        *time.Timer
	retryAt      *time.Time
	failedBefore bool

	// What OpenAI last said about this tunnel, set by the group's upstream
	// check. Guarded by mu.
	upstream   string
	upstreamAt *time.Time
}

// NewManager builds a tunnel manager. A zero TunnelID leaves it disabled.
func NewManager(cfg Config, factory ServerFactory, log *slog.Logger) *Manager {
	state := StateStopped
	if cfg.TunnelID == "" {
		state = StateDisabled
	}
	return &Manager{cfg: cfg, factory: factory, log: log, state: state, now: time.Now}
}

// Status reports the current state.
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s := Status{
		State:             m.state,
		Message:           m.message,
		ConnectedAt:       m.connectedAt,
		Requests:          m.requests,
		LastRequestAt:     m.lastRequest,
		Activity:          m.history.series(m.now()),
		Errors:            m.troubles.series(m.now()),
		Trouble:           m.trouble,
		TroubleAt:         m.troubleAt,
		Degraded:          m.degradedLocked(m.now()) >= degradedAfter,
		Attempts:          m.attempts,
		NextRetryAt:       m.retryAt,
		Upstream:          m.upstream,
		UpstreamCheckedAt: m.upstreamAt,
	}
	s.Plugin = m.cfg.Plugin
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
	m.ops.Lock()
	defer m.ops.Unlock()
	return m.start(ctx)
}

// start connects the tunnel. The caller holds ops.
func (m *Manager) start(ctx context.Context) error {
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
	// Snapshotted here, under the lock, and used for the whole of this call
	// and by every goroutine it leaves behind. Reading m.cfg after the unlock
	// is what raced with Reconfigure writing it -- and the two goroutines
	// below outlive this function, so their reads landed whenever the control
	// plane happened to answer.
	cfg := m.cfg
	m.mu.Unlock()

	// Neither of these will be different in two seconds: a configuration
	// that does not validate and a server that cannot be built are for a
	// person, not the supervisor.
	if err := cfg.Validate(); err != nil {
		m.fail(err, false)
		return err
	}

	// The tunnel drives an MCP server in this process, built for the identity
	// this tunnel carries.
	principal := cfg.Principal
	server, err := m.factory(&principal)
	if err != nil {
		m.fail(fmt.Errorf("tunnel: could not build the MCP server: %w", err), false)
		return err
	}

	// A context detached from the caller's: the tunnel outlives the request
	// that started it and is stopped explicitly.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	// Scoped to this attempt, so a later reconnect reports a fresh rejection.
	// The client logs the same 401 on every backoff, and one explanation is
	// the useful number.
	var rejectOnce sync.Once
	out := logWriter{
		log: m.log,
		rejected: func(code string) {
			rejectOnce.Do(func() {
				// Not retried: the key will still be wrong in ten minutes.
				m.fail(fmt.Errorf("tunnel: %s", diagnose(cfg.APIKey, code)), false)
				// Halting is the point. The client would otherwise keep
				// retrying a credential the control plane has already
				// rejected, filling the log with a failure nobody is going
				// to see. Halted rather than stopped: a stop would reset the
				// state and erase the explanation, and recording the
				// failure a second time to put it back is how every rejected
				// key was reported twice.
				go func() {
					haltCtx, haltCancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer haltCancel()
					if err := m.halt(haltCtx); err != nil {
						m.log.WarnContext(ctx, "tunnel did not stop cleanly after a rejected key", "error", err)
					}
				}()
			})
		},
		trouble: func(line string) { m.noteTrouble(redactKey(errors.New(line), cfg.APIKey).Error()) },
	}

	client, err := newRuntime(cfg, server, runCtx, m.log, out, m.noteRequest)
	if err != nil {
		cancel()
		m.fail(fmt.Errorf("tunnel: %w", redactKey(err, cfg.APIKey)), true)
		return err
	}

	if err := client.Start(runCtx); err != nil {
		cancel()
		m.fail(fmt.Errorf("tunnel: %w", redactKey(err, cfg.APIKey)), true)
		return err
	}

	m.running.Add(1)
	go func() {
		defer m.running.Done()
		<-runCtx.Done()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		if err := client.Stop(stopCtx); err != nil {
			m.log.WarnContext(ctx, "tunnel did not stop cleanly", "error", err)
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
			now := m.now()
			m.mu.Lock()
			// Only claim connected if nothing stopped us in the meantime.
			recovered := false
			if m.state == StateStarting {
				m.state = StateConnected
				m.connectedAt = &now
				m.message = ""
				// Working again: the supervisor starts from the beginning
				// next time, and whoever was told about the failure is told
				// it is over.
				m.attempts = 0
				m.retryAt = nil
				recovered = m.failedBefore
				m.failedBefore = false
			}
			m.mu.Unlock()
			m.log.InfoContext(ctx, "tunnel connected",
				"tunnel_id", cfg.TunnelID,
				"principal", cfg.Principal.ID,
				"plugins", cfg.Principal.Plugins)
			if recovered && m.onRecovered != nil {
				m.onRecovered(cfg.Plugin, cfg.TunnelID)
			}
		case <-runCtx.Done():
		}
	}()

	// The watchdog. The client backs off for ever on its own and never says
	// so; a tunnel whose client has been reporting errors for restartAfter
	// with nothing served in between is restarted, because a fresh client
	// is the one thing that reliably clears a stuck one.
	m.running.Add(1)
	go func() {
		defer m.running.Done()
		ticker := time.NewTicker(watchdogEvery)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
			m.mu.RLock()
			stuck := m.state == StateConnected && m.degradedLocked(m.now()) >= restartAfter
			m.mu.RUnlock()
			if !stuck {
				continue
			}
			m.log.WarnContext(ctx, "tunnel has been reporting errors with nothing served; restarting it",
				"tunnel_id", cfg.TunnelID, "for", restartAfter.String())
			go func() {
				restartCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
				defer done()
				if err := m.Restart(restartCtx); err != nil {
					m.log.WarnContext(ctx, "tunnel did not restart cleanly", "error", err)
				}
			}()
			return
		}
	}()

	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	m.log.InfoContext(ctx, "tunnel started; waiting for the first control-plane poll",
		"tunnel_id", cfg.TunnelID,
		"principal", cfg.Principal.ID,
		"plugins", cfg.Principal.Plugins)
	return nil
}

// Stop disconnects the tunnel and waits for its goroutines.
func (m *Manager) Stop(ctx context.Context) error {
	m.ops.Lock()
	defer m.ops.Unlock()
	return m.stop(ctx)
}

// stop disconnects the tunnel and waits for its goroutines. The caller holds
// ops.
func (m *Manager) stop(ctx context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	if m.state != StateDisabled {
		m.state = StateStopped
	}
	m.connectedAt = nil
	// A stop is a decision, and a retry that fired after it would undo it.
	if m.retry != nil {
		m.retry.Stop()
		m.retry = nil
	}
	m.retryAt = nil
	m.attempts = 0
	m.failedBefore = false
	m.requests = 0
	m.lastRequest = nil
	m.troubleSince = nil
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

// halt tears the client down after a failure the state already records,
// leaving the state and its explanation as they are. What stop does, minus
// the reset: a halted tunnel still reads as failed, with the reason, and
// the supervisor's bookkeeping is untouched.
func (m *Manager) halt(ctx context.Context) error {
	m.ops.Lock()
	defer m.ops.Unlock()

	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
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
	// Held for the whole of stop, replace and start. Two settings changes
	// arriving together are serialised into two complete reconfigurations
	// rather than interleaved into one incoherent one.
	m.ops.Lock()
	defer m.ops.Unlock()

	if err := m.stop(ctx); err != nil {
		m.log.WarnContext(ctx, "previous tunnel did not stop cleanly before reconfiguring", "error", err)
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

	return m.start(ctx)
}

// Restart stops the tunnel and starts it again with the configuration it
// has. It is what the dashboard's button does, what the watchdog does, and
// what the supervisor does between attempts -- one path, so a restart means
// the same thing however it was asked for.
func (m *Manager) Restart(ctx context.Context) error {
	m.ops.Lock()
	defer m.ops.Unlock()
	if !m.Enabled() {
		return errors.New("tunnel: no tunnel is configured")
	}
	if err := m.stop(ctx); err != nil {
		m.log.WarnContext(ctx, "tunnel did not stop cleanly before restarting", "error", err)
	}
	return m.start(ctx)
}

// Enabled reports whether a tunnel is configured.
func (m *Manager) Enabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state != StateDisabled
}

// fail records a failure and, where retrying could help, schedules the next
// attempt.
//
// retryable is decided by the caller, because only it knows what failed: a
// control plane that could not be reached is worth trying again, a rejected
// credential or a configuration that does not validate is not. The
// supervisor never guesses.
func (m *Manager) fail(err error, retryable bool) {
	m.mu.Lock()
	was := m.state
	m.state = StateFailed
	m.message = err.Error()
	m.connectedAt = nil
	plugin, tunnelID := m.cfg.Plugin, m.cfg.TunnelID
	first := !m.failedBefore
	m.failedBefore = true
	var attempt int
	var delay time.Duration
	if retryable && m.cfg.Enabled && m.retry == nil {
		m.attempts++
		attempt = m.attempts
		delay = backoff(attempt)
		at := m.now().Add(delay)
		m.retryAt = &at
		m.retry = time.AfterFunc(delay, m.retryNow)
	}
	m.mu.Unlock()
	m.log.Error("tunnel failed", "error", err, "retrying", retryable, "attempt", attempt)

	if m.onFailure == nil {
		return
	}
	// Once per failure rather than once per report of one, and again at the
	// attempt where "retrying" has stopped being reassuring. The
	// rejected-credential path calls this twice deliberately -- Stop resets
	// the state and would erase the explanation -- and nobody needs telling
	// the same thing twice.
	switch {
	case was != StateFailed && first:
		m.onFailure(plugin, tunnelID, err.Error(), retryable)
	case retryable && attempt == stillDownAfter:
		m.onFailure(plugin, tunnelID,
			fmt.Sprintf("still not connecting after %d attempts: %s", attempt, err.Error()), true)
	case !retryable && was != StateFailed:
		// A failure that was being retried and has now become final: the
		// person who was told mcpd would keep trying needs to know it will
		// not.
		m.onFailure(plugin, tunnelID, err.Error(), false)
	}
}

// retryNow is the supervisor's next attempt, from the timer.
func (m *Manager) retryNow() {
	m.mu.Lock()
	m.retry = nil
	m.retryAt = nil
	enabled := m.cfg.Enabled && m.state == StateFailed
	m.mu.Unlock()
	if !enabled {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Start, not Restart: the failed attempt left nothing running to stop,
	// and stop would reset the attempt count the backoff is built on.
	if err := m.Start(ctx); err != nil {
		// Already recorded by fail, which scheduled the attempt after this.
		return
	}
}

// noteRequest records that ChatGPT sent something through the tunnel. Called
// from the transport for every message, so it is cheap and holds no lock for
// longer than two stores.
func (m *Manager) noteRequest() {
	now := m.now()
	m.mu.Lock()
	m.requests++
	m.lastRequest = &now
	m.history.note(now)
	// Something got through, so whatever the client was complaining about
	// was not stopping it.
	m.troubleSince = nil
	m.mu.Unlock()
}

// noteTrouble records an error line from the client. The first of a run
// starts the clock the watchdog reads; a run ends when a request is served
// or the errors stop for troubleWindow.
func (m *Manager) noteTrouble(line string) {
	now := m.now()
	m.mu.Lock()
	if m.troubleSince == nil || m.troubleAt == nil || now.Sub(*m.troubleAt) > troubleWindow {
		since := now
		m.troubleSince = &since
	}
	m.trouble = line
	m.troubleAt = &now
	m.troubles.note(now)
	m.mu.Unlock()
}

// degradedLocked reports how long the client has been reporting errors with
// nothing served since, or zero. The caller holds mu.
func (m *Manager) degradedLocked(now time.Time) time.Duration {
	if m.troubleSince == nil || m.troubleAt == nil {
		return 0
	}
	if now.Sub(*m.troubleAt) > troubleWindow {
		// The errors stopped on their own.
		return 0
	}
	if m.lastRequest != nil && m.lastRequest.After(*m.troubleSince) {
		return 0
	}
	return now.Sub(*m.troubleSince)
}

// Inherit carries the history that outlives a restart from the manager this
// one replaces: the hourly request counts and when the last request came,
// so a rebuild does not read as a connector that has never been used.
func (m *Manager) Inherit(from *Manager) {
	if from == nil || from == m {
		return
	}
	from.mu.RLock()
	history, troubles, last := from.history, from.troubles, from.lastRequest
	upstream, upstreamAt := from.upstream, from.upstreamAt
	from.mu.RUnlock()
	m.mu.Lock()
	m.history = history
	m.troubles = troubles
	m.lastRequest = last
	m.upstream, m.upstreamAt = upstream, upstreamAt
	m.mu.Unlock()
}

// SetUpstream records what OpenAI said about this tunnel: whether it still
// exists there. Called by the group's check, and read by Status.
func (m *Manager) SetUpstream(present bool, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if present {
		m.upstream = "present"
	} else {
		m.upstream = "missing"
	}
	m.upstreamAt = &at
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
//
// It also watches for a rejected credential. The client treats a 401 as
// retryable and backs off for ever, which leaves the dashboard reporting
// "starting" indefinitely for a tunnel that will never start -- and the one
// place the reason appears is a log line an operator has no reason to read.
type logWriter struct {
	log      *slog.Logger
	rejected func(string)
	// trouble is told every error line, so the manager can tell a tunnel
	// that is quietly failing from one that is quietly idle.
	trouble func(string)
}

func (w logWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if w.rejected != nil {
			if code := credentialRejection(line); code != "" {
				w.rejected(code)
			}
		}
		// Errors from the client matter more than its routine chatter, and an
		// operator scanning for a cause should not have to read past the rest.
		if strings.Contains(line, "level=ERROR") || strings.Contains(line, `"level":"ERROR"`) {
			w.log.Error("tunnel-client", "line", line)
			if w.trouble != nil {
				w.trouble(shorten(line, 300))
			}
			continue
		}
		if pollFailure(line) {
			// A poll that failed is the tunnel not being served, whatever
			// level the client filed it under.
			if w.trouble != nil {
				w.trouble(shorten(line, 300))
			}
			w.log.Warn("tunnel-client", "line", line)
			continue
		}
		w.log.Info("tunnel-client", "line", line)
	}
	return len(p), nil
}

// credentialRejection names the control plane's verdict on the API key, or
// returns "" for anything that might yet succeed on a retry.
//
// Only the two definitive codes count. A network blip or a 5xx is worth
// backing off for; a key the control plane says is wrong will still be wrong
// in ten minutes.
func credentialRejection(line string) string {
	for _, code := range []string{"invalid_api_key", "token_invalidated", "tunnel_use_forbidden"} {
		if strings.Contains(line, "error_code="+code) ||
			strings.Contains(line, `"error_code":"`+code+`"`) {
			return code
		}
	}
	return ""
}

// pollFailure reports whether a client line is its poll loop backing off.
//
// The client logs these at WARN, once per attempt, for as long as the
// control plane refuses it. They are the only running sign that a tunnel
// which once connected is no longer being served -- and, left alone, the one
// line that floods a log at one every ten seconds.
func pollFailure(line string) bool {
	return strings.Contains(line, "poll failed") &&
		(strings.Contains(line, "level=WARN") || strings.Contains(line, `"level":"WARN"`) ||
			strings.Contains(line, "level=ERROR") || strings.Contains(line, `"level":"ERROR"`))
}

// shorten cuts a log line to what a status can carry.
func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// diagnose names the likeliest cause of a failure to connect.
//
// The two credentials involved look alike and are obtained from adjacent pages,
// so the mistake is easy to make and nearly impossible to spot from a 401. An
// admin key is recognisable by its prefix, which turns a guess into a
// statement.
func diagnose(apiKey, code string) string {
	// One sentence of cause and one of remedy. These reach a phone as a
	// notification, where a paragraph is not read.
	if code == "tunnel_use_forbidden" {
		// The same code for a tunnel that is gone and one in another
		// organisation; only the upstream check can tell them apart, and
		// the Tunnels page shows its answer.
		return "OpenAI refused this account's key for this tunnel (tunnel_use_forbidden). " +
			"Either the tunnel no longer exists in OpenAI, or it belongs to another " +
			"organisation or workspace. The Tunnels page says which; remove it if it is gone"
	}
	if code == "token_invalidated" {
		return "OpenAI has invalidated this account's key (token_invalidated). " +
			"Paste a new runtime key into the account under Settings › ChatGPT"
	}
	if strings.HasPrefix(apiKey, "sk-admin-") {
		return "The account's key is an admin key (sk-admin-…), which cannot run a " +
			"tunnel. Paste a runtime key into the account under Settings › ChatGPT"
	}
	return "OpenAI rejected the key (invalid_api_key). Check the account's runtime " +
		"key under Settings › ChatGPT has Tunnels Read and Use"
}

// Config returns the configuration this tunnel is running with, so a caller
// rebuilding it does not have to resolve the settings again and risk starting
// the replacement from a different configuration than the one it replaced.
func (m *Manager) Config() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// SameAs reports whether a configuration would produce the same tunnel.
//
// Reconfiguring restarts, and restarting drops a connector until it
// reconnects, so a save that changed an unrelated setting must not disturb a
// working tunnel.
func (m *Manager) SameAs(cfg Config) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	current := m.cfg
	return current.Enabled == cfg.Enabled &&
		current.Plugin == cfg.Plugin &&
		current.TunnelID == cfg.TunnelID &&
		current.APIKey == cfg.APIKey &&
		current.ControlPlaneBaseURL == cfg.ControlPlaneBaseURL &&
		current.DiagnosticsAddr == cfg.DiagnosticsAddr &&
		current.Debug == cfg.Debug &&
		current.Principal.Equal(cfg.Principal)
}
