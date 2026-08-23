package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/admin"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// instanceNamePattern is what a name may be. It matches the host's own plugin
// name rule, because an instance name becomes a URL path segment, a tool
// prefix, a database value, and an entry in a credential's plugin list.
var instanceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// instanceKeyPrefix namespaces the record of an instance's existence, as
// opposed to its settings.
const instanceKeyPrefix = "instances."

// Instance is one configured plugin, wherever it was configured.
type Instance struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Runtime says what is behind it: an integration this binary was built
	// with, or a remote MCP server. It is the discriminator every path that
	// has to treat the two differently branches on, rather than each of them
	// inferring it from something else.
	Runtime plugins.Runtime `json:"runtime"`
	// FromFile reports that this instance came from the configuration file
	// rather than the dashboard.
	FromFile bool `json:"from_file"`
	Enabled  bool `json:"enabled"`
	// Required is the file's `required` flag: a deployment saying the host
	// should not run without this integration. Only a file can say it, so it
	// is false for everything else.
	Required bool `json:"required"`
	// Removed reports that an administrator removed a file-defined instance
	// from the dashboard. The declaration is still in the file and the file is
	// untouched; what changed is that mcpd ignores it, now and on every
	// restart, until someone restores it.
	//
	// A removed instance stays in this list rather than vanishing from it.
	// Somebody who removes the wrong thing and then cannot find it to undo is
	// worse off than before they had the button.
	Removed   bool      `json:"removed,omitempty"`
	RemovedBy string    `json:"removed_by,omitempty"`
	RemovedAt time.Time `json:"removed_at,omitzero"`
}

// instanceRecord is what the store holds for an instance added in the
// dashboard. Its settings live separately, under their own keys, so removing
// an instance and re-adding it under the same name finds its old values --
// which is what someone correcting a typo in a type expects.
type instanceRecord struct {
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
}

