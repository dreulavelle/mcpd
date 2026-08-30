// Package app is the composition root. It is the only place that knows which
// concrete implementations satisfy which interfaces, and the only place that
// names specific plugins.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/admin"
	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/apikeys"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/auth/sso"
	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/backup"
	"github.com/spoked/mcpd/internal/config"
	mcphost "github.com/spoked/mcpd/internal/mcp"
	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/messaging"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/registry"
	"github.com/spoked/mcpd/internal/servertls"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
	"github.com/spoked/mcpd/internal/trust"
	"github.com/spoked/mcpd/internal/tunnel"
	"github.com/spoked/mcpd/internal/updates"
)

// Version is the host version, overridden at build time via -ldflags.
// App holds every long-lived component.
type App struct {
	cfg     *config.Config
	log     *slog.Logger
	db      *sqlite.DB
	ops     *sqlite.OperationStore
	outbox  *sqlite.OutboxStore
	audit   *sqlite.AuditStore
	manager *plugins.Manager
	health  *observability.HealthRegistry
	// metrics is nil when the endpoint is switched off. Every method on it is
	// nil-safe, so nothing downstream branches on that.
	metrics *observability.Metrics
	// errors is nil unless an operator supplied a DSN, which is what crash
	// reporting being off looks like. Every method on it is nil-safe, which
	// matters here more than for metrics: the call sites are panic handlers,
	// and a forgotten check in one would turn a recovered panic into a second.
	errors *observability.ErrorReporter

	accounts *users.Store
	groups   *groups.Store
	keys     *apikeys.Store
	trust    *trust.Store
	// trustPool caches the roots built from that store: the system ones plus
	// every certificate an operator added.
	trustPool     trustPool
	logStream     *observability.LogStream
	sso           *sso.Service
	ssoStates     *sso.StateStore
	types         *plugins.Catalog
	approval      *operations.ApprovalPolicy
	opsService    *operations.Service
	executor      *operations.Executor
	reaper        *operations.Reaper
	bus           *messaging.InProcessBus
	publisher     *messaging.Publisher
	tunnels       *tunnel.Group
	tunnelFactory tunnel.ServerFactory
	tunnelCheck   *tunnel.Checker
	settings      *settings.Store
	tls           *servertls.Materials

	// bypasses are the windows in which this host stops asking. Read on every
	// proposal; see activeBypass for why it is not cached.
	bypasses *sqlite.BypassStore

	// calls is the record of who called what. Distinct from the audit trail,
	// which is for administrative acts and mutations inside the transaction
	// that made them, and from the counters, which cannot name a caller.
	calls *sqlite.ToolCallStore

	// backups writes and stages whole-instance archives. keyFingerprint
	// identifies the settings encryption key its archives are readable under;
	// empty when this host has no key.
	backups        *backup.Service
	keyFingerprint string

	// mcpStore holds the imported remote MCP servers and their tool
	// snapshots. The cache beside it exists because instances() consults them
	// on nearly every dashboard request, and it is refreshed by the only code
	// that writes them.
	mcpStore   *sqlite.MCPServerStore
	mcpMu      sync.RWMutex
	mcpServers map[string]mcpservers.Server

	// accounts holds the ChatGPT accounts tunnels connect with, and the
	// per-account rate limiters built from them. A store rather than settings
	// keys because an account is a privilege grant with a credential attached
	// and there can be any number of them; see migration 0018.
	chatgpt  *sqlite.ChatGPTAccountStore
	limiters *accountLimiters

	// pluginOverrides is what the dashboard has said about the plugins the
	// configuration file declares: removed, or switched on or off. It is a
	// store rather than a settings key because it overrides the deployment's
	// own configuration, which is an administrative act and belongs in the
	// audit trail. Cached beside it for the same reason the servers are --
	// instances() reads it on nearly every request.
	pluginOverrides *sqlite.PluginOverrideStore
	overrideMu      sync.RWMutex
	overrideCache   map[string]sqlite.PluginOverride

	// catalog browses the public catalogues of MCP servers. Each source holds
	// its own TTL cache and reaches the network only when a request asks it
	// to; nil when the deployment has switched every source off.
	catalog *registry.Index

	// serving closes once the MCP listener is accepting, so anything that has
	// to reach mcpd over HTTP -- the tunnel client probing itself, above all
	// -- can wait rather than fail against a port nothing is on yet.
	serving chan struct{}

	// lastReconcileErr holds why an instance is not mounted, when the reason
	// is not "it has not been configured yet". A plugin that failed to start
	// has no health record to carry the message, and the operator who just
	// pasted the credential is the person who needs it.
	reconcileMu      sync.Mutex
	lastReconcileErr map[string]string

	workers     sync.WaitGroup
	stopWorkers context.CancelFunc
	// restartCh carries a dashboard-requested restart to Run, which drains
	// and exits so the supervisor starts a new process.
	restartCh chan string
	// startedAt is when this process began, for the uptime the resources
	// panel reports.
	startedAt time.Time
	// updates answers what the newest published release is, when the operator
	// has switched checking on.
	updates  *updates.Checker
	server   *http.Server
	frontend *http.Server
	host     *mcphost.Host
}

// Option adjusts how the application is built.
type Option func(*options)

type options struct {
	logControl *observability.LogControl
	logStream  *observability.LogStream
}

// WithLogControl hands New the control that changes the running logger.
//
// How much mcpd says and in what shape are settings, and settings are in a
// database that is not open when the logger has to exist. This is how the
// stored values reach a logger that was already built.
func WithLogControl(ctl *observability.LogControl) Option {
	return func(o *options) { o.logControl = ctl }
}

// WithLogStream hands the dashboard the copy of the log it shows. Absent, the
// Logs page says the host is not keeping one rather than showing an empty
// screen that looks like silence.
func WithLogStream(s *observability.LogStream) Option {
	return func(o *options) { o.logStream = s }
}

