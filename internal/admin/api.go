// Package admin serves the operator dashboard: a JSON API and the static
// single-page application that consumes it.
//
// It runs on its own listener, separate from the MCP endpoint. The two have
// different audiences and different exposure -- agents reach MCP over a
// tunnel, while the dashboard is for operators on an internal interface -- and
// separating them means a firewall rule can tell them apart.
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/sso"
	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/cachestore"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/tunnel"
	"github.com/spoked/mcpd/internal/updates"
)

// Options configures the dashboard.
type Options struct {
	Log        *slog.Logger
	Verifier   auth.TokenVerifier
	Authorizer *auth.Authorizer
	Approval   *operations.ApprovalPolicy
	Service    *operations.Service
	Repo       operations.Repository
	Manager    *plugins.Manager
	Health     *observability.HealthRegistry
	// Errors reports panics off the machine when an operator has configured
	// somewhere to send them. Nil means off, and every method on it is safe on
	// nil so no call site branches.
	Errors  *observability.ErrorReporter
	Version string
	// StartedAt is when this process began serving, for the uptime the
	// resources panel reports.
	StartedAt time.Time
	// Updates answers what the current release is, or is nil when this build
	// has no checker wired.
	Updates *updates.Checker
	// Restart asks the host to stop cleanly so its supervisor starts it
	// again. Nil when nothing is supervising, in which case the dashboard is
	// told the button would not work rather than being offered one that
	// stops mcpd for good.
	Restart func(reason string) error

	// Bypasses are the windows in which this host stops asking, or nil when
	// they are not configured.
	Bypasses Bypasses

	// BypassOpened is told when a window is opened, so the host can announce
	// it. A function rather than the notifier itself: this package should be
	// able to report the event without being able to invent others.
	BypassOpened func(ctx context.Context, b *operations.Bypass)

	// NotifyTest sends one event to whatever address is configured, for the
	// button that answers "did I type the URL correctly".
	NotifyTest func(ctx context.Context) error

	// Calls is the record of who called what, or nil when this host is not
	// keeping one.
	Calls CallLedger

	// Backup writes and restores this whole instance as one encrypted file.
	// Nil leaves the routes answering "not configured" rather than offering a
	// page whose every button fails.
	Backup BackupService

	// Metrics serves the Prometheus exposition format, or is nil when the
	// endpoint is switched off. MetricsPublic serves it unauthenticated.
	Metrics       http.Handler
	MetricsPublic bool
	// Performance reports the same collectors Metrics serves, shaped for the
	// console. A function rather than the metrics type so this package keeps
	// knowing nothing about Prometheus; nil means nothing is being collected.
	Performance func() observability.Performance
	// Logs is the copy of this host's log kept for the dashboard, or nil when
	// the host is not keeping one.
	Logs *observability.LogStream
	// Audit reads the append-only trail.
	Audit AuditReader

	// Pruner removes history past its retention, or all of it. It is separate
	// from AuditReader because reading and removing are different rights.
	Pruner AuditPruner

	// PluginTypes lists the integrations this build has, so the dashboard can
	// offer them when someone adds an instance.
	PluginTypes func() []PluginTypeInfo
	// AddPlugin, RemovePlugin, RestorePlugin and SetPluginEnabled manage
	// instances. Adding one records intent -- it mounts once it has what it
	// needs, which is what the response says rather than leaving someone to
	// wonder why the tools never appear.
	//
	// RemovePlugin's last argument acknowledges that the instance is marked
	// `required: true` in the configuration file. Without it a removal of one
	// is refused, because that flag is the deployment saying the host should
	// not run without the integration and clicking past it should be a
	// deliberate act rather than a side effect of confirming something else.
	//
	// RestorePlugin undoes a removal that overrode the file. There is one
	// because a one-way door an operator can only reopen over SSH is the
	// problem this replaced, moved rather than solved.
	AddPlugin        func(ctx context.Context, actor, name, typeName string) error
	RemovePlugin     func(ctx context.Context, actor, name string, acknowledgeRequired bool) error
	RestorePlugin    func(ctx context.Context, actor, name string) error
	SetPluginEnabled func(ctx context.Context, actor, name string, enabled bool) error
	// Instances lists what is configured, mounted or not.
	Instances func(ctx context.Context) []PluginInstanceInfo
	// StaleRemovals lists removals whose configuration-file declaration has
	// since gone, which are otherwise invisible and would silently apply if
	// the name were declared again.
	StaleRemovals func(ctx context.Context) []StaleRemoval

	// PluginType reports what integration an instance is of, which is not the
	// instance's own name once someone configures two of something.
	PluginType func(instance string) string

	// Catalog returns every setting the running host has, its own and the
	// configured plugin instances' alike. A function because instances change
	// while the host runs, so a value captured at construction would describe
	// the host as it started rather than as it is.
	Catalog func() *settings.Catalog

	// Accounts backs the sign-in form and the Users page. It is separate from
	// Verifier because the two authenticate different callers: a person with a
	// password and a session, or a script with a bearer token.
	Accounts Accounts

	// Identities backs registration, the pending queue, and the providers an
	// account can sign in with. Separate from Accounts because the two answer
	// different questions; see Registrations.
	Identities Registrations

	// Groups grant plugin access. They are the second half of the one
	// authorization model: a role says what a caller may do, a group says what
	// it may reach, and both an account and an API key draw their reach the
	// same way.
	Groups Groups

	// Keys are the bearer credentials this host issued itself. Nil leaves the
	// routes answering "not configured" rather than panicking.
	Keys Keys

	// Certificates are the roots this host trusts in addition to the system
	// ones, for upstreams behind a company authority or an appliance's own
	// certificate. Nil leaves the routes answering "not configured".
	Certificates Certificates

	// TrustChanged tells the host that set has changed, so it can rebuild the
	// pool and remount what was using the old one. Without it a certificate
	// would be stored and trusted by nothing until the next restart, which is
	// the confusion the page exists to remove.
	TrustChanged func(ctx context.Context)

	// KeyGrants resolves what a key reaches, through the same union an account
	// goes through. A function rather than a method on Keys, because the union
	// belongs to the groups package and nothing here should be able to compute
	// a second answer.
	KeyGrants func(ctx context.Context, keyID string) ([]string, error)

	// SSO runs the provider flows, or is nil when this build was wired
	// without them. Nil is a refusal rather than a panic: the sign-in page
	// asks what is available and is told nothing.
	SSO *sso.Service

	// RegistrationPolicy is what this host will accept from a stranger, read
	// per request rather than captured at startup so that turning
	// registration off takes effect on the next attempt.
	RegistrationPolicy func(ctx context.Context) users.RegistrationPolicy

	// SessionTTL bounds a browser signed in from now on. Zero leaves the
	// store's default.
	//
	// Read per sign-in rather than captured at startup, for the same reason as
	// the addresses below: it is a setting somebody can change, and a copy
	// taken when the process started would be the value the host was booted
	// with rather than the value it is configured with.
	SessionTTL func(ctx context.Context) time.Duration

	// PublicURL is the address clients reach, used to render a connect URL an
	// operator can copy rather than assemble.
	PublicURL func(ctx context.Context) string
	// FrontendPublicURL is how a browser reaches this dashboard, when something
	// in front of this process terminates TLS. Empty means the connection
	// decides. It is deliberately not PublicURL: that is the MCP endpoint, a
	// different listener that routinely differs in scheme.
	FrontendPublicURL func(ctx context.Context) string

	// PluginSettings returns a plugin's configuration block for display.
	// Values that name credentials are withheld before they are sent.
	PluginSettings func(plugin string) map[string]any

	// AuthMode names the configured authentication mode, so the setup guide
	// can show the steps that actually apply.
	AuthMode string

	// CACertificate returns the certificate authority mcpd issued its own
	// certificate from, or nil when it is not serving HTTPS itself.
	//
	// Offering it for download is the difference between a browser warning an
	// operator has to click past every time and one they resolve once.
	CACertificate func() []byte

	// Tunnel exposes the embedded tunnel so an operator can see its state and
	// start or stop it without restarting mcpd.
	Tunnel TunnelController

	// TunnelInfo reports the embedded tunnel client version against the newest
	// release.
	TunnelInfo func() any

	// Directory manages tunnels in one ChatGPT account's organisation. It
	// takes an account id because a tunnel is created inside an organisation
	// and two accounts are two organisations: a directory built from whichever
	// admin key came first would list one workspace's tunnels and offer to
	// delete them from another's. An empty id means the only account, when
	// there is exactly one.
	//
	// A function rather than a value because the admin key is stored, and one
	// captured at startup would be the key the deployment began with rather
	// than the one an operator just saved.
	Directory func(accountID string) *tunnel.Directory

	// The ChatGPT accounts tunnels connect with. Each carries a credential, an
	// identity and a grant, so adding or editing one is an administrative act
	// and every route below is gated accordingly.
	//
	// Nil leaves the pages reporting that accounts are unavailable, which is
	// what a host with no encryption key has: it cannot store a credential and
	// should say so rather than offering a form that will fail on save.
	ChatGPTAccounts      func(ctx context.Context) ([]tunnel.Account, error)
	AddChatGPTAccount    func(ctx context.Context, actor string, a tunnel.Account) (tunnel.Account, error)
	UpdateChatGPTAccount func(ctx context.Context, actor, id string, up tunnel.AccountUpdate) (tunnel.Account, error)
	RemoveChatGPTAccount func(ctx context.Context, actor, id string) error

	// AccountAssignments maps a tunnel id to the ChatGPT account it connects
	// with. Separate from Assignments because they answer different questions
	// -- which system a tunnel serves, and whose credential it uses -- and a
	// tunnel can have one without the other.
	AccountAssignments func() map[string]string

	// Plugins names the mounted systems, so a tunnel can be assigned to one.
	Plugins func() []string

	// Assignments reports which system each tunnel is pointed at, by tunnel
	// id, with "" meaning every system at once.
	//
	// Read from the stored configuration rather than from what is running,
	// because those differ: a tunnel assigned to a plugin that is not mounted
	// does not run, and a page that showed only running tunnels reported the
	// assignment as absent -- so the choice appeared to revert, and the reason
	// it was not honoured was nowhere.
	Assignments func() map[string]string

	// Settings is the runtime configuration store.
	Settings *settings.Store

	// Bootstrap describes the settings that cannot be edited here, so the UI
	// can show them read-only rather than pretending they do not exist.
	Bootstrap func() []BootstrapSetting

	// MCPServers manages remote MCP servers: importing one, discovering what
	// it offers, and classifying each tool before any of them is served.
	MCPServers MCPServerAPI

	// ServerCatalog browses a public registry of MCP servers, so an operator can
	// pick one rather than hand-author a server.json. It only finds
	// documents; importing one goes through MCPServers like any other paste.
	ServerCatalog CatalogAPI
}

