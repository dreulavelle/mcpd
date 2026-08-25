package settings

import (
	"fmt"
	"regexp"
	"strings"
)

// Catalog is the set of settings a running host actually has.
//
// It exists because not all of them are known at compile time. The host's own
// settings are, but a plugin declares its own, and how many of those there are
// depends on how many instances someone has configured. A package-level lookup
// over a static schema could describe the first kind and not the second, which
// is why plugin credentials had to live in a file: nothing could validate,
// encrypt, or render a field the host had never heard of.
//
// A value rather than a global. Instances come and go while the host runs, and
// a mutable package variable would make "what settings exist" depend on
// initialisation order.
type Catalog struct {
	groups []Group
	byKey  map[string]Field
}

// NewCatalog returns the host's own settings plus any supplied by plugins.
func NewCatalog(extra ...Group) *Catalog {
	groups := append(schema(), extra...)
	byKey := make(map[string]Field)
	for _, g := range groups {
		for _, f := range g.Fields {
			byKey[f.Key] = f
		}
	}
	return &Catalog{groups: groups, byKey: byKey}
}

// Groups returns every group, host and plugin alike.
func (c *Catalog) Groups() []Group { return c.groups }

// FieldFor returns a field's declaration.
func (c *Catalog) FieldFor(key string) (Field, bool) {
	f, ok := c.byKey[key]
	return f, ok
}

// Validate checks a value against its declaration.
func (c *Catalog) Validate(key, value string) error {
	f, ok := c.FieldFor(key)
	if !ok {
		return fmt.Errorf("settings: %q is not an editable setting", key)
	}
	return validateAgainst(f, key, value)
}

// IsSecret reports whether a key holds a credential, which is what decides
// whether its value is encrypted at rest and withheld when read back.
func (c *Catalog) IsSecret(key string) bool {
	f, ok := c.FieldFor(key)
	return ok && f.Kind == KindSecret
}

// --- plugin settings -------------------------------------------------------

// pluginKeyPattern matches a key belonging to a plugin instance.
var pluginKeyPattern = regexp.MustCompile(`^plugins\.([a-z][a-z0-9_-]{1,31})\.([a-z][a-z0-9_]*)$`)

// PluginSettingKey builds the key holding one of an instance's settings.
//
// Namespaced by instance rather than by type, because two instances of one
// integration have different credentials and that is the entire point of
// having two.
func PluginSettingKey(instance, field string) string {
	return "plugins." + instance + "." + field
}

// PluginFromSettingKey reverses PluginSettingKey, returning "" for anything
// that is not a plugin setting.
func PluginFromSettingKey(key string) (instance, field string) {
	m := pluginKeyPattern.FindStringSubmatch(key)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

// PluginGroup builds the settings group for one plugin instance.
//
// The fields a plugin declares are keyed by bare name; this namespaces them so
// two instances cannot collide, and marks the group with the instance so the
// dashboard can put it beside the plugin it configures rather than in a
// general settings list.
func PluginGroup(instance, title string, fields []Field) Group {
	out := Group{
		Name:    "plugin:" + instance,
		Title:   title,
		Section: SectionPlugins,
		Fields:  make([]Field, 0, len(fields)),
	}
	for _, f := range fields {
		f.Key = PluginSettingKey(instance, f.Key)
		f.Group = out.Name
		// A visibility rule names the field it depends on, and that name is
		// namespaced here along with every other key -- so the rule has to be
		// namespaced too or it points at a key this form does not contain. The
		// dashboard then reads an empty value, matches nothing, and hides the
		// field permanently: an integration whose credentials cannot be
		// entered at all.
		//
		// Copied rather than rewritten in place. A plugin declares one rule
		// value and shares it across every field it gates, and the same
		// declaration is used again for a second instance -- so mutating the
		// original would rewrite it for all of them and namespace it twice.
		if f.ShowWhen != nil {
			w := *f.ShowWhen
			w.Field = PluginSettingKey(instance, w.Field)
			f.ShowWhen = &w
		}
		if f.Apply == "" {
			// A plugin holds whatever it was constructed with, so a change
			// means building it again -- which the host does on the spot.
			// Reconnect rather than live: the plugin really is replaced, and
			// calls in flight against the old one finish against it.
			f.Apply = ApplyReconnect
		}
		out.Fields = append(out.Fields, f)
	}
	return out
}

// ValidatePluginField checks a field a plugin declares, before it is namespaced.
//
// Plugins are compiled in, so this catches a developer's mistake rather than
// an operator's: a field with no key, or a key that would not survive being
// namespaced.
func ValidatePluginField(f Field) error {
	if !bareFieldPattern.MatchString(f.Key) {
		return fmt.Errorf("settings: plugin field key %q must match %s",
			f.Key, bareFieldPattern)
	}
	if strings.TrimSpace(f.Label) == "" {
		return fmt.Errorf("settings: plugin field %q needs a label", f.Key)
	}
	if !f.Kind.Valid() {
		return fmt.Errorf("settings: plugin field %q has unknown kind %q", f.Key, f.Kind)
	}
	return nil
}

var bareFieldPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