// New builds the application graph. It opens the database, applies migrations,
// wires authentication and authorization, registers plugins, and constructs
// the HTTP server — but starts nothing.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger, opts ...Option) (*App, error) {
	var built options
	for _, opt := range opts {
		opt(&built)
	}

	for _, w := range cfg.Warnings() {
		log.WarnContext(ctx, "configuration warning", "detail", w)
	}

	if err := os.MkdirAll(cfg.StorageDir(), 0o750); err != nil {
		return nil, fmt.Errorf("app: create storage directory: %w", err)
	}

	// Before anything opens the database, because this is the only moment a
	// restore is safe: the file can be replaced while nothing holds it, no
	// connection is open, and no component is yet holding state read from it.
	// A staged restore that fails here stops the start rather than being
	// skipped -- a host that quietly carried on with the database somebody
	// asked it to replace would be the worst outcome available.
	if err := backup.ApplyPending(cfg.StorageDir(), cfg.Storage.Path, log); err != nil {
		return nil, err
	}
	// A backup writes its snapshot to a directory beside the database. One
	// left behind is a download that was abandoned, and at a cold start there
	// is nothing writing to it.
	backup.SweepWorkDirs(cfg.StorageDir(), log)
	// And the instances past restores replaced. Kept, because a restore is not
	// undoable any other way; bounded, because each one is a database.
	backup.PruneSuperseded(cfg.StorageDir(), backup.KeepSuperseded, log)

	db, err := openStorage(ctx, cfg, log)
	if err != nil {
		return nil, err
	}

	applied, err := sqlite.Migrate(ctx, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	version, _ := sqlite.SchemaVersion(ctx, db)
	log.InfoContext(ctx, "database ready",
		"path", db.Path(), "schema_version", version, "migrations_applied", applied)

	// Built before the graph, because several components take an observer and
	// a nil one taken before this ran would record nothing for the life of the
	// process.
	var metrics *observability.Metrics
	if cfg.Metrics.Enabled {
		metrics = observability.NewMetrics()
	}

	a := &App{
		restartCh:  make(chan string, 1),
		startedAt:  time.Now(),
		metrics:    metrics,
		cfg:        cfg,
		log:        log,
		db:         db,
		ops:        sqlite.NewOperationStore(db, time.Now),
		outbox:     sqlite.NewOutboxStore(db, time.Now),
		audit:      sqlite.NewAuditStore(db),
		calls:      sqlite.NewToolCallStore(db, time.Now),
		bypasses:   sqlite.NewBypassStore(db, time.Now),
		mcpStore:   sqlite.NewMCPServerStore(db, time.Now),
		mcpServers: map[string]mcpservers.Server{},
		catalog:    buildCatalog(cfg.Catalog, metrics, log),
		serving:    make(chan struct{}),
		health:     observability.NewHealthRegistry(2 * time.Second),

		pluginOverrides: sqlite.NewPluginOverrideStore(db, time.Now),
		overrideCache:   map[string]sqlite.PluginOverride{},
	}
	// Runtime configuration lives in the database so it can be managed from
	// the dashboard. Secrets in it are encrypted with a key that stays
	// outside, which is what makes typing one into a form safe.
	//
	// First, because almost everything below is built from it now: what this
	// host advertises, how patient its listeners are, how long a session
	// lasts, what the approval policy is. The file supplies where the database
	// is and the key that opens it, and stops there.
	var cipher *settings.Cipher
	if ref := cfg.SecretKeyRef; ref != "" {
		key, keyErr := settings.ResolveKey(ref, os.Getenv("CREDENTIALS_DIRECTORY"))
		if keyErr != nil {
			db.Close()
			return nil, fmt.Errorf("app: settings encryption key: %w", keyErr)
		}
		if cipher, err = settings.NewCipher(key); err != nil {
			db.Close()
			return nil, err
		}
		// A hash of the key rather than the key, kept so a backup can say
		// which key its contents are readable under without this process
		// holding the key anywhere it could be logged or serialised.
		a.keyFingerprint = backup.Fingerprint(key)
	} else {
		log.WarnContext(ctx, "no settings encryption key is configured; "+
			"secrets cannot be set from the dashboard. "+
			"Generate one with: openssl rand -base64 32")
	}
	a.settings = settings.NewStore(db, cipher, time.Now)
	a.backups = a.newBackupService(cfg, db, log)
	// Passed only when there is one. A nil *settings.Cipher handed to an
	// interface parameter is a non-nil interface holding a nil pointer, which
	// would get past the store's own guard and panic at the first credential.
	if cipher != nil {
		a.chatgpt = sqlite.NewChatGPTAccountStore(db, cipher, time.Now)
	} else {
		a.chatgpt = sqlite.NewChatGPTAccountStore(db, nil, time.Now)
	}
	a.limiters = newAccountLimiters()

	// One turn for the startup file, on the first start after an upgrade, and
	// then never again. See configimport.go for why it is one turn and not a
	// precedence rule.
	imported, err := a.importLegacyConfig(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	// And the same one turn for the credentials that predated accounts. It
	// runs after the import so that a deployment arriving from config.yaml on
	// this very start has its key in the store to be carried.
	if err := a.seedChatGPTAccount(ctx); err != nil {
		// Not fatal. An account that could not be seeded leaves the tunnels
		// unstarted and says so on their status, which is recoverable from the
		// dashboard; refusing to boot is not.
		log.WarnContext(ctx, "could not carry the existing ChatGPT credentials "+
			"into an account", "error", err)
	}
	// With exactly one account, write down which one each tunnel uses. Nothing
	// running changes; what it prevents is adding a second account stopping
	// every tunnel that never had to name one.
	if err := a.pinTunnelsToTheOnlyAccount(ctx); err != nil {
		log.WarnContext(ctx, "could not record which ChatGPT account the existing "+
			"tunnels use; adding a second account will stop them until each one "+
			"names an account", "error", err)
	}
	a.logStream = built.logStream
	a.applyLogSettings(ctx, built.logControl)

	// After the settings store and the legacy import, because the DSN is a
	// setting: a reporter built before this would be built from nothing on
	// every start.
	//
	// The cost is that a crash before this line is not reported. That is the
	// right trade rather than a gap to close -- the alternative is another
	// authority for one key, and a file that could switch on sending data off
	// the machine without the dashboard showing it.
	if a.errors, err = a.buildErrorReporter(ctx); err != nil {
		// Not fatal. Crash reporting is a convenience for whoever ships this,
		// and refusing to start a customer's monitoring host over a bad
		// reporting DSN would be serving our interest at their expense.
		log.ErrorContext(ctx, "crash reporting is configured but could not start; "+
			"continuing without it", "error", err)
		a.errors = nil
	}
	// Warnings and errors become breadcrumbs, so a crash report says what led
	// up to the crash rather than only where it landed. Returns the logger
	// unchanged when reporting is off, which is the normal case.
	if a.errors != nil {
		log = observability.AttachBreadcrumbs(log, a.errors)
		a.log = log
	}
	// Not on the start that did the importing. On that one the store holds
	// what the file supplied because it was just put there, so there is
	// nothing to disagree about and saying so would read as a complaint about
	// an upgrade that went right. From the next start on, anything the file
	// still names that differs is worth hearing about every time.
	if !imported {
		for _, w := range a.staleConfigWarnings(ctx) {
			log.WarnContext(ctx, "a setting is being read from the database, "+
				"not from where it is written", "detail", w)
		}
	}

	// Read once, because what they configure is built once. Every one of them
	// is declared ApplyRestart, which is what the dashboard tells an operator
	// who changes one.
	boot := resolveStartup(ctx, a.settings)
	for _, w := range (config.Effective{
		PublicURL:         boot.publicURL,
		FrontendEnabled:   boot.frontendEnabled,
		MetricsEnabled:    cfg.Metrics.Enabled,
		RelaxedDurability: boot.relaxedDurability,
		TLSSelfSigned:     boot.tlsSelfSigned,
	}).Warnings() {
		log.WarnContext(ctx, "configuration warning", "detail", w)
	}

	// Loaded before anything asks what is configured: a remote server is an
	// instance, and instances() must be complete the first time it is called.
	if err := a.loadMCPServers(ctx); err != nil {
		db.Close()
		return nil, err
	}
	// Same reason, and before anything reads the instance list: a removal
	// applied one request late is a plugin served once after it was removed.
	if err := a.loadOverrides(ctx); err != nil {
		db.Close()
		return nil, err
	}

	// Accounts back the dashboard's sign-in. They are unrelated to the token
	// verifier below: a person signs in with a password and gets a session, a
	// script presents a static token, and neither excludes the other.
	// No account is provisioned here. An instance with none offers to create
	// the first from the dashboard, and whoever does becomes administrator.
	a.accounts = users.NewStore(db, time.Now)

	// Groups are the one place plugin access is decided, for an account and
	// for a key alike. The key store is handed the group store rather than
	// resolving grants itself, so there is exactly one union in the process.
	a.groups = groups.NewStore(db, time.Now)
	a.keys = apikeys.NewStore(db, a.groups, time.Now)
	a.trust = trust.NewStore(db, time.Now)
	// Loaded before anything that reaches an upstream is built: a plugin holds
	// the client it was constructed with, so a pool loaded afterwards would be
	// believed by nothing that was already running.
	a.loadTrustPool(ctx)

	fileTokens, err := buildVerifier(cfg, log)
	if err != nil {
		db.Close()
		return nil, err
	}
	// Static tokens first, then keys issued here. The order is the whole of
	// the compatibility promise: a credential declared in the configuration
	// file is matched in memory and answered exactly as it was before database
	// keys existed, and only a token no file entry matches costs a query.
	verifier := apikeys.NewVerifier(a.keys, fileTokens, log)

	authorizer := auth.NewAuthorizer()
	a.approval = operations.NewApprovalPolicy(authorizer)

	// The bus and publisher exist before plugins register, because a
	// mutation's propose tool needs somewhere to send the resulting event.
	a.bus = messaging.NewInProcessBus(log)
	a.publisher = messaging.NewPublisher(
		sqlite.MessagingAdapter{OutboxStore: a.outbox}, a.bus, log,
		messaging.PublisherConfig{}, time.Now, a.metrics.OutboxPublished)

	ids := operations.NewULIDGenerator(time.Now)
	// Read on every use rather than snapshotted, so a TTL changed in the
	// dashboard applies to the next proposal instead of the next restart.
	policyFn := func() operations.Policy { return a.approvalPolicy(context.Background()) }

	a.opsService = operations.NewService(a.ops, a.approval, policyFn,
		log, time.Now, ids, a.publisher.Notify)

	types, err := builtinTypes()
	if err != nil {
		db.Close()
		return nil, err
	}
	a.types = types

	// After the settings store, because the providers are settings: a client
	// secret pasted into the dashboard has to work without a restart, which
	// is why the service reads them per flow rather than holding a copy.
	a.buildSSO()

	a.manager = plugins.NewManager(log, Version, a.toolGate(authorizer), a.opsService,
		inlinePolicyFunc(policyFn), a.newToolObserver())

	// What an instance covers, in the operator's words, for the tool
	// descriptions and the connect-time instructions. Read at each build
	// rather than captured, so editing it and remounting is enough.
	a.manager.SetPurposeSource(func(instance string) string {
		return a.settings.String(context.Background(),
			settings.PluginSettingKey(instance, settings.PluginPurposeKey), "")
	})

	// A settings change rebuilds the plugin it belongs to, so the form takes
	// effect rather than writing somewhere nothing reads until a restart.
	// Registered before plugins mount, so a change during startup is not lost.
	a.watchPluginSettings()

	if err := a.registerPlugins(ctx); err != nil {
		db.Close()
		return nil, err
	}

	// The executor bridges back into the plugin registry, so it can only be
	// built once plugins are mounted.
	a.executor = operations.NewExecutor(a.ops, plugins.NewRunner(a.manager),
		operations.ExecutorConfig{
			InstanceID: instanceID(cfg),
			LeaseTTL:   a.settings.FieldDuration(ctx, settings.KeyApprovalLeaseTTL),
		}, log, time.Now, ids, a.publisher.Notify)

	a.reaper = operations.NewReaper(a.ops, log, time.Now, ids, a.publisher.Notify, 30*time.Second)

	// The executor subscribes to approvals. The event is only a hint to look:
	// Execute reloads and revalidates everything from the database.
	if err := a.bus.Subscribe("mcpd-executor", messaging.SubjectOperationApproved,
		func(ctx context.Context, e messaging.Event) error {
			if e.OperationID == "" {
				return nil
			}
			return a.executor.Execute(ctx, e.OperationID)
		}); err != nil {
		db.Close()
		return nil, err
	}

	// Issued before the listeners that present it. A browser reaching the
	// dashboard and a direct MCP client both verify against this, and the CA
	// is downloadable from the dashboard so it can be trusted once.
	//
	// The tunnel needs nothing from it: it drives an MCP server in this
	// process over an in-memory transport and never dials mcpd back.
	if boot.tlsSelfSigned {
		materials, err := servertls.EnsureSelfSigned(
			cfg.TLSDir(),
			servertls.HostsFor(boot.publicURL, cfg.Server.Listen),
			time.Now())
		if err != nil {
			return nil, err
		}
		a.tls = materials
		log.InfoContext(ctx, "serving https with mcpd's own certificate",
			"hosts", materials.Hosts,
			"expires", materials.NotAfter.Format(time.RFC3339),
			"issued_now", materials.Issued,
			"ca", materials.CAPath)
	}

	if err := a.buildTunnel(cfg, authorizer, log); err != nil {
		db.Close()
		return nil, err
	}

	a.registerHealthChecks()
	a.registerMetrics()

	host, err := mcphost.NewHost(mcphost.Options{
		Log:            log,
		Manager:        a.manager,
		Verifier:       verifier,
		Authorizer:     authorizer,
		Health:         a.health,
		Errors:         a.errors,
		Plugins:        func() []string { return a.manager.Names() },
		SessionTimeout: cfg.Server.SessionTimeout,
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	a.host = host

	// The dashboard runs on its own listener. Agents reach MCP over a tunnel;
	// operators reach the dashboard on an internal interface, and a firewall
	// rule can only tell them apart if they are separate ports.
	if boot.frontendEnabled {
		a.updates = updates.New(Version, func() updates.Config {
			ctx := context.Background()
			return updates.Config{
				Enabled:    a.settings.FieldBool(ctx, settings.KeyUpdatesEnabled),
				Repository: a.settings.FieldString(ctx, settings.KeyUpdatesRepo),
				Interval: time.Duration(
					a.settings.Int(ctx, settings.KeyUpdatesInterval, 24)) * time.Hour,
			}
		}, nil, time.Now)

		dashboard := admin.NewServer(admin.Options{
			Log:        log.With("component", "dashboard"),
			Verifier:   verifier,
			Authorizer: authorizer,
			Approval:   a.approval,
			Service:    a.opsService,
			Repo:       a.ops,
			Manager:    a.manager,
			Health:     a.health,
			Errors:     a.errors,
			Version:    Version,
			StartedAt:  a.startedAt,
			Updates:    a.updates,
			Restart:    a.RequestRestart,
			Audit:      a.audit,
			Logs:       a.logStream,
			Metrics: func() http.Handler {
				if a.metrics == nil {
					return nil
				}
				return a.metrics.Handler()
			}(),
			MetricsPublic: cfg.Metrics.Public,
			// Nil when nothing is collecting, so the console renders an empty
			// surface rather than zeroes that look like measurements.
			Performance: func() func() observability.Performance {
				if a.metrics == nil {
					return nil
				}
				return a.metrics.Performance
			}(),
			Pruner:            a.audit,
			Backup:            a.backups,
			Calls:             a.calls,
			Bypasses:          a.bypasses,
			PublicURL:         a.publicURL,
			FrontendPublicURL: a.frontendPublicURL,
			Accounts:          a.accounts,
			Identities:        a.accounts,
			Groups:            a.groups,
			Keys:              a.keys,
			Certificates:      a.trust,
			TrustChanged:      a.trustChanged,
			KeyGrants: func(ctx context.Context, keyID string) ([]string, error) {
				return a.groups.Effective(ctx, groups.Key(keyID))
			},
			SSO:                a.sso,
			RegistrationPolicy: a.registrationPolicy,
			Catalog:            a.settingsCatalog,
			PluginType: func(instance string) string {
				for _, inst := range a.instances(context.Background()) {
					if inst.Name == instance {
						return inst.Type
					}
				}
				return instance
			},
			PluginTypes: func() []admin.PluginTypeInfo {
				out := make([]admin.PluginTypeInfo, 0)
				for _, t := range a.types.Types() {
					out = append(out, admin.PluginTypeInfo{
						Name: t.Name, Title: t.Title, Description: t.Description,
						Configurable: len(t.Settings) > 0,
					})
				}
				return out
			},
			Instances: func(ctx context.Context) []admin.PluginInstanceInfo {
				out := make([]admin.PluginInstanceInfo, 0)
				for _, inst := range a.instances(ctx) {
					info := admin.PluginInstanceInfo{
						Name: inst.Name, Type: inst.Type,
						Runtime:  string(inst.Runtime),
						FromFile: inst.FromFile, Enabled: inst.Enabled,
						Required:  inst.Required,
						Removed:   inst.Removed,
						RemovedBy: inst.RemovedBy,
						RemovedAt: inst.RemovedAt,
						Problem:   a.reconcileProblem(inst.Name),
					}
					// A removed instance is not waiting on anything: it is not
					// going to mount whatever is filled in, and listing its
					// unset fields would read as the reason it is not running.
					if !inst.Removed {
						_, info.Missing = a.ready(ctx, inst)
					}
					if inst.FromFile {
						info.Declaration = a.declarationFor(inst.Name)
					}
					out = append(out, info)
				}
				return out
			},
			StaleRemovals: func(ctx context.Context) []admin.StaleRemoval {
				out := make([]admin.StaleRemoval, 0)
				for _, ov := range a.staleRemovals(ctx) {
					out = append(out, admin.StaleRemoval{
						Name:         ov.Name,
						DeclaredType: ov.DeclaredType,
						RemovedBy:    ov.Actor,
						RemovedAt:    time.UnixMilli(ov.UpdatedAt),
					})
				}
				return out
			},
			// The catalogues are built here and fetch nothing. The first
			// call to one happens on the first request that needs it, so a
			// catalogue that is down, or a deployment with no route to it,
			// costs a page rather than a boot. Nil when every source is
			// switched off, which the handler reports as no catalogue being
			// configured.
			ServerCatalog: catalogAPI(a.catalog),
			MCPServers: admin.MCPServerAPI{
				List: func(ctx context.Context) (any, error) {
					return a.MCPServers(ctx)
				},
				Tools:        a.MCPServerTools,
				Import:       a.ImportMCPServer,
				Remove:       a.RemoveMCPServer,
				SetEnabled:   a.SetMCPServerEnabled,
				Discover:     a.DiscoverMCPServer,
				Classify:     a.ClassifyMCPTool,
				Schema:       mcpservers.SchemaDocument,
				AddHeader:    a.AddMCPServerHeader,
				RemoveHeader: a.RemoveMCPServerHeader,
			},
			AddPlugin:        a.AddInstance,
			RemovePlugin:     a.RemoveInstance,
			RestorePlugin:    a.RestoreInstance,
			SetPluginEnabled: a.SetInstanceEnabled,
			SessionTTL:       a.sessionTTL,
			Plugins:          func() []string { return a.manager.Names() },
			Assignments:      func() map[string]string { return a.tunnelAssignments(context.Background()) },
			Directory: func(accountID string) *tunnel.Directory {
				// Read at call time: the admin key belongs to a stored
				// account, and one captured at startup would be the key the
				// deployment began with rather than the one just saved.
				return a.chatgptDirectory(context.Background(), accountID)
			},
			ChatGPTAccounts:      a.ListChatGPTAccounts,
			AddChatGPTAccount:    a.AddChatGPTAccount,
			UpdateChatGPTAccount: a.UpdateChatGPTAccount,
			RemoveChatGPTAccount: a.RemoveChatGPTAccount,
			AccountAssignments: func() map[string]string {
				return a.tunnelAccountAssignments(context.Background())
			},
			CACertificate: func() []byte {
				if a.tls == nil {
					return nil
				}
				return a.tls.CAPEM
			},
			Tunnel:     a.tunnels,
			TunnelInfo: func() any { return a.tunnelCheck.Info() },
			Settings:   a.settings,
			Bootstrap:  func() []admin.BootstrapSetting { return bootstrapSettings(cfg) },
			PluginSettings: func(name string) map[string]any {
				return cfg.Plugins[name].Settings
			},
		})
		a.frontend = &http.Server{
			Addr:              cfg.Server.FrontendListen,
			Handler:           dashboard.Handler(),
			ReadHeaderTimeout: boot.readHeaderTimeout,
			ReadTimeout:       boot.readTimeout,
			WriteTimeout:      boot.writeTimeout,
			IdleTimeout:       boot.idleTimeout,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		}
	}

	a.server = &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           host.Handler(),
		ReadHeaderTimeout: boot.readHeaderTimeout,
		ReadTimeout:       boot.readTimeout,
		WriteTimeout:      boot.writeTimeout,
		IdleTimeout:       boot.idleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	if a.tls != nil {
		a.server.TLSConfig = a.tls.TLSConfig()
	}
	return a, nil
}

// toolGate returns the middleware every plugin tool call passes through.
//
// Authorization runs here rather than inside plugin handlers so that a plugin
// author cannot forget it: a tool that skips the check does not exist, because
// the check is applied by the registry when the tool is attached.
func (a *App) toolGate(az *auth.Authorizer) plugins.ToolMiddleware {
	return func(ctx context.Context, tool string, required auth.Capability) error {
		principal := auth.FromContext(ctx)
		plugin, _, _ := splitToolName(tool)
		if d := az.AuthorizeTool(principal, plugin, required); !d.Allowed {
			observability.Logger(ctx).WarnContext(ctx, "tool call denied",
				"tool", tool, "principal", principal.ID,
				"code", d.Code, "reason", d.Reason)
			return d.Error()
		}
		// Every tool call that got through, at debug. This is the line that
		// answers "did the assistant even ask for that", which is where a
		// support call about a missing answer usually starts -- and it is
		// exactly what a bounded set of breadcrumbs should carry into a crash
		// report.
		observability.Logger(ctx).DebugContext(ctx, "tool call",
			"tool", tool, "plugin", plugin, "principal", principal.ID,
			"capability", string(required))
		return nil
	}
}

// splitToolName separates the plugin prefix the registry attaches.
func splitToolName(qualified string) (plugin, bare string, ok bool) {
	for i := range len(qualified) {
		if qualified[i] == '_' {
			return qualified[:i], qualified[i+1:], true
		}
	}
	return qualified, "", false
}

// registerMetrics wires the numbers whose authority is not this process.
//
// A counter kept in Go would answer "how many operations has this process seen
// since it started", which is a different question from the one an operator
// asks and a worse one: it resets on restart and never mentions the row that
// has been sitting in indeterminate since Tuesday. These are read from the
// database when a scrape arrives.
func (a *App) registerMetrics() {
	if a.metrics == nil {
		return
	}

	a.metrics.AddGauge("mcpd_operations",
		"Operations currently in each state, by plugin and action.",
		[]string{"plugin", "action", "state"},
		func(ctx context.Context) []observability.Sample {
			counts, err := a.ops.StateCounts(ctx)
			if err != nil {
				a.log.Warn("could not read operation counts for metrics", "error", err)
				return nil
			}
			out := make([]observability.Sample, 0, len(counts))
			for _, c := range counts {
				out = append(out, observability.Sample{
					Labels: []string{c.Plugin, c.Action, c.State},
					Value:  float64(c.Count),
				})
			}
			return out
		})

	// Separate from the total rather than a fourth label on it. "How many
	// changes happened with nobody being asked" is the question a standing
	// rule creates, and it is asked on its own.
	a.metrics.AddGauge("mcpd_operations_authorized_by_rule",
		"Operations a standing rule authorised, rather than a person, by plugin and action.",
		[]string{"plugin", "action", "state"},
		func(ctx context.Context) []observability.Sample {
			counts, err := a.ops.StateCounts(ctx)
			if err != nil {
				return nil
			}
			out := make([]observability.Sample, 0, len(counts))
			for _, c := range counts {
				out = append(out, observability.Sample{
					Labels: []string{c.Plugin, c.Action, c.State},
					Value:  float64(c.AuthorizedByRule),
				})
			}
			return out
		})

	a.metrics.AddGauge("mcpd_outbox_pending",
		"Committed events not yet delivered to a consumer.",
		nil,
		func(ctx context.Context) []observability.Sample {
			n, err := a.outbox.PendingCount(ctx)
			if err != nil {
				return nil
			}
			return []observability.Sample{{Value: float64(n)}}
		})
}

// registerHealthChecks wires the components the readiness probe reports on.
func (a *App) registerHealthChecks() {
	a.health.Register("database", func(ctx context.Context) observability.Check {
		if err := a.db.Reader().PingContext(ctx); err != nil {
			return observability.Check{
				Status: observability.StatusDown, Critical: true,
				Message: "database unreachable",
			}
		}
		return observability.Check{Status: observability.StatusUp, Critical: true}
	})

	a.health.Register("outbox", func(ctx context.Context) observability.Check {
		n, err := a.outbox.PendingCount(ctx)
		if err != nil {
			return observability.Check{
				Status:  observability.StatusDegraded,
				Message: "outbox backlog unreadable",
			}
		}
		// A backlog means events are not reaching consumers. The system stays
		// correct — the database is the authority — so this degrades
		// readiness rather than failing it.
		if n > 1000 {
			return observability.Check{
				Status:  observability.StatusDegraded,
				Message: fmt.Sprintf("%d events pending publication", n),
			}
		}
		return observability.Check{Status: observability.StatusUp}
	})

	a.health.Register("plugins", func(ctx context.Context) observability.Check {
		reports := a.manager.CheckHealth(ctx)
		var degraded, down []string
		for name, h := range reports {
			switch h.State {
			case plugins.DegradedState:
				degraded = append(degraded, name)
			case plugins.UnhealthyState:
				down = append(down, name)
			}
		}
		switch {
		case len(down) > 0:
			// One failing plugin must not take the host out of rotation:
			// unrelated integrations are still serving correctly.
			return observability.Check{
				Status:  observability.StatusDegraded,
				Message: fmt.Sprintf("unhealthy: %v", down),
			}
		case len(degraded) > 0:
			return observability.Check{
				Status:  observability.StatusDegraded,
				Message: fmt.Sprintf("degraded: %v", degraded),
			}
		}
		return observability.Check{Status: observability.StatusUp}
	})
}

// Addr returns the configured listen address.
func (a *App) Addr() string { return a.cfg.Server.Listen }

// PluginNames returns the mounted plugin names.
func (a *App) PluginNames() []string { return a.manager.Names() }

// Handler exposes the HTTP handler, for tests.
func (a *App) Handler() http.Handler { return a.host.Handler() }

// nowMillis returns the current time in Unix milliseconds.
func nowMillis() int64 { return time.Now().UnixMilli() }

// instanceID identifies this process in leases and attempt records. It is
// derived from the hostname so that a multi-instance deployment attributes
// work correctly without any additional configuration.
func instanceID(cfg *config.Config) string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "mcpd"
	}
	return host + ":" + cfg.Server.Listen
}

// buildTunnel constructs the embedded tunnel and its release checker.
//
// The checker runs whether or not the tunnel is enabled, because knowing a
// newer client exists is useful before turning one on.
func (a *App) buildTunnel(cfg *config.Config, authorizer *auth.Authorizer, log *slog.Logger) error {
	// The same trust as a plugin gets. This reaches a public address, which
	// needs no help -- until the deployment sits behind a proxy that reissues
	// every certificate under the company's own authority, which is the same
	// deployment that needed this feature in the first place.
	a.tunnelCheck = tunnel.NewChecker(a.pluginHTTPClient(), log, 24*time.Hour)

	a.tunnels = tunnel.NewGroup(log.With("component", "tunnel"))
	a.tunnelFactory = func(principal *auth.Principal) (*sdkmcp.Server, error) {
		granted := authorizer.VisiblePlugins(principal, a.manager.Names())
		if len(granted) == 0 {
			return nil, fmt.Errorf("the tunnel principal is granted no mounted plugins")
		}
		// Its own server, not the shared one: the principal is attached below
		// as middleware, and writing that into a cached instance would give
		// every other caller this identity and stack another layer on every
		// reconnect -- so a changed role would appear to save and do nothing.
		srv, err := a.manager.BuildServer(granted)
		if err != nil {
			return nil, err
		}
		// The identity is attached here because an in-memory transport carries
		// no HTTP request. Every tool call through this server sees the
		// tunnel's configured principal, and the same authorization checks
		// then apply as they would to a bearer token.
		srv.AddReceivingMiddleware(func(next sdkmcp.MethodHandler) sdkmcp.MethodHandler {
			return func(ctx context.Context, method string, req sdkmcp.Request) (sdkmcp.Result, error) {
				// Only calls are limited. Rate-limiting the handshake or a
				// listing would refuse a connector at the point it is trying
				// to connect, which reads as a broken tunnel rather than a
				// busy one -- and neither of those reaches an upstream, which
				// is what the limit is protecting.
				//
				// Keyed on TokenID, which an account's principal sets to the
				// account id. One limiter therefore covers every tunnel the
				// account owns, rather than one allowance per connector.
				if method == "tools/call" && principal != nil {
					if err := a.limiters.allow(principal.TokenID); err != nil {
						return nil, err
					}
				}
				return next(auth.WithPrincipal(ctx, principal), method, req)
			}
		})
		return srv, nil
	}

	if err := a.tunnels.Apply(context.Background(), a.tunnelConfigs(context.Background()), a.tunnelFactory); err != nil {
		// Not fatal. A tunnel that will not start is reported on its own
		// status, and the rest of mcpd -- including every other tunnel --
		// works without it.
		log.Warn("some tunnels did not start", "error", err)
	}

	// A change made in the dashboard has to reach the running tunnel, not just
	// the database. Without this the settings form writes to a store nothing
	// reads, which is worse than not offering the form at all.
	a.settings.Watch(func(changed []string) {
		if !containsPrefix(changed, "tunnel.") {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := a.tunnels.Apply(ctx, a.tunnelConfigs(ctx), a.tunnelFactory); err != nil {
				// Already recorded on the tunnel's status, which is where an
				// operator will look.
				log.Warn("tunnel did not reconnect after a settings change", "error", err)
			}
		}()
	})

	return nil
}

// tunnelConfigs resolves every tunnel mcpd should be running.
//
// There is one per connector. A tunnel forwards to exactly one MCP endpoint,
// so a connector limited to a single system needs a tunnel of its own bound to
// that system's endpoint -- which is what a per-plugin tunnel id means. The
// tunnel without a plugin serves the aggregate endpoint, and both kinds can
// run at once.
//
// Each one connects with a ChatGPT account, which supplies the credential and
// the identity. A tunnel whose account is missing is not started: it would
// otherwise fall back to some other account's key, and a connector quietly
// authenticating as the wrong workspace is worse than one that does not come
// up.
func (a *App) tunnelConfigs(ctx context.Context) []tunnel.Config {
	base := a.tunnelConfig(ctx)
	accounts := a.chatgptAccounts(ctx)

	var out []tunnel.Config
	if id := a.settings.String(ctx, settings.KeyTunnelID, ""); id != "" {
		if cfg, ok := a.bindAccount(ctx, base, accounts,
			a.settings.String(ctx, settings.KeyTunnelAccount, ""), ""); ok {
			cfg.TunnelID = id
			out = append(out, cfg)
		}
	}

	// Every configured instance, not only the mounted ones: an assignment to a
	// plugin that has not started is the case worth saying something about.
	mounted := a.manager.Names()
	for _, inst := range a.instances(ctx) {
		name := inst.Name
		id := a.settings.String(ctx, settings.PluginTunnelKey(name), "")
		if id == "" {
			continue
		}
		if !slices.Contains(mounted, name) {
			// A tunnel bound to a plugin that is not serving would answer
			// ChatGPT with an endpoint that has nothing behind it. Skipped, but
			// said out loud: silently ignoring it is how an assignment that
			// looks made comes to do nothing.
			a.log.WarnContext(ctx, "a tunnel is assigned to a plugin that is not running, "+
				"so it is not being started",
				"plugin", name, "tunnel_id", id)
			continue
		}
		if len(out) > 0 && id == out[0].TunnelID {
			// One tunnel cannot serve two endpoints, and running two clients
			// against the same id has them competing for the same commands.
			a.log.WarnContext(ctx, "ignoring a per-plugin tunnel that reuses the main tunnel's id",
				"plugin", name)
			continue
		}

		scoped, ok := a.bindAccount(ctx, base, accounts,
			a.settings.String(ctx, settings.PluginTunnelAccountKey(name), ""), name)
		if !ok {
			continue
		}
		scoped.Plugin = name
		scoped.TunnelID = id
		// Only one client may bind the diagnostics port.
		scoped.DiagnosticsAddr = ""
		out = append(out, scoped)
	}
	return out
}

// bindAccount attaches an account's credential and identity to a tunnel.
//
// plugin is the single system a per-plugin tunnel serves, or empty for the
// aggregate. It is where the two grants meet: the account says what the
// workspace may reach and the tunnel says what this connector is for, and the
// narrower of the two wins. Assigning a tunnel to an account can therefore
// only ever reduce what that tunnel reaches, never widen it -- which is what
// makes an account a bound rather than a suggestion.
//
// Returns false when the tunnel should not run, having said why. Every reason
// is a configuration mistake an operator can fix, and each is named rather
// than collapsed into one message, because "no account" and "that account
// cannot reach this system" call for different fixes.
func (a *App) bindAccount(ctx context.Context, base tunnel.Config, accounts []tunnel.Account, accountID, plugin string) (tunnel.Config, bool) {
	where := "the main tunnel"
	if plugin != "" {
		where = "the tunnel for " + plugin
	}

	acct, ok := accountFor(accounts, accountID)
	if !ok {
		switch {
		case accountID != "":
			a.log.WarnContext(ctx, "a tunnel names a ChatGPT account that no longer exists, "+
				"so it is not being started",
				"tunnel", where, "account_id", accountID)
		case len(accounts) == 0:
			a.log.WarnContext(ctx, "a tunnel has no ChatGPT account to connect with, "+
				"so it is not being started. Add one on the ChatGPT page",
				"tunnel", where)
		default:
			// Several accounts and no choice made. Picking one would be
			// picking whose credential a connector authenticates with, which
			// is not a decision this host gets to make on somebody's behalf.
			a.log.WarnContext(ctx, "a tunnel does not say which ChatGPT account it uses "+
				"and this host has more than one, so it is not being started",
				"tunnel", where, "accounts", len(accounts))
		}
		return tunnel.Config{}, false
	}
	if !acct.Enabled {
		a.log.WarnContext(ctx, "a tunnel's ChatGPT account is switched off, "+
			"so it is not being started",
			"tunnel", where, "account", acct.Name)
		return tunnel.Config{}, false
	}

	cfg := base
	cfg.AccountID = acct.ID
	cfg.AccountName = acct.Name
	cfg.APIKey = acct.APIKey
	cfg.Principal = acct.AsPrincipal()

	if plugin != "" {
		if !cfg.Principal.CanAccessPlugin(plugin) {
			a.log.WarnContext(ctx, "a tunnel serves a system its ChatGPT account is not "+
				"granted, so it is not being started",
				"tunnel", where, "account", acct.Name, "plugin", plugin)
			return tunnel.Config{}, false
		}
		// Scoped by the principal it carries, which is the whole point: the
		// connector reaches this plugin and cannot discover any other. Every
		// tunnel binds in process, so there is no URL to scope instead.
		cfg.Principal.Plugins = []string{plugin}
	}
	return cfg, true
}

// tunnelConfig resolves the settings a tunnel shares with every other one.
//
// What is left here after accounts is what genuinely is one value for the
// whole host: whether tunnels run at all, how loudly the client logs, where
// its diagnostics listener binds, and which control plane it dials. The
// credential, the identity and the grant come from the account instead, and
// are attached by bindAccount.
func (a *App) tunnelConfig(ctx context.Context) tunnel.Config {
	cfg := tunnel.Config{
		Enabled:             a.settings.FieldBool(ctx, settings.KeyTunnelEnabled),
		ControlPlaneBaseURL: a.settings.String(ctx, settings.KeyTunnelControlPlane, ""),
		LogLevel:            slog.LevelInfo,
	}

	// The tunnel talks to an MCP server in this process: no port, no socket,
	// no credential, and mcpd stays entirely private. The tunnel is then the
	// credential, which is what it already is -- the organisation owns it and a
	// runtime key authenticates it.
	//
	// Loopback only, and only inside this process's network namespace: it is
	// unauthenticated, and it exists for someone already on the host running
	// curl against it.
	cfg.DiagnosticsAddr = a.settings.String(ctx, settings.KeyTunnelDiagnostics, "")
	cfg.Debug = a.settings.FieldBool(ctx, settings.KeyTunnelDebug)
	return cfg
}

// tunnelAssignments reports which system each tunnel is pointed at, by tunnel
// id, including assignments to plugins that are not running.
//
// The dashboard shows this rather than deriving it from the running tunnels,
// which cannot distinguish "not assigned" from "assigned and unable to start".
func (a *App) tunnelAssignments(ctx context.Context) map[string]string {
	out := map[string]string{}
	if id := a.settings.String(ctx, settings.KeyTunnelID, ""); id != "" {
		out[id] = ""
	}
	for _, inst := range a.instances(ctx) {
		if id := a.settings.String(ctx, settings.PluginTunnelKey(inst.Name), ""); id != "" {
			out[id] = inst.Name
		}
	}
	return out
}

func containsPrefix(values []string, prefix string) bool {
	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

// bootstrapSettings is the whole of what the startup file still decides.
//
// Four values, and each is here for a reason it can state. The dashboard shows
// them read-only rather than omitting them, because an operator looking for a
// setting should find out where it lives instead of concluding it does not
// exist -- and because "everything else is on this page" is only a useful
// thing to know if the exceptions are named.
func bootstrapSettings(cfg *config.Config) []admin.BootstrapSetting {
	return []admin.BootstrapSetting{
		{
			Key: "server.listen", Label: "Where assistants connect", Value: cfg.Server.Listen,
			Help: "A bind address stored in the database could lock you out with no " +
				"page left to fix it on, so this one stays in the file.",
		},
		{
			Key: "server.frontend_listen", Label: "Where this page is served",
			Value: cfg.Server.FrontendListen,
			Help:  "Same reason: it is how you got here.",
		},
		{
			Key: "storage.path", Label: "Where everything is stored",
			Value: cfg.Storage.Path,
			Help: "Everything mcpd remembers lives here, including these settings. " +
				"It cannot say where it is from inside itself. Worth backing up.",
		},
		{
			Key: "secret_key_ref", Label: "Key that unlocks stored credentials",
			Value: orNone(cfg.SecretKeyRef),
			Help: "A reference, not the key: the key itself is in the environment, " +
				"or in a file beside the database at mode 600. Everything secret in " +
				"the database is encrypted under it, so it is the one thing that " +
				"cannot be kept in there too.",
		},
	}
}

func orNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "not set"
	}
	return v
}