// BootstrapSetting is a value that must be known before the database opens and
// therefore cannot be stored in it.
type BootstrapSetting struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
	Help  string `json:"help,omitempty"`
}

// AuditPruner removes history. The removal is itself recorded.
type AuditPruner interface {
	Prune(ctx context.Context, actor string, cutoff, now time.Time) (int64, error)
}

// TunnelController is the slice of the tunnel group the dashboard needs.
//
// Status returns one entry per connector, because a tunnel forwards to exactly
// one MCP endpoint: a deployment giving a system its own connector runs a
// tunnel per system, and each has its own state worth showing.
type TunnelController interface {
	Status() []tunnel.Status
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Enabled() bool
	// Restart stops one tunnel and starts it again, rebuilt against the
	// plugins as they are now.
	Restart(ctx context.Context, tunnelID string) error
}

// AuditReader is the slice of the audit store the dashboard needs.
type AuditReader interface {
	Recent(ctx context.Context, limit int) ([]operations.AuditRecord, error)
	ByOperation(ctx context.Context, operationID string) ([]operations.AuditRecord, error)
	VerifyChain(ctx context.Context) (int64, error)
}

// denyAllVerifier refuses every credential. It stands in for a missing
// verifier so a misconfiguration fails closed and loudly.
type denyAllVerifier struct{}

func (denyAllVerifier) Scheme() string { return "unconfigured" }

func (denyAllVerifier) Verify(context.Context, string, *http.Request) (*auth.Principal, error) {
	return nil, auth.ErrUnauthenticated
}

// Server is the dashboard handler.
type Server struct {
	opts Options
	mux  *http.ServeMux

	// tunnelCache holds each account's tunnel listing for a few seconds, and
	// tunnelGroup collapses concurrent fetches of the same one. The dashboard
	// polls this endpoint, so without them every poll was a request to OpenAI
	// per configured account -- and switching between accounts in the form was
	// a fresh round trip each time.
	tunnelCache *cachestore.Store
	tunnelGroup *cachestore.Group
}

// NewServer builds the dashboard.
func NewServer(opts Options) *Server {
	// A nil logger would panic on the first request rather than at
	// construction, which is the worst place to discover it.
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Authorizer == nil {
		opts.Authorizer = auth.NewAuthorizer()
	}
	// One entry per ChatGPT account, and a host has a handful.
	tunnelCache := cachestore.New(32)
	// A missing verifier must deny everything rather than panic on the first
	// request. Failing open here would expose the whole dashboard.
	if opts.Verifier == nil {
		opts.Log.Error("dashboard has no token verifier configured; all requests will be refused")
		opts.Verifier = denyAllVerifier{}
	}
	s := &Server{opts: opts, mux: http.NewServeMux()}
	s.tunnelCache = tunnelCache
	s.tunnelGroup = &cachestore.Group{}
	s.routes()
	return s
}

