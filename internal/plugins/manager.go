package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
)

// Mounted is a registered, started plugin together with the MCP server that
// serves it.
type Mounted struct {
	Descriptor Descriptor
	Server     *mcp.Server
	Registry   *Registry
	// Required reports whether a failure in this plugin should fail host
	// startup, as opposed to marking only this plugin unhealthy.
	Required bool

	plugin Plugin
	mu     sync.RWMutex
	health Health
}

// Health returns the plugin's most recent health report.
func (m *Mounted) Health() Health {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.health
}

func (m *Mounted) setHealth(h Health) {
	m.mu.Lock()
	m.health = h
	m.mu.Unlock()
}

// Manager owns the lifecycle of every registered plugin.
type Manager struct {
	log        *slog.Logger
	middleware ToolMiddleware
	version    string

	approvals ApprovalService
	inline    InlinePolicy
	observer  ToolObserver

	mu      sync.RWMutex
	mounted map[string]*Mounted
	// purposeOf reads what an instance covers, in the operator's words. Nil
	// until the host supplies it, which is what a test and an early startup
	// both look like.
	purposeOf func(instance string) string

	aggMu      sync.RWMutex
	aggregates map[string]*mcp.Server
	order      []string // registration order, for reverse-order shutdown

	// visibility decides which tools a caller is shown. Nil shows every
	// tool, which is what a host wired without an authorizer looks like.
	visibility ToolVisibility
}

// ToolVisibility reports whether the caller in ctx could invoke a tool that
// declares a capability. It answers the same question the gate does, without
// the gate's logging: a listing asks it once per tool per call, and a refusal
// there is not an event.
type ToolVisibility func(ctx context.Context, tool string, required auth.Capability) bool

// SetToolVisibility installs the listing filter. Servers built afterwards
// carry it; see FilterTools.
func (m *Manager) SetToolVisibility(v ToolVisibility) { m.visibility = v }

// FilterTools makes a server list only the tools its caller could invoke.
//
// Every tool of a reachable plugin used to be listed, and a propose tool was
// refused only when called -- so a read-only credential was shown tools it
// would be refused for, and a model choosing among them could not know. The
// filter reads the same declarations the gate does and hides what the gate
// would refuse.
//
// Applied as receiving middleware, which the SDK runs outermost-first in the
// order added. The tunnel attaches its principal the same way after building
// its server, so it calls this itself, afterwards; a server reached over HTTP
// has its principal in the request context before the SDK sees it, so the
// per-plugin and aggregate servers apply it as they are built.
func (m *Manager) FilterTools(srv *mcp.Server, caps map[string]auth.Capability) {
	if m.visibility == nil || srv == nil {
		return
	}
	visible := m.visibility
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if err != nil || method != "tools/list" {
				return res, err
			}
			listed, ok := res.(*mcp.ListToolsResult)
			if !ok {
				return res, err
			}
			kept := make([]*mcp.Tool, 0, len(listed.Tools))
			for _, t := range listed.Tools {
				required, known := caps[t.Name]
				if !known {
					required = auth.CapRead
				}
				if visible(ctx, t.Name, required) {
					kept = append(kept, t)
				}
			}
			listed.Tools = kept
			return listed, nil
		}
	})
}

// Capabilities returns what every tool of the named plugins takes to call,
// for FilterTools. Unknown names contribute nothing.
func (m *Manager) Capabilities(names []string) map[string]auth.Capability {
	out := map[string]auth.Capability{}
	for _, name := range names {
		mounted := m.Lookup(name)
		if mounted == nil || mounted.Registry == nil {
			continue
		}
		for tool, c := range mounted.Registry.Capabilities() {
			out[tool] = c
		}
	}
	return out
}