// approvalPolicy resolves the approval settings.
//
// Read on every use rather than snapshotted, so a ceiling changed in the
// dashboard applies to the next proposal instead of the next restart.
func (a *App) approvalPolicy(ctx context.Context) operations.Policy {
	return operations.Policy{
		ProposalTTL: a.settings.FieldDuration(ctx, settings.KeyApprovalProposalTTL),
		ApprovalTTL: a.settings.FieldDuration(ctx, settings.KeyApprovalApprovalTTL),
		LeaseTTL:    a.settings.FieldDuration(ctx, settings.KeyApprovalLeaseTTL),
		InlineApproval: operations.InlineApprovalPolicy{
			MaxRisk: inlineCeiling(a.settings.FieldString(ctx, settings.KeyApprovalInlineMaxRisk)),
		},
		AutoApprove: operations.AutoApprovalPolicy{
			Rules: a.autoApprovalRules(ctx),
			// The same spelling problem as the inline ceiling, and the same
			// answer: "nothing" in a dropdown is the empty level here.
			Unmatched: inlineCeiling(a.settings.FieldString(ctx, settings.KeyApprovalUnmatched)),
		},
		Bypass: a.activeBypass(ctx),
	}
}

// activeBypass reads the window in force, or nil.
//
// Queried rather than cached, because the thing that makes a bypass safe is
// that it stops applying the moment it expires -- and a cache is a place for a
// closed window to keep authorising changes. A proposal already costs several
// writes; one indexed read against a table with a handful of rows is not the
// part worth optimising.
//
// A failure to read is a failure to find one, which leaves every change going
// to a person. That is the direction to fail in.
func (a *App) activeBypass(ctx context.Context) *operations.Bypass {
	if a.bypasses == nil {
		return nil
	}
	b, err := a.bypasses.Active(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "could not read whether a bypass is open; "+
			"changes will be put to a person", "error", err)
		return nil
	}
	return b
}

