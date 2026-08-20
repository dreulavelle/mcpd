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
	"path/filepath"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/config"
	mcphost "github.com/spoked/mcpd/internal/mcp"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/storage/sqlite"
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
	manager *plugins.Manager
	health  *observability.HealthRegistry
	server  *http.Server
	host    *mcphost.Host
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
		cfg:    cfg,
		log:    log,
		db:     db,
		ops:    sqlite.NewOperationStore(db, time.Now),
		outbox: sqlite.NewOutboxStore(db, time.Now),
		health: observability.NewHealthRegistry(2 * time.Second),
	}

	verifier, err := buildVerifier(cfg, log)
	if err != nil {
		db.Close()
		return nil, err
	}
	authorizer := auth.NewAuthorizer(auth.RiskPolicy{
		RequireDistinctApproverAtOrAbove: operations.RiskLevel(
			cfg.Approval.RequireDistinctApproverAtOrAbove),
	})

	a.manager = plugins.NewManager(log, Version, a.toolGate(authorizer))

	if err := a.registerPlugins(ctx); err != nil {
		db.Close()
		return nil, err
	}

	a.registerHealthChecks()

	host, err := mcphost.NewHost(mcphost.Options{
		Log:                 log,
		Manager:             a.manager,
		Verifier:            verifier,
		Authorizer:          authorizer,
		Health:              a.health,
		PublicURL:           cfg.Server.PublicURL,
		AuthorizationServer: cfg.Auth.OAuth.Issuer,
		SessionTimeout:      cfg.Server.SessionTimeout,
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	a.host = host

	a.server = &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           host.Handler(),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
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

// dataDir returns the directory holding all mutable state.
func (a *App) dataDir() string { return filepath.Dir(a.cfg.Storage.Path) }

// nowMillis returns the current time in Unix milliseconds.
func nowMillis() int64 { return time.Now().UnixMilli() }