// NewManager returns an empty manager. middleware is the host gate applied to
// every tool call; version identifies the host in MCP handshakes.
func NewManager(log *slog.Logger, version string, middleware ToolMiddleware, approvals ApprovalService, inline InlinePolicy, observer ToolObserver) *Manager {
	if middleware == nil {
		// A nil gate would mean unauthenticated tool access, so refuse to
		// operate rather than defaulting to permissive.
		middleware = func(context.Context, string, auth.Capability) error {
			return errors.New("plugins: no authorization middleware configured")
		}
	}
	if observer == nil {
		observer = noObserver{}
	}
	return &Manager{
		log:        log,
		version:    version,
		middleware: middleware,
		approvals:  approvals,
		inline:     inline,
		observer:   observer,
		mounted:    make(map[string]*Mounted),
	}
}

// SetPurposeSource tells the manager where to read an instance's purpose.
//
// A function rather than a value handed in at registration: the purpose is a
// setting, it changes while the host runs, and reading it at each build is
// what makes a change take effect on the remount that follows it. Unset -- in
// a test, or a host wired without settings -- every instance has none, which
// is the same as an operator not having written one.
func (m *Manager) SetPurposeSource(fn func(instance string) string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purposeOf = fn
}

// purpose reads one instance's purpose, or "" when nothing can answer.
func (m *Manager) purpose(instance string) string {
	m.mu.RLock()
	fn := m.purposeOf
	m.mu.RUnlock()
	if fn == nil {
		return ""
	}
	return strings.TrimSpace(fn(instance))
}

// Register mounts a plugin: validates its descriptor, collects its tools and
// mutations, and builds the MCP server that will serve its endpoint.
//
// Registration is explicit and happens at startup. Nothing is discovered by
// scanning, and no init() side effects are involved, so the set of plugins a
// binary can serve is exactly the set named in the composition root.
func (m *Manager) Register(ctx context.Context, p Plugin, instance string, required bool) error {
	d := p.Descriptor()

	// The configured name wins over the one the plugin declares. A plugin
	// knows what it is; only the host knows which of it this is, and the name
	// is what the endpoint, the tool prefix, and every operation record are
	// keyed on. Overriding centrally means a plugin author cannot get it
	// wrong by returning a constant.
	if instance != "" {
		d.Name = instance
	}
	if err := d.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	if _, dup := m.mounted[d.Name]; dup {
		m.mu.Unlock()
		return fmt.Errorf("plugins: %q is registered twice", d.Name)
	}
	m.mu.Unlock()

	mounted, err := m.build(ctx, d, p, required)
	if err != nil {
		return err
	}
	reg := mounted.Registry

	m.mu.Lock()
	m.mounted[d.Name] = mounted
	m.order = append(m.order, d.Name)
	m.mu.Unlock()

	m.invalidateAggregates()

	m.log.InfoContext(ctx, "plugin registered",
		"plugin", d.Name,
		"version", d.Version,
		"endpoint", d.Endpoint(),
		"tools", len(reg.tools),
		"mutations", len(reg.mutations),
		"required", required)
	return nil
}

// build turns a plugin into something mountable, without mounting it.
//
// Shared by Register and Remount so the two cannot drift: a plugin rebuilt
// while the host runs has to be assembled exactly as one built at startup, or
// remounting becomes a second code path with its own bugs.
func (m *Manager) build(ctx context.Context, d Descriptor, p Plugin, required bool) (*Mounted, error) {
	// Read here rather than passed in, because build is the one place Register
	// and Remount share: an instance whose purpose was just edited is rebuilt
	// through the second, and anything that stamped it only in the first would
	// leave the edit invisible until a restart.
	d.Purpose = m.purpose(d.Name)

	reg := newRegistry(d)
	if err := p.Register(ctx, reg); err != nil {
		return nil, fmt.Errorf("plugins: %s registration failed: %w", d.Name, err)
	}
	if err := reg.err(); err != nil {
		return nil, err
	}
	if len(reg.tools) == 0 && len(reg.mutations) == 0 &&
		len(reg.resources) == 0 && len(reg.prompts) == 0 {
		return nil, fmt.Errorf("plugins: %s registered nothing", d.Name)
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    d.Name,
		Title:   d.Title,
		Version: d.Version,
	}, &mcp.ServerOptions{
		Instructions: instructionsFor(d),
		Logger:       m.log.With("plugin", d.Name),
	})
	// The SDK panics on a malformed tool definition rather than returning an
	// error. A plugin -- especially an out-of-process one the operator dropped
	// in -- must not be able to take the host down that way, so registration
	// is recovered and reported as a failed mount.
	if err := attachAll(srv, reg, m.middleware, m.approvals, m.inline, m.observer, m.log); err != nil {
		return nil, fmt.Errorf("plugins: %s: %w", d.Name, err)
	}
	m.FilterTools(srv, reg.Capabilities())

	return &Mounted{
		Descriptor: d,
		Server:     srv,
		Registry:   reg,
		Required:   required,
		plugin:     p,
		health:     Health{State: HealthyState, CheckedAt: time.Now()},
	}, nil
}

