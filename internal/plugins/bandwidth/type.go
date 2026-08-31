package bandwidth

import (
	"encoding/json"
	"fmt"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// Type declares the integration and what an instance of it needs.
//
// One instance reads one Bandwidth account. A credential scoped to four
// accounts is configured four times, which is the shape mcpd already has for
// two of anything -- and it keeps "which account did that answer come from"
// answerable from the instance name rather than from a parameter somebody
// forgot to pass.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "bandwidth",
		Title: "Bandwidth",
		Description: "Calls, conferences, recordings, messages and toll-free " +
			"verification on one Bandwidth account. Read-only.",
		Settings: []settings.Field{
			{
				Key: "client_id", Label: "Client ID", Kind: settings.KindString,
				Required: true,
				Help: "From the Bandwidth console, under API credentials. The " +
					"credential must have at least one account and at least one " +
					"role; give it the fewest roles that cover what you want to " +
					"read.",
			},
			{
				Key: "client_secret", Label: "Client secret", Kind: settings.KindSecret,
				Required: true,
				// The expiry is worth naming here because Bandwidth asks for one
				// when the credential is created, and nothing afterwards reminds
				// anybody: the API does not report it, so mcpd cannot warn.
				Help: "Issued with the client ID. Note the expiry date you set " +
					"when creating it — Bandwidth does not report it through the " +
					"API, so nothing here can warn you before it lapses.",
			},
			{
				Key: "account_id", Label: "Account", Kind: settings.KindString,
				Required:    true,
				Placeholder: "5009021",
				Help: "The account number shown beside the account name in the " +
					"Bandwidth console. One instance reads one account; add a " +
					"second instance for a second account.",
			},
			{
				Key: "environment", Label: "Estate", Kind: settings.KindEnum,
				Default: "production", Options: []string{"production", "test"},
				Help: "Leave as production unless you are pointing at " +
					"Bandwidth's test estate.",
			},
			{
				Key: "max_items", Label: "Most rows per answer",
				Kind: settings.KindInt, Default: defaultMaxItems,
				Min: intPtr(10), Max: intPtr(2000),
				Help: "A listing stops here and says how many it left behind.",
			},
		},
		New: func(deps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			var c Config
			if err := decode(cfg, &c); err != nil {
				return nil, fmt.Errorf("bandwidth: %w", err)
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
