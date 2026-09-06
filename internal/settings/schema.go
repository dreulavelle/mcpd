package settings

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/backup"
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
	// KindCollection is a table of rows, each shaped by the field's Columns.
	//
	// It exists for the setting whose size nobody knows in advance -- the
	// customers one integration instance serves, each with an address and a
	// credential. The flat store cannot hold several of those without
	// synthesising keys, and a form cannot edit them in one masked box. So
	// the rows live in their own table, are edited one at a time through the
	// row endpoints rather than through PUT /api/settings, and reach the
	// plugin as a list of records.
	KindCollection Kind = "collection"
)

// Valid reports whether k is a recognised kind. Plugins declare their own
// fields, so this is checked rather than assumed.
func (k Kind) Valid() bool {
	switch k {
	case KindString, KindSecret, KindBool, KindInt, KindDuration, KindEnum, KindList, KindCollection:
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
	// Options constrains an enum. These are the values stored and validated
	// against, so they are identifiers rather than prose.
	Options []string `json:"options,omitempty"`
	// OptionLabels gives those values the words an operator reads. A value
	// absent here is shown as itself.
	//
	// Separate from Options because the two answer to different people. The
	// value is what configuration records and what code compares against, so
	// it should say what the setting does -- observium's backend is stored as
	// "api" or "database" because that is what changes. The label is what
	// somebody picking from a dropdown needs, and they are not choosing a
	// mechanism, they are saying which licence they have.
	OptionLabels map[string]string `json:"option_labels,omitempty"`
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
	// ShowWhen hides this field unless another field in the same form holds
	// one of the named values. Nil means always shown.
	//
	// It exists for the setting that selects between two ways of doing the
	// same job -- an integration that can read its upstream over an API or
	// straight from its database -- where a single flat form would show every
	// field for both and leave the operator to work out which half applies.
	ShowWhen *ShowWhen `json:"show_when,omitempty"`
	// Columns shape each row of a KindCollection. Ordinary fields, keyed bare
	// within the row: a string, a secret, a list, a number, a switch. The
	// first column is the row's identity -- what a person calls it and what
	// no two rows may share -- so it must be a required string.
	Columns []Field `json:"columns,omitempty"`
}

// Identity returns the column that names a row of a collection: the first
// one, by declaration.
func (f Field) Identity() (Field, bool) {
	if f.Kind != KindCollection || len(f.Columns) == 0 {
		return Field{}, false
	}
	return f.Columns[0], true
}

// ShowWhen makes a field's visibility depend on another field's value.
//
// **It is presentation, not enforcement.** A hidden field keeps whatever value
// it already had, that value is still submitted, and it is still stored. So
// nothing downstream may treat "hidden" as "unset": validation belongs in the
// plugin's own Validate, which sees the whole configuration and is the only
// thing positioned to say that a database password is required *because* the
// backend is a database. A form that hides a field is helping somebody find
// the right box, not deciding what a valid configuration is.
//
// Required interacts with this the same way. A field that is required but
// hidden is not one the operator can fill in, so the form does not block on
// it and the plugin decides.
type ShowWhen struct {
	// Field is the key of the field this one depends on, in the same form.
	Field string `json:"field"`
	// Equals lists the values of that field which reveal this one. Empty
	// reveals nothing, which is refused at declaration rather than shipped as
	// a field nobody can see.
	Equals []string `json:"equals"`
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
	// SectionChatGPT is the page holding the ChatGPT accounts. The tunnel's
	// own switches belong beside the accounts they apply to.
	SectionChatGPT = "chatgpt"
	// SectionApprovals is the page holding the approval rules. How long a
	// suggestion lives and how much may be settled in the conversation are
	// the same subject as the rules themselves, and were a page away from
	// them.
	SectionApprovals = "approvals"
	// SectionAdvanced is what an operator changes while diagnosing something,
	// not while setting the host up: how patient the listeners are, and how
	// the database trades durability for speed.
	SectionAdvanced = "advanced"
	// SectionDiagnostics is what this host says about itself and to whom --
	// how much it logs, and whether a crash leaves the machine.
	SectionDiagnostics = "diagnostics"
	// SectionBackup is the Backup & Restore page. The schedule belongs beside
	// the destinations it sends to and the history it produces, which are on
	// that page and not in a general settings list.
	SectionBackup = "backup"
	// SectionAuthentication is the page deciding who can sign in and how.
	// Separate from the general settings page because the pending queue lives
	// beside these fields and answers the question they raise.
	SectionAuthentication = "authentication"
)

// Setting keys. They are namespaced by group so a key cannot collide and so
// history reads clearly.
const (
	KeyTunnelEnabled = "tunnel.enabled"
	// KeyTunnelID held the tunnel serving every plugin at once, before every
	// tunnel was keyed by its own id. Read once at startup to move it onto
	// that key, and never written again.
	//
	// Deliberately not a settings field, for the same reason as
	// PluginTunnelKey below: tunnels are made and assigned on the Tunnels
	// page, where the id comes from the tunnel that was just created. A text
	// box asking an operator to paste one back in asks them to copy a value
	// the app already has, and it appeared before there was anything to paste.
	//
	// A deployment that would rather not hand mcpd an admin key can still set
	// tunnel.tunnel_id in config.yaml, which this falls back to.
	KeyTunnelID = "tunnel.tunnel_id"
	// KeyTunnelAccount names the ChatGPT account the aggregate tunnel
	// connects with.
	//
	// The account itself lives in `chatgpt_accounts`; this is the assignment,
	// and it is here beside KeyTunnelID because the two are one decision made
	// on one page. Empty means the only account, when there is exactly one --
	// which is what a deployment that has never thought about accounts has,
	// and it should not have to.
	KeyTunnelAccount = "tunnel.account"

	// The single set of OpenAI credentials that predated accounts.
	//
	// No longer editable fields, and no longer read to run anything. They are
	// kept as keys because the first start after the upgrade reads them once
	// to seed an account from them -- see the seeding in internal/app -- and
	// because a config.yaml that still names them has to be recognised in
	// order to be warned about.
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
	// KeyTunnelRole and KeyTunnelPlugins are what a connector may do and
	// reach. Both moved onto the account, so that two workspaces sharing this
	// host can be granted differently; they survive here for the same reason
	// the credentials above do -- the upgrade seeds an account from them.
	KeyTunnelRole    = "tunnel.role"
	KeyTunnelPlugins = "tunnel.plugins"
	KeyTunnelUpdates = "tunnel.check_for_updates"
	KeyTunnelDebug   = "tunnel.debug"

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

	// Whether every tool call is written down, and for how long. See the group
	// below for why this is separate from the history retention beside it.
	// An operator's own catalogue of servers they permit here, fetched as a
	// tarball from wherever they keep it under review.
	KeyCatalogRepoURL   = "catalog.repo_url"
	KeyCatalogRepoToken = "catalog.repo_token"
	KeyCatalogRepoHours = "catalog.repo_refresh_hours"

	// Where this host sends its own events, and in what shape. Empty means
	// nowhere, which is the default; see the group below.
	KeyNotifyURL    = "notifications.url"
	KeyNotifyFormat = "notifications.format"
	KeyNotifyTopic  = "notifications.topic"
	KeyNotifyToken  = "notifications.token"

	KeyCallsRecord        = "calls.record"
	KeyCallsRetentionDays = "calls.retention_days"

	// How often this host re-asks each remote MCP server what it offers. See
	// the group below for why it is on by default when the update check is not.
	KeyDiscoveryIntervalHours = "mcpservers.rediscovery_interval_hours"

	// The host's own runtime configuration. These were once keys in
	// config.yaml; the file no longer supplies them, and the database is the
	// only authority for what they are. See docs/architecture.md, "Where
	// configuration lives".
	//
	// A duration counts whole units of its own Unit rather than carrying a Go
	// duration string, so the number in the box is the number that was typed.
	// Updates. mcpd is deployed inside somebody's network, so asking a
	// public service what the current version is has to be a choice rather
	// than a default -- the same reasoning the tunnel's own update check
	// follows.
	KeyUpdatesEnabled  = "updates.check_enabled"
	KeyUpdatesInterval = "updates.check_interval_hours"
	KeyUpdatesRepo     = "updates.repository"

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
	KeyErrorsDSN         = "errors.dsn"
	KeyErrorsEnvironment = "errors.environment"
	KeyErrorsLabel       = "errors.instance_label"
	KeyErrorsMessages    = "errors.include_messages"
	KeyErrorsTraceRate   = "errors.traces_sample_rate"

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

	// KeyApprovalUnmatched is what happens to a change no rule covers.
	//
	// The lowest-precedence thing in the model: an exclusion still wins
	// outright and a matching grant still decides, so a carve-out written for
	// one dangerous action keeps working whatever this says.
	KeyApprovalUnmatched = "approval.unmatched"

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

	// When a backup happens, and what seals it. There is one schedule and one
	// passphrase, so both are settings in the ordinary sense; the destinations
	// they apply to are a collection and live in their own table.
	KeyBackupScheduleEnabled  = "backup.schedule.enabled"
	KeyBackupScheduleCadence  = "backup.schedule.cadence"
	KeyBackupScheduleWeekday  = "backup.schedule.weekday"
	KeyBackupScheduleTime     = "backup.schedule.time"
	KeyBackupScheduleTimezone = "backup.schedule.timezone"
	KeyBackupPassphrase       = "backup.passphrase"
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

// PluginTunnelAccountKey names the ChatGPT account a per-plugin tunnel
// connects with. Empty means the only account, as with KeyTunnelAccount.
func PluginTunnelAccountKey(plugin string) string {
	return "tunnel.plugin." + plugin + ".account"
}

// A tunnel's own assignment: which plugin it serves and whose credential it
// connects with.
//
// Keyed by the tunnel rather than by the plugin, and that is the whole point.
// Keying by plugin allowed exactly one tunnel per plugin, so pointing a second
// ChatGPT account at a plugin silently overwrote the first account's binding --
// the first account did not lose access, it lost its tunnel, and mcpd stopped
// running it with nothing said. A tunnel is the thing that carries one identity
// and serves one plugin, so a tunnel is the thing to key on. Several tunnels
// may now name the same plugin, which is what lets two workspaces share one
// integration without duplicating its configuration.
const tunnelKeyPrefix = "tunnel."

// TunnelEverything is the plugin value for a tunnel serving every system its
// account's grant allows. Spelled out rather than left as the empty string,
// because empty is what an unassigned tunnel holds, and one value meaning
// both "everything" and "nothing" was how the aggregate tunnel came to need
// a second pair of keys of its own.
const TunnelEverything = "*"

// TunnelPluginKey names the plugin a tunnel serves: a plugin name,
// TunnelEverything, or empty for a tunnel nothing is using.
func TunnelPluginKey(tunnelID string) string {
	return tunnelKeyPrefix + tunnelID + ".plugin"
}

// TunnelAccountKey names the ChatGPT account a tunnel connects with.
func TunnelAccountKey(tunnelID string) string {
	return tunnelKeyPrefix + tunnelID + ".account"
}

// TunnelIDFromKey reverses TunnelPluginKey, returning "" for anything else.
//
// The suffix is checked as well as the prefix because "tunnel." also begins
// the settings that configure tunnelling generally -- tunnel.enabled,
// tunnel.plugins -- and reading one of those as a tunnel id would invent a
// tunnel called "enabled".
func TunnelIDFromKey(key string) string {
	const suffix = ".plugin"
	if !strings.HasPrefix(key, tunnelKeyPrefix) || !strings.HasSuffix(key, suffix) {
		return ""
	}
	id := key[len(tunnelKeyPrefix) : len(key)-len(suffix)]
	// "tunnel.plugin.<name>.plugin" is not a thing, but the old key space
	// shares this prefix and must not be mistaken for the new one.
	if id == "" || strings.Contains(id, ".") {
		return ""
	}
	return id
}

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
					Help: "The address assistants connect to, including https:// and " +
						"the port. Leave it empty and mcpd guesses from the address " +
						"this page is on, which is right on one network and wrong " +
						"through a proxy. A certificate is issued for this address, " +
						"so changing it needs a restart.",
				},
				{
					Key: KeyServerFrontendPublicURL, Label: "Address this page is on",
					Kind: KindString, Group: "server", Apply: ApplyLive,
					Placeholder: "https://mcpd.example.net",
					// Led with the consequence. This field used to open by
					// saying it was only for a TLS-terminating proxy, and to
					// call empty the right answer for reaching mcpd directly --
					// which is true of the thing it was named for and false of
					// the thing it now decides. An operator who read it, left
					// it empty and turned on a provider got a button that never
					// appeared and no message saying why.
					Help: "This dashboard as a browser reaches it. Required for " +
						"signing in with a provider, which sends people back here; " +
						"while it is empty no provider is offered. Otherwise needed " +
						"only when something in front of mcpd terminates TLS.",
				},
				{
					Key: KeyServerTLSMode, Label: "Certificate for the address assistants use",
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
					Help: "Turning it off leaves assistants connected and leaves you " +
						"nothing to administer this host from, until you turn it " +
						"back on in the database by hand.",
				},
			},
		},
		{
			Name:    "updates",
			Title:   "Updates",
			Section: SectionSettings,
			Help: "Whether this host asks GitHub what the current release is. " +
				"It never installs anything: replacing a running mcpd is a " +
				"deployment's own job, and a container that could rewrite " +
				"itself would need privileges this one deliberately drops.",
			Fields: []Field{
				{
					Key: KeyUpdatesEnabled, Label: "Check for updates",
					Kind: KindBool, Group: "updates", Apply: ApplyLive,
					Default: false,
					Help: "Off by default. Turning it on makes this host reach " +
						"github.com on a timer, which on an isolated network is a " +
						"connection somebody should agree to rather than discover.",
				},
				{
					Key: KeyUpdatesInterval, Label: "How often to check",
					Kind: KindInt, Group: "updates", Apply: ApplyLive,
					Default: 24, Min: intPtr(1), Max: intPtr(720),
					Unit: "hours",
					Help: "Releases are not frequent enough for this to be worth " +
						"doing often, and the answer is cached between checks.",
				},
				{
					Key: KeyUpdatesRepo, Label: "Repository",
					Kind: KindString, Group: "updates", Apply: ApplyLive,
					Default:     "dreulavelle/mcpd",
					Placeholder: "owner/name",
					Help: "Where releases are published. Change it only if you " +
						"build mcpd from a fork.",
				},
			},
		},
		{
			Name:      "tunnel",
			Title:     "ChatGPT",
			Section:   SectionChatGPT,
			EnabledBy: KeyTunnelEnabled,
			Help:      "Lets ChatGPT reach mcpd without opening anything to the internet.",
			Fields: []Field{
				{
					Key: KeyTunnelEnabled, Label: "Let ChatGPT connect",
					Kind: KindBool, Group: "tunnel", Apply: ApplyReconnect,
					Default: false,
				},
				// The credentials, the role and the plugin grant are not
				// fields here any more. They belong to an account, because a
				// host serving two ChatGPT workspaces has two of each, and a
				// single text box cannot hold two -- see the ChatGPT page,
				// which edits the accounts themselves. What is left in this
				// group is what genuinely is one value for the whole host.
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
			Name:    "mcpservers",
			Title:   "Remote MCP servers",
			Section: SectionSettings,
			Help: "How often this host re-checks what the servers it has " +
				"imported are offering.",
			Fields: []Field{
				{
					Key: KeyDiscoveryIntervalHours, Label: "Re-check what they offer",
					Kind: KindInt, Group: "mcpservers", Apply: ApplyLive,
					Default: 24, Min: intPtr(0), Max: intPtr(720),
					Unit: "hours",
					// On by default, unlike the update check, and the difference
					// is who is being contacted. That reaches a public service
					// this deployment has no relationship with, so it has to be
					// agreed to. This re-asks servers the operator imported
					// deliberately and that mcpd already dials on every tool
					// call -- no new destination, no new disclosure, and the
					// alternative is a tool changing under an approval that was
					// given for something else.
					Help: "Hours. A server that adds, withdraws or rewrites a tool " +
						"is only noticed when somebody looks, so this looks on a " +
						"timer. A changed tool stops being served until it is " +
						"approved again. Zero checks only when you press Discover.",
				},
			},
		},
		{
			Name:    "catalog",
			Title:   "Your own catalogue",
			Section: SectionSettings,
			Help: "A list of the servers you permit here, kept where you keep " +
				"things under review. The public catalogues answer what exists " +
				"in the world; this answers what is allowed on this host.",
			Fields: []Field{
				{
					Key: KeyCatalogRepoURL, Label: "Archive address",
					Kind: KindString, Group: "catalog", Apply: ApplyLive,
					Placeholder: "https://api.github.com/repos/you/catalog/tarball/main",
					Help: "A gzipped tar archive holding server.json documents. " +
						"GitHub serves one at /repos/{owner}/{repo}/tarball/{ref}; " +
						"GitLab and Gitea have their own archive paths, and any " +
						"file server will do. Empty adds no catalogue.",
				},
				{
					Key: KeyCatalogRepoToken, Label: "Token",
					Kind: KindSecret, Group: "catalog", Apply: ApplyLive,
					Help: "For a private repository. Sent as a bearer credential, " +
						"and dropped if the address redirects to another host.",
				},
				{
					Key: KeyCatalogRepoHours, Label: "Re-read every",
					Kind: KindInt, Group: "catalog", Apply: ApplyLive,
					Default: 6, Min: intPtr(0), Max: intPtr(720),
					Unit: "hours",
					Help: "Hours. A fetch that fails leaves the previous list in " +
						"place, so a git host being down does not empty your " +
						"catalogue. Zero re-reads only on a restart.",
				},
			},
		},
		{
			Name:    "notifications",
			Title:   "Notifications",
			Section: SectionDiagnostics,
			Help: "Off until you fill in an address. mcpd will tell you when a " +
				"remote server changes a tool, when a change runs without " +
				"anybody being asked, and when something stops working. It " +
				"never asks you to approve anything: that happens where the " +
				"work is, in the conversation.",
			Fields: []Field{
				{
					Key: KeyNotifyURL, Label: "Send to",
					Kind: KindSecret, Group: "notifications", Apply: ApplyLive,
					Placeholder: "https://hooks.slack.com/services/…",
					// Secret rather than a plain string: a Slack or Discord
					// webhook URL is a bearer credential wearing an address's
					// clothes, and anybody holding it can post as this host.
					Help: "A webhook address. Treated as a credential, because a " +
						"Slack or Discord webhook URL is one -- anybody who has " +
						"it can post. Empty sends nothing.",
				},
				{
					Key: KeyNotifyFormat, Label: "Shape",
					Kind: KindEnum, Group: "notifications", Apply: ApplyLive,
					Default: "json", Options: []string{"json", "slack", "discord", "ntfy"},
					Help: "Discord posts an embed coloured by severity; paste the " +
						"webhook Discord gave you, with or without its /slack " +
						"suffix. Slack fits Slack and Mattermost. JSON is mcpd's " +
						"own event, for anything else.",
				},
				{
					Key: KeyNotifyTopic, Label: "ntfy topic",
					Kind: KindString, Group: "notifications", Apply: ApplyLive,
					Placeholder: "mcpd-alerts",
					Help:        "Only used by the ntfy shape.",
				},
				{
					Key: KeyNotifyToken, Label: "Token",
					Kind: KindSecret, Group: "notifications", Apply: ApplyLive,
					Help: "Sent as a bearer credential, for a receiver that wants " +
						"one. Slack and Discord do not; ntfy may.",
				},
			},
		},
		{
			Name:    "calls",
			Title:   "Call record",
			Section: SectionSettings,
			Help: "Who called what. Kept on this host and nowhere else, and " +
				"never including a call's arguments or its result.",
			Fields: []Field{
				{
					Key: KeyCallsRecord, Label: "Record tool calls",
					Kind: KindBool, Group: "calls", Apply: ApplyLive,
					Default: true,
					Help: "On, because the counters can say a tool was called four " +
						"hundred times and not who called it, and that is the " +
						"question an incident asks. A row names the caller, the " +
						"tool and how it ended -- never what was asked for.",
				},
				{
					Key: KeyCallsRetentionDays, Label: "Keep calls for",
					Kind: KindInt, Group: "calls", Apply: ApplyLive,
					Default: 30, Min: intPtr(0), Max: intPtr(3650),
					Unit: "days",
					Help: "Days. Separate from the history above because this " +
						"gains a row every time an assistant reads anything, and " +
						"that fills a disk faster than administrative history " +
						"does. Zero keeps everything.",
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
			Name:      "backup",
			Title:     "Scheduled backups",
			Section:   SectionBackup,
			EnabledBy: KeyBackupScheduleEnabled,
			Help: "mcpd takes one encrypted archive and sends it to every " +
				"destination that is switched on. Add a destination below first; " +
				"a schedule with nowhere to send a backup does nothing.",
			Fields: []Field{
				{
					Key: KeyBackupScheduleEnabled, Label: "Back up on a schedule",
					Kind: KindBool, Group: "backup", Apply: ApplyLive,
					Default: false,
					Help: "Off until you turn it on. Switching it on does not take a " +
						"backup straight away; the first one is at the next time below.",
				},
				{
					Key: KeyBackupScheduleCadence, Label: "How often",
					Kind: KindEnum, Group: "backup", Apply: ApplyLive,
					Default: "weekly", Options: []string{"daily", "weekly"},
					OptionLabels: map[string]string{"daily": "Every day", "weekly": "Every week"},
				},
				{
					Key: KeyBackupScheduleWeekday, Label: "Day",
					Kind: KindInt, Group: "backup", Apply: ApplyLive,
					Default: 0, Min: intPtr(0), Max: intPtr(6),
					ShowWhen: &ShowWhen{Field: KeyBackupScheduleCadence, Equals: []string{"weekly"}},
					Help:     "Sunday is 0.",
				},
				{
					Key: KeyBackupScheduleTime, Label: "Time",
					Kind: KindString, Group: "backup", Apply: ApplyLive,
					Default: "04:00", Placeholder: "04:00",
					Help: "As HH:MM, in the time zone below. Avoid anything between " +
						"01:00 and 03:00: those hours do not exist on the day the " +
						"clocks go forward, and a backup at one of them would be a " +
						"backup that quietly does not happen twice a year.",
				},
				{
					Key: KeyBackupScheduleTimezone, Label: "Time zone",
					Kind: KindString, Group: "backup", Apply: ApplyLive,
					Default: "UTC", Placeholder: "America/Chicago",
					Help: "A zone name, such as Europe/London or America/Chicago. " +
						"Stored rather than taken from the machine, so the time above " +
						"means the same thing all year.",
				},
				{
					Key: KeyBackupPassphrase, Label: "Passphrase",
					Kind: KindSecret, Group: "backup", Apply: ApplyLive,
					Help: "Write this down and keep it with the backup. It is stored " +
						"here so a scheduled backup can run when nobody is present, " +
						"and it will not be shown again. A host that has lost its " +
						"database cannot tell you what it was.",
				},
			},
		},
		{
			Name:    "errors",
			Title:   "Crash reporting",
			Section: SectionDiagnostics,
			Help: "Off unless you fill in an address below. mcpd runs on your " +
				"network and manages your equipment, so nothing about a crash " +
				"leaves this machine until you say where to send it.",
			Fields: []Field{
				{
					Key: KeyErrorsDSN, Label: "Where to send crashes",
					Kind: KindSecret, Group: "errors", Apply: ApplyRestart,
					Placeholder: "https://…@sentry.example.com/1",
					Help: "A Sentry or GlitchTip DSN. Empty is off, and off " +
						"means no client is built and nothing is sent — not a " +
						"client pointed at nowhere. Needs a restart.",
				},
				{
					Key: KeyErrorsEnvironment, Label: "Environment",
					Kind: KindString, Group: "errors", Apply: ApplyRestart,
					Default: "production", Placeholder: "production",
					Help: "Separates a test deployment from a real one in " +
						"whatever you are sending to.",
				},
				{
					Key: KeyErrorsLabel, Label: "Name this installation",
					Kind: KindString, Group: "errors", Apply: ApplyRestart,
					Placeholder: "nothing identifying is sent",
					Help: "Empty sends nothing that says which machine this is. " +
						"Sentry would otherwise use the hostname, which on a " +
						"deployment like this one names the site. Fill it in " +
						"only if whoever receives the crashes needs to tell " +
						"installations apart, and choose a label rather than a " +
						"real name.",
				},
				{
					Key: KeyErrorsMessages, Label: "Send error messages",
					Kind: KindBool, Group: "errors", Apply: ApplyRestart,
					Default: false,
					Help: "Off, the report carries the stack trace and the error " +
						"type — enough to find the code path, and structurally " +
						"unable to name a device, because Go does not put " +
						"argument values in a stack trace. On, the sentences go " +
						"too. They are scrubbed of addresses, credentials and " +
						"hostnames either way, but they are written to describe " +
						"your equipment, so sending them is its own decision.",
				},
				{
					Key: KeyErrorsTraceRate, Label: "Sample performance traces",
					Kind: KindInt, Group: "errors", Apply: ApplyRestart,
					Default: 0, Min: intPtr(0), Max: intPtr(100),
					Help: "Percent of requests to time and report. Zero is off " +
						"and is the right answer unless somebody is chasing a " +
						"specific slowness: a trace says more about what your " +
						"assistants are doing than about whether mcpd is broken.",
				},
			},
		},
		{
			Name:    "approval",
			Title:   "Approvals",
			Section: SectionApprovals,
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
					Key: KeyApprovalUnmatched, Label: "When no rule covers a change",
					Kind: KindEnum, Group: "approval", Apply: ApplyLive,
					// Allow, because the alternative puts a queue in this app
					// between an assistant and the person already talking to
					// it. The assistant is where the person is; asking them
					// there is asking them, and asking them twice is not
					// twice as safe.
					Default: "high", Options: []string{RiskNone, "low", "medium", "high"},
					Help: "Assistants ask before they change things. This decides " +
						"whether mcpd asks a second time. \"Nothing\" holds every " +
						"change for approval here; a level lets changes up to it " +
						"run when no rule says otherwise. A change that cannot be " +
						"undone is always held, whatever this says.",
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
			Section: SectionAdvanced,
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
					Help: "A tool call waiting on a slow system spends its time here, " +
						"so this is the one to raise if long calls are being cut off.",
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
			Section: SectionAdvanced,
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
			Section: SectionDiagnostics,
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
	case KindCollection:
		// Rows are edited one at a time, through their own endpoints, so a
		// credential in one row can be replaced without every other row's
		// being sent again. A whole-table write here has no honest way to say
		// "keep that secret".
		return fmt.Errorf("settings: %s is a table; add, edit or remove its rows "+
			"one at a time rather than writing it whole", f.Label)

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
		// The archive is encrypted with this and with nothing else, so a short
		// one is the whole of the protection. Checked here as well as where an
		// archive is written, because a passphrase stored now is used months
		// later by a worker nobody is watching.
		if key == KeyBackupPassphrase && value != "" && len(value) < backup.MinPassphrase {
			return fmt.Errorf(
				"settings: %s must be at least %d characters. It is the only thing "+
					"protecting the archive", f.Label, backup.MinPassphrase)
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
		// A time of day and a zone name, checked here so a typo is a message
		// beside the box rather than a schedule that silently runs at 04:00 UTC
		// on a host nobody looks at.
		if key == KeyBackupScheduleTime {
			if _, _, err := backup.ParseClock(value); err != nil {
				return fmt.Errorf("settings: %s must be a time of day, written as "+
					"HH:MM -- for example 04:00", f.Label)
			}
		}
		if key == KeyBackupScheduleTimezone && strings.TrimSpace(value) != "" {
			if _, err := time.LoadLocation(strings.TrimSpace(value)); err != nil {
				return fmt.Errorf("settings: %s is not a time zone this build "+
					"knows. Use a zone name such as Europe/London or "+
					"America/Chicago", f.Label)
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

// ValidateValue checks a value against a field that is not in any catalog --
// a column of a collection, whose key is bare and whose row is the scope.
func ValidateValue(f Field, value string) error {
	return validateAgainst(f, f.Key, value)
}
