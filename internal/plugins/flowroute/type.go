package flowroute

import (
	"encoding/json"
	"fmt"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// Type declares the integration and what an instance of it needs.
//
// One instance serves many customers, one row per business. Flowroute does
// have parent and child accounts, but the API does not draw that line: a key
// is scoped to a single account when it is created, `GET /numbers` answers for
// "your account" and takes no account argument, and a parent's key reads the
// parent's own inventory rather than its children's. So the tenancy has to
// live here -- a credential per customer -- rather than being asked for
// upstream.
//
// Access is per instance: anyone who reaches it reaches every customer on it,
// so customers that must be kept apart go on separate instances.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "flowroute",
		Title: "Flowroute",
		Description: "Your customers' Flowroute accounts, one row per business: " +
			"numbers, inbound routes, emergency addresses, caller-ID names and " +
			"port orders. Read-only.",
		Guide: plugins.Guide{
			Questions: []string{
				"Where is Acme's main number routed, and what is its emergency address?",
				"Which of Acme's numbers have no inbound route?",
				"Why is the port order for this number still not done?",
			},
			Notes: []string{
				"Every tool takes a customer; list_customers has the names and aliases.",
				"Numbers are written with the country code: 12065550100, or +1 206 555 0100.",
				"A number's alias and note are free text somebody typed — usually " +
					"the site or department, because a Flowroute account has no " +
					"idea what any of its numbers is for.",
				"Call detail records are produced by an export job rather than a " +
					"query. list_cdr_exports shows the jobs; starting one is a write " +
					"and this integration does not make one.",
			},
		},
		Settings: []settings.Field{
			{
				Key: "customers", Label: "Customers", Kind: settings.KindCollection,
				Required: true,
				Help: "One row per business, each with its own Flowroute API key. " +
					"Anyone who can reach this instance reaches every customer on " +
					"it; split them across instances if some people should see " +
					"only some.",
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
						Key: "access_key", Label: "Access key", Kind: settings.KindString,
						Required:    true,
						Placeholder: "a1b2c3d4",
						Help: "From that account's Flowroute Manage, under " +
							"Preferences → API Control. The shorter of the two " +
							"values. A key belongs to one Flowroute account, so " +
							"each customer needs their own.",
					},
					{
						Key: "secret_key", Label: "Secret key", Kind: settings.KindSecret,
						Required: true,
						Help: "Issued with the access key, and shown once. Stored " +
							"encrypted. Flowroute sends both on every request " +
							"rather than exchanging them for a token, so rotating " +
							"the key stops every read for that customer at once.",
					},
				},
			},
			{
				Key: "max_items", Label: "Most rows per answer",
				Kind: settings.KindInt, Default: defaultMaxItems,
				Min: intPtr(10), Max: intPtr(2000),
				Help: "A listing stops here and says so.",
			},
			{
				Key: "requests_per_second", Label: "Requests per second",
				Kind: settings.KindInt, Default: int(defaultRPS),
				Min: intPtr(1), Max: intPtr(20),
				Help: "Bounds how hard mcpd leans on each customer's account. " +
					"Flowroute publishes no rate limit; this is restraint rather " +
					"than a rule.",
			},
		},
		New: func(deps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			var c Config
			if err := decode(cfg, &c); err != nil {
				return nil, fmt.Errorf("flowroute: %w", err)
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
