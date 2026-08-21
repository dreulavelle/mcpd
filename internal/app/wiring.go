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

// registerPlugins mounts every enabled plugin.
//
// Registration is explicit: this switch is the complete list of integrations
// this binary can serve. Adding one means adding a case here, which keeps the
// set auditable and prevents a plugin from mounting itself through an init()
// side effect.
func (a *App) registerPlugins(ctx context.Context) error {
	for _, name := range a.cfg.EnabledPlugins() {
		pc := a.cfg.Plugins[name]
		deps := a.pluginDeps(name)

		// The switch is on the type, not the name. They are the same thing
		// until someone configures two instances of one integration, at which
		// point the name is what tells them apart everywhere else.
		var p plugins.Plugin
		switch pc.ResolvedType(name) {
		case "echo":
			p = echoplugin.New(deps)

		case "cnmaestro":
			var cnCfg cnmaestro.Config
			if err := decodeSettings(pc.Settings, &cnCfg); err != nil {
				return fmt.Errorf("app: plugin %q settings: %w", name, err)
			}
			built, err := cnmaestro.New(deps, cnCfg)
			if err != nil {
				return err
			}
			p = built

		default:
			return fmt.Errorf("app: plugin %q has type %q, which is enabled in "+
				"configuration but not compiled into this binary",
				name, pc.ResolvedType(name))
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