func (s *Server) routes() {
	api := func(path string, h http.HandlerFunc, required auth.Capability) {
		s.mux.Handle(path, s.authenticate(required, h))
	}

	// Unauthenticated: the login page needs to know how to authenticate before
	// it can authenticate.
	s.mux.HandleFunc("GET /api/meta", s.handleMeta)

	// Signing in cannot require being signed in. Signing out is deliberately
	// here too: it authenticates by presenting the cookie it is about to
	// destroy, and refusing an already-invalid session would leave the browser
	// holding a cookie it cannot get rid of.
	s.mux.HandleFunc("POST /api/session", s.handleSignIn)
	// Claiming an unclaimed instance. It refuses once any account exists.
	s.mux.HandleFunc("POST /api/setup", s.handleRegisterFirst)
	s.mux.HandleFunc("DELETE /api/session", s.handleSignOut)
	s.mux.HandleFunc("GET /api/session", s.handleCurrentSession)

	// What the signed-out page may offer: which providers have buttons, and
	// whether there is a sign-up form. Both are discoverable by trying, and
	// withholding them would only mean a page that offers what does not work
	// or hides what does.
	s.mux.HandleFunc("GET /api/auth/options", s.handleAuthOptions)
	// Asking for an account. It is not POST /api/setup: that claims an
	// unclaimed instance and makes an administrator, while this asks for an
	// ordinary account on a host somebody already owns -- and is refused
	// outright when nobody does.
	s.mux.HandleFunc("POST /api/register", s.handleRegister)
	// Signing in through a provider cannot require being signed in. What
	// bounds these is the state: single-use, expiring, and bound to a cookie
	// this host set on the browser that started the flow. A callback that
	// cannot present both is refused.
	s.mux.HandleFunc("POST /api/auth/sso/{provider}/start", s.handleSSOStart)
	s.mux.HandleFunc("GET /api/auth/sso/{provider}/callback", s.handleSSOCallback)

	// Unauthenticated by necessity and harmless by nature: a CA certificate is
	// a public document, and requiring a sign-in to fetch it would mean
	// needing the browser to trust it before it can be trusted.
	s.mux.HandleFunc("GET /api/tls/ca", s.handleCACertificate)

	// Metrics live here rather than beside /health/ready on the MCP listener.
	// That listener is the one a third party reaches through a tunnel, and
	// these series name every plugin, every tool, and how long each upstream
	// takes -- which is exactly the operational detail an unauthenticated
	// readiness probe is careful not to carry. This listener already has the
	// right audience and the right exposure.
	//
	// `read` rather than `admin`: it is a read of this host's own state, which
	// is what read means everywhere else here, and a scraper presents a static
	// token like any other machine caller. MetricsPublic drops the check for a
	// deployment that has fenced the port off instead.
	//
	// Registered even when the endpoint is off, so a scrape config pointing at
	// it gets a 404 rather than the single-page application's own shell, which
	// the catch-all below would otherwise hand back with a 200 and leave
	// somebody reading a parse error.
	switch {
	case s.opts.Metrics == nil:
		s.mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "metrics are not enabled", http.StatusNotFound)
		})
	case s.opts.MetricsPublic:
		s.mux.Handle("GET /metrics", s.opts.Metrics)
	default:
		s.mux.Handle("GET /metrics", s.authenticate(auth.CapRead, s.opts.Metrics.ServeHTTP))
	}

	api("GET /api/operations", s.handleListOperations, auth.CapRead)
	api("GET /api/operations/{id}", s.handleGetOperation, auth.CapRead)
	api("POST /api/operations/{id}/approve", s.handleApprove, auth.CapApprove)
	api("POST /api/operations/{id}/reject", s.handleReject, auth.CapApprove)
	api("POST /api/operations/{id}/cancel", s.handleCancel, auth.CapPropose)
	api("GET /api/plugins", s.handleListPlugins, auth.CapRead)
	api("GET /api/endpoints", s.handleEndpoints, auth.CapRead)
	api("GET /api/tunnel", s.handleTunnelStatus, auth.CapRead)
	api("GET /api/settings", s.handleGetSettings, auth.CapRead)
	// Changing configuration is an administrative act, and it is recorded
	// against the principal who made it.
	api("PUT /api/settings", s.handlePutSettings, auth.CapAdmin)
	api("GET /api/settings/history", s.handleSettingsHistory, auth.CapAdmin)
	// The standing rules that decide which changes are authorised without
	// asking anybody. Reading is an operator's business -- "why was I not
	// asked" is a question they have to be able to answer -- while writing
	// decides when the gate is skipped, which is administrative.
	api("GET /api/approval-policy", s.handleGetApprovalPolicy, auth.CapRead)
	api("PUT /api/approval-policy", s.handlePutApprovalPolicy, auth.CapAdmin)
	// Answering "which rule would apply" computes over configuration and
	// changes nothing, so it needs no more than reading the rules does.
	api("POST /api/approval-policy/evaluate", s.handleEvaluateApprovalPolicy, auth.CapRead)
	// Whether anything is unsupervised right now is readable by anyone who can
	// see the approval queue: a window nobody can see is worse than no window.
	// Opening or closing one decides when the gate is skipped, which is the
	// same right as editing the rules.
	api("GET /api/approval-policy/bypass", s.handleBypassStatus, auth.CapRead)
	api("POST /api/approval-policy/bypass", s.handleOpenBypass, auth.CapAdmin)
	api("DELETE /api/approval-policy/bypass", s.handleRevokeBypasses, auth.CapAdmin)

	// Sending a test reaches an outside service with this host's own address
	// in hand, which is an administrator's act even though what it sends
	// carries nothing privileged.
	api("POST /api/notifications/test", s.handleTestNotification, auth.CapAdmin)
	// Starting and stopping a tunnel changes what an external service can
	// reach, so it takes administrator rights rather than read.
	// Version, resources and releases are readable by anyone who may read.
	// None of it is sensitive and all of it is the first thing asked when
	// something looks wrong.
	api("GET /api/resources", s.handleResources, auth.CapRead)
	api("GET /api/performance", s.handlePerformance, auth.CapRead)
	api("GET /api/updates", s.handleUpdates, auth.CapRead)
	// Forcing a check reaches an external service, so it is an admin action
	// even though what it returns is not privileged.
	api("POST /api/updates/check", s.handleCheckUpdates, auth.CapAdmin)
	api("POST /api/restart", s.handleRestart, auth.CapAdmin)

	// A backup is the whole instance in one file: every account, every group's
	// reach, every stored credential. Reading what one *would* hold is already
	// enough to describe this host's shape, and taking one exports it, so both
	// are administrator's work rather than an operator's.
	// Who called what. Administrator for the same reason the log is: a row
	// names which systems were reached and by whom, which is a wider view than
	// any one account's own work.
	api("GET /api/calls", s.handleListCalls, auth.CapAdmin)
	api("GET /api/calls/callers", s.handleListCallers, auth.CapAdmin)

	api("GET /api/backup", s.handleBackupStatus, auth.CapAdmin)
	api("POST /api/backup", s.handleCreateBackup, auth.CapAdmin)
	api("POST /api/backup/restore", s.handleStageRestore, auth.CapAdmin)
	api("DELETE /api/backup/restore", s.handleCancelRestore, auth.CapAdmin)

	// The ChatGPT accounts tunnels connect with. Reading the list is an
	// operator's business -- it holds no credential, only whether one is set --
	// while adding or editing one hands a whole ChatGPT workspace an identity
	// and a grant on this host, which is an administrator's decision and is
	// written into the hash-chained trail.
	api("GET /api/chatgpt/accounts", s.handleListChatGPTAccounts, auth.CapRead)
	api("POST /api/chatgpt/accounts", s.handleAddChatGPTAccount, auth.CapAdmin)
	api("PATCH /api/chatgpt/accounts/{id}", s.handleUpdateChatGPTAccount, auth.CapAdmin)
	api("DELETE /api/chatgpt/accounts/{id}", s.handleRemoveChatGPTAccount, auth.CapAdmin)

	api("POST /api/tunnel/start", s.handleTunnelStart, auth.CapAdmin)
	api("POST /api/tunnel/stop", s.handleTunnelStop, auth.CapAdmin)
	// Managing tunnels reaches outside this deployment: creating one changes
	// the OpenAI organisation, and deleting one breaks every connector using
	// it, wherever those connectors are.
	api("POST /api/tunnels", s.handleCreateTunnel, auth.CapAdmin)
	api("POST /api/tunnels/{id}/assign", s.handleAssignTunnel, auth.CapAdmin)
	api("POST /api/tunnels/{id}/restart", s.handleRestartTunnel, auth.CapAdmin)
	api("DELETE /api/tunnels/{id}", s.handleDeleteTunnel, auth.CapAdmin)
	// Admin, not read: the log carries every request this host served, which
	// systems were called and by whom. That is a wider view than any one
	// account's own work, and it is the same right that reads the audit
	// trail's verification.
	api("GET /api/logs/stream", s.handleLogStream, auth.CapAdmin)
	api("GET /api/audit", s.handleAudit, auth.CapRead)
	api("GET /api/audit/verify", s.handleVerifyAudit, auth.CapAdmin)
	// Clearing the record is administrative, and is itself recorded.
	api("DELETE /api/audit", s.handleClearAudit, auth.CapAdmin)
	api("GET /api/health", s.handleHealth, auth.CapRead)
	// Integrations this build has, and the instances configured from them.
	api("GET /api/plugin-types", s.handlePluginTypes, auth.CapRead)
	api("GET /api/instances", s.handleInstances, auth.CapRead)
	// Adding an integration decides what an assistant can reach, so it is
	// administrative rather than operational.
	api("POST /api/instances", s.handleAddInstance, auth.CapAdmin)
	api("PATCH /api/instances/{name}", s.handleSetInstanceEnabled, auth.CapAdmin)
	api("DELETE /api/instances/{name}", s.handleRemoveInstance, auth.CapAdmin)
	// Undoing a removal that overrode the configuration file. Same capability
	// as the removal: putting an integration back in front of an assistant is
	// the same kind of decision as taking it away.
	api("POST /api/instances/{name}/restore", s.handleRestoreInstance, auth.CapAdmin)
	// Remote MCP servers. Reading what is imported and what each offers is an
	// operator's business; importing one, connecting to it, and deciding which
	// of its tools an assistant may reach decides what leaves this deployment,
	// so those are an administrator's.
	api("GET /api/mcp-servers", s.handleListMCPServers, auth.CapRead)
	api("GET /api/mcp-servers/schema", s.handleMCPSchema, auth.CapRead)
	api("POST /api/mcp-servers", s.handleImportMCPServer, auth.CapAdmin)
	api("PATCH /api/mcp-servers/{name}", s.handleSetMCPServerEnabled, auth.CapAdmin)
	api("DELETE /api/mcp-servers/{name}", s.handleRemoveMCPServer, auth.CapAdmin)
	api("GET /api/mcp-servers/{name}/tools", s.handleMCPServerTools, auth.CapRead)
	// Discovery reaches out to a third party with this deployment's
	// credentials, which is not a read of local state.
	api("POST /api/mcp-servers/{name}/discover", s.handleDiscoverMCPServer, auth.CapAdmin)
	api("PATCH /api/mcp-servers/{name}/tools/{tool}", s.handleClassifyMCPTool, auth.CapAdmin)
	// Declaring a header decides what credential this host sends to a third
	// party, so it is an admin capability like the import that preceded it.
	api("POST /api/mcp-servers/{name}/headers", s.handleAddMCPServerHeader, auth.CapAdmin)
	api("DELETE /api/mcp-servers/{name}/headers/{header}", s.handleRemoveMCPServerHeader, auth.CapAdmin)
	// The public catalogue. Administrator rather than operator because
	// browsing it makes this host reach a third party, which is a request an
	// operator should not be able to cause; what comes back is public.
	//
	// The entry route takes a wildcard because a registry name carries a
	// slash -- "io.github.example/weather" -- which a single path segment
	// cannot hold.
	api("GET /api/catalog", s.handleListCatalog, auth.CapAdmin)
	api("GET /api/catalog/{name...}", s.handleGetCatalogEntry, auth.CapAdmin)
	// Accounts decide who can reach anything else here, so administering them
	// is an administrator's right.
	api("GET /api/users", s.handleListUsers, auth.CapAdmin)
	api("POST /api/users", s.handleCreateUser, auth.CapAdmin)
	api("PATCH /api/users/{id}", s.handleUpdateUser, auth.CapAdmin)
	// Naming yourself is not administering the host. Every principal that
	// can reach the dashboard holds read, and this route can only ever edit
	// the account the request authenticated as -- there is no identifier in
	// it to point somewhere else -- so the capability it needs is the one
	// that gets you through the door. Naming somebody else is above.
	api("PATCH /api/account", s.handleUpdateAccount, auth.CapRead)
	api("DELETE /api/users/{id}", s.handleDeleteUser, auth.CapAdmin)
	// Attaching a provider to your own account, and detaching one. Read for
	// the same reason PATCH /api/account is: neither route carries an
	// identifier, so neither can address anybody else's account.
	api("GET /api/account/identities", s.handleAccountIdentities, auth.CapRead)
	api("POST /api/account/identities/{provider}/start", s.handleIdentityLinkStart, auth.CapRead)
	api("DELETE /api/account/identities/{provider}", s.handleUnlinkIdentity, auth.CapRead)
	// Deciding who gets an account. Approving one is a privilege grant -- it
	// is the moment somebody gains the ability to do anything here -- so it
	// takes an administrator and is written into the hash-chained trail
	// inside the transaction that performs it.
	// The exact addresses to paste into a provider's console. Administrator
	// because it is part of setting one up, and because it names how this
	// deployment is reached.
	api("GET /api/auth/redirect-uris", s.handleAuthRedirectURIs, auth.CapAdmin)
	// Groups. Reading one tells you what an account or a key can reach, and
	// writing one changes it for every member at once, so both are an
	// administrator's -- the same right that lists accounts, for the same
	// reason.
	api("GET /api/groups", s.handleListGroups, auth.CapAdmin)
	api("POST /api/groups", s.handleCreateGroup, auth.CapAdmin)
	api("GET /api/groups/{id}", s.handleGetGroup, auth.CapAdmin)
	api("PATCH /api/groups/{id}", s.handleUpdateGroup, auth.CapAdmin)
	api("DELETE /api/groups/{id}", s.handleDeleteGroup, auth.CapAdmin)
	// Membership is the grant itself: adding somebody to a group is the moment
	// they gain whatever it reaches.
	api("POST /api/groups/{id}/members", s.handleAddGroupMember, auth.CapAdmin)
	api("DELETE /api/groups/{id}/members/{kind}/{member}", s.handleRemoveGroupMember, auth.CapAdmin)
	// API keys. Only an administrator may create one, which is the owner's
	// rule and the right one: a key is a credential that acts on this host
	// with a role and a reach, and issuing one is handing out both.
	//
	// There is no route that reads a secret back. The only response that has
	// ever carried one is the reply to the request that created it.
	api("GET /api/keys", s.handleListKeys, auth.CapAdmin)
	api("POST /api/keys", s.handleCreateKey, auth.CapAdmin)
	api("PATCH /api/keys/{id}", s.handleUpdateKey, auth.CapAdmin)
	// Revoked rather than deleted: an audit entry naming an identifier that
	// resolves to nothing would not answer "which agent did this".
	api("POST /api/keys/{id}/revoke", s.handleRevokeKey, auth.CapAdmin)
	// Certificates this host trusts on top of the system roots. An
	// administrator's, because adding one decides what every outbound
	// connection this host makes will accept as proof of identity.
	//
	// There is no PATCH. A certificate is the bytes it is; changing them under
	// a name somebody already recognises is how a trust decision gets made
	// twice and reviewed once.
	api("GET /api/certificates", s.handleListCertificates, auth.CapAdmin)
	api("POST /api/certificates", s.handleAddCertificate, auth.CapAdmin)
	api("DELETE /api/certificates/{id}", s.handleDeleteCertificate, auth.CapAdmin)
	api("GET /api/registrations", s.handleListRegistrations, auth.CapAdmin)
	api("POST /api/registrations/{id}/approve", s.handleApproveRegistration, auth.CapAdmin)
	api("POST /api/registrations/{id}/reject", s.handleRejectRegistration, auth.CapAdmin)

	// Everything else is the single-page application.
	s.mux.Handle("/", s.staticHandler())
}

