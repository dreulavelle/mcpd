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

// Valid reports whether k is a recognised kind. Plugins declare their own
// fields, so this is checked rather than assumed.
func (k Kind) Valid() bool {
	switch k {
	case KindString, KindSecret, KindBool, KindInt, KindDuration, KindEnum, KindList:
		return true
	}
	return false
}

// Apply describes when a change takes effect.
type Apply string

const (
	// ApplyLive means the change is picked up without a restart.
	ApplyLive Apply = "live"
	// ApplyReconnect means a component is restarted to pick it up, which the
	// host does itself.
	ApplyReconnect Apply = "reconnect"
	// ApplyRestart means mcpd must be restarted. Listeners and storage are
	// established once at startup, so changing where the host binds or where
	// its database lives is not something that can be done underneath a live
	// connection.
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
	// SectionPlugins is the page listing integrations. A plugin's own
	// settings belong beside the plugin, not in a general list.
	SectionPlugins = "plugins"
	// SectionTunnels is the page listing ChatGPT connectors.
	SectionTunnels = "tunnels"
	// SectionAuthentication is the page deciding who can sign in and how.
	// Separate from the general settings page because the pending queue lives
	// beside these fields and answers the question they raise.
	SectionAuthentication = "authentication"
)

// Setting keys. They are namespaced by group so a key cannot collide and so
// history reads clearly.
const (
	KeyTunnelEnabled = "tunnel.enabled"
	// KeyTunnelID holds the tunnel serving every plugin at once.
	//
	// Deliberately not a settings field, for the same reason as
	// PluginTunnelKey below: tunnels are made and assigned on the Tunnels
	// page, where the id comes from the tunnel that was just created. A text
	// box asking an operator to paste one back in asks them to copy a value
	// the app already has, and it appeared before there was anything to paste.
	//
	// A deployment that would rather not hand mcpd an admin key can still set
	// tunnel.tunnel_id in config.yaml, which this falls back to.
	KeyTunnelID       = "tunnel.tunnel_id"
	KeyTunnelAPIKey   = "tunnel.api_key"
	KeyTunnelAdminKey = "tunnel.admin_key"
	KeyTunnelOrgID    = "tunnel.organization_id"
	// KeyTunnelPrincipal is the identity requests through the tunnel act as.
	//
	// Not a field either. It only decides how ChatGPT is labelled in the
	// history, the default says exactly that, and a form asking an operator to
	// name it invites them to think it means more than it does. config.yaml
	// can still set it.
	KeyTunnelPrincipal = "tunnel.principal"
	KeyTunnelRole      = "tunnel.role"
	KeyTunnelPlugins   = "tunnel.plugins"
	KeyTunnelUpdates   = "tunnel.check_for_updates"
	KeyTunnelDebug     = "tunnel.debug"

	KeyHistoryRetentionDays = "history.retention_days"

	KeyApprovalProposalTTL = "approval.proposal_ttl_minutes"
	KeyApprovalApprovalTTL = "approval.approval_ttl_minutes"
	KeyApprovalLeaseTTL    = "approval.lease_ttl_minutes"

	// KeyApprovalAutoRules holds the standing rules that decide which changes
	// are authorised without asking anybody. It is a JSON array of rules.
	//
	// Deliberately not a settings field, for the same reason as the tunnel id
	// above: a rule is three selectors, a ceiling and a note, and the form
	// kinds here describe a text box. A JSON blob in one would be validated by
	// nothing, which is the wrong shape for the setting that decides when a
	// human is skipped.
	//
	// It lives in the settings store all the same, so a change to it is
	// encrypted-at-rest by the same machinery, recorded in settings_history
	// against the administrator who made it, and readable by the dashboard.
	// What it does not have is a generic form: it has its own endpoints, which
	// validate a whole rule set at once -- the only unit at which "no two rules
	// cover the same thing" can be checked.
	KeyApprovalAutoRules = "approval.auto_approve_rules"

	// Who may make an account here, and on what terms.
	//
	// Off is the default and the default is load-bearing. A host that had no
	// sign-ups before this existed must not have them afterwards because a
	// zero value said so, and the same reasoning that keeps the approval
	// policy empty until somebody writes a rule keeps this shut until
	// somebody opens it.
	KeyRegistrationEnabled  = "auth.registration.enabled"
	KeyRegistrationApproval = "auth.registration.require_approval"
	KeyRegistrationDomains  = "auth.registration.allowed_domains"

	// The identity providers. Each is a switch, a client id and a secret; only
	// Entra needs a fourth thing, and it needs it badly enough that the flow
	// refuses to run without it.
	KeyGoogleEnabled  = "auth.google.enabled"
	KeyGoogleClientID = "auth.google.client_id"
	KeyGoogleSecret   = "auth.google.client_secret"

	KeyGitHubEnabled  = "auth.github.enabled"
	KeyGitHubClientID = "auth.github.client_id"
	KeyGitHubSecret   = "auth.github.client_secret"

	KeyEntraEnabled  = "auth.entra.enabled"
	KeyEntraClientID = "auth.entra.client_id"
	KeyEntraSecret   = "auth.entra.client_secret"
	KeyEntraTenant   = "auth.entra.tenant_id"
)

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
					Key: KeyTunnelAPIKey, Label: "OpenAI key", Kind: KindSecret,
					Group: "tunnel", Apply: ApplyReconnect, Required: true,
					Help: "Needs Tunnels: Read and Use. Stored encrypted.",
				},
				{
					Key: KeyTunnelAdminKey, Label: "OpenAI admin key", Kind: KindSecret,
					Group: "tunnel", Apply: ApplyLive, Required: true,
					Help: "A different key from the one above, and how tunnels get made " +
						"on the Tunnels page. Stored encrypted.",
				},
				{
					Key: KeyTunnelOrgID, Label: "OpenAI organization ID", Kind: KindString,
					Group: "tunnel", Apply: ApplyLive, Required: true,
					Placeholder: "org_...",
					Help:        "Goes with the admin key. Settings, Organization, General.",
				},
				{
					Key: KeyTunnelRole, Label: "What ChatGPT may do", Kind: KindEnum,
					Group: "tunnel", Apply: ApplyReconnect, Default: "user",
					Options: []string{"user", "admin"},
					// A user can approve, and has to be able to: approval
					// happens in the conversation and the agent is what carries
					// the answer back. Admin additionally lets it change this
					// host's own settings, which is almost never wanted for a
					// connector.
					Help: "Changes apply only after you say yes in the conversation. " +
						"Admin also lets it change these settings, which you probably " +
						"do not want.",
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
		{
			Name:      "registration",
			Title:     "Registration",
			Section:   SectionAuthentication,
			EnabledBy: KeyRegistrationEnabled,
			Help:      "Whether people can make their own account here.",
			Fields: []Field{
				{
					Key: KeyRegistrationEnabled, Label: "Let people sign themselves up",
					Kind: KindBool, Group: "registration", Apply: ApplyLive,
					Default: false,
					Help:    "Off unless you turn it on. Upgrading never turns it on.",
				},
				{
					Key: KeyRegistrationApproval, Label: "Approve each one first",
					Kind: KindBool, Group: "registration", Apply: ApplyLive,
					Default: true,
					Help: "New accounts wait here until you say yes. They can sign in " +
						"and see they are waiting, and can do nothing else.",
				},
				{
					Key: KeyRegistrationDomains, Label: "Only these email domains",
					Kind: KindList, Group: "registration", Apply: ApplyLive,
					Placeholder: "corp.com, corp.co.uk",
					Help:        "Comma separated. Empty means any address.",
				},
			},
		},
		{
			Name:      "google",
			Title:     "Google",
			Section:   SectionAuthentication,
			EnabledBy: KeyGoogleEnabled,
			Help:      "Sign in with a Google account.",
			Fields: []Field{
				{
					Key: KeyGoogleEnabled, Label: "Offer Google", Kind: KindBool,
					Group: "google", Apply: ApplyLive, Default: false,
				},
				{
					Key: KeyGoogleClientID, Label: "Client ID", Kind: KindString,
					Group: "google", Apply: ApplyLive, Required: true,
					Placeholder: "...apps.googleusercontent.com",
				},
				{
					Key: KeyGoogleSecret, Label: "Client secret", Kind: KindSecret,
					Group: "google", Apply: ApplyLive, Required: true,
					Help: "Stored encrypted, and never shown again.",
				},
			},
		},
		{
			Name:      "github",
			Title:     "GitHub",
			Section:   SectionAuthentication,
			EnabledBy: KeyGitHubEnabled,
			Help:      "Sign in with a GitHub account.",
			Fields: []Field{
				{
					Key: KeyGitHubEnabled, Label: "Offer GitHub", Kind: KindBool,
					Group: "github", Apply: ApplyLive, Default: false,
				},
				{
					Key: KeyGitHubClientID, Label: "Client ID", Kind: KindString,
					Group: "github", Apply: ApplyLive, Required: true,
					Placeholder: "Iv1....",
				},
				{
					Key: KeyGitHubSecret, Label: "Client secret", Kind: KindSecret,
					Group: "github", Apply: ApplyLive, Required: true,
					Help: "Stored encrypted, and never shown again.",
				},
			},
		},
		{
			Name:      "entra",
			Title:     "Microsoft Entra",
			Section:   SectionAuthentication,
			EnabledBy: KeyEntraEnabled,
			Help:      "Sign in with a work or school Microsoft account.",
			Fields: []Field{
				{
					Key: KeyEntraEnabled, Label: "Offer Microsoft", Kind: KindBool,
					Group: "entra", Apply: ApplyLive, Default: false,
				},
				{
					Key: KeyEntraClientID, Label: "Application (client) ID", Kind: KindString,
					Group: "entra", Apply: ApplyLive, Required: true,
				},
				{
					Key: KeyEntraSecret, Label: "Client secret", Kind: KindSecret,
					Group: "entra", Apply: ApplyLive, Required: true,
					Help: "Stored encrypted, and never shown again.",
				},
				{
					Key: KeyEntraTenant, Label: "Directory (tenant) ID", Kind: KindString,
					Group: "entra", Apply: ApplyLive, Required: true,
					// The refusal of common/organizations/consumers is enforced
					// in the flow, and said here so it is read before it is met.
					Help: "One directory. `common`, `organizations` and `consumers` " +
						"name every directory, which mcpd will not accept.",
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
	return validateAgainst(f, key, value)
}

// validateAgainst checks a value against a field it has already been matched
// to. Split out so a Catalog, which knows about fields this package's static
// schema does not, checks them the same way.
func validateAgainst(f Field, key, value string) error {
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
