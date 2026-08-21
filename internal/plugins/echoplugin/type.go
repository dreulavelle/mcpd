package echoplugin

import "github.com/spoked/mcpd/internal/plugins"

// Type declares the integration.
//
// No settings: echo exists to prove the host works end to end and deliberately
// depends on nothing outside it, which is also what makes it the one plugin
// that can be turned on before anything else is configured.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "echo",
		Title: "Echo",
		Description: "A test connection. One read tool and one harmless change " +
			"to practise approving. It touches nothing outside mcpd.",
		New: func(deps plugins.Deps, _ map[string]any) (plugins.Plugin, error) {
			return New(deps), nil
		},
	}
}
