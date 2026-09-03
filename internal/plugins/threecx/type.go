package threecx

import (
	"encoding/json"
	"fmt"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// Type declares the integration and what an instance of it needs.
//
// One instance is one phone system. An MSP with thirty customers has thirty
// PBXs, each with its own address and its own system-owner extension, and each
// is an instance here -- which is what puts the customer's name in every tool
// the model sees, and what lets a credential be scoped to one of them.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "threecx",
		Title: "3CX",
		Description: "Reads a 3CX v20 phone system: whether it is healthy, " +
			"which extensions and handsets are registered, trunks and the " +
			"numbers on them, where a number rings, ring groups, queues, " +
			"digital receptionists, office hours and holidays, call history " +
			"and the event log. Read-only: it will not create, change or " +
			"delete anything, and every request is checked against a list " +
			"of read endpoints. Credentials the API would hand out -- SIP " +
			"passwords, voicemail PINs, provisioning links, the licence key " +
			"-- are never requested.",
		Settings: []settings.Field{
			{
				Key: "host", Label: "Address", Kind: settings.KindString,
				Required:    true,
				Placeholder: "acme.ny.3cx.us",
				Help: "The phone system's web address, as its console reports " +
					"it — the FQDN, with or without https:// in front. Every " +
					"customer has their own, so there is no default.",
			},
			{
				Key: "extension", Label: "System owner extension", Kind: settings.KindString,
				Required:    true,
				Placeholder: "100",
				Help: "The extension number, or the email address, to sign in " +
					"as. It needs the System Owner role: a normal extension can " +
					"sign in and see only itself, and every listing here would " +
					"answer 403. Make it an extension kept for this purpose " +
					"rather than a person's, so revoking it later does not lock " +
					"anybody out.",
			},
			{
				Key: "password", Label: "Password", Kind: settings.KindSecret,
				Required: true,
				Help: "That extension's web client password. Exchanged for a " +
					"bearer token that lasts an hour, so it crosses the network " +
					"once an hour at sign-in and appears nowhere else. Stored " +
					"encrypted.",
			},
			{
				Key: "max_items", Label: "Most rows per listing",
				Kind: settings.KindInt, Default: defaultMaxItems,
				Min: intPtr(10), Max: intPtr(2000),
				Help: "A listing stops here and says so. It bounds what one " +
					"answer can pull into a conversation, not what the phone " +
					"system holds — a large PBX has a thousand extensions, and " +
					"narrowing with a query is the better answer than raising this.",
			},
			{
				Key: "requests_per_second", Label: "Requests per second",
				Kind: settings.KindInt, Default: int(defaultRPS),
				Min: intPtr(1), Max: intPtr(20),
				Help: "Bounds how hard mcpd leans on the phone system. A 3CX is " +
					"one process on one machine with live calls going through " +
					"it, and the calls matter more than we do, so the default " +
					"is deliberately modest.",
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
