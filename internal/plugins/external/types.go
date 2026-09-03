package external

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// An out-of-process plugin is a type, and its instances are configured the way
// a compiled-in integration's are.
//
// It used to be mounted straight from its directory: one process, named after
// the directory, with its settings declared over the wire and then dropped on
// the floor because nothing on the host ever asked for them. The plugin read
// environment variables from its manifest and the package doc said it never
// would. Making the manifest a type puts it through the same machinery as
// everything else -- the settings form, readiness, rebuild on change, the
// purpose phrase, removal from the dashboard -- and lets two instances of one
// plugin exist, which a directory name cannot express.

// probeTimeout bounds the one call made to learn what a plugin is.
const probeTimeout = 30 * time.Second

// Probe starts a discovered plugin long enough to read its self-description,
// then stops it. The type built from that description is what instances are
// then constructed against; each instance runs its own process.
func Probe(ctx context.Context, dir string, m Manifest, deps plugins.Deps) (DescribeResult, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	p := NewPlugin(dir, m, deps)
	if err := p.Handshake(ctx); err != nil {
		return DescribeResult{}, err
	}
	describe := p.describe
	_ = p.Shutdown(ctx)
	return describe, nil
}

// TypeFor builds the host type for a discovered plugin. Each instance built
// from it starts its own process and is handed its own resolved settings.
func TypeFor(dir string, m Manifest, d DescribeResult) (plugins.Type, error) {
	fields := make([]settings.Field, 0, len(d.Settings))
	for _, sd := range d.Settings {
		fields = append(fields, fieldFrom(sd))
	}
	t := plugins.Type{
		Name:        m.Name,
		Title:       orDefault(d.Title, m.Name),
		Description: d.Description,
		Settings:    fields,
		New: func(instanceDeps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			p := NewPlugin(dir, m, instanceDeps)
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
			defer cancel()
			if err := p.Handshake(ctx); err != nil {
				return nil, err
			}
			if err := p.Configure(ctx, cfg); err != nil {
				_ = p.Shutdown(ctx)
				return nil, err
			}
			return p, nil
		},
	}
	if err := t.Validate(); err != nil {
		return plugins.Type{}, fmt.Errorf("external: plugin %s declares settings the host cannot offer: %w", m.Name, err)
	}
	return t, nil
}

// fieldFrom turns a wire setting into the host's field, columns included.
func fieldFrom(sd SettingDescriptor) settings.Field {
	f := settings.Field{
		Key: sd.Key, Label: sd.Label, Help: sd.Help, Kind: settings.Kind(strings.ToLower(sd.Kind)),
		Default: sd.Default, Options: sd.Options, Min: sd.Min, Max: sd.Max,
		Required: sd.Required, Placeholder: sd.Placeholder, Apply: settings.ApplyLive,
	}
	for _, c := range sd.Columns {
		f.Columns = append(f.Columns, fieldFrom(c))
	}
	return f
}
