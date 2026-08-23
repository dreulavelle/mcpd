package cnmaestro

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
// cnMaestro account without editing a file and restarting.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "cnmaestro",
		Title: "Cambium cnMaestro",
		Description: "Reads a Cambium cnMaestro estate: networks, devices, and " +
			"the state of each. Read-only.",
		Settings: []settings.Field{
			{
				Key: "client_id", Label: "Client ID", Kind: settings.KindSecret,
				Required: true,
				Help: "From Download Credentials in cnMaestro, under " +
					"Services > API Clients. Needs a Super Admin account, and " +
					"the API is a cnMaestro X feature.",
			},
			{
				Key: "client_secret", Label: "Client secret", Kind: settings.KindSecret,
				Required: true,
				Help:     "The other half of the same download. Stored encrypted.",
			},
			{
				// No default. An MSP operator wants every tenant readable, and
				// a default here cannot be cleared: an empty field falls back
				// to it, so pinning one account would be the only thing this
				// form could express.
				Key: "managed_account", Label: "Account to read by default",
				Kind:        settings.KindString,
				Placeholder: "every account",
				Help: "Leave it empty on an MSP installation: reads then span " +
					"every account the credential can see, and an assistant can " +
					"ask about one by name. Fill it in to pin every read to one " +
					"account -- an MSP tenant name, or " + MainAccount + " for " +
					"the main account, matched exactly and case-sensitively. " +
					"Either way a tool call may name an account of its own.",
			},
			{
				Key: "base_url", Label: "Address", Kind: settings.KindString,
				Default:     defaultBaseURL,
				Placeholder: defaultBaseURL,
				Help: "Where tokens are obtained. Cloud accounts are regionally " +
					"sharded and the token response names the host that data " +
					"calls go to, so this is the front door rather than the " +
					"final address.",
			},
			{
				Key: "max_items", Label: "Most items per answer", Kind: settings.KindInt,
				Default: defaultMaxItems, Min: intPtr(10), Max: intPtr(10000),
				Help: "A listing stops here and says so. It bounds what one " +
					"answer can pull into a conversation, not what the estate holds.",
			},
			{
				Key: "requests_per_second", Label: "Requests per second", Kind: settings.KindInt,
				Default: defaultRPS, Min: intPtr(1), Max: intPtr(100),
				Help: "Listing walks pages in a loop, which is the shape most " +
					"likely to trip an upstream rate limit.",
			},
			{
				Key: "device_cache_seconds", Label: "Reuse device reads for", Kind: settings.KindInt,
				Default: int(defaultDeviceTTL / time.Second), Min: intPtr(0), Max: intPtr(300),
				Help: "Seconds a device listing may be answered from memory " +
					"instead of walking every page again. Zero fetches every " +
					"time. Keep it short: cnMaestro's own view of whether a " +
					"device is up is already minutes behind, so a few seconds " +
					"here costs nothing that is not already lost -- but a long " +
					"window would hand an assistant a picture of the estate " +
					"that has stopped being true.",
			},
			{
				Key: "inventory_cache_seconds", Label: "Reuse inventory reads for",
				Kind:    settings.KindInt,
				Default: int(defaultInventoryTTL / time.Second), Min: intPtr(0), Max: intPtr(3600),
				Help: "Seconds a read of networks, sites, towers, WLANs, AP " +
					"groups or tenants may be answered from memory. These " +
					"describe how the estate is arranged and change when " +
					"somebody changes them, so they can be held far longer than " +
					"device state. Zero fetches every time. Alarms, events, " +
					"connected clients and statistics are never held, whatever " +
					"either of these says.",
			},
		},
		New: func(deps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			var c Config
			if err := decode(cfg, &c); err != nil {
				return nil, fmt.Errorf("cnmaestro: %w", err)
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