// Handler returns the fully wrapped dashboard handler.
func (s *Server) Handler() http.Handler {
	return observability.Correlate(s.opts.Log, s.recoverPanics(s.securityHeaders(s.mux)))
}

// recoverPanics turns a panic in a dashboard handler into a 500.
//
// net/http already stops one taking the process down: it logs to the standard
// logger and drops the connection. What it does not do is any of the things
// this host is careful about elsewhere -- the operator gets a connection reset
// with no status, no correlation ID to quote, and a line in a log stream that
// is not the structured one they read. The MCP endpoint has had this from the
// start and the dashboard did not, which was an omission rather than a
// decision.
//
// Inside Correlate so the log line and the response carry the same correlation
// ID, and outside securityHeaders so a panic while setting a header is still
// caught.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			// ErrAbortHandler is net/http's own signal that a handler gave up
			// deliberately -- ReverseProxy raises it on a client disconnect.
			// It is not a bug and must not be reported as one.
			if v == http.ErrAbortHandler {
				panic(v)
			}
			stack := debug.Stack()
			observability.Logger(r.Context()).Error("panic serving the dashboard",
				"path", r.URL.Path, "panic", fmt.Sprint(v))
			s.opts.Errors.CapturePanic(v, stack, "admin.request")
			s.writeError(w, r, http.StatusInternalServerError, "internal error")
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders applies the policy the dashboard needs.
//
// The dashboard renders operation detail that originated upstream -- device
// names, alarm text -- so a restrictive CSP is doing real work here rather
// than ticking a box.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'; font-src 'self'; "+
				"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// authenticate resolves the request's credential and checks one capability.
//
// Two credentials are accepted because two kinds of caller exist: a person in
// a browser holding a session cookie, and a script holding a bearer token.
// principalFor decides between them and writes its own refusal, including the
// CSRF check that only applies to the cookie.
func (s *Server) authenticate(required auth.Capability, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := s.principalFor(w, r)
		if !ok {
			return
		}
		if !principal.Can(required) {
			s.writeError(w, r, http.StatusForbidden, "insufficient permissions")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
}

// catalog returns the settings this host has, falling back to its own when no
// plugin catalog was supplied. Falling back rather than failing keeps the
// settings page working for a host with no plugins configured.
func (s *Server) catalog() *settings.Catalog {
	if s.opts.Catalog != nil {
		if c := s.opts.Catalog(); c != nil {
			return c
		}
	}
	return settings.NewCatalog()
}

// --- handlers --------------------------------------------------------------

type metaResponse struct {
	Version  string `json:"version"`
	AuthMode string `json:"auth_mode"`
	// NeedsSetup reports that no account exists yet, so the dashboard should
	// offer to create the first one instead of asking for a sign-in nobody
	// can complete.
	NeedsSetup bool `json:"needs_setup"`
}

// endpointsResponse describes the two ways to connect.
type endpointsResponse struct {
	// Aggregate serves every integration the credential is granted, from one
	// address. It is what a transport that binds a single URL needs.
	Aggregate string `json:"aggregate"`
	// PerPlugin is the address style that serves exactly one integration.
	PerPlugin string `json:"per_plugin_example"`
}

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	// Deliberately thin. This endpoint is unauthenticated, so it names the
	// authentication scheme and nothing else -- not the plugins, not the
	// configuration, not the host.
	//
	// Whether an account exists is the one addition. It is a fact anyone can
	// establish by trying to register, and withholding it would only mean the
	// dashboard shows a sign-in form on an instance where nobody can sign in.
	needsSetup := false
	if s.opts.Accounts != nil {
		if n, err := s.opts.Accounts.Count(r.Context()); err == nil {
			needsSetup = n == 0
		} else {
			s.opts.Log.Error("could not count accounts", "error", err)
		}
	}
	s.writeJSON(w, r, http.StatusOK, metaResponse{
		Version:    s.opts.Version,
		AuthMode:   s.opts.Verifier.Scheme(),
		NeedsSetup: needsSetup,
	})
}

// handleEndpoints reports the addresses a client can connect to.
func (s *Server) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, endpointsResponse{
		Aggregate: s.connectURL(r.Context(), "/mcp"),
		// {plugin} rather than {name}: the placeholder is shown to an operator
		// beside the route that serves it, and internal/mcp/host.go registers
		// that route as /mcp/{plugin}. Two spellings of one path read as two
		// paths.
		PerPlugin: s.connectURL(r.Context(), "/mcp/{plugin}"),
	})
}

// tunnelResponse is the dashboard's view of the tunnel.
type tunnelResponse struct {
	// Tunnels is one entry per connector, in configuration order.
	Tunnels []tunnel.Status `json:"tunnels"`
	Version any             `json:"version,omitempty"`

	// CanManage reports whether tunnels can be created and deleted from here.
	CanManage bool `json:"can_manage"`
	// Available is every tunnel in the OpenAI organisation, so one already
	// made there can be assigned without copying its id by hand. Nil when
	// there is no admin key, which is different from an organisation with no
	// tunnels in it.
	Available []availableTunnel `json:"available,omitempty"`
	// Problem explains why Available is missing, when it should not be.
	Problem string `json:"problem,omitempty"`
	// Missing names the credential needed to manage tunnels, when one is not
	// set. "The feature is off" is not something anyone can act on.
	Missing string `json:"missing,omitempty"`
	// Plugins names the systems a tunnel can be pointed at.
	Plugins []string `json:"plugins"`
	// Assignments maps a tunnel id to the system it is pointed at, "" meaning
	// every system. A tunnel here but absent from Tunnels is configured and
	// not running, which is what a plugin that has not started yet looks like.
	Assignments map[string]string `json:"assignments"`
	// AccountAssignments maps a tunnel id to the ChatGPT account it connects
	// with. A tunnel with no entry here has not been given one, which on a
	// host with several accounts is why it is not running.
	AccountAssignments map[string]string `json:"account_assignments"`
	// Accounts is every ChatGPT account, without credentials. The Tunnels page
	// needs them to offer a choice when a tunnel is made, and to say which
	// workspace an existing one belongs to.
	Accounts []accountView `json:"accounts"`
	// Workspaces are the ChatGPT workspaces already in use by a tunnel in this
	// organisation.
	//
	// OpenAI publishes no endpoint that lists workspaces, so these are read off
	// the tunnels that have one rather than fetched. That covers every case
	// except a organisation whose first tunnel is being made right now, where
	// the id still has to be supplied once.
	Workspaces []string `json:"workspaces"`
}

func (s *Server) handleTunnelStatus(w http.ResponseWriter, r *http.Request) {
	// Empty rather than nil, and kept that way below: a nil slice encodes as
	// null, and the dashboard maps over these to build its selects. A host
	// with nothing mounted yet is the ordinary state of a new install, not an
	// error, and it should not be the one that blanks the page.
	resp := tunnelResponse{
		Tunnels: []tunnel.Status{}, Plugins: []string{}, Workspaces: []string{},
		Assignments: map[string]string{}, AccountAssignments: map[string]string{},
		Accounts: []accountView{},
	}
	if s.opts.Tunnel != nil {
		if list := s.opts.Tunnel.Status(); len(list) > 0 {
			resp.Tunnels = list
		}
	}
	if s.opts.Plugins != nil {
		if names := s.opts.Plugins(); len(names) > 0 {
			resp.Plugins = names
		}
	}
	if s.opts.Assignments != nil {
		if assigned := s.opts.Assignments(); len(assigned) > 0 {
			resp.Assignments = assigned
		}
	}
	if s.opts.AccountAssignments != nil {
		if assigned := s.opts.AccountAssignments(); len(assigned) > 0 {
			resp.AccountAssignments = assigned
		}
	}

	// One listing per account, because each is a separate organisation. A
	// failure against one is recorded on that account and does not stop the
	// others: an expired admin key in one workspace should not blank the page
	// for every other.
	accounts, accountsErr := s.chatgptAccounts(r.Context())
	if accountsErr != nil {
		resp.Problem = accountsErr.Error()
	}
	var seen []tunnel.TunnelInfo
	for _, acct := range accounts {
		view := newAccountView(acct)
		dir := s.directory(acct.ID)
		view.Missing = dir.Missing()
		if dir.Available() {
			view.CanManage = true
			resp.CanManage = true
			if list, err := s.listTunnels(r.Context(), acct.ID, dir); err != nil {
				view.Problem = err.Error()
			} else {
				list = ours(list, resp.Assignments)
				for _, t := range list {
					resp.Available = append(resp.Available, availableTunnel{
						TunnelInfo:  t,
						AccountID:   acct.ID,
						AccountName: acct.Name,
					})
				}
				// This account's own workspaces and the ones its tunnels
				// report, not the host's. The page offers these when a
				// tunnel is made under this account.
				view.Workspaces = tunnel.NormalizeWorkspaces(append(view.Workspaces, workspacesIn(list)...))
				seen = append(seen, list...)
			}
		}
		resp.Accounts = append(resp.Accounts, view)
	}
	if len(resp.Accounts) == 0 {
		// No accounts at all is the state a new install is in, and the one
		// thing an operator can do about it. Named here rather than left to
		// the page to infer from an empty list.
		resp.Missing = "a ChatGPT account"
	}
	if ws := workspacesIn(seen); len(ws) > 0 {
		resp.Workspaces = ws
	}
	if s.opts.TunnelInfo != nil {
		resp.Version = s.opts.TunnelInfo()
	}
	s.writeJSON(w, r, http.StatusOK, resp)
}

func (s *Server) handleTunnelStart(w http.ResponseWriter, r *http.Request) {
	if s.opts.Tunnel == nil || !s.opts.Tunnel.Enabled() {
		s.writeError(w, r, http.StatusBadRequest, "no tunnel is configured")
		return
	}
	if err := s.opts.Tunnel.Start(r.Context()); err != nil {
		// The tunnel's own error is shown: an operator acting on this needs to
		// know whether the tunnel id was wrong or the key lacked permission,
		// and the manager already redacts the credential.
		s.writeJSON(w, r, http.StatusConflict, map[string]any{
			"error":  "tunnel_failed",
			"detail": err.Error(),
			"status": s.opts.Tunnel.Status(),
		})
		return
	}
	s.writeJSON(w, r, http.StatusOK, s.opts.Tunnel.Status())
}

