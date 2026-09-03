package echoplugin

import (
	"encoding/json"
	"fmt"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// Type declares the integration.
//
// One setting, and it is not a connection detail. Echo still depends on
// nothing outside mcpd, and is still the one plugin that can be turned on
// before anything else is configured -- the field below defaults to off and
// changes nothing until somebody asks for it.
//
// It earns its place because what it gates is a tool whose purpose is to
// generate load. A model reading "benchmark" in a tool list has every reason
// to try it while checking that a connection works, and the answer to that is
// for the tool not to be there.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "echo",
		Title: "Echo",
		Description: "A test connection. One read tool and one harmless change " +
			"to practise approving. It touches nothing outside mcpd.",
		Guide: plugins.Guide{
			Questions: []string{
				"Echo back \"hello\" so I can see the connection works.",
				"What is the echo plugin's status?",
				"Propose setting the echo label to \"test\" so I can practise approving a change.",
			},
			Notes: []string{
				"A test integration. It reaches nothing outside mcpd.",
			},
		},
		Settings: []settings.Field{
			{
				Key:   "benchmarks_enabled",
				Label: "Enable the benchmark tool",
				Kind:  settings.KindBool,
				Help: "Adds a tool that returns a result of a size you choose. " +
					"It exists to measure this host rather than any upstream: " +
					"echo has no far end, so what a call costs is what mcpd " +
					"costs. Off by default — it is a way to generate load, and " +
					"an assistant checking that a connection works should not " +
					"find it.",
			},
		},
		New: func(deps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			var c Config
			if err := decode(cfg, &c); err != nil {
				return nil, fmt.Errorf("echo: %w", err)
			}
			return New(deps, c), nil
		},
	}
}

// Config is what echo can be told.
type Config struct {
	BenchmarksEnabled bool `json:"benchmarks_enabled"`
}

// decode turns resolved settings into a Config, through JSON so the struct
// tags are the only description of the mapping.
func decode(in map[string]any, out *Config) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	return nil
}
