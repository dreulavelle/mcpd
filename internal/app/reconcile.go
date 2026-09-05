package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// reconcileTimeout bounds one instance's rebuild. Generous, because Start may
// reach an upstream system, and bounded so a hung upstream cannot leave the
// reconcile goroutine alive for the life of the process.
const reconcileTimeout = 2 * time.Minute

// ready reports whether an instance has everything it needs to run.
//
// Derived from the schema the type already declares: every field marked
// Required must resolve to a value. That means the host can answer "is this
// configured" without asking the plugin, and a plugin gets the behaviour by
// saying which of its settings are required -- which it had to say anyway for
// the form to validate.
//
// An instance that is not ready is not mounted. It exists, it appears on the
// Plugins page with its form, and it serves nothing -- which is better than
// mounting something that will fail every call, and much better than refusing
// to start the host.
func (a *App) ready(ctx context.Context, inst Instance) (bool, []string) {
	fields, err := a.fieldsFor(ctx, inst)
	if err != nil {
		return false, []string{err.Error()}
	}

	cfg, err := a.resolveFields(ctx, inst.Name, fields)
	if err != nil {
		return false, []string{err.Error()}
	}

	var missing []string
	for _, f := range fields {
		if !f.Required {
			continue
		}
		if v, ok := cfg[f.Key]; !ok || isEmpty(v) {
			missing = append(missing, f.Label)
		}
	}
	if inst.Runtime == plugins.RuntimeMCP && len(missing) == 0 {
		// A remote server with nothing enabled has no tools to mount, and the
		// registry refuses a plugin that registers nothing. Reported here as a
		// thing still to do rather than as a build failure, because it is: the
		// next step is to discover and classify.
		tools, err := a.mcpStore.EnabledTools(ctx, inst.Name)
		if err != nil {
			return false, []string{err.Error()}
		}
		if len(tools) == 0 {
			missing = append(missing, "at least one tool enabled")
		}
	}
	return len(missing) == 0, missing
}

// fieldsFor returns the settings an instance asks for, whichever runtime it is.
func (a *App) fieldsFor(ctx context.Context, inst Instance) ([]settings.Field, error) {
	if inst.Runtime == plugins.RuntimeMCP {
		srv, ok := a.mcpServer(inst.Name)
		if !ok {
			return nil, fmt.Errorf("no remote MCP server named %q", inst.Name)
		}
		return a.mcpFields(srv)
	}
	t, ok := a.types.Lookup(inst.Type)
	if !ok {
		return nil, fmt.Errorf("unknown integration %s", inst.Type)
	}
	return t.Settings, nil
}

func isEmpty(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) == ""
	case []map[string]any:
		// A required table with no rows is a setting nobody has filled in.
		return len(t) == 0
	case []any:
		return len(t) == 0
	}
	return false
}

// problemUnknownType is what a person reads when an instance names a plugin
// type this binary was not built with.
//
// One constant because two paths reach the same field: a rebuild through
// reconcile, and the startup wiring that finds a stale instance record and
// keeps the host up rather than refusing to start. Both are stored as
// PluginInstance.problem and printed on their own by the Plugins list and by
// a plugin's own page, so it is a whole sentence carrying no package prefix --
// and two copies of it drift.
const problemUnknownType = "This plugin's type is not in this build of mcpd, " +
	"so it cannot start. Remove it, or run a build that includes it."

// noteReconcile records why an instance is not serving, or clears the note
// when it is. Kept beside the instance rather than on the plugin because the
// case that matters is the one where there is no plugin to hang it on.
func (a *App) noteReconcile(name string, err error) {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	if err == nil {
		delete(a.lastReconcileErr, name)
		return
	}
	if a.lastReconcileErr == nil {
		a.lastReconcileErr = map[string]string{}
	}
	a.lastReconcileErr[name] = err.Error()
}

// reconcileProblem returns the recorded reason, if there is one.
func (a *App) reconcileProblem(name string) string {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	return a.lastReconcileErr[name]
}

// buildInstance constructs one instance from its current settings.
func (a *App) buildInstance(ctx context.Context, inst Instance) (plugins.Plugin, plugins.Type, error) {
	if inst.Runtime == plugins.RuntimeMCP {
		p, err := a.buildMCPInstance(ctx, inst.Name)
		return p, plugins.Type{}, err
	}
	t, ok := a.types.Lookup(inst.Type)
	if !ok {
		return nil, t, errors.New(problemUnknownType)
	}
	cfg, err := a.resolveInstanceSettings(ctx, inst.Name, t)
	if err != nil {
		return nil, t, err
	}
	p, err := t.New(a.pluginDeps(inst.Name), cfg)
	if err != nil {
		// The plugin wrote the inner text, so it is quoted rather than run in.
		return nil, t, fmt.Errorf("This plugin could not start. It said: %q", err.Error())
	}
	return p, t, nil
}

