package bandwidth

import (
	"encoding/json"
	"fmt"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// Type declares the integration and what an instance of it needs.
//
// One instance answers for every account the credential reaches. That is not
// the shape the other integrations take -- two Observiums are two instances --
// and the difference is in the credential rather than in the preference: a
// Bandwidth API credential is scoped to a set of accounts when it is made and
// says so in the token it issues, so the host already knows what it may read
// and there is nothing for an operator to repeat.
//
// It is also the shape the question takes. "Are any of our ports stuck" is
// about the estate, and somebody asking it should not have to know how many
// accounts there are, nor get four answers they have to add up.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "bandwidth",
		Title: "Bandwidth",
		Description: "Calls, messages, numbers, port-ins, 10DLC registration " +
			"and E911, across every Bandwidth account this credential " +
			"reaches. Read-only.",
		Guide: plugins.Guide{
			Questions: []string{
				"Which numbers on the account are not assigned to anything?",
				"Why is the port-in order for this number stalled?",
				"Show me the calls from this number in the last hour and how each ended.",
			},
			Notes: []string{
				"Numbers are written in E.164, with the country code.",
				"list_products says which Bandwidth products this account actually has.",
			},
		},
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
				Key: "default_account_id", Label: "Default account",
				Kind: settings.KindString, Placeholder: "9000001",
				Help: "Optional. The credential already says which accounts it " +
					"reaches, and any of them can be named on a call. This only " +
					"decides what a question that names none should mean — " +
					"leave it empty and one will be asked for.",
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
