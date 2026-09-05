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
	// FromPluginsDir reports that this instance is a plugin found in the
	// plugins directory, mounted under its own name unless something says
	// otherwise. Like a file declaration, it is removed and switched through
	// an override rather than by deleting a record, because the directory is
	// still there on the next start.
	FromPluginsDir bool `json:"from_plugins_dir,omitempty"`
	Enabled        bool `json:"enabled"`
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
// dashboard. Its settings live separately, under their own keys, and a removal
// deletes both -- see RemoveInstance.
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

	// A plugin in the plugins directory is an instance of itself, so a binary
	// dropped in still mounts with nothing else configured. The file and the
	// store layer over it, so it can also be named there with settings, or
	// configured twice under other names.
	for name, m := range a.externalManifests {
		byName[name] = Instance{
			Name: name, Type: name, Runtime: plugins.RuntimeBuiltin,
			FromPluginsDir: true, Enabled: true, Required: m.Required,
		}
	}

	for name, pc := range a.cfg.Plugins {
		existing := byName[name]
		byName[name] = Instance{
			Name:           name,
			Type:           pc.ResolvedType(name),
			Runtime:        plugins.RuntimeBuiltin,
			FromFile:       true,
			FromPluginsDir: existing.FromPluginsDir,
			Enabled:        pc.Enabled,
			Required:       pc.Required || existing.Required,
		}
	}

	for key, raw := range a.settings.WithPrefix(ctx, instanceKeyPrefix) {
		name := strings.TrimPrefix(key, instanceKeyPrefix)
		if !instanceNamePattern.MatchString(name) {
			continue
		}
		var rec instanceRecord
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			a.log.WarnContext(ctx, "ignoring an unreadable plugin instance record",
				"instance", name, "error", err)
			continue
		}
		existing := byName[name]
		byName[name] = Instance{
			Name:           name,
			Type:           rec.Type,
			Runtime:        plugins.RuntimeBuiltin,
			FromFile:       existing.FromFile,
			FromPluginsDir: existing.FromPluginsDir,
			Enabled:        rec.Enabled,
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
		if !declared || !(existing.FromFile || existing.FromPluginsDir) {
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
			// which returns the moment somebody adds the other back.
			return fmt.Errorf("%q is listed in the configuration file and was "+
				"removed here. Add it back from that listing rather than adding "+
				"a second plugin under the same name", name)
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

// RemoveInstance takes a plugin off this host and forgets what it held.
//
// However it was defined, the removal keeps nothing this host stored: every
// setting under the instance's name and every row of its table settings goes,
// credentials included. Keeping them so that a later restore came back "as it
// was" meant a credential outliving the decision to stop using it, and a name
// reused later could silently inherit it.
//
// What the configuration file itself supplies under `settings:` is the
// exception, and it is not one this host can make: that file is not written
// here, so a value in it comes back with the plugin. Everything a person typed
// into the dashboard is gone. Anything saying otherwise to an operator is
// wrong in one direction or the other, so both halves are said.
//
// What differs by origin is the record, not the wipe. An instance added in the
// dashboard is deleted outright. One declared in the configuration file cannot
// be deleted, because mcpd does not write that file -- the systemd unit is
// ProtectSystem=strict, and a deployment provisioned from configuration
// management would put the entry back on the next deploy anyway. So the
// declaration is overridden instead: a row records the removal, every read
// from now on ignores the file's entry for that name, and a restart changes
// nothing. The file still lists it, so it can be added again from that
// declaration, carrying only what the file itself provides.
//
// Removing something already removed is not an error. It re-runs the wipe and
// returns, so a removal interrupted between its two halves is finished by
// doing it again rather than refused.
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
	if found.FromFile || found.FromPluginsDir {
		// `required: true` is the deployment saying the host should not run
		// without this integration, and a removal here means the next start
		// comes up without it and reports itself healthy. That is a decision
		// an operator is allowed to make -- the file may be declaring
		// something that no longer exists -- but not one to make by clicking
		// through a confirmation about something else, so it takes a second,
		// explicit yes. Not asked again of something already removed: that
		// call finishes a decision already taken.
		if found.Required && !found.Removed && !acknowledgeRequired {
			return fmt.Errorf("%q is marked `required: true` in the configuration "+
				"file, which means this host is meant not to run without it. "+
				"Removing it here overrides that, and the next start comes up "+
				"without it. Confirm that you mean to", name)
		}
		// The wipe first, the override second, and the order is the recovery
		// story. If the wipe fails nothing is removed and the operator can try
		// again; if the override fails the plugin is still listed with nothing
		// set up, which is visible and safe. The other order leaves a plugin
		// removed with its credentials still stored -- the state this change
		// exists to prevent -- and a retry with nothing to retry.
		//
		// The cost is that the plugin is briefly still mounted while its
		// settings go, so a reconcile may rebuild it against an empty
		// configuration and mark it unhealthy. The override lands immediately
		// after and unmounts it; every reconcile reads the instance fresh, so
		// they converge whichever order the detached ones run in.
		if err := a.forgetInstance(ctx, actor, name); err != nil {
			a.log.ErrorContext(ctx, "a plugin's settings could not be forgotten, so it was not removed",
				"plugin", name, "error", err)
			return err
		}
		if found.Removed {
			// A second removal of something already removed. The override is
			// there, so the wipe above was the whole of what was left -- which
			// is how a removal interrupted between the two halves is finished,
			// rather than by a refusal that leaves the settings in place.
			return nil
		}
		if err := a.pluginOverrides.Remove(ctx, actor, name, a.declaredAs(*found)); err != nil {
			return err
		}
		return a.overridesChanged(ctx, name)
	}

	// Its own record goes in the same write as its settings: two writes could
	// be interrupted between, leaving settings under a name with no instance
	// for the next plugin called that to inherit.
	return a.forgetInstance(ctx, actor, name,
		settings.Change{Key: instanceKeyPrefix + name, Delete: true})
}

// forgetInstance wipes what an instance held -- every setting stored under its
// name, and every row of its table settings -- along with any further changes
// the caller needs made in the same write.
//
// By namespace rather than by the fields the type declares today. A type this
// build does not have has no field list to walk, and a field renamed or
// dropped since a value was stored is in nobody's list -- either way an
// encrypted credential would be left behind under a name that can be used
// again, which is the whole of what a removal promises not to do.
//
// One helper for both origins, because a removal keeps nothing either way.
// What the configuration file itself supplies under `settings:` is not here to
// wipe: it is in a file mcpd does not write, and it comes back with the plugin.
func (a *App) forgetInstance(ctx context.Context, actor, name string, also ...settings.Change) error {
	changes := also
	for key := range a.settings.WithPrefix(ctx, settings.PluginSettingPrefix(name)) {
		changes = append(changes, settings.Change{Key: key, Delete: true})
	}
	if a.pluginRows != nil {
		if err := a.pluginRows.DeleteAll(ctx, name); err != nil {
			return err
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return a.settings.Apply(ctx, actor, changes)
}

// RestoreInstance adds a plugin the configuration file still declares back to
// this host, empty.
//
// The name is what the method and its endpoint have always been called, and
// callers depend on both, but what it does is an add: the removal forgot
// everything this host had stored for the plugin, so it comes back with only
// what the configuration file itself provides.
//
// It comes back under the file's declaration, not under what the file said
// when it was removed: the override is forgotten entirely, so whatever
// config.yaml declares today is what comes back. Anything else would be this
// host holding a copy of an old declaration and serving from it.
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
		if inst.FromFile || inst.FromPluginsDir {
			if inst.Removed {
				return fmt.Errorf("%q is removed, so there is nothing to switch "+
					"on or off; add it back first", name)
			}
			if err := a.pluginOverrides.SetEnabled(ctx, actor, name, a.declaredAs(inst), enabled); err != nil {
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

// declaredAs renders what a declaration outside the dashboard says about an
// instance, for the override that ignores or switches it: the file's entry
// when there is one, otherwise the plugins directory's, which declares a
// plugin of its own name, enabled, and required if its manifest says so.
func (a *App) declaredAs(inst Instance) sqlite.PluginDeclaration {
	if pc, declared := a.cfg.Plugins[inst.Name]; declared {
		return sqlite.PluginDeclaration{
			Type: pc.ResolvedType(inst.Name), Enabled: pc.Enabled, Required: pc.Required,
		}
	}
	return sqlite.PluginDeclaration{Type: inst.Type, Enabled: true, Required: inst.Required}
}