// Remount replaces a running plugin with a freshly built one.
//
// This is what makes a settings change take effect without restarting the
// host. A plugin holds whatever it was constructed with -- a client, a token,
// an address -- so changing its configuration means building it again rather
// than telling it to reread anything.
//
// The new plugin is built and started before the old one is touched. A build
// that fails, or a start that fails, leaves the old one serving: a mistyped
// credential should not cost an integration that was working a moment ago.
//
// Requests already in flight hold the old server and finish against it. The
// old plugin is shut down after the swap, so a call that is mid-flight when an
// operator saves a change can still fail -- narrow, and the alternative is
// leaving the old one's connections open indefinitely.
func (m *Manager) Remount(ctx context.Context, instance string, p Plugin, required bool) error {
	m.mu.RLock()
	existing := m.mounted[instance]
	m.mu.RUnlock()

	d := p.Descriptor()
	d.Name = instance
	if err := d.Validate(); err != nil {
		return err
	}

	rebuilt, err := m.build(ctx, d, p, required)
	if err != nil {
		return err
	}
	if starter, ok := p.(Starter); ok {
		if err := starter.Start(ctx); err != nil {
			// The old one is still mounted and still serving. Reporting the
			// failure is the whole point: an operator who has just saved a
			// wrong credential needs to know it was not taken up.
			// No instance name in the sentence: every caller has it as a
			// field or a key already, and the dashboard prints it in bold
			// immediately before this text -- which read as "graylog graylog
			// did not start".
			return fmt.Errorf("did not start with the new settings: %w",
				Explain(ownName(instance, err)))
		}
	}

	m.mu.Lock()
	m.mounted[instance] = rebuilt
	if existing == nil {
		m.order = append(m.order, instance)
	}
	m.mu.Unlock()

	m.invalidateAggregates()

	if existing != nil {
		if stopper, ok := existing.plugin.(Stopper); ok {
			if err := stopper.Shutdown(ctx); err != nil {
				m.log.WarnContext(ctx, "previous plugin did not shut down cleanly after a remount",
					"plugin", instance, "error", err)
			}
		}
	}

	m.log.InfoContext(ctx, "plugin remounted",
		"plugin", instance,
		"tools", len(rebuilt.Registry.tools),
		"mutations", len(rebuilt.Registry.mutations))
	return nil
}