func (s *Server) handleTunnelStop(w http.ResponseWriter, r *http.Request) {
	if s.opts.Tunnel == nil || !s.opts.Tunnel.Enabled() {
		s.writeError(w, r, http.StatusBadRequest, "no tunnel is configured")
		return
	}
	if err := s.opts.Tunnel.Stop(r.Context()); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "the tunnel did not stop cleanly")
		return
	}
	s.writeJSON(w, r, http.StatusOK, s.opts.Tunnel.Status())
}

// settingsResponse is the dashboard's view of configuration.
type settingsResponse struct {
	Groups []settings.Group `json:"groups"`
	// Values holds current values. A secret is never included; Secrets says
	// which keys are set instead.
	Values map[string]any `json:"values"`
	// SecretsSet names the secret keys that hold a value, so the UI can show
	// "set" rather than an empty box that looks unconfigured.
	SecretsSet map[string]bool `json:"secrets_set"`
	// Encryption reports whether secrets can be stored at all.
	Encryption bool `json:"encryption_available"`
	// Bootstrap lists the read-only settings from the startup file.
	Bootstrap []BootstrapSetting `json:"bootstrap"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if s.opts.Settings == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "settings are unavailable")
		return
	}
	ctx := r.Context()

	resp := settingsResponse{
		Groups:     s.catalog().Groups(),
		Values:     map[string]any{},
		SecretsSet: map[string]bool{},
		Encryption: s.opts.Settings.HasCipher(),
	}
	if s.opts.Bootstrap != nil {
		resp.Bootstrap = s.opts.Bootstrap()
	}

	for _, g := range resp.Groups {
		for _, f := range g.Fields {
			raw, ok, err := s.opts.Settings.Get(ctx, f.Key)
			if err != nil {
				s.opts.Log.Warn("could not read a setting", "key", f.Key, "error", err)
				continue
			}
			if f.Kind == settings.KindSecret {
				// A stored secret is never sent back, not even to an
				// administrator. The dashboard is a place a screen gets
				// shared, and there is no reason to read one out.
				resp.SecretsSet[f.Key] = ok && raw != ""
				continue
			}
			if !ok {
				if f.Default != nil {
					resp.Values[f.Key] = f.Default
				}
				continue
			}
			var decoded any
			if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
				decoded = raw
			}
			resp.Values[f.Key] = decoded
		}
	}
	s.writeJSON(w, r, http.StatusOK, resp)
}

type putSettingsRequest struct {
	// Values maps a setting key to its new value, as a string. Strings rather
	// than typed JSON because the schema already declares the type and
	// validates it, and a form submits strings.
	Values map[string]string `json:"values"`
	// ClearSecrets names secret keys to remove.
	ClearSecrets []string `json:"clear_secrets,omitempty"`
}

type putSettingsResponse struct {
	Applied []string `json:"applied"`
	// RestartRequired names settings that were stored but will not take effect
	// until mcpd restarts. Saying so is the difference between a setting that
	// looks broken and one that is simply pending.
	RestartRequired []string `json:"restart_required,omitempty"`
	// Reconnected reports components restarted to pick a change up.
	Reconnected []string `json:"reconnected,omitempty"`
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if s.opts.Settings == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "settings are unavailable")
		return
	}

	var req putSettingsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "the request could not be read")
		return
	}

	actor := auth.FromContext(r.Context()).ID

	// Everything is validated before anything is written, so a form with one
	// bad field changes nothing rather than applying the rest.
	var problems []string
	for key, value := range req.Values {
		if err := s.catalog().Validate(key, value); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		s.writeJSON(w, r, http.StatusBadRequest, map[string]any{
			"error":    "invalid_settings",
			"problems": problems,
		})
		return
	}

	changes := make([]settings.Change, 0, len(req.Values)+len(req.ClearSecrets))
	resp := putSettingsResponse{}

	for key, value := range req.Values {
		field, _ := s.catalog().FieldFor(key)
		secret := field.Kind == settings.KindSecret

		stored := value
		if !secret {
			// Non-secret values are stored as JSON so they round-trip with
			// their type intact.
			encoded, err := settings.Encode(field.Kind, value)
			if err != nil {
				s.writeError(w, r, http.StatusBadRequest, err.Error())
				return
			}
			stored = encoded
		}
		changes = append(changes, settings.Change{Key: key, Value: stored, Secret: secret})
		resp.Applied = append(resp.Applied, key)

		switch field.Apply {
		case settings.ApplyRestart:
			resp.RestartRequired = append(resp.RestartRequired, key)
		case settings.ApplyReconnect:
			resp.Reconnected = append(resp.Reconnected, field.Group)
		}
	}
	for _, key := range req.ClearSecrets {
		changes = append(changes, settings.Change{Key: key, Delete: true})
		resp.Applied = append(resp.Applied, key)
	}

	if err := s.opts.Settings.Apply(r.Context(), actor, changes); err != nil {
		s.opts.Log.Error("could not apply settings", "actor", actor, "error", err)
		s.writeJSON(w, r, http.StatusConflict, map[string]string{
			"error":  "settings_not_applied",
			"detail": err.Error(),
		})
		return
	}

	sort.Strings(resp.Applied)
	resp.Reconnected = dedupe(resp.Reconnected)
	s.opts.Log.Info("settings changed", "actor", actor, "keys", resp.Applied)
	s.writeJSON(w, r, http.StatusOK, resp)
}

func (s *Server) handleSettingsHistory(w http.ResponseWriter, r *http.Request) {
	if s.opts.Settings == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "settings are unavailable")
		return
	}
	entries, err := s.opts.Settings.History(r.Context(), parseLimit(r.URL.Query().Get("limit"), 50, 200))
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "could not read the change history")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
}

// dedupe collapses repeats while keeping the first of each.
func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) handleListOperations(w http.ResponseWriter, r *http.Request) {
	principal := auth.FromContext(r.Context())

	var states []operations.OperationState
	if raw := r.URL.Query().Get("state"); raw != "" {
		st := operations.OperationState(raw)
		if !st.Valid() {
			s.writeError(w, r, http.StatusBadRequest, "unknown state")
			return
		}
		states = []operations.OperationState{st}
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 50, 200)

	// Listing spans only the plugins this principal may reach, so the
	// dashboard never shows an operation belonging to an integration the
	// caller has no access to.
	visible := s.opts.Authorizer.VisiblePlugins(principal, s.opts.Manager.Names())

	// One lookup for the whole page rather than one per row.
	names := s.displayNames(r.Context())

	var all []operationDTO
	for _, plugin := range visible {
		ops, err := s.opts.Service.List(r.Context(), principal, plugin, states, limit)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "could not list operations")
			return
		}
		for _, op := range ops {
			all = append(all, toDTO(op, names))
		}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"operations": all,
		"count":      len(all),
	})
}

func (s *Server) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.opts.Service.Get(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, http.StatusNotFound, "operation not found")
		return
	}

	history, err := s.opts.Audit.ByOperation(r.Context(), op.ID)
	if err != nil {
		s.opts.Log.Warn("could not read audit history", "operation_id", op.ID, "error", err)
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"operation": toDTO(op, s.displayNames(r.Context())),
		"audit":     toAuditDTOs(history),
	})
}

type decisionRequest struct {
	Reason string `json:"reason"`
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	s.decide(w, r, s.opts.Service.Approve)
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	s.decide(w, r, s.opts.Service.Reject)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	s.decide(w, r, s.opts.Service.Cancel)
}

type decisionFunc func(context.Context, *auth.Principal, string, string) (*operations.Operation, error)

func (s *Server) decide(w http.ResponseWriter, r *http.Request, fn decisionFunc) {
	var req decisionRequest
	if r.Body != nil {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req)
	}

	op, err := fn(r.Context(), auth.FromContext(r.Context()), r.PathValue("id"), req.Reason)
	if err != nil {
		// A guard refusal is the system working, so it is reported as a 409
		// with its stable code rather than a generic failure. The operator
		// needs to know it was refused and why.
		var guardErr *operations.GuardError
		if errorsAs(err, &guardErr) {
			s.writeJSON(w, r, http.StatusConflict, map[string]string{
				"error":          guardErr.Code(),
				"detail":         guardErr.Detail,
				"correlation_id": observability.CorrelationID(r.Context()),
			})
			return
		}
		s.writeError(w, r, http.StatusBadRequest, "the operation could not be updated")
		return
	}
	s.writeJSON(w, r, http.StatusOK, toDTO(op, s.displayNames(r.Context())))
}

type pluginDTO struct {
	Name string `json:"name"`
	// Type is the integration this is an instance of. It equals Name unless
	// someone has configured more than one of something, which is the case the
	// dashboard has to group by.
	Type        string `json:"type"`
	Version     string `json:"version"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Endpoint    string `json:"endpoint"`
	// ConnectURL is the full address to paste into a client, rather than the
	// bare path. It is the thing an operator actually needs and the thing they
	// are most likely to assemble wrongly by hand.
	ConnectURL string    `json:"connect_url"`
	Health     string    `json:"health"`
	Message    string    `json:"health_message,omitempty"`
	Tools      []toolDTO `json:"tools"`
	Mutations  []string  `json:"mutations"`
	Required   bool      `json:"required"`
	// SettingsGroup names this instance's group in the settings payload, so
	// the page listing plugins can render the form beside the plugin it
	// configures rather than sending someone elsewhere. Empty when the plugin
	// declares no settings.
	SettingsGroup string       `json:"settings_group,omitempty"`
	Settings      []settingDTO `json:"settings"`
}

