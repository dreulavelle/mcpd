package external

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin adapts a subprocess to the host's plugin contract.
//
// It is a second implementation of the same interface an in-tree plugin
// satisfies, which is why the host needs no branch for external plugins: they
// are mounted, authorized, audited, and approval-gated identically.
type Plugin struct {
	manifest Manifest
	dir      string
	deps     plugins.Deps

	mu        sync.RWMutex
	proc      *Process
	describe  DescribeResult
	planState map[string]json.RawMessage
}

// NewPlugin builds an adapter. The subprocess starts in Start, not here, so a
// registration failure does not leave an orphaned process.
func NewPlugin(dir string, m Manifest, deps plugins.Deps) *Plugin {
	return &Plugin{manifest: m, dir: dir, deps: deps}
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return plugins.Descriptor{
		Name:        p.manifest.Name,
		Version:     orDefault(p.describe.Version, "0.0.0"),
		Title:       orDefault(p.describe.Title, p.manifest.Name),
		Description: p.describe.Description,
	}
}

// Handshake starts the subprocess and reads its self-description.
//
// It runs before Register because the host has to know what tools exist in
// order to mount them, and only the plugin knows.
func (p *Plugin) Handshake(ctx context.Context) error {
	env, err := p.resolveEnv()
	if err != nil {
		return err
	}

	proc, err := Spawn(ctx, p.dir, p.manifest, env, p.deps.Log)
	if err != nil {
		return err
	}

	var describe DescribeResult
	if err := proc.Call(ctx, MethodDescribe, nil, &describe); err != nil {
		proc.Kill()
		return fmt.Errorf("external: plugin %s failed to describe itself: %w", p.manifest.Name, err)
	}

	if err := validateDescribe(p.manifest.Name, describe); err != nil {
		proc.Kill()
		return err
	}

	p.mu.Lock()
	p.proc = proc
	p.describe = describe
	p.mu.Unlock()

	p.deps.Log.Info("external plugin handshake complete",
		"plugin", describe.Name, "version", describe.Version,
		"protocol", describe.Protocol,
		"tools", len(describe.Tools), "mutations", len(describe.Mutations))
	return nil
}

// validateDescribe checks a plugin's self-description before anything is
// mounted from it.
func validateDescribe(manifestName string, d DescribeResult) error {
	if d.Protocol != ProtocolVersion {
		// A plugin built against a different contract is more dangerous than
		// one that will not load: it may misread a mutation payload.
		return fmt.Errorf(
			"external: plugin %s speaks protocol %q but this host speaks %q",
			manifestName, d.Protocol, ProtocolVersion)
	}
	if d.Name != manifestName {
		// The directory and the binary disagree about which plugin this is,
		// which would make grants and audit records point at the wrong thing.
		return fmt.Errorf(
			"external: plugin in directory %q reports its name as %q; they must match",
			manifestName, d.Name)
	}
	if len(d.Tools) == 0 && len(d.Mutations) == 0 {
		return fmt.Errorf("external: plugin %s exposes no tools or mutations", manifestName)
	}
	for _, m := range d.Mutations {
		if !operations.RiskLevel(m.Risk).Valid() {
			return fmt.Errorf("external: plugin %s mutation %q declares invalid risk %q",
				manifestName, m.Action, m.Risk)
		}
	}
	return nil
}

// resolveEnv turns the manifest's secret references into values.
//
// References are resolved by the host, so a plugin never sees the reference
// syntax and cannot read a secret it was not granted.
func (p *Plugin) resolveEnv() (map[string]string, error) {
	out := make(map[string]string, len(p.manifest.Env))
	for key, ref := range p.manifest.Env {
		value, err := p.deps.Secrets.Secret(ref)
		if err != nil {
			// Not every env entry is a secret; a literal passes through.
			out[key] = ref
			continue
		}
		out[key] = value
	}
	return out, nil
}

// Register implements plugins.Plugin.
//
// Tools and mutations are declared with json.RawMessage parameters because
// their schemas are known only at run time. Validation happens in the plugin,
// which is the only place that knows what it accepts.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	p.mu.RLock()
	describe := p.describe
	p.mu.RUnlock()

	for _, tool := range describe.Tools {
		spec := plugins.ToolSpec{
			Name:        tool.Name,
			Title:       tool.Title,
			Description: tool.Description,
			Idempotent:  tool.Idempotent,
		}
		name := tool.Name
		plugins.Tool(r, spec, func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
			return p.callTool(ctx, name, args)
		})
	}

	for _, mutation := range describe.Mutations {
		plugins.Mutation(r, plugins.MutationSpec{
			Action:      mutation.Action,
			Title:       mutation.Title,
			Description: mutation.Description,
			Risk:        operations.RiskLevel(mutation.Risk),
			Reversible:  mutation.Reversible,
		}, &mutationBridge{plugin: p, action: mutation.Action})
	}
	return nil
}