// Unmount stops a plugin and removes it.
func (m *Manager) Unmount(ctx context.Context, instance string) error {
	m.mu.Lock()
	existing := m.mounted[instance]
	if existing == nil {
		m.mu.Unlock()
		return fmt.Errorf("plugins: %q is not mounted", instance)
	}
	delete(m.mounted, instance)
	for i, name := range m.order {
		if name == instance {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	m.mu.Unlock()

	m.invalidateAggregates()

	if stopper, ok := existing.plugin.(Stopper); ok {
		if err := stopper.Shutdown(ctx); err != nil {
			m.log.WarnContext(ctx, "plugin did not shut down cleanly when unmounted",
				"plugin", instance, "error", err)
		}
	}
	m.log.InfoContext(ctx, "plugin unmounted", "plugin", instance)
	return nil
}

// Start brings every registered plugin up, in registration order.
//
// A required plugin that fails to start fails host startup. An optional one is
// marked unhealthy and the host continues, so an outage in a single upstream
// system does not take down unrelated integrations.
func (m *Manager) Start(ctx context.Context) error {
	for _, name := range m.names() {
		mp := m.Lookup(name)
		starter, ok := mp.plugin.(Starter)
		if !ok {
			continue
		}
		if err := starter.Start(ctx); err != nil {
			if mp.Required {
				return fmt.Errorf("plugins: required plugin %s failed to start: %w",
					name, Explain(ownName(name, err)))
			}
			// Explained here as well as on the remount path: the first
			// failure an operator meets is usually this one, at startup,
			// where "certificate signed by unknown authority" names no
			// address and no way to fix it.
			explained := Explain(ownName(name, err))
			mp.setHealth(Unhealthy(explained.Error()))
			m.log.ErrorContext(ctx, "optional plugin failed to start; continuing without it",
				"plugin", name, "error", explained)
			continue
		}
		// A start that returned nil means healthy, for a plugin whose Start
		// fails when its upstream will not have it. A remote MCP server's does
		// not -- being down is an operational state there and not a
		// misconfiguration, so Start records what it found and returns -- and
		// overwriting that would show a green dot for however long it takes
		// the first health check to come round, which is exactly the window an
		// operator watching startup is looking in.
		//
		// Asked rather than re-established: Check would dial the same
		// unreachable address a second time, serially, for every plugin, on a
		// context whose cancellation the dial does not take. HealthReporter
		// returns what Start already learned.
		if reporter, ok := mp.plugin.(HealthReporter); ok {
			mp.setHealth(reporter.Health())
			continue
		}
		mp.setHealth(Healthy())
	}
	return nil
}

// Shutdown stops every plugin in reverse registration order, so a plugin that
// depends on one registered before it is stopped first.
func (m *Manager) Shutdown(ctx context.Context) error {
	names := m.names()
	var errs []error
	for i := len(names) - 1; i >= 0; i-- {
		mp := m.Lookup(names[i])
		stopper, ok := mp.plugin.(Stopper)
		if !ok {
			continue
		}
		if err := stopper.Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("plugins: %s shutdown: %w", names[i], err))
		}
	}
	return errors.Join(errs...)
}

// CheckHealth refreshes health for every plugin implementing Checker.
func (m *Manager) CheckHealth(ctx context.Context) map[string]Health {
	out := make(map[string]Health)
	for _, name := range m.names() {
		mp := m.Lookup(name)
		if checker, ok := mp.plugin.(Checker); ok {
			mp.setHealth(checker.Check(ctx))
		}
		out[name] = mp.Health()
	}
	return out
}

// AggregateServer returns one MCP server exposing every named plugin.
//
// It exists so a single endpoint can serve everything a caller is granted,
// rather than requiring one connection per integration. Tool names already
// carry their plugin prefix, so combining them cannot collide.
//
// Servers are cached by the exact set of plugins, because building one
// re-registers every tool and a request must not pay that. The set is the
// cache key rather than the principal: two credentials granted the same
// plugins get the same server, and a credential whose grants change gets a
// different one.
func (m *Manager) AggregateServer(names []string) (*mcp.Server, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("plugins: no plugins to aggregate")
	}
	key := aggregateKey(names)

	m.aggMu.RLock()
	cached := m.aggregates[key]
	m.aggMu.RUnlock()
	if cached != nil {
		return cached, nil
	}

	m.aggMu.Lock()
	defer m.aggMu.Unlock()
	// Re-check: another request may have built it while we waited.
	if cached := m.aggregates[key]; cached != nil {
		return cached, nil
	}

	srv, err := m.BuildServer(names)
	if err != nil {
		return nil, err
	}
	m.FilterTools(srv, m.Capabilities(names))

	if m.aggregates == nil {
		m.aggregates = make(map[string]*mcp.Server)
	}
	m.aggregates[key] = srv
	m.log.Info("built aggregate endpoint", "plugins", names)
	return srv, nil
}

