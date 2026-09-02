package textable

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
// instance without editing a file and restarting.
//
// One instance covers a whole Textable deployment, because a service account is
// instance-wide rather than tied to an account. That is the difference from the
// user-token approach this replaced, where one credential saw one account and
// serving several tenants would have meant one plugin instance per tenant.
func Type() plugins.Type {
	return plugins.Type{
		Name:  "textable",
		Title: "Textable",
		Description: "Reads a Textable business-SMS instance as a service " +
			"account: the tenants on it, the organizations and users inside " +
			"them, and individual users, organizations and contacts by id. " +
			"Read-only.",
		Settings: []settings.Field{
			{
				Key: "base_url", Label: "Address", Kind: settings.KindString,
				Required:    true,
				Placeholder: "https://your-instance.textable.app",
				Help: "The instance root, without /api on the end. A white-label " +
					"deployment is https://<project-id>.textable.app; retail " +
					"Textable is https://api.textable.app.",
			},
			{
				Key: "api_key", Label: "Service account token",
				Kind: settings.KindSecret, Required: true,
				Help: "A Textable *service account* token, sent as a bearer " +
					"credential. Not a user token — those are written " +
					"accountUid:apiKey, and the v2 endpoints this reads do not " +
					"accept one; a value with a colon in it is refused here " +
					"rather than failing later as an unexplained 401.\n\n" +
					"Grant it only the read scopes it needs: read-all-tenants, " +
					"read-all-users, read-all-organizations and read-contacts. " +
					"The transport refuses a destructive endpoint whatever the " +
					"token carries, but a credential an assistant can reach " +
					"should not hold delete-tenant or revoke-tenant-admin in " +
					"the first place. sync-billing is not a read either, and " +
					"nothing here calls it. Stored encrypted.",
			},
			{
				Key: "max_items", Label: "Most rows per listing",
				Kind: settings.KindInt, Default: defaultMaxItems,
				Min: intPtr(1), Max: intPtr(2000),
				Help: "The ceiling on every listing. It matters most for users: " +
					"the tenant report returns every user on the instance in " +
					"one response and there is no way to ask for fewer, so this " +
					"is where a large deployment is cut. A listing that stops " +
					"short says so in its result, and narrowing by tenant or a " +
					"query is the better answer than raising this.",
			},
			{
				Key: "requests_per_second", Label: "Requests per second",
				Kind: settings.KindInt, Default: int(defaultRPS),
				Min: intPtr(1), Max: intPtr(50),
				Help: "Bounds how hard mcpd leans on Textable. This is a live " +
					"messaging platform with the tenant's own staff working in " +
					"it, so the default is deliberately modest.",
			},
			{
				Key: "cache_seconds", Label: "Reuse configuration reads for",
				Kind: settings.KindInt, Unit: "seconds",
				Default: int(defaultCacheTTL / time.Second),
				Min:     intPtr(0), Max: intPtr(3600),
				Help: "Seconds a read may be answered from memory. This mostly " +
					"decides how often the tenant report is refetched — it is " +
					"the directory behind most of the tools and the most " +
					"expensive read here, so holding it is most of what makes " +
					"this integration quick. Zero fetches every time. A contact " +
					"is never held whatever this says: whether somebody has " +
					"opted out is acted on, and a stale answer to that has " +
					"consequences outside the conversation.",
			},
		},
		New: func(deps plugins.Deps, cfg map[string]any) (plugins.Plugin, error) {
			var c Config
			if err := decode(cfg, &c); err != nil {
				return nil, fmt.Errorf("textable: %w", err)
			}
			return New(deps, c)
		},
	}
}

func intPtr(i int) *int { return &i }

// decode turns resolved settings into a Config.
//
// Round-tripping through JSON rather than reflecting field by field: the struct
// tags already describe the mapping, and a second description of it would be a
// second thing to keep in step.
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
