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
	EnabledBy string `json:"enabled_by,omitempty"`
	// Section names the page that owns this group.
	//
	// Settings people cannot find are settings that do not work. Tunnel
	// configuration belongs beside the tunnels it configures, not in a general
	// list, so the group says where it goes rather than the dashboard guessing
	// from its name.
	Section string  `json:"section"`
	Fields  []Field `json:"fields"`
}

// Sections a group can belong to.
const (
	// SectionSettings is the general settings page.
	SectionSettings = "settings"
	// SectionTunnels is the page listing ChatGPT connectors.
	SectionTunnels = "tunnels"
)

// Setting keys. They are namespaced by group so a key cannot collide and so
// history reads clearly.
const (
	KeyTunnelEnabled   = "tunnel.enabled"
	KeyTunnelID        = "tunnel.tunnel_id"
	KeyTunnelAPIKey    = "tunnel.api_key"
	KeyTunnelAdminKey  = "tunnel.admin_key"
	KeyTunnelOrgID     = "tunnel.organization_id"
	KeyTunnelPrincipal = "tunnel.principal"
	KeyTunnelRole      = "tunnel.role"
	KeyTunnelPlugins   = "tunnel.plugins"
	KeyTunnelUpdates   = "tunnel.check_for_updates"
	KeyTunnelDebug     = "tunnel.debug"

	KeyHistoryRetentionDays = "history.retention_days"

	KeyApprovalProposalTTL = "approval.proposal_ttl_minutes"
	KeyApprovalApprovalTTL = "approval.approval_ttl_minutes"
	KeyApprovalLeaseTTL    = "approval.lease_ttl_minutes"
)

// PluginKey builds the key holding a plugin's settings.
func PluginKey(plugin string) string { return "plugins." + plugin + ".settings" }

// PluginEnabledKey builds the key controlling whether a plugin is on.
func PluginEnabledKey(plugin string) string { return "plugins." + plugin + ".enabled" }

func intPtr(i int) *int { return &i }

// PluginTunnelKey builds the key holding a plugin's own tunnel id.
//
// A tunnel forwards to exactly one MCP endpoint, so a connector serving one
// plugin needs a tunnel of its own bound to that plugin's endpoint. This is
// where the assignment is stored.
//
// It is deliberately not a settings field. Tunnels are made and assigned on
// the Tunnels page, where the id comes from the tunnel that was just created;
// a text box asking an operator to paste one back in would be asking them to
// copy a value the app already has.
func PluginTunnelKey(plugin string) string { return "tunnel.plugin." + plugin + ".tunnel_id" }

// PluginFromTunnelKey reverses PluginTunnelKey, returning "" for anything else.
func PluginFromTunnelKey(key string) string {
	const prefix = "tunnel.plugin."
	const suffix = ".tunnel_id"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
		return ""
	}
	return key[len(prefix) : len(key)-len(suffix)]
}

// Schema returns every editable group.
func Schema() []Group { return schema() }

func schema() []Group {
	return []Group{
		{
			Name:      "tunnel",
			Title:     "ChatGPT",
			Section:   SectionSettings,
			EnabledBy: KeyTunnelEnabled,
			Help:      "Lets ChatGPT reach mcpd without opening anything to the internet.",
			Fields: []Field{
				{
					Key: KeyTunnelEnabled, Label: "Let ChatGPT connect",
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
					Key: KeyTunnelAPIKey, Label: "OpenAI key", Kind: KindSecret,
					Group: "tunnel", Apply: ApplyReconnect, Required: true,
					Help: "Needs Tunnels: Read and Use. Stored encrypted.",
				},
				{
					Key: KeyTunnelAdminKey, Label: "OpenAI admin key", Kind: KindSecret,
					Group: "tunnel", Apply: ApplyLive,
					Help: "Optional, and a different key from the one above. Lets you " +
						"make tunnels from the Tunnels page.",
				},
				{
					Key: KeyTunnelOrgID, Label: "OpenAI organization ID", Kind: KindString,
					Group: "tunnel", Apply: ApplyLive, Placeholder: "org_...",
					Help: "Needed alongside the admin key. Settings, Organization, General.",
				},
				{
					Key: KeyTunnelPrincipal, Label: "Show it as", Kind: KindString,
					Group: "tunnel", Apply: ApplyReconnect, Default: "svc:chatgpt",
					Help: "How ChatGPT appears in your history.",
				},
				{
					Key: KeyTunnelRole, Label: "What ChatGPT may do", Kind: KindEnum,
					Group: "tunnel", Apply: ApplyReconnect, Default: "operator",
					Options: []string{"viewer", "operator", "approver"},
					Help:    "Approving its own changes removes the point of approving them.",
				},
				{
					Key: KeyTunnelPlugins, Label: "Systems ChatGPT can reach", Kind: KindList,
					Group: "tunnel", Apply: ApplyReconnect,
					Help: "Comma separated. Empty means all of them.",
				},
				{
					Key: KeyTunnelDebug, Label: "Log everything (for diagnosis)",
					Kind: KindBool, Group: "tunnel", Apply: ApplyReconnect, Default: false,
					Help: "Records full requests and responses, credentials included. " +
						"Turn it off when you're done.",
				},
				{
					Key: KeyTunnelUpdates, Label: "Mention new versions",
					Kind: KindBool, Group: "tunnel", Apply: ApplyLive, Default: true,
					Help: "Nothing updates itself.",
				},
			},
		},
		{
			Name:    "history",
			Title:   "History",
			Section: SectionSettings,
			Help:    "How long the record is kept.",
			Fields: []Field{
				{
					Key: KeyHistoryRetentionDays, Label: "Keep history for", Kind: KindInt,
					Group: "history", Apply: ApplyLive,
					Default: 7, Min: intPtr(0), Max: intPtr(3650),
					Help: "Days. Older entries are removed once a day, and the removal " +
						"is itself recorded. Zero keeps everything.",
				},
			},
		},
		{
			Name:    "approval",
			Title:   "Approvals",
			Section: SectionSettings,
			Help:    "How long a change waits, and who may approve one.",
			Fields: []Field{
				{
					Key: KeyApprovalProposalTTL, Label: "Suggestions expire after",
					Kind: KindDuration, Group: "approval", Apply: ApplyLive,
					Default: 30, Min: intPtr(1), Max: intPtr(10080),
					Help: "A suggestion nobody acted on is dropped after this.",
				},
				{
					Key: KeyApprovalApprovalTTL, Label: "Approvals expire after",
					Kind: KindDuration, Group: "approval", Apply: ApplyLive,
					Default: 15, Min: intPtr(1), Max: intPtr(1440),
					Help: "Stops an old decision firing against a system that has changed.",
				},
				{
					Key: KeyApprovalLeaseTTL, Label: "Flag a stuck change after",
					Kind: KindDuration, Group: "approval", Apply: ApplyLive,
					Default: 2, Min: intPtr(1), Max: intPtr(60),
					Help: "How long before a half-applied change is flagged for checking.",
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