// BuildServer returns a server that is nobody else's.
//
// AggregateServer caches by plugin set, which is right when identity arrives
// with each request. It is wrong when the identity is attached to the server
// itself: a caller that wraps the result in middleware carrying a principal
// would be writing that principal into an instance other callers share, and
// adding another layer every time it reconnected. The first identity attached
// would then answer for everyone, and changing it would appear to do nothing.
//
// A tunnel is exactly that caller, so it gets its own.
func (m *Manager) BuildServer(names []string) (*mcp.Server, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("plugins: no plugins to serve")
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "mcpd",
		Title:   "mcpd",
		Version: m.version,
	}, &mcp.ServerOptions{
		Instructions: m.aggregateInstructions(names),
		Logger:       m.log.With("endpoint", "aggregate"),
	})

	for _, name := range names {
		mounted := m.Lookup(name)
		if mounted == nil {
			continue
		}
		if err := attachAll(srv, mounted.Registry, m.middleware, m.approvals, m.inline, m.observer, m.log); err != nil {
			return nil, fmt.Errorf("plugins: aggregate %s: %w", name, err)
		}
	}
	return srv, nil
}

// aggregateInstructions tells a model what it is looking at, since the tool
// list spans several systems rather than one.
func (m *Manager) aggregateInstructions(names []string) string {
	var b strings.Builder
	b.WriteString("This endpoint manages several systems. Tool names are prefixed " +
		"with the system they belong to." + "\n\n")
	for _, name := range names {
		mounted := m.Lookup(name)
		if mounted == nil {
			continue
		}
		// The purpose comes first where there is one. Two instances of one
		// integration have identical descriptions, so the line that tells them
		// apart is the only line worth reading twice.
		if purpose := mounted.Descriptor.Purpose; purpose != "" {
			fmt.Fprintf(&b, "- %s: %s. %s\n", name, purpose, mounted.Descriptor.Description)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", name, mounted.Descriptor.Description)
	}
	b.WriteString("\nChanges are never applied directly. A tool that changes " +
		"something records a proposal and returns an operation id; a person " +
		"must approve it before anything happens.")
	return b.String()
}

// aggregateKey builds a stable cache key from a plugin set.
func aggregateKey(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return strings.Join(sorted, "\x00")
}

// invalidateAggregates drops cached servers, so a plugin registered after one
// was built is not missing from it.
func (m *Manager) invalidateAggregates() {
	m.aggMu.Lock()
	m.aggregates = nil
	m.aggMu.Unlock()
}

// SetHealth records a plugin's state from outside its own Check.
//
// Used when the host, rather than the plugin, knows something is wrong: a
// rebuild that failed after a settings change leaves the old plugin serving,
// and the operator who just saved needs to see why their change was not taken
// up rather than a green dot.
func (m *Manager) SetHealth(instance string, h Health) {
	m.mu.RLock()
	mounted := m.mounted[instance]
	m.mu.RUnlock()
	if mounted == nil {
		return
	}
	mounted.setHealth(h)
}

// Lookup returns a mounted plugin, or nil.
func (m *Manager) Lookup(name string) *Mounted {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mounted[name]
}

// Names returns every registered plugin name, sorted.
func (m *Manager) Names() []string {
	names := m.names()
	sort.Strings(names)
	return names
}

// names returns registration order without sorting.
func (m *Manager) names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string(nil), m.order...)
}

// All returns every mounted plugin, in registration order.
func (m *Manager) All() []*Mounted {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Mounted, 0, len(m.order))
	for _, n := range m.order {
		out = append(out, m.mounted[n])
	}
	return out
}

