package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/settings"
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
	// FromFile reports that this instance came from the configuration file
	// rather than the dashboard. The dashboard will not delete one, because
	// it would reappear on the next start and look like the delete failed.
	FromFile bool `json:"from_file"`
	Enabled  bool `json:"enabled"`
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
			FromFile: true,
			Enabled:  pc.Enabled,
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
			FromFile: existing.FromFile,
			Enabled:  rec.Enabled,
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
// It does not mount it. A plugin is built once, at startup, from the settings
// it had then, so the honest thing to report is that a restart is needed --
// rather than accepting the instance, showing it in the list, and leaving
// someone to wonder why its tools never appear.
func (a *App) AddInstance(ctx context.Context, actor, name, typeName string) error {
	if !instanceNamePattern.MatchString(name) {
		return fmt.Errorf("a plugin name must be lowercase letters, digits, "+
			"dashes or underscores, 2 to 32 characters (got %q)", name)
	}
	if _, known := a.types.Lookup(typeName); !known {
		return fmt.Errorf("%q is not an integration this build has", typeName)
	}
	for _, existing := range a.instances(ctx) {
		if existing.Name == name {
			return fmt.Errorf("a plugin named %q already exists", name)
		}
	}

	rec, err := json.Marshal(instanceRecord{Type: typeName, Enabled: true})
	if err != nil {
		return err
	}
	return a.settings.Apply(ctx, actor, []settings.Change{
		{Key: instanceKeyPrefix + name, Value: string(rec)},
	})
}

// RemoveInstance forgets an instance and the settings it held.
//
// The settings go with it. Leaving them would mean a name reused later
// silently inheriting someone else's credentials, which is a worse surprise
// than having to type them again.
func (a *App) RemoveInstance(ctx context.Context, actor, name string) error {
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
	if found.FromFile {
		return fmt.Errorf("%q is defined in the configuration file, so removing "+
			"it here would not stick -- it would return on the next start. "+
			"Remove it from the file instead", name)
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

// SetInstanceEnabled turns an instance on or off.
func (a *App) SetInstanceEnabled(ctx context.Context, actor, name string, enabled bool) error {
	for _, inst := range a.instances(ctx) {
		if inst.Name != name {
			continue
		}
		if inst.FromFile {
			return fmt.Errorf("%q is defined in the configuration file; change "+
				"`enabled` there", name)
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

// pluginConfigFor returns the file configuration for an instance, which is
// empty for one added in the dashboard.
func (a *App) pluginConfigFor(name string) config.PluginConfig {
	return a.cfg.Plugins[name]
}