func (p *Plugin) callTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	proc, err := p.process()
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err := proc.Call(ctx, MethodCallTool, CallToolParams{Name: name, Args: args}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (p *Plugin) process() (*Process, error) {
	p.mu.RLock()
	proc := p.proc
	p.mu.RUnlock()
	if proc == nil || !proc.Running() {
		return nil, fmt.Errorf("external: plugin %s is not running", p.manifest.Name)
	}
	return proc, nil
}

// Start implements plugins.Starter.
func (p *Plugin) Start(context.Context) error {
	_, err := p.process()
	return err
}

// Shutdown implements plugins.Stopper.
func (p *Plugin) Shutdown(ctx context.Context) error {
	p.mu.RLock()
	proc := p.proc
	p.mu.RUnlock()
	if proc == nil {
		return nil
	}
	return proc.Stop(ctx)
}

// Check implements plugins.Checker.
func (p *Plugin) Check(ctx context.Context) plugins.Health {
	proc, err := p.process()
	if err != nil {
		return plugins.Unhealthy("plugin process is not running")
	}
	var result HealthResult
	if err := proc.Call(ctx, MethodHealth, nil, &result); err != nil {
		return plugins.Degraded("plugin did not answer its health check")
	}
	switch plugins.HealthState(result.State) {
	case plugins.HealthyState:
		return plugins.Healthy()
	case plugins.DegradedState:
		return plugins.Degraded(result.Message)
	default:
		return plugins.Unhealthy(result.Message)
	}
}

// mutationBridge routes the three mutation phases to the subprocess.
//
// Parameters and state stay as raw JSON throughout. The host never needs to
// understand a plugin's types, and the plugin never has to express them in a
// form the host could misinterpret.
type mutationBridge struct {
	plugin *Plugin
	action string
}

func (b *mutationBridge) Plan(ctx context.Context, params json.RawMessage) (plugins.Plan[json.RawMessage], error) {
	var zero plugins.Plan[json.RawMessage]

	proc, err := b.plugin.process()
	if err != nil {
		return zero, err
	}

	var result PlanResult
	if err := proc.Call(ctx, MethodPlan,
		MutationParams{Action: b.action, Params: params}, &result); err != nil {
		return zero, err
	}

	changes := make([]operations.Change, len(result.Changes))
	for i, c := range result.Changes {
		changes[i] = operations.Change{Field: c.Field, From: c.From, To: c.To}
	}

	var preconditions map[string]any
	if len(result.Preconditions) > 0 {
		if err := json.Unmarshal(result.Preconditions, &preconditions); err != nil {
			return zero, fmt.Errorf("external: plugin %s returned unreadable preconditions: %w",
				b.plugin.manifest.Name, err)
		}
	}

	plan := plugins.Plan[json.RawMessage]{
		Before:        result.Before,
		Desired:       result.Desired,
		Preconditions: preconditions,
		Changes:       changes,
		Impact:        result.Impact,
		Rollback:      result.Rollback,
	}
	if risk := operations.RiskLevel(result.RiskOverride); risk.Valid() {
		plan.RiskOverride = &risk
	}
	// The plugin's opaque state travels back to apply through Rollback's
	// sibling field; storing it on the plan keeps it out of the operation
	// payload, which must stay exactly what was hashed.
	b.plugin.stashState(b.action, params, result.State)
	return plan, nil
}

func (b *mutationBridge) Apply(ctx context.Context, params json.RawMessage, _ plugins.Plan[json.RawMessage]) (plugins.ApplyResult, error) {
	proc, err := b.plugin.process()
	if err != nil {
		return plugins.ApplyResult{}, err
	}

	var result ApplyResult
	err = proc.Call(ctx, MethodApply, MutationParams{
		Action: b.action,
		Params: params,
		Plan:   b.plugin.takeState(b.action, params),
	}, &result)

	if err != nil {
		// A plugin reporting INDETERMINATE means it could not establish
		// whether the write landed. Translating that into the domain's
		// sentinel is what stops the executor retrying it.
		var protoErr *Error
		if errorsAs(err, &protoErr) && protoErr.Code == CodeIndeterminate {
			return plugins.ApplyResult{}, fmt.Errorf("%s: %w",
				protoErr.Message, operations.ErrIndeterminate)
		}
		return plugins.ApplyResult{}, err
	}
	return plugins.ApplyResult{UpstreamRef: result.UpstreamRef, Async: result.Async}, nil
}

func (b *mutationBridge) Observe(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
	proc, err := b.plugin.process()
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	if err := proc.Call(ctx, MethodObserve,
		MutationParams{Action: b.action, Params: params}, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// stashState retains a plugin's opaque plan state between plan and apply.
func (p *Plugin) stashState(action string, params, state json.RawMessage) {
	if len(state) == 0 {
		return
	}
	p.mu.Lock()
	if p.planState == nil {
		p.planState = make(map[string]json.RawMessage)
	}
	p.planState[action+"|"+string(params)] = state
	p.mu.Unlock()
}

// takeState retrieves and clears stashed state. A plan is valid for exactly
// one execution.
func (p *Plugin) takeState(action string, params json.RawMessage) json.RawMessage {
	key := action + "|" + string(params)
	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.planState[key]
	delete(p.planState, key)
	return state
}

// Discover reads every plugin manifest under root.
//
// A malformed or unreadable plugin is reported and skipped rather than failing
// discovery: one bad directory in a bind mount must not stop the others from
// loading.
func Discover(root string, log logger) ([]Manifest, map[string]string, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("external: read plugins directory %s: %w", root, err)
	}

	var manifests []Manifest
	dirs := make(map[string]string)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		path := filepath.Join(dir, "plugin.json")

		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			log.Warn("skipping plugin directory", "dir", entry.Name(), "error", err)
			continue
		}

		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			log.Warn("skipping plugin with an unreadable manifest",
				"dir", entry.Name(), "error", err)
			continue
		}
		if m.Name == "" {
			m.Name = entry.Name()
		}
		if err := m.Validate(); err != nil {
			log.Warn("skipping plugin with an invalid manifest",
				"dir", entry.Name(), "error", err)
			continue
		}
		if m.Name != entry.Name() {
			log.Warn("skipping plugin whose manifest name does not match its directory",
				"dir", entry.Name(), "name", m.Name)
			continue
		}

		manifests = append(manifests, m)
		dirs[m.Name] = dir
	}

	sort.Slice(manifests, func(i, j int) bool { return manifests[i].Name < manifests[j].Name })
	return manifests, dirs, nil
}

// logger is the slice of slog this package needs.
type logger interface {
	Warn(msg string, args ...any)
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