// toolDTO describes one tool for the plugin list.
type toolDTO struct {
	Name string `json:"name"`
	// Kind separates what a tool can do, which is the distinction that
	// matters to whoever is deciding whether to hand out access.
	Kind string `json:"kind"` // "read" or "propose"
	// Description is the one line the model is told, so the dashboard can
	// answer "what does this one do" without a trip to the plugin's docs.
	Description string `json:"description,omitempty"`
}

// settingDTO is one plugin configuration value.
//
// Values that look like credentials are never sent. They are configured by
// reference rather than by value, so the reference is what an operator needs
// to see anyway -- and the dashboard is a place a screen gets shared.
type settingDTO struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Secret marks a value that was withheld.
	Secret bool `json:"secret"`
}

func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	principal := auth.FromContext(r.Context())

	var out []pluginDTO
	for _, m := range s.opts.Manager.All() {
		d := m.Descriptor
		// A principal never learns that a plugin exists which it cannot use.
		if !principal.CanAccessPlugin(d.Name) {
			continue
		}
		h := m.Health()
		mutations := m.Registry.MutationActions()

		// A propose tool is derived from a mutation action, so the two views
		// are reconciled here rather than leaving the UI to guess which is
		// which from a name.
		proposeTools := make(map[string]bool, len(mutations))
		for _, action := range mutations {
			proposeTools[d.Name+"_"+strings.ReplaceAll(action, ".", "_")] = true
		}

		descriptions := m.Registry.ToolDescriptions()
		tools := make([]toolDTO, 0, len(m.Registry.ToolNames()))
		for _, name := range m.Registry.ToolNames() {
			kind := "read"
			if proposeTools[name] {
				kind = "propose"
			}
			tools = append(tools, toolDTO{Name: name, Kind: kind, Description: descriptions[name]})
		}
		for name := range proposeTools {
			tools = append(tools, toolDTO{Name: name, Kind: "propose", Description: descriptions[name]})
		}
		sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

		out = append(out, pluginDTO{
			Name: d.Name, Version: d.Version, Title: d.Title,
			Description: d.Description, Endpoint: d.Endpoint(),
			ConnectURL: s.connectURL(r.Context(), d.Endpoint()),
			Health:     string(h.State), Message: h.Message,
			Tools:         tools,
			Mutations:     mutations,
			Required:      m.Required,
			Type:          s.pluginType(d.Name),
			SettingsGroup: s.settingsGroupFor(d.Name),
			Settings:      s.settingsFor(d.Name),
		})
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"plugins": out, "count": len(out)})
}

// pluginType reports what an instance is an instance of.
func (s *Server) pluginType(instance string) string {
	if s.opts.PluginType == nil {
		return instance
	}
	if t := s.opts.PluginType(instance); t != "" {
		return t
	}
	return instance
}

// settingsGroupFor names an instance's settings group, when it has one.
func (s *Server) settingsGroupFor(instance string) string {
	name := "plugin:" + instance
	for _, g := range s.catalog().Groups() {
		if g.Name == name {
			return name
		}
	}
	return ""
}

// connectURL builds the address a client connects to.
func (s *Server) connectURL(ctx context.Context, endpoint string) string {
	base := strings.TrimRight(s.publicURL(ctx), "/")
	if base == "" {
		return endpoint
	}
	return base + endpoint
}

// publicURL and frontendPublicURL read the two addresses, or nothing when the
// host did not supply a reader. Nil-safe because half the tests in this
// package build a Server with only the options their case needs.
func (s *Server) publicURL(ctx context.Context) string {
	if s.opts.PublicURL == nil {
		return ""
	}
	return s.opts.PublicURL(ctx)
}

func (s *Server) frontendPublicURL(ctx context.Context) string {
	if s.opts.FrontendPublicURL == nil {
		return ""
	}
	return s.opts.FrontendPublicURL(ctx)
}

// sessionTTL is how long a browser signing in now stays signed in.
func (s *Server) sessionTTL(ctx context.Context) time.Duration {
	if s.opts.SessionTTL == nil {
		return 0
	}
	return s.opts.SessionTTL(ctx)
}

// secretish reports whether a configuration key names a credential.
//
// Matching on the key rather than inspecting the value is deliberate: a
// reference like "env:MCPD_TOKEN" is not itself secret, but a key named
// "password" holding a literal certainly is, and the dashboard is a place a
// screen gets shared.
func secretish(key string) bool {
	lower := strings.ToLower(key)
	for _, marker := range []string{
		"secret", "password", "passwd", "token", "key", "credential", "auth",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// settingsFor renders a plugin's configuration for display.
func (s *Server) settingsFor(plugin string) []settingDTO {
	if s.opts.PluginSettings == nil {
		return nil
	}
	raw := s.opts.PluginSettings(plugin)
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]settingDTO, 0, len(keys))
	for _, k := range keys {
		value := fmt.Sprint(raw[k])
		if secretish(k) {
			// A reference is safe to show and is what the operator needs; a
			// literal is not shown at all.
			if strings.HasPrefix(value, "env:") || strings.HasPrefix(value, "file:") ||
				strings.HasPrefix(value, "credential:") {
				out = append(out, settingDTO{Key: k, Value: value, Secret: true})
				continue
			}
			out = append(out, settingDTO{Key: k, Value: "(set)", Secret: true})
			continue
		}
		out = append(out, settingDTO{Key: k, Value: value})
	}
	return out
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r.URL.Query().Get("limit"), 100, 500)
	records, err := s.opts.Audit.Recent(r.Context(), limit)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "could not read the audit trail")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"records": toAuditDTOs(records),
		"count":   len(records),
	})
}

func (s *Server) handleVerifyAudit(w http.ResponseWriter, r *http.Request) {
	brokenAt, err := s.opts.Audit.VerifyChain(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, "could not verify the audit chain")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"intact":    brokenAt == 0,
		"broken_at": brokenAt,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, s.opts.Health.Readiness(r.Context()))
}

// --- helpers ---------------------------------------------------------------

func (s *Server) writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.opts.Log.Error("failed to encode a dashboard response",
			"path", r.URL.Path, "error", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.writeJSON(w, r, status, map[string]string{
		"error":          msg,
		"correlation_id": observability.CorrelationID(r.Context()),
	})
}

// writeUpstreamError answers a refusal from OpenAI with the reason beside the
// sentence.
//
// The reason is what the dashboard branches on to show the right explanation.
// Without it the page can only print the sentence, and the sentence is
// deliberately too short to act on -- the acting-on is a laid-out page, not a
// paragraph flattened into a toast.
func (s *Server) writeUpstreamError(w http.ResponseWriter, r *http.Request, status int, err error) {
	body := map[string]string{
		"error":          "upstream_refused",
		"detail":         err.Error(),
		"correlation_id": observability.CorrelationID(r.Context()),
	}
	if reason := tunnel.Reason(err); reason != "" {
		body["error"] = reason
	}
	s.writeJSON(w, r, status, body)
}

func parseLimit(raw string, fallback, max int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	if n > max {
		return max
	}
	return n
}

