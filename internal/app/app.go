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
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/admin"
	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/config"
	mcphost "github.com/spoked/mcpd/internal/mcp"
	"github.com/spoked/mcpd/internal/messaging"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/servertls"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
	"github.com/spoked/mcpd/internal/tunnel"
)

// Version is the host version, overridden at build time via -ldflags.
var Version = "dev"

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

	accounts      *users.Store
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

	// serving closes once the MCP listener is accepting, so anything that has
	// to reach mcpd over HTTP -- the tunnel client probing itself, above all
	// -- can wait rather than fail against a port nothing is on yet.
	serving chan struct{}

	workers     sync.WaitGroup
	stopWorkers context.CancelFunc
	server      *http.Server
	frontend    *http.Server
	host        *mcphost.Host
}

// New builds the application graph. It opens the database, applies migrations,
// wires authentication and authorization, registers plugins, and constructs
// the HTTP server — but starts nothing.
func New(ctx context.Context, cfg *config.Config, log *slog.Logger) (*App, error) {
	for _, w := range cfg.Warnings() {
		log.Warn("configuration warning", "detail", w)
	}

	if err := os.MkdirAll(cfg.StorageDir(), 0o750); err != nil {
		return nil, fmt.Errorf("app: create storage directory: %w", err)
	}

	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              cfg.Storage.Path,
		ReadPoolSize:      cfg.Storage.ReadPoolSize,
		BusyTimeout:       cfg.Storage.BusyTimeout,
		RelaxedDurability: cfg.Storage.RelaxedDurability,
	})
	if err != nil {
		return nil, err
	}

	applied, err := sqlite.Migrate(ctx, db)
	if err != nil {
		db.Close()
		return nil, err
	}
	version, _ := sqlite.SchemaVersion(ctx, db)
	log.Info("database ready",
		"path", db.Path(), "schema_version", version, "migrations_applied", applied)

	a := &App{
		cfg:     cfg,
		log:     log,
		db:      db,
		ops:     sqlite.NewOperationStore(db, time.Now),
		outbox:  sqlite.NewOutboxStore(db, time.Now),
		audit:   sqlite.NewAuditStore(db),
		serving: make(chan struct{}),
		health:  observability.NewHealthRegistry(2 * time.Second),
	}

	// Accounts back the dashboard's sign-in. They are unrelated to the token
	// verifier below: a person signs in with a password and gets a session, a
	// script presents a static token, and neither excludes the other.
	// No account is provisioned here. An instance with none offers to create
	// the first from the dashboard, and whoever does becomes administrator.
	a.accounts = users.NewStore(db, time.Now)

	verifier, err := buildVerifier(cfg, log)
	if err != nil {
		db.Close()
		return nil, err
	}

	authorizer := auth.NewAuthorizer()
	a.approval = operations.NewApprovalPolicy(authorizer)

	// The bus and publisher exist before plugins register, because a
	// mutation's propose tool needs somewhere to send the resulting event.
	a.bus = messaging.NewInProcessBus(log)
	a.publisher = messaging.NewPublisher(
		sqlite.MessagingAdapter{OutboxStore: a.outbox}, a.bus, log,
		messaging.PublisherConfig{}, time.Now)

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

	// Runtime configuration lives in the database so it can be managed from
	// the dashboard. Secrets in it are encrypted with a key that stays
	// outside, which is what makes typing one into a form safe.
	//
	// Before plugins are built, because a plugin's own settings live here now
	// and it is constructed from them.
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
	} else {
		log.Warn("no settings encryption key is configured; " +
			"secrets cannot be set from the dashboard. " +
			"Generate one with: openssl rand -base64 32")
	}
	a.settings = settings.NewStore(db, cipher, time.Now)

	a.manager = plugins.NewManager(log, Version, a.toolGate(authorizer), a.opsService,
		inlinePolicyFunc(policyFn))

	if err := a.registerPlugins(ctx); err != nil {
		db.Close()
		return nil, err
	}

	// The executor bridges back into the plugin registry, so it can only be
	// built once plugins are mounted.
	a.executor = operations.NewExecutor(a.ops, plugins.NewRunner(a.manager),
		operations.ExecutorConfig{
			InstanceID: instanceID(cfg),
			LeaseTTL:   cfg.Approval.LeaseTTL,
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
	if cfg.Server.TLS.Enabled() {
		materials, err := servertls.EnsureSelfSigned(
			cfg.TLSDir(),
			servertls.HostsFor(cfg.Server.PublicURL, cfg.Server.Listen),
			time.Now())
		if err != nil {
			return nil, err
		}
		a.tls = materials
		log.Info("serving https with mcpd's own certificate",
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

	host, err := mcphost.NewHost(mcphost.Options{
		Log:            log,
		Manager:        a.manager,
		Verifier:       verifier,
		Authorizer:     authorizer,
		Health:         a.health,
		Plugins:        func() []string { return a.manager.Names() },
		PublicURL:      cfg.Server.PublicURL,
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
	if cfg.Server.FrontendEnabled {
		dashboard := admin.NewServer(admin.Options{
			Log:        log.With("component", "dashboard"),
			Verifier:   verifier,
			Authorizer: authorizer,
			Approval:   a.approval,
			Service:    a.opsService,
			Repo:       a.ops,
			Manager:    a.manager,
			Health:     a.health,
			Version:    Version,
			Audit:      a.audit,
			Pruner:     a.audit,
			PublicURL:  cfg.Server.PublicURL,
			Accounts:   a.accounts,
			Catalog:    a.settingsCatalog,
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
					out = append(out, admin.PluginInstanceInfo{
						Name: inst.Name, Type: inst.Type,
						FromFile: inst.FromFile, Enabled: inst.Enabled,
					})
				}
				return out
			},
			AddPlugin:        a.AddInstance,
			RemovePlugin:     a.RemoveInstance,
			SetPluginEnabled: a.SetInstanceEnabled,
			SessionTTL:       cfg.Auth.Accounts.SessionTTL,
			Plugins:          func() []string { return a.manager.Names() },
			Directory: func() *tunnel.Directory {
				// Read at call time: the admin key is a setting, and one
				// captured at startup would be the key the deployment began
				// with rather than the one just saved.
				ctx := context.Background()
				return tunnel.NewDirectory(
					a.settings.Secret(ctx, settings.KeyTunnelAdminKey, ""),
					a.settings.String(ctx, settings.KeyTunnelOrgID, ""),
					a.cfg.Tunnel.ControlPlaneBaseURL)
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
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
			ReadTimeout:       cfg.Server.ReadTimeout,
			WriteTimeout:      cfg.Server.WriteTimeout,
			IdleTimeout:       cfg.Server.IdleTimeout,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		}
	}

	a.server = &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           host.Handler(),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
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
			observability.Logger(ctx).Warn("tool call denied",
				"tool", tool, "principal", principal.ID,
				"code", d.Code, "reason", d.Reason)
			return d.Error()
		}
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
	a.tunnelCheck = tunnel.NewChecker(newPluginHTTPClient(), log, 24*time.Hour)

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
func (a *App) tunnelConfigs(ctx context.Context) []tunnel.Config {
	base := a.tunnelConfig(ctx)

	var out []tunnel.Config
	if base.TunnelID != "" {
		out = append(out, base)
	}

	for _, name := range a.manager.Names() {
		id := a.settings.String(ctx, settings.PluginTunnelKey(name), "")
		if id == "" {
			continue
		}
		if id == base.TunnelID {
			// One tunnel cannot serve two endpoints, and running two clients
			// against the same id has them competing for the same commands.
			a.log.Warn("ignoring a per-plugin tunnel that reuses the main tunnel's id",
				"plugin", name)
			continue
		}

		scoped := base
		scoped.Plugin = name
		scoped.TunnelID = id
		// Scoped by the principal it carries, which is the whole point: the
		// connector reaches this plugin and cannot discover any other. Every
		// tunnel binds in process, so there is no URL to scope instead.
		scoped.Principal.Plugins = []string{name}
		// Only one client may bind the diagnostics port.
		scoped.DiagnosticsAddr = ""
		out = append(out, scoped)
	}
	return out
}

// tunnelConfig resolves the tunnel's settings.
//
// The store wins over the file, because a value changed in the dashboard has
// to take precedence over the one it was started with. The file supplies
// defaults for a deployment that has never used the dashboard.
func (a *App) tunnelConfig(ctx context.Context) tunnel.Config {
	file := a.cfg.Tunnel

	cfg := tunnel.Config{
		Enabled:             a.settings.Bool(ctx, settings.KeyTunnelEnabled, file.Enabled),
		TunnelID:            a.settings.String(ctx, settings.KeyTunnelID, file.TunnelID),
		ControlPlaneBaseURL: file.ControlPlaneBaseURL,
		LogLevel:            slog.LevelInfo,
		Principal: auth.Principal{
			ID:          a.settings.String(ctx, settings.KeyTunnelPrincipal, orDefault(file.Principal, "svc:chatgpt")),
			DisplayName: "tunnel",
			Role:        auth.Role(a.settings.String(ctx, settings.KeyTunnelRole, orDefault(file.Role, string(auth.RoleUser)))),
			Plugins:     a.settings.Strings(ctx, settings.KeyTunnelPlugins, file.Plugins),
			TokenID:     "tunnel",
		},
	}

	// The tunnel talks to an MCP server in this process: no port, no socket,
	// no credential, and mcpd stays entirely private. The tunnel is then the
	// credential, which is what it already is -- the organisation owns it and a
	// runtime key authenticates it.
	//
	// There is no longer an option to bind over HTTP so the connector can sign
	// people in. It required an authorization server reachable from the public
	// internet, which is the one thing a tunnel exists to avoid: OpenAI's
	// documentation is explicit that the authorization server "is not
	// automatically tunneled" and that Harpoon is "not a general-purpose
	// proxy", so the connector fetched the protected-resource metadata, found
	// an authorization server it could not reach, and stopped.
	// Loopback only, and only inside this process's network namespace: it is
	// unauthenticated, and it exists for someone already on the host running
	// curl against it.
	cfg.DiagnosticsAddr = a.cfg.Tunnel.DiagnosticsAddr
	cfg.Debug = a.settings.Bool(ctx, settings.KeyTunnelDebug, false)

	// The key comes from the store when it is there, and otherwise from the
	// reference in the file, so an existing deployment keeps working.
	cfg.APIKey = a.settings.Secret(ctx, settings.KeyTunnelAPIKey, "")
	if cfg.APIKey == "" && file.APIKeyRef != "" {
		if key, err := config.NewSecretResolver().Resolve(file.APIKeyRef); err == nil {
			cfg.APIKey = key
		}
	}

	// An empty grant reaches nothing, which is never what someone leaving the
	// field blank meant. Everything the principal could see is the sensible
	// reading, and it is still bounded by what is mounted.
	if len(cfg.Principal.Plugins) == 0 {
		cfg.Principal.Plugins = []string{auth.Wildcard}
	}
	return cfg
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func containsPrefix(values []string, prefix string) bool {
	for _, v := range values {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

// bootstrapSettings describes what cannot be edited at runtime.
//
// These are needed before the database opens, so they cannot live in it. The
// dashboard shows them read-only rather than omitting them, because an
// operator looking for a setting should find out where it lives instead of
// concluding it does not exist.
func bootstrapSettings(cfg *config.Config) []admin.BootstrapSetting {
	return []admin.BootstrapSetting{
		{
			Key: "server.listen", Label: "Where assistants connect", Value: cfg.Server.Listen,
			Help: "Has to be known before mcpd can listen, so it can't be changed here.",
		},
		{
			Key: "server.frontend_listen", Label: "Where this page lives",
			Value: cfg.Server.FrontendListen,
		},
		{
			Key: "server.public_url", Label: "Address others use",
			Value: cfg.Server.PublicURL,
			Help:  "How mcpd looks from outside. Must match what assistants actually use.",
		},
		{
			Key: "storage.path", Label: "Where everything is stored",
			Value: cfg.Storage.Path,
			Help:  "Everything mcpd remembers lives here. Worth backing up.",
		},
		{
			Key: "auth.accounts", Label: "How people sign in", Value: "email and password",
			Help: "Everyone has their own account, so the history names a person " +
				"rather than a shared key. Manage them on the Users page.",
		},
	}
}

// approvalPolicy resolves the approval settings, store over file.
func (a *App) approvalPolicy(ctx context.Context) operations.Policy {
	file := a.cfg.Approval

	return operations.Policy{
		ProposalTTL: a.settings.Minutes(ctx, settings.KeyApprovalProposalTTL, file.ProposalTTL),
		ApprovalTTL: a.settings.Minutes(ctx, settings.KeyApprovalApprovalTTL, file.ApprovalTTL),
		LeaseTTL:    a.settings.Minutes(ctx, settings.KeyApprovalLeaseTTL, file.LeaseTTL),
		InlineApproval: operations.InlineApprovalPolicy{
			MaxRisk: operations.RiskLevel(file.InlineMaxRisk),
		},
	}
}

// inlinePolicyFunc adapts a live policy lookup to the plugins package's
// interface, so an inline ceiling changed in the dashboard applies at once.
type inlinePolicyFunc func() operations.Policy

func (f inlinePolicyFunc) AllowsInline(risk operations.RiskLevel) bool {
	return f().InlineApproval.Allows(risk)
}
