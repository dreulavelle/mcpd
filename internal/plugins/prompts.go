package plugins

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/spoked/mcpd/internal/auth"
)

var promptNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}$`)

// PromptArg is one argument a prompt takes.
type PromptArg struct {
	Name        string
	Description string
	Required    bool
}

// PromptSpec declares a named piece of work a plugin knows how to set up.
//
// A prompt is the integration saying "here is how to ask me something useful",
// which is a different thing from a tool. Diagnosing an access point is a
// sequence of reads and a way of reading them; the reads are tools, and the
// sequence is knowledge that otherwise lives only in whoever wrote the plugin.
//
// It is offered rather than invoked: a client lists prompts and a person picks
// one. Nothing here runs on its own.
type PromptSpec struct {
	Name        string
	Title       string
	Description string
	Args        []PromptArg
	// Capability is what a caller must hold. Empty means read -- a prompt
	// returns text and performs nothing, so it is a read even when what it
	// suggests would not be.
	Capability auth.Capability
}

func (s PromptSpec) validate(plugin string) error {
	if !promptNamePattern.MatchString(s.Name) {
		return fmt.Errorf("plugins: %s prompt name %q must match %s",
			plugin, s.Name, promptNamePattern)
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("plugins: %s prompt %q requires a description", plugin, s.Name)
	}
	seen := map[string]bool{}
	for _, a := range s.Args {
		if strings.TrimSpace(a.Name) == "" {
			return fmt.Errorf("plugins: %s prompt %q has an unnamed argument", plugin, s.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("plugins: %s prompt %q declares argument %q twice",
				plugin, s.Name, a.Name)
		}
		seen[a.Name] = true
	}
	if s.Capability != "" && !s.Capability.Valid() {
		return fmt.Errorf("plugins: %s prompt %q has unknown capability %q",
			plugin, s.Name, s.Capability)
	}
	return nil
}

// Prompt registers a named prompt.
//
// The handler receives the arguments a client supplied and returns the text to
// put in front of the model. Returning text rather than performing anything is
// the whole contract: a prompt that acted would be a tool wearing a name that
// hides it from every check tools go through.
func Prompt(r *Registry, spec PromptSpec, fn func(context.Context, map[string]string) (string, error)) {
	if err := spec.validate(r.descriptor.Name); err != nil {
		r.errs = append(r.errs, err)
		return
	}
	qualified := r.descriptor.Name + "_" + spec.Name
	if r.hasPrompt(qualified) {
		r.errs = append(r.errs, fmt.Errorf(
			"plugins: %s registers prompt %q twice", r.descriptor.Name, spec.Name))
		return
	}

	capability := spec.Capability
	if capability == "" {
		capability = auth.CapRead
	}

	args := make([]*mcp.PromptArgument, 0, len(spec.Args))
	for _, a := range spec.Args {
		args = append(args, &mcp.PromptArgument{
			Name: a.Name, Description: a.Description, Required: a.Required,
		})
	}

	r.prompts = append(r.prompts, registeredPrompt{
		spec:       spec,
		qualified:  qualified,
		capability: capability,
		attach: func(s *mcp.Server, mw ToolMiddleware) {
			s.AddPrompt(&mcp.Prompt{
				Name:        qualified,
				Title:       spec.Title,
				Description: spec.Description,
				Arguments:   args,
			}, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				// Same gate as everything else. A prompt names an integration
				// and describes its shape, which is exactly what per-plugin
				// scoping exists to withhold from a caller not granted it.
				if err := mw(ctx, qualified, capability); err != nil {
					return nil, err
				}
				var supplied map[string]string
				if req != nil && req.Params != nil {
					supplied = req.Params.Arguments
				}
				for _, a := range spec.Args {
					if a.Required && strings.TrimSpace(supplied[a.Name]) == "" {
						return nil, fmt.Errorf("%s is required", a.Name)
					}
				}
				text, err := fn(ctx, supplied)
				if err != nil {
					return nil, err
				}
				return &mcp.GetPromptResult{
					Description: spec.Description,
					Messages: []*mcp.PromptMessage{{
						Role:    "user",
						Content: &mcp.TextContent{Text: text},
					}},
				}, nil
			})
		},
	})
}

// registeredPrompt is one prompt and how to attach it.
type registeredPrompt struct {
	spec       PromptSpec
	qualified  string
	capability auth.Capability
	attach     func(*mcp.Server, ToolMiddleware)
}

func (r *Registry) hasPrompt(qualified string) bool {
	for _, existing := range r.prompts {
		if existing.qualified == qualified {
			return true
		}
	}
	return false
}

// Prompts returns what was registered, for the dashboard.
func (r *Registry) Prompts() []registeredPrompt { return r.prompts }