// operationDTO is the dashboard's view of an operation.
type operationDTO struct {
	ID       string              `json:"id"`
	Plugin   string              `json:"plugin"`
	Action   string              `json:"action"`
	State    string              `json:"state"`
	Risk     string              `json:"risk"`
	Impact   string              `json:"impact"`
	Changes  []operations.Change `json:"changes,omitempty"`
	Target   any                 `json:"target,omitempty"`
	Before   any                 `json:"before,omitempty"`
	Desired  any                 `json:"desired,omitempty"`
	Observed any                 `json:"observed,omitempty"`
	// RequestedBy and ApprovedBy are the stored identifiers, and they are
	// what the record actually says. The *Name fields beside them are a
	// rendering: resolved from accounts at read time, never stored, and never
	// compared against anything. A record that named people by a value its
	// subject can change would be a record of nothing.
	RequestedBy     string     `json:"requested_by"`
	RequestedByName string     `json:"requested_by_name"`
	RequestedAt     time.Time  `json:"requested_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	ApprovedBy      string     `json:"approved_by,omitempty"`
	ApprovedByName  string     `json:"approved_by_name,omitempty"`
	ApprovedAt      *time.Time `json:"approved_at,omitempty"`
	// AuthorizedByRule names the standing rule that authorised this change
	// when nobody was asked, and is empty where a person decided. The page
	// must not render the two the same way: "approved by system:policy" reads
	// as somebody having clicked, and nobody did.
	AuthorizedByRule string     `json:"authorized_by_rule,omitempty"`
	ExecuteBy        *time.Time `json:"execute_by,omitempty"`
	TerminalAt       *time.Time `json:"terminal_at,omitempty"`
	// Verified is three-valued on purpose. True means the target was re-read
	// and matched, false means it was re-read and did not, and absent means no
	// check was performed -- which is a different fact from a failed one, and
	// the dashboard should not render them the same way.
	Verified *bool `json:"verified,omitempty"`
	// Assurance, DriftChecked and OutcomeVerifiable say what this record can
	// prove. A reviewed change carries all of it; a gated call carries a
	// human's yes and the fact that the call was made.
	Assurance         string `json:"assurance"`
	DriftChecked      bool   `json:"drift_checked"`
	OutcomeVerifiable bool   `json:"outcome_verifiable"`
	Attempts          int    `json:"attempts"`
	ErrorCode         string `json:"error_code,omitempty"`
	ErrorDetail       string `json:"error_detail,omitempty"`
	Terminal          bool   `json:"terminal"`
}

// toDTO renders an operation. names resolves a stored identifier to something
// to read, and may be nil, in which case the identifier is what is rendered --
// which is the correct fallback rather than a degraded one.
func toDTO(op *operations.Operation, names map[string]string) operationDTO {
	return operationDTO{
		ID: op.ID, Plugin: op.Plugin, Action: op.Action,
		State: op.State.String(), Risk: op.Risk.String(), Impact: op.Impact,
		Changes:     op.Changes,
		Target:      decodeJSON(op.Target),
		Before:      decodeJSON(op.Before),
		Desired:     decodeJSON(op.Desired),
		Observed:    decodeJSON(op.Observed),
		RequestedBy: op.RequestedBy, RequestedByName: renderName(names, op.RequestedBy),
		RequestedAt: op.RequestedAt,
		ExpiresAt:   op.ExpiresAt,
		ApprovedBy:  op.ApprovedBy, ApprovedByName: renderName(names, op.ApprovedBy),
		AuthorizedByRule: op.AuthorizedByRule,
		ApprovedAt:       op.ApprovedAt, ExecuteBy: op.ApprovalExpiresAt,
		TerminalAt: op.TerminalAt, Verified: op.OutcomeVerified,
		Assurance:    op.Assurance().String(),
		DriftChecked: op.DriftChecked(), OutcomeVerifiable: op.Verifiable,
		Attempts: op.AttemptCount, ErrorCode: op.ErrorCode,
		ErrorDetail: op.ErrorDetail, Terminal: op.State.IsTerminal(),
	}
}

type auditDTO struct {
	Seq       int64     `json:"seq"`
	At        time.Time `json:"at"`
	Kind      string    `json:"kind"`
	Actor     string    `json:"actor"`
	Operation string    `json:"operation_id,omitempty"`
	Plugin    string    `json:"plugin,omitempty"`
	Action    string    `json:"action,omitempty"`
	From      string    `json:"from_state,omitempty"`
	To        string    `json:"to_state,omitempty"`
	Risk      string    `json:"risk,omitempty"`
	Detail    any       `json:"detail,omitempty"`
}

func toAuditDTOs(records []operations.AuditRecord) []auditDTO {
	out := make([]auditDTO, 0, len(records))
	for _, r := range records {
		out = append(out, auditDTO{
			Seq: r.Seq, At: r.At, Kind: r.Entry.Kind, Actor: r.Entry.Actor,
			Operation: r.Entry.OperationID, Plugin: r.Entry.Plugin,
			Action: r.Entry.Action, From: r.Entry.FromState.String(),
			To: r.Entry.ToState.String(), Risk: r.Entry.Risk.String(),
			Detail: decodeJSON(r.Entry.Detail),
		})
	}
	return out
}

// displayNames maps principal identifiers to something a person can read.
//
// Only accounts are in it. A static token's principal has no row here and
// renders as its own identifier, which is right: "service:chatgpt-connector"
// is already the most informative thing anyone can say about it.
//
// One query, and it is deliberately not cached. Accounts are few, the page
// that reads this is not on any hot path, and a cache here would mean a
// renamed account keeping its old name on a screen until something evicted it.
//
// The map is built from every account and then read only for identifiers that
// already appear on an operation the caller was allowed to see, so it does not
// widen what a non-administrator learns about who else has an account. Nothing
// from it reaches a response on its own.
func (s *Server) displayNames(ctx context.Context) map[string]string {
	if s.opts.Accounts == nil {
		return nil
	}
	list, err := s.opts.Accounts.List(ctx)
	if err != nil {
		// A name is a convenience. Failing the whole page because one could
		// not be looked up would trade the operation list for a nicety.
		s.opts.Log.WarnContext(ctx, "could not resolve display names", "error", err)
		return nil
	}
	out := make(map[string]string, len(list))
	for _, u := range list {
		out["user:"+u.Email] = u.Name()
	}
	return out
}

// renderName resolves an identifier, falling back to the identifier itself. An
// empty name is never returned for a non-empty identifier: a blank column
// where a person should be is worse than a technical string.
func renderName(names map[string]string, id string) string {
	if id == "" {
		return ""
	}
	if name, ok := names[id]; ok && name != "" {
		return name
	}
	return id
}

func decodeJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}

// handleCACertificate serves the certificate authority for installation.
func (s *Server) handleCACertificate(w http.ResponseWriter, r *http.Request) {
	if s.opts.CACertificate == nil {
		s.writeError(w, r, http.StatusNotFound, "mcpd is not using a certificate of its own")
		return
	}
	pem := s.opts.CACertificate()
	if len(pem) == 0 {
		s.writeError(w, r, http.StatusNotFound, "no certificate authority available")
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="mcpd-ca.pem"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(pem)
}

// directory returns the tunnel manager for one account, never nil.
func (s *Server) directory(accountID string) *tunnel.Directory {
	if s.opts.Directory == nil {
		return tunnel.NewDirectory("", "", "")
	}
	return s.opts.Directory(accountID)
}

// handleCreateTunnel makes a tunnel at OpenAI and points it at a system.
func (s *Server) handleCreateTunnel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Plugin    string `json:"plugin"`
		Workspace string `json:"workspace_id"`
		// Account is whose organisation the tunnel is made in, and whose
		// credential the connector will authenticate with. Empty means the
		// only account when there is exactly one.
		Account string `json:"account"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	account, err := s.resolveAccount(r.Context(), body.Account)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	dir := s.directory(account.ID)
	if !dir.Available() {
		s.writeError(w, r, http.StatusBadRequest,
			"the ChatGPT account "+account.Name+" needs "+dir.Missing()+" first")
		return
	}
	if err := s.checkPlugin(body.Plugin); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	created, err := dir.Create(r.Context(),
		tunnelName(body.Name, body.Plugin), createdByMCPD, body.Workspace)
	if err != nil {
		s.writeUpstreamError(w, r, http.StatusBadGateway, err)
		return
	}
	// The page reloads next, and a listing that predates this makes the tunnel
	// somebody just created look as though it does not exist.
	s.forgetTunnels(account.ID)
	// Assigning is the point of creating: a tunnel nothing is bound to is an
	// object in someone's account doing nothing.
	if err := s.assign(r, created.ID, body.Plugin, account.ID); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// And running it is the point of assigning. Leaving the subsystem switched
	// off would mean making a connector, watching it sit at "switched off",
	// and having to find a toggle on another page to finish the job nobody
	// started for any other reason.
	if err := s.enableTunnels(r); err != nil {
		s.opts.Log.Warn("tunnel created but the subsystem could not be enabled", "error", err)
	}
	s.writeJSON(w, r, http.StatusCreated, created)
}

// handleRestartTunnel stops one tunnel and starts it again.
//
// The one recovery a person can do without changing anything: a tunnel that
// the supervisor has given up on, or one somebody wants fresh after a change
// they cannot name. It answers with the tunnel's status afterwards, which is
// what the person pressing the button is waiting to read.
func (s *Server) handleRestartTunnel(w http.ResponseWriter, r *http.Request) {
	if s.opts.Tunnel == nil {
		s.writeError(w, r, http.StatusBadRequest, "no tunnel is configured")
		return
	}
	id := r.PathValue("id")
	if err := s.opts.Tunnel.Restart(r.Context(), id); err != nil {
		s.writeJSON(w, r, http.StatusConflict, map[string]any{
			"error":  "tunnel_failed",
			"detail": err.Error(),
			"status": s.opts.Tunnel.Status(),
		})
		return
	}
	s.opts.Log.InfoContext(r.Context(), "tunnel restarted from the dashboard",
		"tunnel", id, "by", auth.FromContext(r.Context()).ID)
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "restarted", "tunnels": s.opts.Tunnel.Status()})
}

