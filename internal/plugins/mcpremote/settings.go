// Package mcpremote mounts a remote MCP server as a plugin.
//
// It is a second implementation of the same contract an in-tree plugin
// satisfies, so the host needs no branch for it: a remote server's tools are
// scoped per plugin, gated by the same middleware, audited the same way, and
// rate limited the same way. What differs is trust, and that difference is
// carried by plugins.RuntimeMCP rather than by convention -- the registry
// refuses a mutation from this runtime, relaxes the tool-name rule to the
// specification's, and attaches tool by tool so one bad descriptor costs one
// tool.
//
// Two rules shape everything here. Register reads the SQLite snapshot and
// never the network, so boot does not depend on a third party being up. And
// nothing the server says is authority: its tool annotations are hints the
// specification itself says not to trust, and its input schemas are used as
// published or the tool is refused.
package mcpremote

import (
	"fmt"

	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/settings"
)

// KeyRequestsPerSecond bounds calls to one remote server, across all of its
// tools.
//
// The host's own rate limit is per tool, which is right when the expensive
// call is one endpoint of an integration this project ships. A remote MCP
// server is one upstream: thirty tools sharing one process behind one address,
// where a model working through a list can hammer something that never agreed
// to be hammered. So the budget is per server, and it is in addition to
// anything a tool declares rather than instead of it.
const KeyRequestsPerSecond = "requests_per_second"

// DefaultRequestsPerSecond is a deliberately conservative starting point. An
// operator who knows the far end can take more can say so.
const DefaultRequestsPerSecond = 5

// Fields returns the settings an operator fills in for one imported server.
//
// The mapping is the server.json input model onto the host's field model, and
// it is total: every input a document declares becomes a field, or the import
// was refused. A field this host added -- the request budget -- comes last, so
// the form reads as the document's questions followed by ours.
func Fields(doc *mcpservers.Document) ([]settings.Field, error) {
	inputs, err := doc.Inputs()
	if err != nil {
		return nil, err
	}

	out := make([]settings.Field, 0, len(inputs)+1)
	for _, in := range inputs {
		f, err := fieldFor(in)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}

	out = append(out, settings.Field{
		Key:   KeyRequestsPerSecond,
		Label: "Calls per second",
		Kind:  settings.KindInt,
		// Live rather than reconnect: the budget is read on the way into a
		// call, so there is nothing to rebuild.
		Apply:   settings.ApplyLive,
		Default: DefaultRequestsPerSecond,
		Min:     intPtr(0),
		Max:     intPtr(1000),
		Help: "Across every tool this server offers, because they are all one " +
			"system at the far end. Zero removes the limit.",
	})
	return out, nil
}

// fieldFor maps one server.json input onto one settings field.
func fieldFor(in mcpservers.ConfigInput) (settings.Field, error) {
	f := settings.Field{
		Key:         in.Key,
		Label:       label(in),
		Help:        in.Input.Description,
		Required:    in.Input.IsRequired,
		Placeholder: in.Input.Placeholder,
		// Everything a remote server is configured with is held by the client
		// that was built with it, so a change means building it again. The
		// host does that itself.
		Apply: settings.ApplyReconnect,
	}
	if in.Input.Default != "" {
		f.Default = in.Input.Default
	}

	switch {
	// Secret first, and unconditionally. A value the document marks secret is
	// encrypted at rest and withheld when read back, whatever else it looks
	// like -- a token that also declared choices is still a token.
	case in.Input.IsSecret:
		f.Kind = settings.KindSecret
		// A secret's default would be a credential in a form field, visible to
		// anyone who can open the page.
		f.Default = nil

	case len(in.Input.Choices) > 0:
		f.Kind = settings.KindEnum
		f.Options = append([]string(nil), in.Input.Choices...)
		if d, ok := f.Default.(string); ok && !contains(f.Options, d) {
			return settings.Field{}, fmt.Errorf(
				"mcpremote: %q defaults to %q, which is not one of its choices", in.Name, d)
		}

	default:
		switch in.Input.Format {
		case mcpservers.FormatNumber:
			f.Kind = settings.KindInt
			f.Default = nil
		case mcpservers.FormatBoolean:
			f.Kind = settings.KindBool
			f.Default = in.Input.Default == "true"
		default:
			f.Kind = settings.KindString
		}
	}

	if err := settings.ValidatePluginField(f); err != nil {
		return settings.Field{}, fmt.Errorf("mcpremote: input %q: %w", in.Name, err)
	}
	return f, nil
}

// label is what the operator reads beside the box.
//
// The input's own name, because that is the thing they will recognise from the
// server's documentation. The role is appended for a header so that a form
// asking for both a variable and a header of similar names is not ambiguous.
func label(in mcpservers.ConfigInput) string {
	if in.Role == mcpservers.RoleHeader {
		return in.Name + " header"
	}
	return in.Name
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func intPtr(i int) *int { return &i }
