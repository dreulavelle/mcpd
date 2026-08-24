package settings

import (
	"errors"
	"fmt"
	"net"
	"net/url"
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

// Units a duration field can be counted in. Empty means minutes, which is
// what every duration meant before any of them was counted in anything else.
const (
	UnitSeconds = "seconds"
	UnitMinutes = "minutes"
	UnitHours   = "hours"
)

// RiskNone is the enum value standing for "no risk level at all".
//
// The policy spells this as the empty string, which a dropdown cannot offer
// and a person cannot tell apart from an unset field. It is spelled out here
// and translated where the policy is read.
const RiskNone = "none"

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
	// Unit names what a duration is counted in -- "seconds", "minutes",
	// "hours". Durations are stored as a whole number of their own unit
	// rather than as a Go duration string, because the dashboard renders them
	// as a number box and a value a person typed should round-trip as the
	// number they typed. Empty means minutes, which is what every duration
	// field meant before any of them was counted in anything else.
	Unit string `json:"unit,omitempty"`
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

	// KeyTunnelDiagnostics binds the tunnel client's own health and admin
	// listener, which reports OAuth discovery state mcpd cannot see. Empty
	// leaves it off. Bind it to loopback: it is unauthenticated.
	KeyTunnelDiagnostics = "tunnel.diagnostics_addr"

	// KeyTunnelControlPlane overrides the OpenAI endpoint the tunnel client
	// dials.
	//
	// Deliberately not a field, for the same reason as the tunnel id above.
	// It exists so a test can point the client somewhere it controls, and for
	// the day OpenAI moves the endpoint; nobody operating this host should be
	// typing one, and a text box in the tunnel form would invite it.
	KeyTunnelControlPlane = "tunnel.control_plane_base_url"

	KeyHistoryRetentionDays = "history.retention_days"

	// The host's own runtime configuration. These were once keys in
	// config.yaml; the file no longer supplies them, and the database is the
	// only authority for what they are. See docs/architecture.md, "Where
	// configuration lives".
	//
	// A duration counts whole units of its own Unit rather than carrying a Go
	// duration string, so the number in the box is the number that was typed.
	KeyServerPublicURL         = "server.public_url"
	KeyServerFrontendPublicURL = "server.frontend_public_url"
	KeyServerTLSMode           = "server.tls_mode"
	KeyServerFrontendEnabled   = "server.frontend_enabled"

	KeyServerReadHeaderTimeout = "server.read_header_timeout_seconds"
	KeyServerReadTimeout       = "server.read_timeout_seconds"
	KeyServerWriteTimeout      = "server.write_timeout_seconds"
	KeyServerIdleTimeout       = "server.idle_timeout_seconds"
	KeyServerShutdownTimeout   = "server.shutdown_timeout_seconds"

	KeyStorageBusyTimeout       = "storage.busy_timeout_seconds"
	KeyStorageRelaxedDurability = "storage.relaxed_durability"

	KeyAccountsSessionTTL = "auth.accounts.session_ttl_hours"

	KeyLoggingLevel  = "logging.level"
	KeyLoggingFormat = "logging.format"

	// KeyConfigImported records that the values config.yaml used to supply
	// have been read into this store, once.
	//
	// Deliberately not a field: it is a fact about this deployment's history
	// rather than a knob, and an operator clearing it would make the file
	// authoritative again for one start, which is the disagreement the whole
	// arrangement exists to remove. It holds the JSON record of what was
	// imported and when, so the question "where did this value come from" has
	// an answer in the store rather than only in a log line.
	KeyConfigImported = "config.file_imported"

	KeyApprovalProposalTTL = "approval.proposal_ttl_minutes"
	KeyApprovalApprovalTTL = "approval.approval_ttl_minutes"
	KeyApprovalLeaseTTL    = "approval.lease_ttl_minutes"

	// KeyApprovalInlineMaxRisk is the highest risk a person may approve from
	// a single yes/no prompt raised by their own client. "none" withholds the
	// prompt for everything, which is the strictest setting rather than a
	// disabled one -- the decision still happens, it just cannot be made in
	// one tap.
	KeyApprovalInlineMaxRisk = "approval.inline_max_risk"

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

	// KeyRegistrationDefaultGroup names a group every new registration joins.
	//
	// Empty is the default and joins none, which keeps the zero value of this
	// feature at "reaches nothing" like every other default here. It holds a
	// group's name rather than its identifier because an operator types it
	// into this field, and a name is the thing they can type; a name matching
	// no group grants nothing rather than failing the registration.
	KeyRegistrationDefaultGroup = "auth.registration.default_group"

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

	// A provider the operator runs themselves. The issuer is the extra thing
	// here and the important one: it decides where the client secret is sent
	// and whose signature is believed for an identity.
	KeyOIDCEnabled  = "auth.oidc.enabled"
	KeyOIDCLabel    = "auth.oidc.label"
	KeyOIDCIssuer   = "auth.oidc.issuer"
	KeyOIDCClientID = "auth.oidc.client_id"
	KeyOIDCSecret   = "auth.oidc.client_secret"
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
			Name:    "server",
			Title:   "Addresses",
			Section: SectionSettings,
			Help: "How this host is reached, and whether it serves its own " +
				"certificate. Where it binds is in the startup file, not here: " +
				"a bad bind address stored in the database would take away the " +
				"page you would fix it on.",
			Fields: []Field{
				{
					Key: KeyServerPublicURL, Label: "Address assistants use",
					Kind: KindString, Group: "server", Apply: ApplyLive,
					Placeholder: "https://mcp.example.net",
					Help: "The MCP endpoint as it looks from outside, which is what " +
						"gets copied into a client. Behind a proxy it is also how " +
						"mcpd knows the connection is really https. If this host " +
						"serves its own certificate, that certificate names the " +
						"address it was issued for, so changing this needs a restart " +
						"to reissue it.",
				},
				{
					Key: KeyServerFrontendPublicURL, Label: "Address this page is on",
					Kind: KindString, Group: "server", Apply: ApplyLive,
					Placeholder: "https://mcpd.example.net",
					Help: "Only when something in front of mcpd terminates TLS for " +
						"the dashboard. Empty lets the connection decide, which is " +
						"right when you reach mcpd directly. It also has to be right " +
						"for signing in with Google, GitHub or Microsoft to work.",
				},
				{
					Key: KeyServerTLSMode, Label: "Certificate for the MCP endpoint",
					Kind: KindEnum, Group: "server", Apply: ApplyRestart,
					Default: "off", Options: []string{"off", "self-signed"},
					Help: "Leave this off when a reverse proxy already terminates " +
						"TLS. Self-signed is for reaching mcpd directly, where the " +
						"alternative is a browser warning every visit.",
				},
				{
					Key: KeyServerFrontendEnabled, Label: "Serve this dashboard",
					Kind: KindBool, Group: "server", Apply: ApplyRestart,
					Default: true,
					Help: "Turning it off leaves the MCP endpoint running and nothing " +
						"to administer it from until you turn it back on in the " +
						"database by hand.",
				},
			},
		},
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
					Key: KeyTunnelDiagnostics, Label: "Diagnostics address",
					Kind: KindString, Group: "tunnel", Apply: ApplyReconnect,
					Placeholder: "127.0.0.1:9095",
					Help: "Where the tunnel client answers its own health questions, " +
						"which say more than mcpd can about a connection that will " +
						"not come up. Empty leaves it off. Bind it to loopback: " +
						"nothing authenticates it.",
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
					Kind: KindDuration, Unit: UnitMinutes, Group: "approval", Apply: ApplyLive,
					Default: 30, Min: intPtr(1), Max: intPtr(10080),
					Help: "A suggestion nobody acted on is dropped after this.",
				},
				{
					Key: KeyApprovalApprovalTTL, Label: "Approvals expire after",
					Kind: KindDuration, Unit: UnitMinutes, Group: "approval", Apply: ApplyLive,
					Default: 15, Min: intPtr(1), Max: intPtr(1440),
					Help: "Stops an old decision firing against a system that has changed.",
				},
				{
					Key: KeyApprovalLeaseTTL, Label: "Flag a stuck change after",
					Kind: KindDuration, Unit: UnitMinutes, Group: "approval", Apply: ApplyLive,
					Default: 2, Min: intPtr(1), Max: intPtr(60),
					Help: "How long before a half-applied change is flagged for checking.",
				},
				{
					Key: KeyApprovalInlineMaxRisk, Label: "Approve in the conversation up to",
					Kind: KindEnum, Group: "approval", Apply: ApplyLive,
					Default: "medium", Options: []string{RiskNone, "low", "medium", "high", "critical"},
					Help: "Above this the shortcut is withheld, not the decision: the " +
						"assistant has to show the change in full and be told " +
						"explicitly. Either way a person decides, in the conversation. " +
						"\"Nothing\" is the strictest setting, not a disabled one.",
				},
			},
		},
		{
			Name:    "timeouts",
			Title:   "Timeouts",
			Section: SectionSettings,
			Help: "How patient the two HTTP listeners are. Both are built once " +
				"when mcpd starts, so a change here waits for a restart.",
			Fields: []Field{
				{
					Key: KeyServerReadHeaderTimeout, Label: "Wait for request headers",
					Kind: KindDuration, Unit: UnitSeconds, Group: "timeouts", Apply: ApplyRestart,
					Default: 10, Min: intPtr(1), Max: intPtr(600),
					Help: "The defence against a caller that opens a connection and " +
						"then dawdles. Keep it short.",
				},
				{
					Key: KeyServerReadTimeout, Label: "Wait for the whole request",
					Kind: KindDuration, Unit: UnitSeconds, Group: "timeouts", Apply: ApplyRestart,
					Default: 60, Min: intPtr(1), Max: intPtr(3600),
				},
				{
					Key: KeyServerWriteTimeout, Label: "Allow for a reply",
					Kind: KindDuration, Unit: UnitSeconds, Group: "timeouts", Apply: ApplyRestart,
					Default: 120, Min: intPtr(1), Max: intPtr(3600),
					Help: "A tool call that reaches a slow upstream spends its time " +
						"here, so this is the one to raise if long calls are being cut off.",
				},
				{
					Key: KeyServerIdleTimeout, Label: "Hold an idle connection",
					Kind: KindDuration, Unit: UnitSeconds, Group: "timeouts", Apply: ApplyRestart,
					Default: 120, Min: intPtr(1), Max: intPtr(3600),
				},
				{
					Key: KeyServerShutdownTimeout, Label: "Allow for a graceful stop",
					Kind: KindDuration, Unit: UnitSeconds, Group: "timeouts", Apply: ApplyLive,
					Default: 30, Min: intPtr(1), Max: intPtr(600),
					Help: "How long work in flight is given to finish when mcpd is " +
						"asked to stop. Read when that happens, so a change to it " +
						"applies to the next stop rather than the next start.",
				},
			},
		},
		{
			Name:    "storage",
			Title:   "Database",
			Section: SectionSettings,
			Help: "Where the database is kept is in the startup file -- the host " +
				"has to know it before it can read anything here. How it is " +
				"opened is below, and the pools are opened once, so both take a " +
				"restart.",
			Fields: []Field{
				{
					Key: KeyStorageBusyTimeout, Label: "Wait for a locked database",
					Kind: KindDuration, Unit: UnitSeconds, Group: "storage", Apply: ApplyRestart,
					Default: 5, Min: intPtr(1), Max: intPtr(300),
					Help: "How long a statement waits for a lock before giving up.",
				},
				{
					Key: KeyStorageRelaxedDurability, Label: "Trade durability for speed",
					Kind: KindBool, Group: "storage", Apply: ApplyRestart,
					Default: false,
					Help: "Leave this off. On, the most recent writes can be lost in a " +
						"power cut -- and those writes are the approvals that " +
						"authorise changes to your systems.",
				},
			},
		},
		{
			Name:    "logging",
			Title:   "Logging",
			Section: SectionSettings,
			Help:    "What mcpd writes to its own output.",
			Fields: []Field{
				{
					Key: KeyLoggingLevel, Label: "How much to log", Kind: KindEnum,
					Group: "logging", Apply: ApplyLive, Default: "info",
					Options: []string{"debug", "info", "warn", "error"},
					Help:    "Debug is loud. It is the right setting while something is wrong.",
				},
				{
					Key: KeyLoggingFormat, Label: "Format", Kind: KindEnum,
					Group: "logging", Apply: ApplyLive, Default: "json",
					Options: []string{"json", "text"},
					Help: "JSON for anything collecting logs; text when you are " +
						"reading them yourself.",
				},
			},
		},
		{
			Name:    "sessions",
			Title:   "Sessions",
			Section: SectionAuthentication,
			Help:    "How long a signed-in browser stays signed in.",
			Fields: []Field{
				{
					Key: KeyAccountsSessionTTL, Label: "Sign people out after",
					Kind: KindDuration, Unit: UnitHours, Group: "sessions", Apply: ApplyLive,
					Default: 12, Min: intPtr(1), Max: intPtr(8760),
					Help: "Applies to sessions started from now on. Sessions already " +
						"issued keep the expiry they were given.",
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
					// The second sentence is the load-bearing one and belongs
					// here rather than in a paragraph somewhere: turning this
					// off is safe for a provider, which checked the address,
					// and would not be safe for the form, which did not.
					Help: "New accounts wait here until you say yes. They can sign in " +
						"and see they are waiting, and can do nothing else. " +
						"Turning this off applies to Google, GitHub and Microsoft only " +
						"— sign-ups with a password always wait, because nothing has " +
						"checked the address.",
				},
				{
					Key: KeyRegistrationDomains, Label: "Only these email domains",
					Kind: KindList, Group: "registration", Apply: ApplyLive,
					Placeholder: "corp.com, corp.co.uk",
					Help: "Comma separated. Empty means any address. Through a " +
						"provider this says who may have an account; through the " +
						"password form it only says what may be typed, which is why " +
						"those wait for you.",
				},
				{
					Key: KeyRegistrationDefaultGroup, Label: "Put new accounts in",
					Kind: KindString, Group: "registration", Apply: ApplyLive,
					Placeholder: "Read only",
					Help: "The name of a group. Empty means none, and a new " +
						"account then reaches nothing until you grant it " +
						"something.",
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
		{
			Name:      "oidc",
			Title:     "Your own provider",
			Section:   SectionAuthentication,
			EnabledBy: KeyOIDCEnabled,
			Help: "Any OpenID Connect provider you run: Keycloak, Authentik, " +
				"Authelia, Zitadel, Okta. mcpd reads the rest from the issuer, " +
				"so the address is the only thing it has to be told.",
			Fields: []Field{
				{
					Key: KeyOIDCEnabled, Label: "Offer your provider", Kind: KindBool,
					Group: "oidc", Apply: ApplyLive, Default: false,
				},
				{
					Key: KeyOIDCLabel, Label: "Button text", Kind: KindString,
					Group: "oidc", Apply: ApplyLive,
					Placeholder: "Authentik",
					Help:        "What the sign-in button says. Empty says Single sign-on.",
				},
				{
					Key: KeyOIDCIssuer, Label: "Issuer URL", Kind: KindString,
					Group: "oidc", Apply: ApplyLive, Required: true,
					Placeholder: "https://auth.example.com/application/o/mcpd",
					// The address without the well-known path, because that is
					// what the provider's own screen calls the issuer and
					// pasting the discovery URL is the mistake to head off.
					Help: "The issuer itself, not its .well-known address — mcpd " +
						"adds that. https, unless the provider is on this machine.",
				},
				{
					Key: KeyOIDCClientID, Label: "Client ID", Kind: KindString,
					Group: "oidc", Apply: ApplyLive, Required: true,
				},
				{
					Key: KeyOIDCSecret, Label: "Client secret", Kind: KindSecret,
					Group: "oidc", Apply: ApplyLive, Required: true,
					Help: "Stored encrypted, and never shown again. mcpd is a " +
						"confidential client: a provider set up as public has no " +
						"secret to paste here and should be changed.",
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
		// The same argument for the addresses. A public URL with no scheme is
		// the ordinary mistake, and it surfaces later as a client copying an
		// address that reaches nothing.
		if key == KeyServerPublicURL || key == KeyServerFrontendPublicURL {
			if err := validPublicURL(f.Label, value); err != nil {
				return err
			}
		}
		if key == KeyOIDCIssuer && strings.TrimSpace(value) != "" {
			if err := ValidateIssuerURL(value); err != nil {
				return fmt.Errorf("settings: %s %w", f.Label, err)
			}
		}
		if key == KeyTunnelDiagnostics && strings.TrimSpace(value) != "" {
			if _, _, err := net.SplitHostPort(strings.TrimSpace(value)); err != nil {
				return fmt.Errorf(
					"settings: %s must be host:port, such as 127.0.0.1:9095", f.Label)
			}
		}
	}
	return nil
}

// validPublicURL checks one of the two address fields.
//
// Absent is allowed: an empty public URL means the dashboard cannot show a
// connect address, which is a warning at startup rather than a refusal, and an
// empty frontend URL is correct whenever nothing terminates TLS in front.
func validPublicURL(label, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	switch {
	case err != nil:
		return fmt.Errorf("settings: %s is not a valid URL: %v", label, err)
	case u.Scheme != "http" && u.Scheme != "https":
		return fmt.Errorf("settings: %s must start with http:// or https://", label)
	case u.Host == "":
		return fmt.Errorf("settings: %s has no host", label)
	}
	return nil
}

// ValidateIssuerURL checks an address this host will send a client secret to
// and believe signatures from.
//
// Stricter than the public-URL check above, and for a different reason. Those
// two addresses are places this host tells other people to go; this one is a
// place this host itself goes, carrying a credential. A query or a fragment is
// refused rather than trimmed because discovery appends a path, and quietly
// dropping half of what an operator pasted would send the secret somewhere
// other than where they believe it goes -- which is exactly the mistake that
// pasting a .well-known URL makes.
//
// Exported because the sign-in flow enforces the same rule before it runs, and
// a rule spelled out twice is a rule that will eventually be two rules.
func ValidateIssuerURL(value string) error {
	value = strings.TrimSpace(value)
	u, err := url.Parse(value)
	switch {
	case err != nil || u.Host == "":
		return errors.New("must be a full address, like https://auth.example.com/application/o/mcpd")
	case u.User != nil:
		return errors.New("must not carry a username or password")
	case u.RawQuery != "" || u.Fragment != "":
		return errors.New("must be the issuer itself, with no ? or # after it")
	case strings.HasSuffix(strings.TrimSuffix(u.Path, "/"), "/.well-known/openid-configuration"):
		return errors.New("should be the issuer, not its .well-known address — mcpd adds that")
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http":
		if h := u.Hostname(); h == "localhost" || h == "127.0.0.1" || h == "::1" {
			return nil
		}
		return errors.New("must be https unless the provider is on this machine")
	}
	return errors.New("must start with https://")
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
