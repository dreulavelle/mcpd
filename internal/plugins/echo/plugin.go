// Package echo is a minimal reference plugin.
//
// It exists to exercise the host end to end — registration, endpoint routing,
// per-plugin authorization, tool dispatch — without depending on an external
// system. It is the template a real integration follows, and it is what the
// host's integration tests run against.
package echo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Plugin is the echo integration.
type Plugin struct {
	deps  plugins.Deps
	start time.Time
}

// New constructs the plugin.
func New(deps plugins.Deps) *Plugin {
	return &Plugin{deps: deps, start: deps.Now()}
}

// Descriptor implements plugins.Plugin.
func (p *Plugin) Descriptor() plugins.Descriptor {
	return plugins.Descriptor{
		Name:    "echo",
		Version: "1.0.0",
		Title:   "Echo",
		Description: "A diagnostic integration for verifying connectivity and " +
			"authorization. It reads and computes only; it changes nothing.",
	}
}

// EchoInput is the argument to the echo tool.
type EchoInput struct {
	Message string `json:"message" jsonschema:"the text to echo back"`
}

// EchoOutput is the echo tool's result.
type EchoOutput struct {
	Message string `json:"message"`
	Length  int    `json:"length"`
}

// StatusInput takes no arguments.
type StatusInput struct{}

// StatusOutput reports host-visible state.
type StatusOutput struct {
	Plugin   string `json:"plugin"`
	Version  string `json:"version"`
	UptimeMS int64  `json:"uptime_ms"`
}

// Register implements plugins.Plugin.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "echo",
		Title: "Echo a message",
		Description: "Returns the supplied message unchanged, with its length. " +
			"Use this to verify that the connection and credentials work.",
		Idempotent: true,
	}, func(_ context.Context, in EchoInput) (EchoOutput, error) {
		if strings.TrimSpace(in.Message) == "" {
			return EchoOutput{}, fmt.Errorf("message must not be empty")
		}
		return EchoOutput{Message: in.Message, Length: len(in.Message)}, nil
	})

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "status",
		Title:       "Plugin status",
		Description: "Reports this plugin's version and how long it has been running.",
		Idempotent:  true,
	}, func(_ context.Context, _ StatusInput) (StatusOutput, error) {
		d := p.Descriptor()
		return StatusOutput{
			Plugin:   d.Name,
			Version:  d.Version,
			UptimeMS: p.deps.Now().Sub(p.start).Milliseconds(),
		}, nil
	})

	return nil
}

// Check implements plugins.Checker. The echo plugin has no upstream
// dependency, so it is healthy whenever the process is.
func (p *Plugin) Check(context.Context) plugins.Health { return plugins.Healthy() }
