package settings

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The schema is what makes dashboard management safe. Every editable setting
// is declared here with its type, its constraints, and whether changing it
// takes effect immediately -- so the UI can render a real form, the API can
// reject a bad value before it is stored, and an operator is told when a
// restart is needed rather than wondering why nothing happened.

// Kind is a setting's type, which decides how the UI renders it and how the
// API validates it.
type Kind string

const (
	KindString   Kind = "string"
	KindSecret   Kind = "secret"
	KindBool     Kind = "bool"
	KindInt      Kind = "int"
	KindDuration Kind = "duration"
	KindEnum     Kind = "enum"
	KindList     Kind = "list"
)

// Apply describes when a change takes effect.
type Apply string

const (
	// ApplyLive means the change is picked up without a restart.
	ApplyLive Apply = "live"
	// ApplyReconnect means a component is restarted to pick it up, which the
	// host does itself.
	ApplyReconnect Apply = "reconnect"
	// ApplyRestart means mcpd must be restarted. Plugins are mounted into MCP
	// servers at startup, so turning one on is not something that can be done
	// underneath a live connection.
	ApplyRestart Apply = "restart"
)

// Field declares one editable setting.
type Field struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Help is shown under the field. It says what the setting is for, in the
	// same plain language as the rest of the dashboard.
	Help    string `json:"help,omitempty"`
	Kind    Kind   `json:"kind"`
	Group   string `json:"group"`
	Apply   Apply  `json:"apply"`
	Default any    `json:"default,omitempty"`
	// Options constrains an enum.
	Options []string `json:"options,omitempty"`
	// Min and Max bound an int or duration, in the unit the field uses.
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`
	// Required marks a field that must be set when its group is enabled.
	Required bool `json:"required,omitempty"`
	// Placeholder is example text for the input.
	Placeholder string `json:"placeholder,omitempty"`
}

// Group is a related set of fields.
type Group struct {
	Name string `json:"name"`
	// Title and Help address the operator, not the developer.
	Title string `json:"title"`
	Help  string `json:"help,omitempty"`
	// EnabledBy names a bool field that switches the whole group on, if any.
	EnabledBy string  `json:"enabled_by,omitempty"`
	Fields    []Field `json:"fields"`
}

// Setting keys. They are namespaced by group so a key cannot collide and so
// history reads clearly.
const (
	KeyTunnelEnabled   = "tunnel.enabled"
	KeyTunnelID        = "tunnel.tunnel_id"
	KeyTunnelAPIKey    = "tunnel.api_key"
	KeyTunnelPrincipal = "tunnel.principal"
	KeyTunnelRole      = "tunnel.role"
	KeyTunnelPlugins   = "tunnel.plugins"
	KeyTunnelUpdates   = "tunnel.check_for_updates"

	KeyApprovalDistinct    = "approval.require_distinct_approver_at_or_above"
	KeyApprovalProposalTTL = "approval.proposal_ttl_minutes"
	KeyApprovalApprovalTTL = "approval.approval_ttl_minutes"
	KeyApprovalLeaseTTL    = "approval.lease_ttl_minutes"
)

// PluginKey builds the key holding a plugin's settings.
func PluginKey(plugin string) string { return "plugins." + plugin + ".settings" }

// PluginEnabledKey builds the key controlling whether a plugin is on.
func PluginEnabledKey(plugin string) string { return "plugins." + plugin + ".enabled" }

func intPtr(i int) *int { return &i }

// Schema returns every editable group.
func Schema() []Group {
	return []Group{
		{
			Name:      "tunnel",
			Title:     "ChatGPT tunnel",
			EnabledBy: KeyTunnelEnabled,
			Help: "Lets ChatGPT reach mcpd without opening anything to the internet. " +
				"The connection is made outward from here, so nothing needs to reach in.",
			Fields: []Field{
				{
					Key: KeyTunnelEnabled, Label: "Turn the tunnel on",
					Kind: KindBool, Group: "tunnel", Apply: ApplyReconnect,
					Default: false,
				},
				{
					Key: KeyTunnelID, Label: "Tunnel ID", Kind: KindString,
					Group: "tunnel", Apply: ApplyReconnect, Required: true,
					Placeholder: "tunnel_0123456789abcdef0123456789abcdef",
					Help:        "From your OpenAI account, under Settings, Organization, Tunnels.",
				},
				{
					Key: KeyTunnelAPIKey, Label: "API key", Kind: KindSecret,
					Group: "tunnel", Apply: ApplyReconnect, Required: true,
					Help: "A runtime key from your OpenAI account, not an admin key. " +
						"Admin keys only create and delete tunnels; they won't connect one. " +
						"Stored encrypted.",
				},
				{
					Key: KeyTunnelPrincipal, Label: "Acts as", Kind: KindString,
					Group: "tunnel", Apply: ApplyReconnect, Default: "svc:chatgpt",
					Help: "A name for whatever connects through this tunnel. It appears " +
						"in the history against everything the tunnel does.",
				},
				{
					Key: KeyTunnelRole, Label: "What it's allowed to do", Kind: KindEnum,
					Group: "tunnel", Apply: ApplyReconnect, Default: "operator",
					Options: []string{"viewer", "operator", "approver"},
					Help: "Viewer can only look things up. Operator can also suggest " +
						"changes. Approver can approve them too, which means an " +
						"assistant could approve its own suggestion — pick that " +
						"deliberately.",
				},
				{
					Key: KeyTunnelPlugins, Label: "Systems it can reach", Kind: KindList,
					Group: "tunnel", Apply: ApplyReconnect,
					Help: "Leave empty for all of them, or name the ones you want. " +
						"Anything not listed is invisible to it.",
				},
				{
					Key: KeyTunnelUpdates, Label: "Tell me about new versions",
					Kind: KindBool, Group: "tunnel", Apply: ApplyLive, Default: true,
					Help: "Checks daily and mentions it here. Nothing updates itself.",
				},
			},
		},
		{
			Name:  "approval",
			Title: "Approvals",
			Help:  "How long a suggested change waits, and who is allowed to approve one.",
			Fields: []Field{
				{
					Key: KeyApprovalDistinct, Label: "Needs a second person from",
					Kind: KindEnum, Group: "approval", Apply: ApplyLive,
					Options: []string{"", "low", "medium", "high", "critical"},
					Default: "high",
					Help: "Changes at this level or above can't be approved by whoever " +
						"suggested them. This only works when people sign in with " +
						"their own accounts — with a shared token mcpd can't tell " +
						"anyone apart, so it refuses those changes instead of " +
						"pretending.",
				},
				{
					Key: KeyApprovalProposalTTL, Label: "Suggestions expire after",
					Kind: KindDuration, Group: "approval", Apply: ApplyLive,
					Default: 30, Min: intPtr(1), Max: intPtr(10080),
					Help: "Minutes. After this, a suggestion nobody acted on is dropped.",
				},
				{
					Key: KeyApprovalApprovalTTL, Label: "Approvals expire after",
					Kind: KindDuration, Group: "approval", Apply: ApplyLive,
					Default: 15, Min: intPtr(1), Max: intPtr(1440),
					Help: "Minutes. An approval that hasn't been applied by then is " +
						"dropped, so an old decision can't fire against a system " +
						"that has since changed.",
				},
				{
					Key: KeyApprovalLeaseTTL, Label: "Give up on a stuck change after",
					Kind: KindDuration, Group: "approval", Apply: ApplyLive,
					Default: 2, Min: intPtr(1), Max: intPtr(60),
					Help: "Minutes. If mcpd stops partway through applying something, " +
						"this is how long before it's marked as unknown for someone " +
						"to check.",
				},
			},
		},
	}
}

// FieldFor returns a field's declaration.
func FieldFor(key string) (Field, bool) {
	for _, g := range Schema() {
		for _, f := range g.Fields {
			if f.Key == key {
				return f, true
			}
		}
	}
	return Field{}, false
}

// Validate checks a value against its declaration.
//
// Validation lives here rather than in the handler so the same rules apply
// whether a value arrives from the dashboard, the API, or a future import.
func Validate(key, value string) error {
	f, ok := FieldFor(key)
	if !ok {
		return fmt.Errorf("settings: %q is not an editable setting", key)
	}

	switch f.Kind {
	case KindBool:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("settings: %s must be true or false", f.Label)
		}

	case KindInt, KindDuration:
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("settings: %s must be a whole number", f.Label)
		}
		if f.Min != nil && n < *f.Min {
			return fmt.Errorf("settings: %s must be at least %d", f.Label, *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return fmt.Errorf("settings: %s must be at most %d", f.Label, *f.Max)
		}

	case KindEnum:
		if !slices.Contains(f.Options, value) {
			return fmt.Errorf("settings: %s must be one of %v", f.Label, f.Options)
		}

	case KindSecret:
		if f.Required && strings.TrimSpace(value) == "" {
			return fmt.Errorf("settings: %s is required", f.Label)
		}

	case KindString:
		if f.Required && strings.TrimSpace(value) == "" {
			return fmt.Errorf("settings: %s is required", f.Label)
		}
		// The tunnel ID has a documented shape, and checking it here turns a
		// typo into an immediate message rather than a confusing failure to
		// connect several seconds later.
		if key == KeyTunnelID && strings.TrimSpace(value) != "" {
			if !validTunnelID(value) {
				return fmt.Errorf(
					"settings: %s should look like tunnel_ followed by 32 characters",
					f.Label)
			}
		}
	}
	return nil
}

func validTunnelID(id string) bool {
	const prefix = "tunnel_"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	hex := id[len(prefix):]
	if len(hex) != 32 {
		return false
	}
	for _, r := range hex {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// IsSecret reports whether a key holds a credential.
func IsSecret(key string) bool {
	f, ok := FieldFor(key)
	return ok && f.Kind == KindSecret
}
