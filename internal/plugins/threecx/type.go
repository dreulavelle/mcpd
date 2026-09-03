package threecx

import (
	"encoding/json"
	"fmt"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// Type declares the integration and what an instance of it needs.
//
// One instance serves many phone systems. An MSP with thirty customers has
// thirty PBXs, each with its own address and its own system-owner extension;
// they are rows of one instance here, so the MSP runs one endpoint, one tunnel
// and one connector, and every tool says which customer it is about. The cost
// is that access is per instance: anyone who reaches it reaches every customer
// on it, so customers that must be kept apart go on separate instances.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "threecx",
		Title: "3CX",
		Description: "The 3CX v20 phone systems of one or more customers. Read-only; " +
			"credentials the API would return are never requested.",
		Settings: []settings.Field{
			{
				Key: "customers", Label: "Customers", Kind: settings.KindCollection,
				Required: true,
				Help: "One row per business. Anyone who can reach this instance " +
					"reaches every customer on it; split them across instances if " +
					"some people should see only some.",
				Columns: []settings.Field{
					{
						Key: "name", Label: "Business name", Kind: settings.KindString,
						Required:    true,
						Placeholder: "Acme Dental Group",
						Help:        "What the business is called.",
					},
					{
						Key: "aliases", Label: "Aliases", Kind: settings.KindList,
						Placeholder: "acme, acme dental, ADG",
						Help:        "Other names people use for it, separated by commas.",
					},
					{
						Key: "host", Label: "Address", Kind: settings.KindString,
						Required:    true,
						Placeholder: "acme.ny.3cx.us",
						Help:        "The phone system's FQDN, with or without https://.",
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