// inlineCeiling turns the stored enum into the policy's own vocabulary.
//
// The policy spells "nothing may be approved inline" as the empty risk level,
// which a dropdown cannot offer and a person cannot tell apart from a field
// nobody filled in. The dropdown says "none"; this is where the two meet.
func inlineCeiling(stored string) operations.RiskLevel {
	if stored == settings.RiskNone {
		return ""
	}
	return operations.RiskLevel(stored)
}

// autoApprovalRules reads the standing rules that decide which changes are
// authorised without asking anybody.
//
// They live only in the settings store, and deliberately have no fallback in
// the configuration file. A rule is the one setting whose effect is to skip a
// human, so it has to be written where the change is recorded against the
// administrator who made it -- and where an operator can see the whole set on
// the page that owns it, rather than in a file the dashboard cannot show them.
//
// Anything unreadable produces no rules at all, which puts every change to a
// person. It is the only direction to fail in: a policy that loosens when its
// own configuration is corrupt is worse than no policy.
func (a *App) autoApprovalRules(ctx context.Context) []operations.AutoApprovalRule {
	raw, ok, err := a.settings.Get(ctx, settings.KeyApprovalAutoRules)
	if err != nil {
		a.log.ErrorContext(ctx, "the stored approval rules could not be read; "+
			"every change will be put to a person", "error", err)
		return nil
	}
	if !ok {
		return nil
	}
	// Decoded strictly and re-validated on read as well as on write. The write
	// path is not the only way a value reaches this table -- a restore, a hand
	// edit -- and a rule set the current rules refuse must not be the one
	// deciding who gets interrupted.
	rules, err := operations.DecodeRules([]byte(raw))
	if err != nil {
		a.log.ErrorContext(ctx, "the stored approval rules are not valid; "+
			"every change will be put to a person", "error", err)
		return nil
	}
	return rules
}