// attachAll wires every tool and mutation onto an MCP server, converting a
// panic from the SDK into an error.
func attachAll(srv *mcp.Server, reg *Registry, mw ToolMiddleware, approvals ApprovalService, inline InlinePolicy, obs ToolObserver, log *slog.Logger) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("tool registration failed: %v", v)
		}
	}()

	// One recover around the whole loop is right for a plugin this project
	// ships: every tool in it came from the same source, and if one is
	// malformed the mount is what wants fixing. It is wrong for a remote MCP
	// server, where a single bad descriptor out of three hundred would take
	// the other two hundred and ninety-nine down with it -- and the operator
	// cannot fix the far end's catalogue. So that runtime attaches tool by
	// tool and loses only what it must.
	perTool := reg.descriptor.EffectiveRuntime() == RuntimeMCP

	if obs == nil {
		obs = noObserver{}
	}

	for _, t := range reg.tools {
		if !perTool {
			t.attach(srv, mw, obs)
			continue
		}
		if failure := attachOne(func() { t.attach(srv, mw, obs) }); failure != nil {
			log.Error("skipping a remote tool the MCP SDK refused",
				"plugin", reg.descriptor.Name, "tool", t.qualified, "error", failure)
		}
	}
	for _, res := range reg.resources {
		res.attach(srv, mw)
	}
	for _, pr := range reg.prompts {
		pr.attach(srv, mw)
	}

	// Mutations become propose tools, and any endpoint with a mutation also
	// gets the operation lifecycle tools. Without an approval service there is
	// nowhere for a proposal to go, so registering one would produce a tool
	// that cannot work.
	if len(reg.mutations) > 0 {
		if approvals == nil {
			return fmt.Errorf("registers mutations but no approval service is configured")
		}
		for _, mu := range reg.mutations {
			mu.attach(srv, mw, approvals, inline, obs)
		}
		attachApprovalTools(srv, reg.descriptor.Name, approvals, mw)
	}
	return nil
}

// attachOne runs one attachment, turning a panic from the SDK into an error
// the caller can log and step past.
func attachOne(fn func()) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("%v", v)
		}
	}()
	fn()
	return nil
}

// ownName drops a plugin's own name from the front of its error.
//
// A plugin prefixes its errors with what it is, and so does the host when it
// says which plugin failed. Together they produced "plugins: cnmaestro did not
// start with the new settings: cnmaestro: ..." -- a sentence that says the
// name twice and buries the part an operator needs. The original error is
// still wrapped, so errors.Is and errors.As are unaffected.
func ownName(instance string, err error) error {
	if err == nil {
		return nil
	}
	prefix := instance + ": "
	msg := err.Error()
	if !strings.HasPrefix(msg, prefix) {
		return err
	}
	return trimmedError{err: err, msg: strings.TrimPrefix(msg, prefix)}
}

// trimmedError reads as its trimmed message and unwraps to the original.
type trimmedError struct {
	err error
	msg string
}

func (e trimmedError) Error() string { return e.msg }
func (e trimmedError) Unwrap() error { return e.err }

// instructionsFor is what a client hands a model when it connects to one
// plugin's endpoint.
//
// The purpose leads. A description says what an integration is -- true of
// every instance of it, and the same words twice when there are two -- while
// the purpose says which one this is, which is the sentence that decides
// whether the right tools get called.
func instructionsFor(d Descriptor) string {
	purpose := strings.TrimSpace(d.Purpose)
	if purpose == "" {
		return d.Description
	}
	if d.Description == "" {
		return purpose + "."
	}
	return purpose + ". " + d.Description
}

// describeTool composes what a model reads in the tool list.
//
// Appended rather than prefixed, and kept to a phrase: this is paid once per
// tool entry on every conversation, and a plugin with fourteen tools would
// otherwise carry fourteen copies of a full sentence to say one thing.
//
// Nothing is added where no purpose is set, which is the ordinary case: an
// integration configured once is already unambiguous, and a line repeated
// across its tools to restate its own name is the sort of context that costs
// something and buys nothing.
func describeTool(purpose, description string) string {
	purpose = strings.TrimSpace(purpose)
	if purpose == "" {
		return description
	}
	if description == "" {
		return purpose + "."
	}
	return strings.TrimRight(description, " ") + " " + purpose + "."
}
