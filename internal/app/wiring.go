package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/plugins/cnmaestro"
	"github.com/spoked/mcpd/internal/plugins/echoplugin"
	"github.com/spoked/mcpd/internal/plugins/external"
)

// decodeSettings converts a plugin's untyped YAML settings into its own config
// struct.
//
// It round-trips through YAML rather than reflecting field by field, so a
// plugin declares its configuration once, with its own tags, and the host does
// not need to know the shape.
func decodeSettings(settings map[string]any, into any) error {
	if len(settings) == 0 {
		return nil
	}
	encoded, err := yaml.Marshal(settings)
	if err != nil {
		return fmt.Errorf("re-encode settings: %w", err)
	}
	if err := yaml.Unmarshal(encoded, into); err != nil {
		return fmt.Errorf("decode settings: %w", err)
	}
	return nil
}

// buildVerifier constructs the verifier for machine callers.
//
// Secrets are resolved here, once, at startup. A missing credential fails the
// process rather than producing a host that silently rejects every request
// from one agent.
func buildVerifier(cfg *config.Config, log *slog.Logger) (auth.TokenVerifier, error) {
	resolver := config.NewSecretResolver()

	var tokens []*auth.StaticToken
	for _, t := range cfg.Auth.StaticTokens {
		secret, err := resolver.Resolve(t.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("auth: token %q: %w", t.ID, err)
		}
		st, err := auth.NewStaticToken(t.ID, secret, auth.Principal{
			ID:          t.Principal,
			DisplayName: t.ID,
			Role:        auth.Role(t.Role),
			Plugins:     t.Plugins,
		})
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, st)
		log.Info("static credential loaded",
			"token_id", t.ID, "principal", t.Principal,
			"role", t.Role, "plugins", t.Plugins)
	}

	// Static tokens are the only bearer credential now. People sign in to the
	// dashboard with a password and hold a session; a script that cannot
	// complete a sign-in form presents one of these instead.
	//
	// An empty set is legitimate: a deployment reached only through the tunnel
	// and the dashboard has no machine caller to issue one to.
	return auth.NewStaticVerifier(tokens...)
}

// builtinTypes is the complete list of integrations this binary can serve.
//
// Explicit rather than discovered: a type is added by naming it here, which
// keeps the set auditable and stops a plugin mounting itself through an init()
// side effect. It is also what the dashboard offers when someone adds an
// instance, so a type absent here cannot be configured at all.
func builtinTypes() (*plugins.Catalog, error) {
	return plugins.NewCatalog(
		echoplugin.Type(),
		cnmaestro.Type(),
	)
}

