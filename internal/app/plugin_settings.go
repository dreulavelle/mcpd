package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// resolveInstanceSettings assembles what one plugin instance is configured
// with, from the three places a value can come from.
//
// In order of precedence:
//
//  1. The settings store, which is what the dashboard writes. Secrets are
//     decrypted here and handed over as values.
//  2. The configuration file's `settings:` block. A secret may be given as
//     <field>_ref, a reference resolved the way every other credential in the
//     file is, so a file still never holds one.
//  3. The field's declared default.
//
// The store winning matters: a value changed in the dashboard has to take
// precedence over the one the host started with, or the form writes to
// somewhere nothing reads. The file remains useful for provisioning a host
// that has never been opened.
//
// A plugin receives resolved values and never a reference. Where a credential
// came from is the host's problem, and asking every plugin to understand
// env:, file:, and credential: would be asking each of them to get it right.
func (a *App) resolveInstanceSettings(ctx context.Context, instance string, t plugins.Type) (map[string]any, error) {
	fileSettings := a.cfg.Plugins[instance].Settings
	resolver := config.NewSecretResolver()
	out := make(map[string]any, len(t.Settings))

	for _, f := range t.Settings {
		key := settings.PluginSettingKey(instance, f.Key)

		if f.Kind == settings.KindSecret {
			if v := a.settings.Secret(ctx, key, ""); v != "" {
				out[f.Key] = v
				continue
			}
			// A file names a reference rather than carrying the value, so
			// that a config file committed to a repository leaks nothing.
			if ref, ok := stringFrom(fileSettings, f.Key+"_ref"); ok {
				v, err := resolver.Resolve(ref)
				if err != nil {
					return nil, fmt.Errorf("app: plugin %q setting %q: %w", instance, f.Key, err)
				}
				out[f.Key] = v
				continue
			}
			// A literal in the file works and is not recommended. Accepting it
			// avoids a confusing failure for someone who has not met the
			// reference syntax yet.
			if v, ok := stringFrom(fileSettings, f.Key); ok {
				out[f.Key] = v
			}
			continue
		}

		if raw, ok, err := a.settings.Get(ctx, key); err == nil && ok && raw != "" {
			out[f.Key] = decodeSetting(f.Kind, raw)
			continue
		}
		if v, ok := fileSettings[f.Key]; ok {
			out[f.Key] = v
			continue
		}
		if f.Default != nil {
			out[f.Key] = f.Default
		}
	}

	// Anything the file carries that the type did not declare is passed
	// through untouched. A plugin may accept more than it exposes for editing,
	// and silently dropping it would be worse than not offering a field.
	for k, v := range fileSettings {
		if _, taken := out[k]; taken || strings.HasSuffix(k, "_ref") {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// decodeSetting turns a stored value back into the type its field declares.
//
// Values are stored as JSON, so a string arrives quoted and a number arrives
// as a number. Decoding by declared kind rather than by inspection keeps a
// port that happens to be written "443" from reaching a plugin as a string.
func decodeSetting(kind settings.Kind, raw string) any {
	trimmed := strings.TrimSpace(raw)
	switch kind {
	case settings.KindBool:
		return trimmed == "true" || trimmed == `"true"`
	case settings.KindInt, settings.KindDuration:
		var n int
		if _, err := fmt.Sscanf(strings.Trim(trimmed, `"`), "%d", &n); err == nil {
			return n
		}
		return trimmed
	case settings.KindList:
		var list []string
		if err := unmarshalJSON(trimmed, &list); err == nil {
			return list
		}
		return splitList(trimmed)
	default:
		var s string
		if err := unmarshalJSON(trimmed, &s); err == nil {
			return s
		}
		return trimmed
	}
}

// unmarshalJSON is a named wrapper so the decode sites read as intent rather
// than as plumbing.
func unmarshalJSON(raw string, into any) error {
	return json.Unmarshal([]byte(raw), into)
}

func stringFrom(m map[string]any, key string) (string, bool) {
	raw, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", false
	}
	return s, true
}

func splitList(raw string) []string {
	parts := strings.Split(strings.Trim(raw, `"`), ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// settingsCatalog is every setting this host has: its own, plus one group per
// configured plugin instance.
//
// Built per call rather than cached, because the answer changes when an
// instance is added or removed and a stale catalog would refuse to validate a
// field the dashboard had just drawn.
func (a *App) settingsCatalog() *settings.Catalog {
	var groups []settings.Group
	for _, inst := range a.instances(context.Background()) {
		g, ok := a.types.SettingsFor(inst.Name, inst.Type)
		if !ok {
			continue
		}
		groups = append(groups, g)
	}
	// Stable order, so the settings page does not reshuffle between requests.
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	return settings.NewCatalog(groups...)
}