// handleAssignTunnel points an existing tunnel at a system.
func (s *Server) handleAssignTunnel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Plugin  string `json:"plugin"`
		Account string `json:"account"`
		// Unassign says the tunnel is not to be used here at all. Without
		// it an empty plugin means everything, as it always has.
		Unassign bool `json:"unassign"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if body.Unassign {
		if err := s.unassign(r, r.PathValue("id")); err != nil {
			s.writeError(w, r, http.StatusBadRequest, err.Error())
			return
		}
		s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "unassigned"})
		return
	}
	if err := s.checkPlugin(body.Plugin); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	account, err := s.resolveAccount(r.Context(), body.Account)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.assign(r, r.PathValue("id"), body.Plugin, account.ID); err != nil {
		var wrong *errWrongOwner
		if errors.As(err, &wrong) {
			ids := make([]string, 0, len(wrong.owners))
			for _, o := range wrong.owners {
				ids = append(ids, o.ID)
			}
			s.writeJSON(w, r, http.StatusConflict, map[string]any{
				"error": "wrong_account", "detail": err.Error(), "owners": ids,
			})
			return
		}
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "assigned"})
}

// handleDeleteTunnel removes a tunnel from the organisation.
func (s *Server) handleDeleteTunnel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Which organisation to delete it from. Taken from the assignment when the
	// caller does not say, because that is the account mcpd already knows owns
	// this tunnel -- and deleting from the wrong organisation is a request
	// that cannot be taken back.
	wanted := r.URL.Query().Get("account")
	if wanted == "" && s.opts.AccountAssignments != nil {
		wanted = s.opts.AccountAssignments()[id]
	}
	account, err := s.resolveAccount(r.Context(), wanted)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	dir := s.directory(account.ID)
	if !dir.Available() {
		s.writeError(w, r, http.StatusBadRequest,
			"deleting a tunnel needs "+dir.Missing()+" on the account "+account.Name)
		return
	}
	if err := dir.Delete(r.Context(), id); err != nil {
		s.writeUpstreamError(w, r, http.StatusBadGateway, err)
		return
	}
	s.forgetTunnels(account.ID)
	// Whatever was pointing at it now points at nothing, so the assignment
	// goes too: leaving it would keep mcpd trying to run a tunnel that no
	// longer exists.
	if err := s.unassign(r, id); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "deleted"})
}

// checkPlugin rejects a system that is not mounted, because a tunnel bound to
// one would connect and then serve nothing.
func (s *Server) checkPlugin(plugin string) error {
	if plugin == "" || s.opts.Plugins == nil {
		return nil
	}
	if slices.Contains(s.opts.Plugins(), plugin) {
		return nil
	}
	return fmt.Errorf("there is no system called %q", plugin)
}

// assign records which system a tunnel serves and whose account it uses.
//
// Both in one write. They are one decision -- this connector, for that system,
// on that workspace's credential -- and applying them separately would leave a
// window in which a tunnel is pointed at a system with no account, which is
// exactly the state that refuses to start.
// errWrongOwner is an assignment to an account whose organisation is not
// among those the tunnel belongs to. A tunnel is run only by the key of an
// organisation it is associated with, so this is refused rather than stored.
type errWrongOwner struct {
	owners []tunnel.Account
}

func (e *errWrongOwner) Error() string {
	names := make([]string, 0, len(e.owners))
	for _, o := range e.owners {
		names = append(names, o.Name)
	}
	if len(names) == 1 {
		return fmt.Sprintf("that tunnel is in %s's organisation and can only run there", names[0])
	}
	return fmt.Sprintf("that tunnel belongs to %s and can only run under one of them", strings.Join(names, ", "))
}

// ownersOf reports every account whose organisation lists a tunnel, from the
// listings the page already keeps. A tunnel's record names organisations as
// a list, so there may be several; empty when no account with an admin key
// can see it.
func (s *Server) ownersOf(ctx context.Context, id string) []tunnel.Account {
	accounts, err := s.chatgptAccounts(ctx)
	if err != nil {
		return nil
	}
	var out []tunnel.Account
	for _, acct := range accounts {
		dir := s.directory(acct.ID)
		if !dir.Available() {
			continue
		}
		list, err := s.listTunnels(ctx, acct.ID, dir)
		if err != nil {
			continue
		}
		for _, t := range list {
			if t.ID == id {
				out = append(out, acct)
				break
			}
		}
	}
	return out
}

// assign points a tunnel at a system under an account. The dashboard spells
// "everything" as an empty plugin, which the store spells out; an empty
// plugin that means "not used" is the unassign path, not this one.
func (s *Server) assign(r *http.Request, id, plugin, accountID string) error {
	if s.opts.Settings == nil {
		return fmt.Errorf("settings are unavailable")
	}
	if owners := s.ownersOf(r.Context(), id); len(owners) > 0 &&
		!slices.ContainsFunc(owners, func(a tunnel.Account) bool { return a.ID == accountID }) {
		return &errWrongOwner{owners: owners}
	}
	stored := plugin
	if stored == "" {
		stored = settings.TunnelEverything
	}
	encodedPlugin, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	encodedAccount, err := json.Marshal(accountID)
	if err != nil {
		return err
	}
	changes := []settings.Change{
		{Key: settings.TunnelPluginKey(id), Value: string(encodedPlugin)},
		{Key: settings.TunnelAccountKey(id), Value: string(encodedAccount)},
	}
	return s.opts.Settings.Apply(r.Context(), auth.FromContext(r.Context()).ID, changes)
}

// ours narrows a tunnel listing to the ones this host has anything to do with.
//
// An organisation's tunnels are not all mcpd's. Adding a ChatGPT account
// listed every tunnel anyone had ever made in that organisation -- other
// people's connectors, other tools' -- as though they were this host's to
// assign, which is noise at best and an invitation to take over somebody
// else's connector at worst.
//
// Two things count as ours: a tunnel this host created, which it stamps on
// creation, and a tunnel this host has assigned, whoever made it. The second
// matters because an operator who adopted an existing tunnel by assigning it
// must not watch it vanish from the page for the crime of not having been
// created here.
func ours(list []tunnel.TunnelInfo, assigned map[string]string) []tunnel.TunnelInfo {
	out := make([]tunnel.TunnelInfo, 0, len(list))
	for _, t := range list {
		if t.Description == createdByMCPD {
			out = append(out, t)
			continue
		}
		if _, mine := assigned[t.ID]; mine {
			out = append(out, t)
		}
	}
	return out
}

// createdByMCPD is the description this host puts on tunnels it makes, and the
// only durable mark distinguishing them: the control plane has no field for
// who created a tunnel.
const createdByMCPD = "Created by mcpd"

// workspacesIn collects the distinct workspaces the listed tunnels belong to,
// in a stable order so the dashboard's default does not move between polls.
func workspacesIn(tunnels []tunnel.TunnelInfo) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, t := range tunnels {
		for _, w := range t.WorkspaceIDs {
			if w == "" || seen[w] {
				continue
			}
			seen[w] = true
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

// tunnelName settles what a new tunnel is called.
//
// mcpd already knows what the tunnel is for, so asking someone to type a name
// before the button will work is asking them to restate it. A name is still
// accepted -- it is what shows in the OpenAI console, and a deployment with
// several hosts will want to tell them apart -- but leaving it blank is the
// ordinary case rather than an error.
func tunnelName(name, plugin string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	if plugin == "" {
		return "mcpd"
	}
	return "mcpd: " + plugin
}

// enableTunnels turns the tunnel subsystem on.
//
// It is idempotent and never turns it off: the switch remains a deliberate way
// to stop every connector at once, and this only removes the step of finding
// it after making one.
func (s *Server) enableTunnels(r *http.Request) error {
	if s.opts.Settings == nil {
		return fmt.Errorf("settings are unavailable")
	}
	return s.opts.Settings.Apply(r.Context(), auth.FromContext(r.Context()).ID,
		[]settings.Change{{Key: settings.KeyTunnelEnabled, Value: "true"}})
}

// unassign clears every reference to a tunnel id.
func (s *Server) unassign(r *http.Request, id string) error {
	if s.opts.Settings == nil {
		return fmt.Errorf("settings are unavailable")
	}
	// One pair of keys per tunnel, and nothing else to look for.
	return s.opts.Settings.Apply(r.Context(), auth.FromContext(r.Context()).ID, []settings.Change{
		{Key: settings.TunnelPluginKey(id), Delete: true},
		{Key: settings.TunnelAccountKey(id), Delete: true},
	})
}

// decode reads a small JSON body, reporting a failure to the client.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(out); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "the request could not be read")
		return false
	}
	return true
}

// decodeOptional reads a body that is allowed to be absent, leaving out at its
// zero value when there is none.
//
// For an endpoint whose body only ever adds to what it would do anyway --
// approving a registration, with or without a group to put them in. Deciding
// from Content-Length would get a chunked request wrong; an empty stream reads
// as io.EOF and nothing else does.
func (s *Server) decodeOptional(w http.ResponseWriter, r *http.Request, out any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(out)
	if err == nil || errors.Is(err, io.EOF) {
		return true
	}
	s.writeError(w, r, http.StatusBadRequest, "the request could not be read")
	return false
}

// handleClearAudit removes the whole history.
//
// It is not a hole in the append-only guarantee. The removal is written into
// the trail as it happens, so what remains says that something was cleared, by
// whom, and how much -- and still verifies as a chain from there.
func (s *Server) handleClearAudit(w http.ResponseWriter, r *http.Request) {
	if s.opts.Pruner == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "history cannot be cleared here")
		return
	}
	now := time.Now()
	removed, err := s.opts.Pruner.Prune(r.Context(), auth.FromContext(r.Context()).ID, now, now)
	if err != nil {
		s.opts.Log.Error("clearing history failed", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "couldn't clear the history")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]int64{"removed": removed})
}

// handleResources reports what this process is costing the machine.
//
// Read rather than admin: an operator diagnosing a slow host should not have
// to be an administrator to see how much memory it is holding, and none of
// these numbers say anything about what the host is monitoring.
func (s *Server) handleResources(w http.ResponseWriter, r *http.Request) {
	started := s.opts.StartedAt
	if started.IsZero() {
		// Better an uptime of zero than a nonsense one measured from the zero
		// time, which would render as fifty-five years.
		started = time.Now()
	}
	s.writeJSON(w, r, http.StatusOK,
		observability.Snapshot(s.opts.Version, started, time.Now()))
}

// handlePerformance reports what the host's own collectors have seen.
//
// Read at request time rather than kept between calls: the registry is the
// authority, and a copy held here would be a second one to keep true.
//
// An empty surface is a valid answer, not an error. A host with metrics
// switched off, or one that has served no calls yet, has nothing to show and
// should say so with an empty table rather than a failure the console has to
// render as something being broken.
func (s *Server) handlePerformance(w http.ResponseWriter, r *http.Request) {
	if s.opts.Performance == nil {
		s.writeJSON(w, r, http.StatusOK, observability.Performance{
			Tools:    []observability.ToolStats{},
			Upstream: []observability.UpstreamStats{},
			Cache:    []observability.CacheStats{},
		})
		return
	}
	s.writeJSON(w, r, http.StatusOK, s.opts.Performance())
}

// handleUpdates reports the running version against what has been published.
func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	if s.opts.Updates == nil {
		s.writeJSON(w, r, http.StatusOK, updates.Status{
			Current: s.opts.Version,
			Error:   "this build has no update checker",
		})
		return
	}
	s.writeJSON(w, r, http.StatusOK, s.opts.Updates.Status(r.Context(), false))
}

// handleCheckUpdates fetches now rather than when the cache expires.
func (s *Server) handleCheckUpdates(w http.ResponseWriter, r *http.Request) {
	if s.opts.Updates == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this build has no update checker")
		return
	}
	s.writeJSON(w, r, http.StatusOK, s.opts.Updates.Status(r.Context(), true))
}

// handleRestart stops this host so that whatever supervises it starts it
// again.
//
// mcpd cannot restart itself in any other sense: it is one process, and the
// thing that brings it back is Docker's restart policy or the systemd unit.
// So this exits cleanly and trusts the supervisor -- which is why Restart is
// nil when nothing is supervising, and why this refuses rather than pretending
// in that case. Stopping a host that nothing will restart is not a restart.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if s.opts.Restart == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"nothing is supervising this host, so stopping it would not start "+
				"it again. Restart it the way it was started.")
		return
	}

	who := auth.FromContext(r.Context()).ID
	if who == "" {
		who = "unknown"
	}
	s.opts.Log.WarnContext(r.Context(), "restart requested from the dashboard",
		"principal", who)

	if err := s.opts.Restart("requested by " + who); err != nil {
		s.writeError(w, r, http.StatusConflict, err.Error())
		return
	}
	// Answered before the process goes, so the browser gets a reply rather
	// than a dropped connection it would report as a failure.
	s.writeJSON(w, r, http.StatusAccepted, map[string]string{
		"status": "restarting",
		"note": "mcpd is draining and will be started again by its supervisor. " +
			"This page will reconnect on its own.",
	})
}
