package sdk

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// SettingField is one thing a plugin needs configured.
//
// Declaring it means the host renders the form, validates what is typed,
// encrypts it if it is a secret, and hands back a resolved value. A plugin
// never reads a file, an environment variable, or a credential reference: what
// it receives is already the value, whichever of those it came from.
type SettingField struct {
	// Key is bare -- "api_token", not "plugins.thing.api_token". The host
	// namespaces it per instance, which is what lets one integration be
	// configured twice with different credentials.
	Key   string
	Label string
	Help  string
	// Kind is one of: string, secret, bool, int, duration, enum, list.
	//
	// secret is the one that matters: it is stored encrypted, never sent back
	// to the dashboard, and withheld from anything that dumps configuration.
	Kind        string
	Default     any
	Options     []string
	Min         *int
	Max         *int
	Required    bool
	Placeholder string
}

// SettingKinds are the kinds a field may declare.
const (
	KindString   = "string"
	KindSecret   = "secret"
	KindBool     = "bool"
	KindInt      = "int"
	KindDuration = "duration"
	KindEnum     = "enum"
	KindList     = "list"
)

var settingKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (f SettingField) validate() error {
	if !settingKeyPattern.MatchString(f.Key) {
		return fmt.Errorf("sdk: setting key %q must match %s", f.Key, settingKeyPattern)
	}
	if strings.TrimSpace(f.Label) == "" {
		return fmt.Errorf("sdk: setting %q needs a label", f.Key)
	}
	switch f.Kind {
	case KindString, KindSecret, KindBool, KindInt, KindDuration, KindEnum, KindList:
	default:
		return fmt.Errorf("sdk: setting %q has unknown kind %q", f.Key, f.Kind)
	}
	if f.Kind == KindEnum && len(f.Options) == 0 {
		return fmt.Errorf("sdk: setting %q is an enum with no options", f.Key)
	}
	return nil
}

// Settings declares what this plugin needs configured.
//
// Call it once, before Serve. The host reads the declaration during the
// handshake and offers the fields on the plugin's own card.
func Settings(p *Plugin, fields ...SettingField) {
	for _, f := range fields {
		if err := f.validate(); err != nil {
			p.errs = append(p.errs, err)
			return
		}
	}
	p.mu.Lock()
	p.settings = append(p.settings, fields...)
	p.mu.Unlock()
}

// Configured returns the resolved value of one setting.
//
// Available once the host has handed over configuration, which happens before
// any tool is called. A field the operator left empty returns "" and false, so
// a plugin can tell "not set" from "set to nothing".
func (p *Plugin) Configured(key string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.config[key]
	return v, ok && v != ""
}

// --- resources -------------------------------------------------------------

// ResourceSpec declares something readable by address rather than by calling a
// tool.
//
// Worth keeping distinct. A tool is an action a model chooses and reasons
// about choosing; a resource is reference material it pulls in when relevant.
// Expressing a config dump or a topology as a resource keeps it out of the
// tool catalogue, where every entry costs the model attention on every call.
type ResourceSpec struct {
	// Path is scheme-relative: "shares" becomes "weather://shares" for a
	// plugin named weather. The host binds the scheme so one plugin cannot
	// serve another's addresses.
	Path        string
	Name        string
	Title       string
	Description string
	// MIMEType describes the content. Empty means text/plain.
	MIMEType string
	// Capability is what a caller must hold. Empty means read.
	Capability string
}

// Resource registers something readable by address.
func Resource(p *Plugin, spec ResourceSpec, fn func(context.Context) (string, error)) {
	if strings.TrimSpace(spec.Path) == "" {
		p.errs = append(p.errs, fmt.Errorf("sdk: resource requires a path"))
		return
	}
	if strings.Contains(spec.Path, "://") {
		p.errs = append(p.errs, fmt.Errorf(
			"sdk: resource path %q must not carry a scheme; the host binds one", spec.Path))
		return
	}
	if strings.TrimSpace(spec.Description) == "" {
		p.errs = append(p.errs, fmt.Errorf(
			"sdk: resource %q requires a description; a model reading a list of "+
				"them has nothing else to go on", spec.Path))
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, dup := p.resources[spec.Path]; dup {
		p.errs = append(p.errs, fmt.Errorf("sdk: resource %q registered twice", spec.Path))
		return
	}
	if p.resources == nil {
		p.resources = map[string]*registeredResource{}
	}
	p.resources[spec.Path] = &registeredResource{spec: spec, fn: fn}
	p.resOrder = append(p.resOrder, spec.Path)
}

type registeredResource struct {
	spec ResourceSpec
	fn   func(context.Context) (string, error)
}

// --- prompts ---------------------------------------------------------------

// PromptArg is one argument a prompt takes.
type PromptArg struct {
	Name        string
	Description string
	Required    bool
}

// PromptSpec declares a named piece of work the plugin knows how to set up.
//
// A prompt is the integration saying "here is how to ask me something useful".
// Diagnosing a device is a sequence of reads and a way of reading them; the
// reads are tools, and the sequence is knowledge that otherwise lives only in
// whoever wrote the plugin.
//
// It is offered, not invoked: a client lists prompts and a person picks one.
type PromptSpec struct {
	Name        string
	Title       string
	Description string
	Args        []PromptArg
	// Capability is what a caller must hold. Empty means read -- a prompt
	// returns text and performs nothing, so it is a read even when what it
	// suggests would not be.
	Capability string
}

var promptNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}$`)

// Prompt registers a named prompt.
//
// The handler returns text. Returning text rather than performing anything is
// the whole contract: a prompt that acted would be a tool wearing a name that
// hides it from every check tools go through.
func Prompt(p *Plugin, spec PromptSpec, fn func(context.Context, map[string]string) (string, error)) {
	if !promptNamePattern.MatchString(spec.Name) {
		p.errs = append(p.errs, fmt.Errorf(
			"sdk: prompt name %q must match %s", spec.Name, promptNamePattern))
		return
	}
	if strings.TrimSpace(spec.Description) == "" {
		p.errs = append(p.errs, fmt.Errorf("sdk: prompt %q requires a description", spec.Name))
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, dup := p.prompts[spec.Name]; dup {
		p.errs = append(p.errs, fmt.Errorf("sdk: prompt %q registered twice", spec.Name))
		return
	}
	if p.prompts == nil {
		p.prompts = map[string]*registeredPrompt{}
	}
	p.prompts[spec.Name] = &registeredPrompt{spec: spec, fn: fn}
	p.promptOrder = append(p.promptOrder, spec.Name)
}

type registeredPrompt struct {
	spec PromptSpec
	fn   func(context.Context, map[string]string) (string, error)
}
