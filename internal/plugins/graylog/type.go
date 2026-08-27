package graylog

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
// Graylog installation without editing a file and restarting.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "graylog",
		Title: "Graylog",
		Description: "Reads a Graylog installation: log messages, counts and " +
			"summaries over them, the alerts and rules built on them, and " +
			"whether Graylog itself is well. Read-only.",
		Settings: []settings.Field{
			{
				Key: "base_url", Label: "Address", Kind: settings.KindString,
				Required:    true,
				Placeholder: "https://graylog.internal.example.com",
				Help: "The web root — the address you type to reach Graylog, " +
					"without /api on the end. Include the port if it is not " +
					"the default one; Graylog usually serves its interface and " +
					"its API on 9000.",
			},
			{
				Key: "token", Label: "Access token", Kind: settings.KindSecret,
				Required: true,
				Help: "From your own user page in Graylog, under Edit tokens. " +
					"The only credential this integration takes: it carries " +
					"only the permissions of the account that made it and " +
					"revoking it does not change anyone's login. Note the TTL " +
					"you give it — a token stops working when it expires, and " +
					"from here that looks exactly like a revoked one. Stored " +
					"encrypted.",
			},
			{
				Key: "max_messages", Label: "Most messages per search",
				Kind: settings.KindInt, Default: defaultMaxMessages,
				Min: intPtr(1), Max: intPtr(1000),
				Help: "A search stops here and says so. A log message can be " +
					"kilobytes on its own, so this is the setting that decides " +
					"whether one answer fits in a conversation. Ask for a " +
					"count with the aggregate tool rather than raising this to " +
					"count things.",
			},
			{
				Key: "max_items", Label: "Most rows per listing",
				Kind: settings.KindInt, Default: defaultMaxItems,
				Min: intPtr(10), Max: intPtr(5000),
				Help: "The ceiling on everything that is not a message: " +
					"aggregation rows, events, streams, alert rules, field " +
					"names. These are small uniform rows, so it can be far " +
					"higher than the message limit.",
			},
			{
				Key: "default_range_seconds", Label: "Default search window",
				Kind: settings.KindInt, Unit: "seconds", Default: defaultRangeSeconds,
				Min: intPtr(1), Max: intPtr(31536000),
				Help: "How far back a search reaches when whoever asked did " +
					"not say. Fifteen minutes answers “what is happening”, " +
					"which is the question somebody is asking when they do not " +
					"name a window. Anything wider is a decision, and this is " +
					"the setting that makes it one somebody has to make.",
			},
			{
				Key: "max_range_seconds", Label: "Furthest a search may reach",
				Kind: settings.KindInt, Unit: "seconds", Default: 0,
				Min: intPtr(0), Max: intPtr(31536000),
				Help: "A hard ceiling on how far back any search goes. Zero, " +
					"the default, is no ceiling — reviewing an incident from " +
					"last month is the second most common thing this is for, " +
					"and refusing it by default would be guessing at a policy " +
					"nobody stated. Set it if searching old indices is " +
					"expensive on your cluster. Note that a window given in " +
					"words rather than numbers is refused while this is set, " +
					"because Graylog resolves those and mcpd cannot check them.",
			},
			{
				Key: "requests_per_second", Label: "Requests per second",
				Kind: settings.KindInt, Default: int(defaultRPS),
				Min: intPtr(1), Max: intPtr(100),
				Help: "Bounds how hard mcpd leans on Graylog. A search is a " +
					"fan-out across every index in its window and is the most " +
					"expensive thing this integration can ask for, so this is " +
					"deliberately modest.",
			},
			{
				Key: "cache_seconds", Label: "Reuse configuration reads for",
				Kind: settings.KindInt, Unit: "seconds",
				Default: int(defaultCacheTTL / time.Second),
				Min:     intPtr(0), Max: intPtr(3600),
				Help: "Seconds a read of how Graylog is arranged — streams, " +
					"alert rules, field names, inputs — may be answered from " +
					"memory. Zero fetches every time. Searches, aggregations, " +
					"events and health are never held whatever this says: they " +
					"are questions about now, and a cached answer to one of " +
					"those is indistinguishable from a true one.",
			},
		},
		New: func(deps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			var c Config
			if err := decode(cfg, &c); err != nil {
				return nil, fmt.Errorf("graylog: %w", err)
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
