package extremecloudiq

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// Type declares the integration and what an instance of it needs.
//
// The fields are what the dashboard renders, validates and encrypts. Declaring
// them here rather than in a config file is what lets someone add a second
// ExtremeCloud IQ account without editing a file and restarting.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "extremecloudiq",
		Title: "ExtremeCloud IQ",
		Description: "Access points, switches, clients, alerts, and what is " +
			"going wrong with any of them. Read-only.",
		Guide: plugins.Guide{
			Questions: []string{
				"Which access points at the high school are offline?",
				"How many clients are connected right now, and on which SSIDs?",
				"What alerts have fired in the last 24 hours?",
			},
			Notes: []string{
				"Location names match the ExtremeCloud IQ location tree.",
			},
		},
		Settings: []settings.Field{
			{
				Key: "api_token", Label: "API token", Kind: settings.KindSecret,
				Required: true,
				// Naming the wrong page is worth the words. It is the only page in
				// ExtremeCloud IQ with "API Token" in its name, it issues v1
				// credentials that cannot work here, and it asks for a Client ID —
				// so anybody who finds it first loses an hour to it.
				Help: "Extreme Platform ONE → your profile → API keys. Give it " +
					"read-only scopes and a long expiry. Not ExtremeCloud IQ’s own " +
					"API Token Management — that page is for the retired v1 API.",
			},
			{
				Key: "base_url", Label: "Address", Kind: settings.KindString,
				Default:     defaultBaseURL,
				Placeholder: defaultBaseURL,
				Help: "Leave as-is. It is regionless and routes to whichever data " +
					"centre holds your account.",
			},
			{
				Key: "max_items", Label: "Most rows per answer",
				Kind: settings.KindInt, Default: defaultMaxItems,
				Min: intPtr(10), Max: intPtr(5000),
				Help: "A listing stops here and says how many it left behind.",
			},
			{
				Key: "default_range_seconds", Label: "Default window",
				Kind: settings.KindInt, Unit: "seconds", Default: defaultRangeSeconds,
				Min: intPtr(60), Max: intPtr(31536000),
				Help: "How far back alerts, audit logs and device history reach " +
					"when nobody says.",
			},
			{
				Key: "max_range_seconds", Label: "Furthest a read may reach",
				Kind: settings.KindInt, Unit: "seconds", Default: 0,
				Min: intPtr(0), Max: intPtr(31536000),
				Help: "Hard ceiling on any window. Zero is no ceiling.",
			},
			{
				Key: "requests_per_second", Label: "Requests per second",
				Kind: settings.KindInt, Default: int(defaultRPS),
				Min: intPtr(1), Max: intPtr(50),
				Help: "ExtremeCloud IQ meters calls per account per hour. This " +
					"protects that budget.",
			},
			{
				Key: "cache_seconds", Label: "Reuse arrangement reads for",
				Kind: settings.KindInt, Unit: "seconds",
				Default: int(defaultCacheTTL / time.Second),
				Min:     intPtr(0), Max: intPtr(3600),
				Help: "Sites, policies and SSIDs only. Devices, clients and alerts " +
					"are never cached.",
			},
		},
		New: func(deps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			var c Config
			if err := decode(cfg, &c); err != nil {
				return nil, fmt.Errorf("extremecloudiq: %w", err)
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
