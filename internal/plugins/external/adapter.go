package external

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/spoked/mcpd/internal/auth"
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

	mu       sync.RWMutex
	proc     *Process
	describe DescribeResult
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
	if len(d.Tools) == 0 && len(d.Mutations) == 0 &&
		len(d.Resources) == 0 && len(d.Prompts) == 0 {
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
		// The plugin's own published schema is used verbatim. Parameters
		// arrive here as raw JSON, so an inferred schema would describe a byte
		// array rather than the object the plugin actually accepts.
		spec := plugins.ToolSpec{
			Name:        tool.Name,
			Title:       tool.Title,
			Description: tool.Description,
			Idempotent:  tool.Idempotent,
			InputSchema: normalizeSchema(tool.InputSchema),
			Capability:  auth.Capability(tool.Capability),
			RateLimit:   tool.RateLimit,
		}
		name := tool.Name
		// The result type is `any` rather than json.RawMessage on purpose: the
		// SDK generates no output schema for `any`, whereas a []byte would be
		// described as an array and then fail to validate against the object
		// the plugin actually returned.
		plugins.Tool(r, spec, func(ctx context.Context, args json.RawMessage) (any, error) {
			raw, err := p.callTool(ctx, name, args)
			if err != nil {
				return nil, err
			}
			return decodeResult(raw), nil
		})
	}

	for _, res := range describe.Resources {
		path := res.Path
		plugins.Resource(r, plugins.ResourceSpec{
			Path:        res.Path,
			Name:        res.Name,
			Title:       res.Title,
			Description: res.Description,
			MIMEType:    res.MIMEType,
			Capability:  auth.Capability(res.Capability),
		}, func(ctx context.Context) (string, error) {
			return p.readResource(ctx, path)
		})
	}

	for _, pr := range describe.Prompts {
		name := pr.Name
		args := make([]plugins.PromptArg, 0, len(pr.Args))
		for _, a := range pr.Args {
			args = append(args, plugins.PromptArg{
				Name: a.Name, Description: a.Description, Required: a.Required,
			})
		}
		plugins.Prompt(r, plugins.PromptSpec{
			Name:        pr.Name,
			Title:       pr.Title,
			Description: pr.Description,
			Args:        args,
			Capability:  auth.Capability(pr.Capability),
		}, func(ctx context.Context, supplied map[string]string) (string, error) {
			return p.getPrompt(ctx, name, supplied)
		})
	}

	for _, mutation := range describe.Mutations {
		plugins.Mutation(r, plugins.MutationSpec{
			Action:      mutation.Action,
			Title:       mutation.Title,
			Description: mutation.Description,
			Risk:        operations.RiskLevel(mutation.Risk),
			Reversible:  mutation.Reversible,
			Verifiable:  mutation.Verifiable,
			InputSchema: normalizeSchema(mutation.InputSchema),
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

	return planFrom(b.plugin.manifest.Name, result)
}

// planFrom translates a plugin's wire response into a plan.
//
// Split out from the call so it can be tested without a subprocess: what this
// does with a value it does not recognise is the interesting part, and it
// should not take a compiled binary to check it.
func planFrom(plugin string, result PlanResult) (plugins.Plan[json.RawMessage], error) {
	var zero plugins.Plan[json.RawMessage]

	changes := make([]operations.Change, len(result.Changes))
	for i, c := range result.Changes {
		changes[i] = operations.Change{Field: c.Field, From: c.From, To: c.To}
	}

	var preconditions map[string]any
	if len(result.Preconditions) > 0 {
		if err := json.Unmarshal(result.Preconditions, &preconditions); err != nil {
			return zero, fmt.Errorf("external: plugin %s returned unreadable preconditions: %w",
				plugin, err)
		}
	}

	plan := plugins.Plan[json.RawMessage]{
		Before:        result.Before,
		Desired:       result.Desired,
		Preconditions: preconditions,
		Changes:       changes,
		Impact:        result.Impact,
		Rollback:      result.Rollback,
		// The plugin's opaque state rides on the plan itself, which is the
		// value Apply is handed. It stays out of the operation payload, which
		// must remain exactly what was hashed.
		State: result.State,
	}

	// Whatever the plugin said survives, including a level this build does not
	// recognise.
	//
	// Dropping an unrecognised value was silently the most dangerous reading
	// available. A plugin returning "catastrophic" -- a typo, or a level a
	// newer plugin knows and this host does not -- lost the override
	// altogether, so the mutation went on looking like whatever it declared
	// statically. Under a low ceiling that auto-approves, and because the
	// executor re-plans through this same code the override was dropped a
	// second time, so the guard that refuses an auto-approved change whose
	// risk was raised never saw a raise to refuse.
	//
	// An unknown classification has to travel as unknown. MaxRisk ranks it
	// above every level this host defines, so it can only raise, and the
	// refusals downstream are then the ones that decide what happens to it.
	if result.RiskOverride != "" {
		risk := operations.RiskLevel(result.RiskOverride)
		plan.RiskOverride = &risk
	}
	return plan, nil
}

// Apply sends the write, carrying the state from the plan it was given.
//
// The plan is read from the argument rather than from a map on the plugin.
// That map was keyed on the action and the parameters, which two live
// proposals of the same change share: whichever applied first took the entry
// and the second was handed nothing, silently, with no way for the plugin to
// tell that its plan had been replaced by someone else's.
func (b *mutationBridge) Apply(ctx context.Context, params json.RawMessage, plan plugins.Plan[json.RawMessage]) (plugins.ApplyResult, error) {
	proc, err := b.plugin.process()
	if err != nil {
		return plugins.ApplyResult{}, err
	}

	state, err := planState(plan)
	if err != nil {
		return plugins.ApplyResult{}, err
	}

	var result ApplyResult
	err = proc.Call(ctx, MethodApply, MutationParams{
		Action: b.action,
		Params: params,
		Plan:   state,
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

// planState recovers the opaque state this bridge put on the plan.
//
// Absent state is ordinary: a plugin whose apply needs nothing from its plan
// returns none. State of an unexpected type is not, and is reported rather
// than dropped -- it means the plan came from somewhere other than this
// bridge, and a plugin handed nothing when it produced something would apply
// against the wrong snapshot with no way to tell.
func planState(plan plugins.Plan[json.RawMessage]) (json.RawMessage, error) {
	if plan.State == nil {
		return nil, nil
	}
	state, ok := plan.State.(json.RawMessage)
	if !ok {
		return nil, fmt.Errorf(
			"external: plan state is %T, not the raw JSON this bridge stored", plan.State)
	}
	return state, nil
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

// decodeResult turns a plugin's raw JSON result into a value the SDK can
// return as structured content. A result that will not decode is passed
// through as text rather than dropped, so a plugin bug is visible instead of
// silently producing an empty response.
func decodeResult(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]any{"raw": string(raw)}
	}
	return v
}

// normalizeSchema guarantees a usable object schema.
//
// The MCP SDK requires a tool's input schema to declare type "object" and
// refuses anything else. A plugin that publishes nothing, or something
// malformed, gets a permissive object rather than preventing its own mount --
// the plugin validates parameters itself regardless, so the schema exists to
// help a model construct a call rather than to enforce anything.
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	const permissive = `{"type":"object"}`

	if len(raw) == 0 {
		return json.RawMessage(permissive)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return json.RawMessage(permissive)
	}
	if parsed["type"] != "object" {
		return json.RawMessage(permissive)
	}
	return raw
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// Configure hands resolved settings to the plugin process.
//
// Best-effort by design: a plugin written before settings existed does not
// implement the method, and failing the handshake over that would break every
// plugin that was working yesterday.
func (p *Plugin) Configure(ctx context.Context, cfg map[string]string) {
	if len(cfg) == 0 {
		return
	}
	var ack map[string]bool
	if err := p.proc.Call(ctx, MethodConfigure, cfg, &ack); err != nil {
		p.deps.Log.Debug("plugin does not accept settings; continuing without them",
			"error", err)
	}
}

// SettingFields returns what this plugin says it needs configured.
func (p *Plugin) SettingFields() []SettingDescriptor {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.describe.Settings
}

// readResource fetches one resource's body from the plugin process.
func (p *Plugin) readResource(ctx context.Context, path string) (string, error) {
	var out ReadResourceResult
	if err := p.proc.Call(ctx, MethodReadResource, ReadResourceParams{Path: path}, &out); err != nil {
		return "", err
	}
	return out.Body, nil
}

// getPrompt renders one prompt in the plugin process.
func (p *Plugin) getPrompt(ctx context.Context, name string, args map[string]string) (string, error) {
	var out GetPromptResult
	if err := p.proc.Call(ctx, MethodGetPrompt, GetPromptParams{Name: name, Args: args}, &out); err != nil {
		return "", err
	}
	return out.Text, nil
}