// registerPlugins mounts every enabled plugin.
func (a *App) registerPlugins(ctx context.Context) error {
	for _, inst := range a.enabledInstances(ctx) {
		name := inst.Name
		pc := a.pluginConfigFor(name)

		// A remote MCP server has no compiled-in type to look up. What it is,
		// is the document it was imported from, and buildInstance reads that.
		if inst.Runtime != plugins.RuntimeMCP {
			if _, known := a.types.Lookup(inst.Type); !known {
				if !inst.FromFile {
					// A type the binary does not have is a mistake either way,
					// but where the mistake lives decides what to do about it.
					// In the configuration file it is an operator's typo, and
					// failing loudly is how they find it. In the settings
					// store it is a record only the dashboard can correct --
					// and refusing to start removes the dashboard, so the host
					// would be unrecoverable without a SQLite prompt. This is
					// the shape a stale instance record takes after a plugin
					// is removed from a build, or after an earlier version of
					// this host wrote one it should not have.
					err := fmt.Errorf("this instance is recorded as type %q, "+
						"which this build does not have; remove it, or run a "+
						"build that does", inst.Type)
					a.log.Error("skipping a plugin instance of an unknown type",
						"plugin", name, "type", inst.Type)
					a.noteReconcile(name, err)
					continue
				}
				return fmt.Errorf("app: plugin %q has type %q, which is enabled in "+
					"configuration but not compiled into this binary", name, inst.Type)
			}
		}

		// An instance nobody has finished configuring is not mounted. It
		// appears on the Plugins page with its form and serves nothing, and
		// the moment the last required field is filled in it is mounted
		// without a restart. Mounting it now would put tools in front of a
		// model that fail every call.
		if ready, missing := a.ready(ctx, inst); !ready {
			a.log.Info("plugin is waiting to be configured",
				"plugin", name, "type", inst.Type, "missing", missing)
			continue
		}

		p, _, err := a.buildInstance(ctx, inst)
		if err != nil {
			// Same rule as a failed Start: a required plugin failing is the
			// host failing, and anything else is one integration down. A
			// plugin that cannot be built from what it has been given is
			// usually one nobody has finished configuring, and taking the
			// host down for that would remove the page they would configure
			// it on.
			if pc.Required {
				return fmt.Errorf("app: plugin %q: %w", name, err)
			}
			a.log.Error("plugin could not be built; continuing without it",
				"plugin", name, "type", inst.Type, "error", err)
			a.noteReconcile(name, err)
			continue
		}
		if err := a.manager.Register(ctx, p, name, pc.Required); err != nil {
			return err
		}
	}
	if err := a.registerExternalPlugins(ctx); err != nil {
		return err
	}

	if len(a.manager.Names()) == 0 {
		a.log.Warn("no plugins enabled; the host will serve only operational endpoints")
	}
	return nil
}

// registerExternalPlugins mounts every plugin found in the plugins directory.
//
// Discovery is additive: a plugin that fails to start is reported and skipped
// unless its manifest marks it required. One bad directory in a bind mount
// must not stop the others from loading.
func (a *App) registerExternalPlugins(ctx context.Context) error {
	dir := a.cfg.PluginsDir()

	manifests, dirs, err := external.Discover(dir, a.log)
	if err != nil {
		return err
	}
	if len(manifests) == 0 {
		return nil
	}
	a.log.Info("discovered external plugins", "dir", dir, "count", len(manifests))

	for _, m := range manifests {
		// A compiled-in plugin of the same name wins. Otherwise a writable
		// bind mount could shadow an integration the operator reviewed.
		if a.manager.Lookup(m.Name) != nil {
			a.log.Warn("ignoring an external plugin that shadows a compiled-in one",
				"plugin", m.Name)
			continue
		}

		p := external.NewPlugin(dirs[m.Name], m, a.pluginDeps(m.Name))
		if err := p.Handshake(ctx); err != nil {
			if m.Required {
				return fmt.Errorf("app: required external plugin %s: %w", m.Name, err)
			}
			a.log.Error("external plugin failed to start; continuing without it",
				"plugin", m.Name, "error", err)
			continue
		}
		if err := a.manager.Register(ctx, p, m.Name, m.Required); err != nil {
			if m.Required {
				return err
			}
			a.log.Error("external plugin failed to register; continuing without it",
				"plugin", m.Name, "error", err)
			_ = p.Shutdown(ctx)
		}
	}
	return nil
}

// pluginDeps builds the dependency set handed to one plugin.
//
// Everything here is namespaced to the plugin, so a plugin cannot read another
// plugin's stored state or publish events under another plugin's subject.
func (a *App) pluginDeps(name string) plugins.Deps {
	return plugins.Deps{
		Instance: name,
		Log:      a.log.With("plugin", name),
		Store:    newPluginStore(a.db, name),
		Events:   newPluginPublisher(a.db, name),
		Secrets:  newPluginSecrets(a.cfg, name),
		HTTP:     newPluginHTTPClient(),
		Now:      time.Now,
	}
}

// newPluginHTTPClient builds the client plugins use to reach upstream systems.
//
// Bounded rather than default: Go's http.DefaultClient has no timeout at all,
// so a hung upstream would pin a goroutine and a tool call indefinitely. The
// connection caps keep one plugin from exhausting file descriptors shared with
// the rest of the host.
func newPluginHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   8,
			MaxConnsPerHost:       16,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}
}
