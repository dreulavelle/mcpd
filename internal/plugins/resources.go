package plugins

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/spoked/mcpd/internal/auth"
)

// uriSchemePattern constrains the scheme a plugin may serve resources under.
var uriSchemePattern = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

// ResourceSpec declares something a model can read by address rather than by
// calling a tool.
//
// The distinction is worth keeping. A tool is an action a model chooses to
// take and reasons about taking; a resource is reference material it can pull
// in when relevant. Expressing a config dump or a topology as a resource keeps
// it out of the tool catalogue, where every entry costs the model attention on
// every call.
type ResourceSpec struct {
	// URI addresses the resource. The scheme is the plugin's own namespace,
	// bound by the host so one plugin cannot serve another's addresses.
	//
	// Given as a scheme-relative path -- "shares" becomes
	// "cnmaestro://shares" for an instance named cnmaestro.
	Path string
	// Name is the programmatic identifier; Title is what a person reads.
	Name  string
	Title string
	// Description tells a model what is here and when it is worth reading.
	Description string
	// MIMEType describes the content. Empty means text/plain.
	MIMEType string
	// Capability is what a caller must hold. Empty means read.
	Capability auth.Capability
}

func (s ResourceSpec) validate(plugin string) error {
	if strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("plugins: %s resource requires a path", plugin)
	}
	if strings.Contains(s.Path, "://") {
		return fmt.Errorf("plugins: %s resource path %q must not carry a scheme; "+
			"the host binds one", plugin, s.Path)
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("plugins: %s resource %q requires a name", plugin, s.Path)
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("plugins: %s resource %q requires a description; a model "+
			"reading a list of them has nothing else to go on", plugin, s.Path)
	}
	if s.Capability != "" && !s.Capability.Valid() {
		return fmt.Errorf("plugins: %s resource %q has unknown capability %q",
			plugin, s.Path, s.Capability)
	}
	return nil
}

// Resource registers something readable by address.
//
// The handler returns the content and its type. Everything else -- the scheme,
// authorization, the plugin's own rate limiting -- is the host's, for the same
// reason it is on tools: a plugin that cannot get those wrong is one fewer
// place they can be got wrong.
func Resource(r *Registry, spec ResourceSpec, fn func(context.Context) (string, error)) {
	if err := spec.validate(r.descriptor.Name); err != nil {
		r.errs = append(r.errs, err)
		return
	}
	scheme := r.descriptor.Name
	if !uriSchemePattern.MatchString(scheme) {
		r.errs = append(r.errs, fmt.Errorf(
			"plugins: %s cannot be a URI scheme", scheme))
		return
	}
	uri := scheme + "://" + strings.TrimPrefix(spec.Path, "/")
	if r.hasResource(uri) {
		r.errs = append(r.errs, fmt.Errorf(
			"plugins: %s registers resource %q twice", r.descriptor.Name, uri))
		return
	}

	capability := spec.Capability
	if capability == "" {
		capability = auth.CapRead
	}
	mime := spec.MIMEType
	if mime == "" {
		mime = "text/plain"
	}

	r.resources = append(r.resources, registeredResource{
		spec:       spec,
		uri:        uri,
		capability: capability,
		attach: func(s *mcp.Server, mw ToolMiddleware) {
			s.AddResource(&mcp.Resource{
				URI:         uri,
				Name:        spec.Name,
				Title:       spec.Title,
				Description: spec.Description,
				MIMEType:    mime,
			}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				// Reading a resource is reaching the same upstream a tool
				// would, so it passes the same gate. A resource that skipped
				// it would be a way around per-plugin scoping.
				if err := mw(ctx, uri, capability); err != nil {
					return nil, err
				}
				body, err := fn(ctx)
				if err != nil {
					return nil, err
				}
				return &mcp.ReadResourceResult{
					Contents: []*mcp.ResourceContents{{
						URI: uri, MIMEType: mime, Text: body,
					}},
				}, nil
			})
		},
	})
}

// registeredResource is one resource and how to attach it.
type registeredResource struct {
	spec       ResourceSpec
	uri        string
	capability auth.Capability
	attach     func(*mcp.Server, ToolMiddleware)
}

func (r *Registry) hasResource(uri string) bool {
	for _, existing := range r.resources {
		if existing.uri == uri {
			return true
		}
	}
	return false
}

// Resources returns what was registered, for the dashboard.
func (r *Registry) Resources() []registeredResource { return r.resources }
