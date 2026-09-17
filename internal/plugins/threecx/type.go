package threecx

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// Type declares the integration and what an instance of it needs.
//
// One instance serves many phone systems. An MSP with thirty customers has at
// least thirty PBXs, each with its own address and its own system-owner
// extension; they are rows of one instance here, so the MSP runs one endpoint,
// one tunnel and one connector, and every tool says which customer and which
// of their phone systems it is about. The cost is that access is per instance:
// anyone who reaches it reaches every customer on it, so customers that must
// be kept apart go on separate instances.
//
// A row is a phone system rather than a business, because a business can have
// more than one and the Business column is what says so. Leaving that column
// empty means the row is its own business, which is what every row meant
// before the column existed -- so a table filled in under the old shape keeps
// its exact meaning.
func Type() plugins.Type {
	return plugins.Type{
		Name:        "threecx",
		Title:       "3CX",
		Description: "Your customers' 3CX phone systems, one row per system. Read-only.",
		Settings: []settings.Field{
			{
				Key: "customers", Label: "Customers", Kind: settings.KindCollection,
				Required: true,
				Help: "One row per phone system. A business with two of them gets two " +
					"rows naming the same business. Anyone who can reach this instance " +
					"reaches every customer on it; split them across instances if " +
					"some people should see only some.",
				Columns: []settings.Field{
					{
						Key: "name", Label: "Name", Kind: settings.KindString,
						Required:    true,
						Placeholder: "Acme Dental Group",
						Help: "What this phone system is called, and what no two rows may " +
							"share. For a business with one, its name. For a business with " +
							"several, name each system — Acme HQ, Acme Branch — so a " +
							"question can say which.",
					},
					{
						Key: "customer", Label: "Business", Kind: settings.KindString,
						Placeholder: "Acme Dental Group",
						Help: "The business this phone system belongs to. Leave it empty " +
							"unless the business has more than one system; then fill it in " +
							"on every one of its rows, spelt the same way, and they are " +
							"listed together as one customer.",
					},
					{
						Key: "aliases", Label: "Aliases", Kind: settings.KindList,
						Placeholder: "acme, acme dental, ADG",
						Help: "Other names people use for this system, separated by commas. " +
							"Two systems of one business may not share one.",
					},
					{
						Key: "host", Label: "Address", Kind: settings.KindString,
						Required:    true,
						Placeholder: "acme.ny.3cx.us",
						Help:        "The phone system's FQDN, with a port if it uses one.",
					},
					{
						Key: "extension", Label: "System owner extension", Kind: settings.KindString,
						Required:    true,
						Placeholder: "100",
						Help: "The extension number or email to sign in as. It needs the " +
							"System Owner role; use one kept for this purpose rather than a person's.",
					},
					{
						Key: "password", Label: "Password", Kind: settings.KindSecret,
						Required: true,
						Help:     "That extension's web client password. Stored encrypted.",
					},
				},
			},
			{
				Key: "max_items", Label: "Most rows per listing",
				Kind: settings.KindInt, Default: defaultMaxItems,
				Min: intPtr(10), Max: intPtr(2000),
				Help: "A listing stops here and says so.",
			},
			{
				Key: "timeout", Label: "How long to wait for an answer",
				Kind: settings.KindDuration, Unit: settings.UnitSeconds,
				Default: int(defaultTimeout / time.Second),
				Min:     intPtr(minTimeoutSeconds), Max: intPtr(maxTimeoutSeconds),
				Help: "A large phone system can take longer than the default to answer " +
					"a wide question. Raise this if listings time out.",
			},
			{
				Key: "requests_per_second", Label: "Requests per second",
				Kind: settings.KindInt, Default: int(defaultRPS),
				Min: intPtr(1), Max: intPtr(20),
				Help: "Bounds how hard mcpd leans on each phone system.",
			},
		},
		New: func(deps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			var c Config
			if err := decode(cfg, &c); err != nil {
				return nil, fmt.Errorf("3cx: %w", err)
			}
			return New(deps, c)
		},
	}
}

func intPtr(i int) *int { return &i }

// decode turns resolved settings into a Config.
//
// Round-tripping through JSON rather than reflecting field by field: the
// struct tags already describe the mapping, and a second description of it
// would be a second thing to keep in step.
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