// inlinePolicyFunc adapts a live policy lookup to the plugins package's
// interface, so an inline ceiling changed in the dashboard applies at once.
type inlinePolicyFunc func() operations.Policy

func (f inlinePolicyFunc) AllowsInline(risk operations.RiskLevel) bool {
	return f().InlineApproval.Allows(risk)
}

// buildErrorReporter reads the crash-reporting settings.
//
// Nil, nil is the ordinary answer: no DSN configured, nothing sent, no client
// built. See internal/observability/errors.go for why that is not the same as
// a disabled client.
func (a *App) buildErrorReporter(ctx context.Context) (*observability.ErrorReporter, error) {
	dsn := a.settings.Secret(ctx, settings.KeyErrorsDSN, "")
	if dsn == "" {
		return nil, nil
	}
	// Stored as a percentage because that is what the form asks for, and a
	// person typing 1 means one in a hundred rather than everything.
	rate := float64(a.settings.Int(ctx, settings.KeyErrorsTraceRate, 0)) / 100

	return observability.NewErrorReporter(observability.ErrorReporterOptions{
		DSN:              dsn,
		Environment:      a.settings.String(ctx, settings.KeyErrorsEnvironment, "production"),
		Release:          Version,
		InstanceLabel:    a.settings.String(ctx, settings.KeyErrorsLabel, ""),
		IncludeMessages:  a.settings.FieldBool(ctx, settings.KeyErrorsMessages),
		TracesSampleRate: rate,
		Log:              a.log,
	})
}

// Errors exposes the reporter to the components that recover panics. Nil is a
// valid reporter and every method on it is safe, so no caller branches.
func (a *App) Errors() *observability.ErrorReporter { return a.errors }
