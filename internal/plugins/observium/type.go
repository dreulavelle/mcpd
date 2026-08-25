package observium

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
// Observium installation without editing a file and restarting.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "observium",
		Title: "Observium",
		Description: "Reads an Observium network monitoring estate: devices, " +
			"interfaces, sensors, capacity, topology and alerts. Read-only. " +
			"Needs Observium's REST API, which is a subscription feature.",
		Settings: []settings.Field{
			{
				Key: "base_url", Label: "Address", Kind: settings.KindString,
				Required:    true,
				Placeholder: "https://observium.internal.example.com",
				Help: "The web root — the address you type to reach Observium, " +
					"without /api/v0 on the end. Observium is normally on your " +
					"own network, so there is no default that could be right.",
			},
			{
				Key: "token", Label: "API token", Kind: settings.KindSecret,
				Help: "From Profile > API tokens > Manage, and only present on " +
					"a subscription — Community Edition has no REST API and " +
					"cannot be read by this integration. Preferred over a " +
					"username and password: it can be issued read-only, it " +
					"carries only the permissions of the account that made it, " +
					"and revoking it does not change anyone's login. Stored " +
					"encrypted.",
			},
			{
				Key: "username", Label: "Username", Kind: settings.KindString,
				Help: "Only for an installation too old for API tokens, where " +
					"the API takes HTTP basic auth instead. Leave both this and " +
					"the password empty if you set a token — the token wins.",
			},
			{
				Key: "password", Label: "Password", Kind: settings.KindSecret,
				Help: "The other half of basic auth. Stored encrypted.",
			},
			{
				Key: "max_items", Label: "Most items per answer", Kind: settings.KindInt,
				Default: defaultMaxItems, Min: intPtr(10), Max: intPtr(10000),
				Help: "A listing stops here and says so. It bounds what one " +
					"answer can pull into a conversation, not what the estate " +
					"holds — a large estate has tens of thousands of interfaces.",
			},
			{
				Key: "page_size", Label: "Items per request", Kind: settings.KindInt,
				Default: defaultPageSize, Min: intPtr(1), Max: intPtr(50000),
				Help: "How many entities one request asks Observium for. " +
					"Without pagination Observium builds the whole answer " +
					"first, which on a big estate is a slow query rather than " +
					"a fast one, so this is about the upstream as much as about " +
					"the response size.",
			},
			{
				Key: "requests_per_second", Label: "Requests per second", Kind: settings.KindInt,
				Default: int(defaultRPS), Min: intPtr(1), Max: intPtr(100),
				Help: "Bounds how hard mcpd leans on Observium — pages fetched " +
					"over the API, or queries put to the database. Either way it " +
					"is usually one PHP application and one MySQL server on " +
					"somebody's own hardware, and the poller matters more than " +
					"we do, so this is deliberately modest.",
			},
			{
				Key: "state_cache_seconds", Label: "Reuse readings for",
				Kind:    settings.KindInt,
				Default: int(defaultStateTTL / time.Second), Min: intPtr(0), Max: intPtr(300),
				Help: "Seconds a reading of current state — interfaces, " +
					"sensors, capacity — may be answered from memory. Zero " +
					"fetches every time. Keep it well under Observium's polling " +
					"interval: the point is to stop three tools in one turn " +
					"making the same call three times, not to hold a picture of " +
					"the network that has stopped being true. Alerts are never " +
					"held, whatever this says.",
			},
			{
				Key: "inventory_cache_seconds", Label: "Reuse inventory reads for",
				Kind:    settings.KindInt,
				Default: int(defaultInventoryTTL / time.Second), Min: intPtr(0), Max: intPtr(3600),
				Help: "Seconds a read of how the estate is arranged — the " +
					"device list, hardware inventory, VLANs, addresses, " +
					"neighbours — may be answered from memory. These change " +
					"when an operator changes them rather than on a poll cycle, " +
					"so they can be held far longer than a sensor reading.",
			},
		},
		New: func(deps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			var c Config
			if err := decode(cfg, &c); err != nil {
				return nil, fmt.Errorf("observium: %w", err)
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