// instances returns every configured instance, from the file and the store.
//
// The file is read first so a host provisioned by configuration management
// still works, and the store layers over it: an instance named in both takes
// the store's type and enabled state, because that is where someone last
// changed it.
func (a *App) instances(ctx context.Context) []Instance {
	byName := map[string]Instance{}

	for name, pc := range a.cfg.Plugins {
		byName[name] = Instance{
			Name:     name,
			Type:     pc.ResolvedType(name),
			Runtime:  plugins.RuntimeBuiltin,
			FromFile: true,
			Enabled:  pc.Enabled,
			Required: pc.Required,
		}
	}

	for key, raw := range a.settings.WithPrefix(ctx, instanceKeyPrefix) {
		name := strings.TrimPrefix(key, instanceKeyPrefix)
		if !instanceNamePattern.MatchString(name) {
			continue
		}
		var rec instanceRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			a.log.Warn("ignoring an unreadable plugin instance record",
				"instance", name, "error", err)
			continue
		}
		existing := byName[name]
		byName[name] = Instance{
			Name:     name,
			Type:     rec.Type,
			Runtime:  plugins.RuntimeBuiltin,
			FromFile: existing.FromFile,
			Enabled:  rec.Enabled,
			// Only the file can say this, so it survives the store's record of
			// the same name. Dropping it would let a `required: true` plugin
			// be removed without the acknowledgement that flag exists to
			// require, for no better reason than that someone had also
			// touched it in the dashboard.
			Required: existing.Required,
		}
	}

	// The store's overrides of the file's declarations, applied last of the
	// two file-backed layers, because they are what the dashboard said about
	// the file rather than about an instance of its own.
	//
	// Only a name the file declares can be overridden. A row for anything else
	// is inert -- it says the file's declaration is ignored, and there is no
	// declaration -- and is surfaced as a stale removal rather than acted on,
	// because silently applying one to a name the file later reuses is how a
	// plugin disappears with nothing saying why.
	for name, ov := range a.overrides() {
		existing, declared := byName[name]
		if !declared || !existing.FromFile {
			continue
		}
		if ov.Removed {
			existing.Removed = true
			existing.RemovedBy = ov.Actor
			existing.RemovedAt = time.UnixMilli(ov.UpdatedAt)
			// Not mounted, and not counted as configured by anything that asks
			// what to serve. Enabled is the one field every such caller reads,
			// so this is where the removal has to land.
			existing.Enabled = false
		} else if ov.Enabled != nil {
			existing.Enabled = *ov.Enabled
		}
		byName[name] = existing
	}

	// Remote MCP servers are their own source. They are not recorded under
	// instances. because the document that describes one is the record, and
	// keeping a second copy of "does this exist" is a second thing to keep in
	// step.
	//
	// Import refuses a name already taken, but that check happens once and the
	// other side can move afterwards: someone can add [plugins.weather] to the
	// configuration file after importing a remote server called weather. The
	// remote wins, because it is the one whose record is here rather than in a
	// file this host only reads. The collision is reported by shadowedNames, called
	// once at startup -- not from here, which is a read path the dashboard
	// hits on every request and which would turn one static misconfiguration
	// into a log line per page load.
	for _, name := range a.mcpServerNames() {
		srv, ok := a.mcpServer(name)
		if !ok {
			continue
		}
		byName[name] = Instance{
			Name:    name,
			Type:    mcpInstanceType,
			Runtime: plugins.RuntimeMCP,
			Enabled: srv.Enabled,
		}
	}

	out := make([]Instance, 0, len(byName))
	for _, inst := range byName {
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// enabledInstances returns what should be mounted.
func (a *App) enabledInstances(ctx context.Context) []Instance {
	var out []Instance
	for _, inst := range a.instances(ctx) {
		if inst.Enabled {
			out = append(out, inst)
		}
	}
	return out
}

// AddInstance records a new plugin instance.
//
// It does not mount it here: an instance arrives with nothing filled in, and a
// plugin with no credentials would fail every call. It mounts itself the
// moment its settings are complete, which is what the response says -- leaving
// somebody to wonder why the tools never appeared is the failure this wording
// exists to avoid.
func (a *App) AddInstance(ctx context.Context, actor, name, typeName string) error {
	if !instanceNamePattern.MatchString(name) {
		return fmt.Errorf("a plugin name must be lowercase letters, digits, "+
			"dashes or underscores, 2 to 32 characters (got %q)", name)
	}
	if _, known := a.types.Lookup(typeName); !known {
		return fmt.Errorf("%q is not an integration this build has", typeName)
	}
	for _, existing := range a.instances(ctx) {
		if existing.Name != name {
			continue
		}
		if existing.Removed {
			// The name is still taken: the file declares it, and the store
			// only says to ignore that declaration. Adding a second instance
			// under it would leave two answers to what the name means, one of
			// which returns the moment somebody restores the other.
			return fmt.Errorf("%q is declared in the configuration file and was "+
				"removed here; restore it rather than adding a second one under "+
				"the same name", name)
		}
		return fmt.Errorf("a plugin named %q already exists", name)
	}

	rec, err := json.Marshal(instanceRecord{Type: typeName, Enabled: true})
	if err != nil {
		return err
	}
	return a.settings.Apply(ctx, actor, []settings.Change{
		{Key: instanceKeyPrefix + name, Value: string(rec)},
	})
}

// RemoveInstance forgets an instance, or overrides the file's declaration of
// one.
//
// Which of the two it is depends on where the instance came from, and the
// difference is worth stating because the promises are different.
//
// An instance added in the dashboard is deleted outright, and its settings go
// with it: leaving them would mean a name reused later silently inheriting
// someone else's credentials.
//
// One declared in the configuration file cannot be deleted, because mcpd does
// not write that file -- it is mounted read-only in the container image, the
// root filesystem is read-only, the systemd unit is ProtectSystem=strict, and
// a deployment provisioned from configuration management would restore the
// entry on the next deploy anyway. So the declaration is overridden instead:
// a row records the removal, every read from now on ignores the file's entry
// for that name, and a restart changes nothing. Its settings are kept, because
// the removal is reversible and a restore that came back without the
// credentials somebody typed in would be a restore in name only.
func (a *App) RemoveInstance(ctx context.Context, actor, name string, acknowledgeRequired bool) error {
	var found *Instance
	for _, inst := range a.instances(ctx) {
		if inst.Name == name {
			found = &inst
			break
		}
	}
	if found == nil {
		return fmt.Errorf("no plugin named %q", name)
	}
	if found.Runtime == plugins.RuntimeMCP {
		// A remote server's record is its imported document, not an
		// instances. key. Writing one here would leave a second, contradictory
		// answer to "does this exist" -- and a stale one, because this path
		// cannot remove the document. Point at the endpoint that owns it.
		return fmt.Errorf("%q is a remote MCP server; remove it with "+
			"DELETE /api/mcp-servers/%s, which also takes its tool approvals "+
			"and its settings with it", name, name)
	}
	if found.FromFile {
		if found.Removed {
			return fmt.Errorf("%q is already removed; restore it if you want it back", name)
		}
		// `required: true` is the deployment saying the host should not run
		// without this integration, and a removal here means the next start
		// comes up without it and reports itself healthy. That is a decision
		// an operator is allowed to make -- the file may be declaring
		// something that no longer exists -- but not one to make by clicking
		// through a confirmation about something else, so it takes a second,
		// explicit yes.
		if found.Required && !acknowledgeRequired {
			return fmt.Errorf("%q is marked `required: true` in the configuration "+
				"file, which means this host is meant not to run without it. "+
				"Removing it here overrides that, and the next start comes up "+
				"without it. Confirm that you mean to", name)
		}
		pc := a.pluginConfigFor(name)
		if err := a.pluginOverrides.Remove(ctx, actor, name, sqlite.PluginDeclaration{
			Type:     pc.ResolvedType(name),
			Enabled:  pc.Enabled,
			Required: pc.Required,
		}); err != nil {
			return err
		}
		return a.overridesChanged(ctx, name)
	}

	changes := []settings.Change{{Key: instanceKeyPrefix + name, Delete: true}}
	if t, ok := a.types.Lookup(found.Type); ok {
		for _, f := range t.Settings {
			changes = append(changes, settings.Change{
				Key: settings.PluginSettingKey(name, f.Key), Delete: true,
			})
		}
	}
	return a.settings.Apply(ctx, actor, changes)
}

// RestoreInstance puts a removed file-defined instance back under the file's
// declaration.
//
// Under the file's, not under what the file said when it was removed: the
// override is forgotten entirely, so whatever config.yaml declares today is
// what comes back. Anything else would be this host holding a copy of an old
// declaration and serving from it.
func (a *App) RestoreInstance(ctx context.Context, actor, name string) error {
	if err := a.pluginOverrides.Restore(ctx, actor, name); err != nil {
		if errors.Is(err, sqlite.ErrNotRemoved) {
			return fmt.Errorf("%q is not removed", name)
		}
		return err
	}
	return a.overridesChanged(ctx, name)
}

// SetInstanceEnabled turns an instance on or off.
//
// A file-defined instance is switched through the same override the removal
// uses. `enabled: false` in a file nobody can edit is the same dead end one
// step smaller, and the store already beats the file everywhere else.
func (a *App) SetInstanceEnabled(ctx context.Context, actor, name string, enabled bool) error {
	for _, inst := range a.instances(ctx) {
		if inst.Name != name {
			continue
		}
		if inst.Runtime == plugins.RuntimeMCP {
			// Whether a remote server is on lives in mcp_servers.enabled. A
			// record written here would be shadowed by that on the next read,
			// so the toggle would report success and change nothing -- and it
			// would outlive the server, leaving an enabled instance of a type
			// no binary has, which is a host that will not start.
			return fmt.Errorf("%q is a remote MCP server; turn it on or off with "+
				"PATCH /api/mcp-servers/%s", name, name)
		}
		if inst.FromFile {
			if inst.Removed {
				return fmt.Errorf("%q is removed, so there is nothing to switch "+
					"on or off; restore it first", name)
			}
			pc := a.pluginConfigFor(name)
			if err := a.pluginOverrides.SetEnabled(ctx, actor, name, sqlite.PluginDeclaration{
				Type:     pc.ResolvedType(name),
				Enabled:  pc.Enabled,
				Required: pc.Required,
			}, enabled); err != nil {
				return err
			}
			return a.overridesChanged(ctx, name)
		}
		rec, err := json.Marshal(instanceRecord{Type: inst.Type, Enabled: enabled})
		if err != nil {
			return err
		}
		return a.settings.Apply(ctx, actor, []settings.Change{
			{Key: instanceKeyPrefix + name, Value: string(rec)},
		})
	}
	return fmt.Errorf("no plugin named %q", name)
}

// overrides returns the cached overrides, by name.
//
// Cached for the same reason the remote servers are: instances() is consulted
// on nearly every dashboard request, and the only code that writes an override
// refreshes this immediately afterwards.
func (a *App) overrides() map[string]sqlite.PluginOverride {
	a.overrideMu.RLock()
	defer a.overrideMu.RUnlock()
	return a.overrideCache
}

// loadOverrides fills the cache from the database.
func (a *App) loadOverrides(ctx context.Context) error {
	list, err := a.pluginOverrides.List(ctx)
	if err != nil {
		return err
	}
	byName := make(map[string]sqlite.PluginOverride, len(list))
	for _, o := range list {
		byName[o.Name] = o
	}
	a.overrideMu.Lock()
	a.overrideCache = byName
	a.overrideMu.Unlock()
	return nil
}

// overridesChanged refreshes the cache and brings the instance's mounted state
// in line with what the override now says.
//
// The reconcile is detached for the same reason the settings watcher's is: it
// outlives the request, and building a plugin may reach an upstream system
// slower than the operator's browser is willing to wait.
func (a *App) overridesChanged(ctx context.Context, name string) error {
	if err := a.loadOverrides(ctx); err != nil {
		return err
	}
	a.reconcileDetached(name)
	return nil
}

// staleRemovals returns removals that no longer match anything the file
// declares.
//
// They are kept rather than discarded. Discarding one would mean a host that
// started once with a truncated or missing configuration file forgetting every
// removal an operator had made, and resurrecting all of them on the next good
// deploy. So they persist, and are shown instead -- an operator who has since
// deleted the entry from their YAML can forget the removal deliberately, and
// one who has not is not left with a name that quietly refuses to come back.
func (a *App) staleRemovals(ctx context.Context) []sqlite.PluginOverride {
	live := map[string]bool{}
	for _, inst := range a.instances(ctx) {
		live[inst.Name] = true
	}
	var out []sqlite.PluginOverride
	for name, ov := range a.overrides() {
		if !ov.Removed {
			continue
		}
		// Still declared, so the override is doing its job and the instance's
		// own row says so. Or the name has been taken since by something else
		// -- an instance added in the dashboard, a remote server imported
		// under it -- in which case reporting a removal beside a plugin that
		// is working would describe a state nobody is in.
		if live[name] {
			continue
		}
		out = append(out, ov)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// overriddenNames reports file-declared plugins the store is overriding, once,
// at startup.
//
// The same reasoning as shadowedNames: a plugin the configuration file says is
// enabled and that this host is not serving is among the harder things to
// diagnose from outside, and the answer belongs in the log the operator
// already has open. A removed `required: true` plugin is named with its flag,
// because that one overrides an explicit statement that the host should not
// run without it.
func (a *App) overriddenNames() []string {
	var out []string
	for name, ov := range a.overrides() {
		pc, declared := a.cfg.Plugins[name]
		if !declared {
			continue
		}
		switch {
		case ov.Removed:
			out = append(out, name)
			a.log.Warn("a plugin declared in the configuration file was removed "+
				"from the dashboard, so it is not being served; the file is unchanged",
				"plugin", name, "removed_by", ov.Actor, "required", pc.Required)
		case ov.Enabled != nil && *ov.Enabled != pc.Enabled:
			out = append(out, name)
			a.log.Info("a plugin's enabled state was changed in the dashboard, "+
				"overriding the configuration file",
				"plugin", name, "file_says", pc.Enabled, "serving", *ov.Enabled)
		}
	}
	sort.Strings(out)
	return out
}

// shadowedNames reports configured plugins that a remote MCP server has taken
// the name of.
//
// A plugin silently answering as something other than what its configuration
// says is among the harder things to diagnose, so it is said out loud -- once,
// at startup, rather than on every read of the instance list.
func (a *App) shadowedNames() []string {
	var out []string
	for _, name := range a.mcpServerNames() {
		pc, inFile := a.cfg.Plugins[name]
		if inFile {
			out = append(out, name)
			a.log.Warn("a remote MCP server has the same name as a plugin in the "+
				"configuration file; the remote server is what will be served",
				"plugin", name, "shadowed_type", pc.ResolvedType(name))
		}
	}
	return out
}

// pluginConfigFor returns the file configuration for an instance, which is
// empty for one added in the dashboard.
func (a *App) pluginConfigFor(name string) config.PluginConfig {
	return a.cfg.Plugins[name]
}

// declarationFor renders the configuration file's entry for an instance, so
// the dashboard can show what the file says about something it is overriding.
//
// An operator who removes a file-declared plugin here has a second, slower
// question afterwards -- "what do I delete when I next touch the YAML" -- and
// this answers it without them having to find and read the file.
//
// Keys without values. This travels on a read-capability endpoint and a
// `settings:` block is where a credential often is.
func (a *App) declarationFor(name string) *admin.PluginDeclaration {
	pc, declared := a.cfg.Plugins[name]
	if !declared {
		return nil
	}
	keys := make([]string, 0, len(pc.Settings))
	for k := range pc.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return &admin.PluginDeclaration{
		Type:         pc.ResolvedType(name),
		Enabled:      pc.Enabled,
		Required:     pc.Required,
		SettingsKeys: keys,
	}
}
