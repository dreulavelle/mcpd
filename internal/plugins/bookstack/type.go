package bookstack

import (
	"encoding/json"
	"fmt"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
)

// Type declares the integration and what an instance of it needs.
//
// One instance is one BookStack, because a knowledge base is one place. Two
// instances are two knowledge bases -- a staging copy, or a second business's.
//
// The token decides what this can do. BookStack applies the permissions of the
// user the token belongs to, so an instance given a viewer's token can read
// and nothing else, and one given an admin's token can do everything the
// mutations here describe. That is worth choosing deliberately: the safest
// token is the least-privileged one that covers what you actually want to
// change.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "bookstack",
		Title: "BookStack",
		Description: "Your BookStack knowledge base: search it, read shelves, " +
			"books, chapters and pages, and — through approved changes — write " +
			"to it.",
		Guide: plugins.Guide{
			Questions: []string{
				"What do we have written down about this customer's firewall?",
				"Draft a page in the Runbooks book covering what we just worked out.",
				"Which pages mention this hostname, and when were they last touched?",
			},
			Notes: []string{
				"Reading is a tool; changing anything is a proposal that has to be " +
					"approved before it happens.",
				"search_content is the way in: it matches on what pages say. The " +
					"listings match on titles only and carry no page text.",
				"Paste a BookStack link straight into get_page, get_book, " +
					"get_chapter or get_shelf — they take a url or a slug as well " +
					"as an id.",
				"Deleting a book, chapter, page or shelf sends it to the recycle " +
					"bin, so it can be restored. Emptying the recycle bin cannot.",
				"BookStack applies the token owner's permissions, so what this can " +
					"see and change is whatever that user could.",
			},
		},
		Settings: []settings.Field{
			{
				Key: "host", Label: "Address", Kind: settings.KindString,
				Required:    true,
				Placeholder: "kb.example.com or 10.0.0.5:8080",
				Help: "Where BookStack is served: a URL, or a host with an " +
					"optional port. A bare host is treated as http.",
			},
			{
				Key: "token_id", Label: "Token ID", Kind: settings.KindString,
				Required: true,
				Help: "From the BookStack user's profile page, under API Tokens. " +
					"The public half.",
			},
			{
				Key: "token_secret", Label: "Token secret", Kind: settings.KindSecret,
				Required: true,
				Help: "The private half, shown once when the token is created. " +
					"Stored encrypted. It is sent on every request rather than " +
					"exchanged for a session, so revoking the token stops every " +
					"read and every change here at once.",
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
				Help: "Bounds how hard mcpd leans on the instance. BookStack " +
					"throttles its own API at 180 requests a minute by default.",
			},
		},
		New: func(deps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			var c Config
			if err := decode(cfg, &c); err != nil {
				return nil, fmt.Errorf("bookstack: %w", err)
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
