package plugins

import (
	"fmt"
	"sort"

	"github.com/spoked/mcpd/internal/settings"
)

// Type is an integration this binary can serve, as opposed to a configured
// instance of one.
//
// The distinction is what lets settings be managed from the dashboard. A
// plugin's configuration cannot be described by the host's own schema — how
// many instances exist is a deployment decision, and what each needs is the
// integration's business — so a type declares its fields and the host builds
// the rest: validation, encryption at rest, and a form to fill in.
//
// New takes the resolved settings rather than reading them itself. Resolution
// is the host's job because it is the host that knows where a value came from:
// a dashboard field, a config file, or a field's own default.
type Type struct {
	// Name is the identifier used as `type` in configuration, and as the
	// instance name when someone configures only one.
	Name string
	// Title and Description address an operator choosing an integration, not
	// a developer reading code.
	Title       string
	Description string
	// Settings are the fields an instance needs, keyed bare. The host
	// namespaces them per instance.
	Settings []settings.Field
	// New builds an instance from resolved settings.
	New func(deps Deps, cfg map[string]any) (Plugin, error)
}

// Validate checks a type declaration.
//
// Types are compiled in, so everything here is a developer's mistake caught at
// startup rather than an operator's caught at runtime.
func (t Type) Validate() error {
	if !namePattern.MatchString(t.Name) {
		return fmt.Errorf("plugins: type name %q must match %s", t.Name, namePattern)
	}
	if t.Title == "" {
		return fmt.Errorf("plugins: type %s requires a title", t.Name)
	}
	if t.New == nil {
		return fmt.Errorf("plugins: type %s has no constructor", t.Name)
	}
	seen := map[string]bool{}
	for _, f := range t.Settings {
		if err := settings.ValidatePluginField(f); err != nil {
			return fmt.Errorf("plugins: type %s: %w", t.Name, err)
		}
		if seen[f.Key] {
			return fmt.Errorf("plugins: type %s declares setting %q twice", t.Name, f.Key)
		}
		seen[f.Key] = true
	}
	return nil
}

// Catalog is the set of integrations a binary was built with.
//
// Explicit rather than discovered: a type is added by naming it here, which
// keeps the set auditable and stops a plugin mounting itself through an init()
// side effect.
type Catalog struct {
	byName map[string]Type
}

// NewCatalog returns a catalog of the given types.
func NewCatalog(types ...Type) (*Catalog, error) {
	c := &Catalog{byName: make(map[string]Type, len(types))}
	for _, t := range types {
		if err := t.Validate(); err != nil {
			return nil, err
		}
		if _, dup := c.byName[t.Name]; dup {
			return nil, fmt.Errorf("plugins: type %q is declared twice", t.Name)
		}
		c.byName[t.Name] = t
	}
	return c, nil
}

// Lookup returns a type by name.
func (c *Catalog) Lookup(name string) (Type, bool) {
	t, ok := c.byName[name]
	return t, ok
}

// Types returns every type, in a stable order so a dashboard listing them does
// not reshuffle between requests.
func (c *Catalog) Types() []Type {
	out := make([]Type, 0, len(c.byName))
	for _, t := range c.byName {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SettingsFor returns the settings fields an instance of a type needs, already
// namespaced to that instance.
func (c *Catalog) SettingsFor(instance, typeName string) (settings.Group, bool) {
	t, ok := c.byName[typeName]
	if !ok || len(t.Settings) == 0 {
		return settings.Group{}, false
	}
	g := settings.PluginGroup(instance, t.Title, t.Settings)
	g.Help = t.Description
	return g, true
}