// buildMCPInstance constructs a remote server from its snapshot.
//
// The tools come from SQLite and nothing else. That is what makes a host whose
// upstream is unreachable come up serving the tools it served yesterday,
// unhealthy and honest about it, rather than serving nothing.
func (a *App) buildMCPInstance(ctx context.Context, name string) (plugins.Plugin, error) {
	srv, ok := a.mcpServer(name)
	if !ok {
		return nil, fmt.Errorf("This plugin names a remote MCP server, %q, that is not on this host.", name)
	}
	tools, err := a.mcpStore.EnabledTools(ctx, name)
	if err != nil {
		return nil, err
	}
	return a.buildMCPPlugin(ctx, srv, tools)
}

// reconcileInstance brings one instance's mounted state in line with its
// configuration, without restarting the host.
//
// A plugin holds whatever it was constructed with -- a client, a token, an
// address -- so a settings change means building it again rather than telling
// it to reread anything. That is the whole reason this exists: an operator who
// pastes a credential should see the integration start working, not a note
// asking them to restart.
//
// Four cases, and each is the obvious one:
//
//   - configured and enabled: mount it, or replace what is mounted
//   - not configured, or disabled: unmount it if it is running
//   - a build that fails: leave whatever is mounted alone and report
//   - not mounted and not ready: nothing to do
func (a *App) reconcileInstance(ctx context.Context, name string) (err error) {
	// Whatever the outcome, it is what the Plugins page will show. Recorded
	// here so every return path is covered, including the ones that succeed
	// and must clear a previous failure.
	defer func() { a.noteReconcile(name, err) }()

	var inst *Instance
	for _, candidate := range a.instances(ctx) {
		if candidate.Name == name {
			inst = &candidate
			break
		}
	}

	mounted := a.manager.Lookup(name) != nil

	// Gone from configuration entirely.
	if inst == nil {
		if mounted {
			return a.manager.Unmount(ctx, name)
		}
		return nil
	}

	ready, missing := a.ready(ctx, *inst)
	if !inst.Enabled || !ready {
		if mounted {
			why := "switched off"
			if len(missing) > 0 {
				why = "missing " + strings.Join(missing, ", ")
			}
			a.log.InfoContext(ctx, "unmounting a plugin that is no longer serving",
				"plugin", name, "reason", why)
			return a.manager.Unmount(ctx, name)
		}
		return nil
	}

	p, _, err := a.buildInstance(ctx, *inst)
	if err != nil {
		return err
	}
	if err := a.manager.Remount(ctx, name, p, a.pluginConfigFor(name).Required); err != nil {
		return err
	}
	// After the remount, never before: a tunnel rebuilt first would capture
	// the instance it was already serving. A tunnel builds its own MCP server
	// and that server holds the plugin it was built from, so a plugin replaced
	// underneath one leaves it serving the old instance -- and an operator who
	// has just replaced a revoked credential sees the dashboard work while the
	// connector reports that the credential was rejected.
	a.rebuildTunnelFor(ctx, name)
	return nil
}

// rebuildTunnelFor restarts the tunnel bound to one plugin, if there is one.
//
// Failure is logged rather than returned: the remount itself succeeded, and
// reporting the whole reconcile as failed would suggest the settings did not
// take when they did. The tunnel records its own status, which is where an
// operator looks for it.
func (a *App) rebuildTunnelFor(ctx context.Context, name string) {
	if a.tunnels == nil || a.tunnelFactory == nil {
		return
	}
	if err := a.tunnels.Rebuild(ctx, name, a.tunnelFactory); err != nil {
		a.log.WarnContext(ctx, "the tunnel for this plugin did not restart after "+
			"its settings changed, so a connector may still be using the "+
			"previous credential", "plugin", name, "error", err)
	}
}

// watchPluginSettings remounts an instance when its configuration changes.
//
// Without this the settings form writes to a store nothing reads until the
// next restart, which is the same failure the tunnel had: a form that reports
// success and changes nothing.
func (a *App) watchPluginSettings() {
	a.settings.Watch(func(changed []string) {
		touched := map[string]bool{}
		for _, key := range changed {
			if instance, _ := settings.PluginFromSettingKey(key); instance != "" {
				touched[instance] = true
				continue
			}
			if name := strings.TrimPrefix(key, instanceKeyPrefix); name != key {
				touched[name] = true
			}
		}
		if len(touched) == 0 {
			return
		}
		names := make([]string, 0, len(touched))
		for name := range touched {
			names = append(names, name)
		}
		a.reconcileDetached(names...)
	})
}

// reconcileDetached brings instances in line with their configuration, out of
// band.
//
// Detached from the write that triggered it: the reconcile outlives the
// request, and a plugin's Start may reach an upstream system that is slower
// than the operator's browser is willing to wait. Every caller is a write that
// has already been recorded, so a reconcile that fails costs a plugin marked
// unhealthy rather than a change that half happened.
func (a *App) reconcileDetached(names ...string) {
	if len(names) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), reconcileTimeout)
		defer cancel()
		for _, name := range names {
			if err := a.reconcileInstance(ctx, name); err != nil {
				// Reported on the plugin's own health rather than thrown
				// away, since the operator who just saved is looking at it.
				a.log.Warn("plugin did not take up its new configuration",
					"plugin", name, "error", err)
				a.manager.SetHealth(name, plugins.Unhealthy(err.Error()))
			}
		}
	}()
}
