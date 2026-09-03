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
	// Guide is how somebody uses the integration, shown on its page under the
	// address. For the person about to ask their first question, not for a
	// developer: a few things worth asking, and the notes that save a wrong
	// first attempt.
	Guide Guide
	// New builds an instance from resolved settings.
	New func(deps Deps, cfg map[string]any) (Plugin, error)
}

// Guide is a type's getting-started notes for the people who will use it.
type Guide struct {
	// Questions are things worth asking an assistant connected to this
	// integration, written the way a person would ask them. Three or so.
	Questions []string
	// Notes are the facts that save a wrong first attempt: what to configure
	// first, what a name has to look like, what the integration will not do.
	Notes []string
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
	// Checked here rather than in ValidatePluginField because it is the only
	// place that sees the whole set: a reference is only meaningful against
	// the other fields beside it.
	if err := checkShowWhen(t.Name, t.Settings); err != nil {
		return err
	}
	return nil
}

// checkShowWhen refuses a visibility rule that cannot fire.
//
// Every failure here produces the same symptom -- a field nobody can see, and
// therefore a setting nobody can fill in -- which is invisible until an
// operator cannot configure the integration and has no way to tell why. It is
// a developer's mistake, so it is caught at startup.
func checkShowWhen(typeName string, fields []settings.Field) error {
	byKey := make(map[string]settings.Field, len(fields))
	for _, f := range fields {
		byKey[f.Key] = f
	}

	for _, f := range fields {
		w := f.ShowWhen
		if w == nil {
			continue
		}
		if w.Field == f.Key {
			return fmt.Errorf("plugins: type %s: setting %q is shown when its "+
				"own value matches, which it cannot be until it is visible",
				typeName, f.Key)
		}
		on, ok := byKey[w.Field]
		if !ok {
			return fmt.Errorf("plugins: type %s: setting %q is shown when %q "+
				"matches, and there is no setting %q", typeName, f.Key, w.Field, w.Field)
		}
		if len(w.Equals) == 0 {
			return fmt.Errorf("plugins: type %s: setting %q names no value of "+
				"%q that would reveal it", typeName, f.Key, w.Field)
		}
		// A controlling field that is itself conditional makes visibility
		// depend on a chain, and a chain is a thing to debug rather than read.
		if on.ShowWhen != nil {
			return fmt.Errorf("plugins: type %s: setting %q depends on %q, "+
				"which is itself conditional; keep the control field always visible",
				typeName, f.Key, w.Field)
		}
		// An enum is the only kind whose values are known in advance, so it is
		// the only kind where a typo can be caught rather than waited for.
		if on.Kind == settings.KindEnum {
			options := make(map[string]bool, len(on.Options))
			for _, o := range on.Options {
				options[o] = true
			}
			for _, want := range w.Equals {
				if !options[want] {
					return fmt.Errorf("plugins: type %s: setting %q is shown when "+
						"%q is %q, which is not one of its options %v",
						typeName, f.Key, w.Field, want, on.Options)
				}
			}
		}
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

// Add registers a type discovered after construction: an out-of-process
// plugin found in the plugins directory. Validated like the compiled-in ones,
// and refused if the name is taken, so a bind mount cannot shadow an
// integration the operator reviewed.
func (c *Catalog) Add(t Type) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if _, dup := c.byName[t.Name]; dup {
		return fmt.Errorf("plugins: type %q is declared twice", t.Name)
	}
	c.byName[t.Name] = t
	return nil
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
