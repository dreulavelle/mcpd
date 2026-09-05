package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/roles"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/plugins/bandwidth"
	"github.com/spoked/mcpd/internal/plugins/bookstack"
	"github.com/spoked/mcpd/internal/plugins/cnmaestro"
	"github.com/spoked/mcpd/internal/plugins/echoplugin"
	"github.com/spoked/mcpd/internal/plugins/external"
	"github.com/spoked/mcpd/internal/plugins/extremecloudiq"
	"github.com/spoked/mcpd/internal/plugins/flowroute"
	"github.com/spoked/mcpd/internal/plugins/graylog"
	"github.com/spoked/mcpd/internal/plugins/observium"
	"github.com/spoked/mcpd/internal/plugins/textable"
	"github.com/spoked/mcpd/internal/plugins/threecx"
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
// from one agent. The role named for each token is resolved the same way: a
// token naming a role this host does not have fails startup, because the
// alternative is a token that authenticates and holds nothing, which reads
// as a broken host rather than a typo in a file.
func buildVerifier(ctx context.Context, cfg *config.Config, rs *roles.Store, log *slog.Logger) (auth.TokenVerifier, error) {
	resolver := config.NewSecretResolver()

	var tokens []*auth.StaticToken
	for _, t := range cfg.Auth.StaticTokens {
		secret, err := resolver.Resolve(t.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("auth: token %q: %w", t.ID, err)
		}
		role, err := resolveRole(ctx, rs, t.Role)
		if err != nil {
			return nil, fmt.Errorf("auth: token %q: %w", t.ID, err)
		}
		grants := auth.GrantsAt(t.Plugins, auth.LevelWrite)
		for _, g := range t.Grants {
			grants = append(grants, auth.Grant{Plugin: g.Plugin, Level: auth.Level(g.Level)})
		}
		st, err := auth.NewStaticToken(t.ID, secret, auth.Principal{
			ID:          t.Principal,
			DisplayName: t.ID,
			RoleID:      role.ID,
			RoleName:    role.Name,
			Permissions: role.Permissions,
			Grants:      grants.Normalize(),
		})
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, st)
		log.Info("static credential loaded",
			"token_id", t.ID, "principal", t.Principal,
			"role", role.Name, "grants", grants.Normalize())
	}

	// Static tokens are the only bearer credential now. People sign in to the
	// dashboard with a password and hold a session; a script that cannot
	// complete a sign-in form presents one of these instead.
	//
	// An empty set is legitimate: a deployment reached only through the tunnel
	// and the dashboard has no machine caller to issue one to.
	return auth.NewStaticVerifier(tokens...)
}

// resolveRole finds the role a configuration file named: a built-in by its
// old or new name, or any role by its name or id.
func resolveRole(ctx context.Context, rs *roles.Store, name string) (*auth.Role, error) {
	if id, ok := auth.LegacyRoleID(strings.ToLower(strings.TrimSpace(name))); ok {
		if r, ok := auth.BuiltinRole(id); ok {
			return &r, nil
		}
	}
	if rs == nil {
		return nil, fmt.Errorf("role %q is not one of reader, operator or administrator", name)
	}
	if r, err := rs.ByName(ctx, name); err == nil {
		return r, nil
	}
	if r, err := rs.ByID(ctx, strings.TrimSpace(name)); err == nil {
		return r, nil
	}
	return nil, fmt.Errorf("role %q does not exist on this host; make it under Settings, Roles, or name a built-in", name)
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
		bandwidth.Type(),
		bookstack.Type(),
		cnmaestro.Type(),
		extremecloudiq.Type(),
		flowroute.Type(),
		graylog.Type(),
		observium.Type(),
		textable.Type(),
		threecx.Type(),
	)
}

// registerPlugins mounts every enabled plugin.
func (a *App) registerPlugins(ctx context.Context) error {
	// Out-of-process plugins first, because they are types the instance list
	// below has to know about before it can mount an instance of one.
	if err := a.discoverExternalTypes(ctx); err != nil {
		return err
	}

	// Said once, here, rather than from the instance list -- which the
	// dashboard reads on every request.
	a.shadowedNames()
	a.overriddenNames()

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
					a.log.ErrorContext(ctx, "skipping a plugin instance of an unknown type",
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
			a.log.InfoContext(ctx, "plugin is waiting to be configured",
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
			a.log.ErrorContext(ctx, "plugin could not be built; continuing without it",
				"plugin", name, "type", inst.Type, "error", err)
			a.noteReconcile(name, err)
			continue
		}
		if err := a.manager.Register(ctx, p, name, pc.Required || inst.Required); err != nil {
			return err
		}
	}

	if len(a.manager.Names()) == 0 {
		a.log.WarnContext(ctx, "no plugins enabled; the host will serve only operational endpoints")
	}
	return nil
}

// discoverExternalTypes turns every plugin in the plugins directory into a
// type, so its instances are configured and mounted like everything else.
//
// Each is started once, long enough to say what it is and what it needs
// configured, and stopped again; the instances built from the type run their
// own processes. A plugin that fails to describe itself is reported and
// skipped unless its manifest marks it required, because one bad directory in
// a bind mount must not stop the others from loading.
//
// A compiled-in type of the same name wins. Otherwise a writable bind mount
// could shadow an integration the operator reviewed.
func (a *App) discoverExternalTypes(ctx context.Context) error {
	dir := a.cfg.PluginsDir()
	manifests, dirs, err := external.Discover(dir, a.log)
	if err != nil {
		return err
	}
	if len(manifests) == 0 {
		return nil
	}
	a.log.InfoContext(ctx, "discovered external plugins", "dir", dir, "count", len(manifests))
	a.externalManifests = map[string]external.Manifest{}

	for _, m := range manifests {
		if _, taken := a.types.Lookup(m.Name); taken {
			a.log.WarnContext(ctx, "ignoring an external plugin that shadows a compiled-in one",
				"plugin", m.Name)
			continue
		}
		describe, err := external.Probe(ctx, dirs[m.Name], m, a.pluginDeps(m.Name))
		if err == nil {
			var t plugins.Type
			t, err = external.TypeFor(dirs[m.Name], m, describe)
			if err == nil {
				err = a.types.Add(t)
			}
		}
		if err != nil {
			if m.Required {
				return fmt.Errorf("app: required external plugin %s: %w", m.Name, err)
			}
			a.log.ErrorContext(ctx, "external plugin could not be described; continuing without it",
				"plugin", m.Name, "error", err)
			continue
		}
		a.externalManifests[m.Name] = m
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
		HTTP:     a.pluginHTTPClient(),
		Now:      time.Now,
		// Both are the metrics surface, handed over through interfaces narrow
		// enough that a plugin can report its own cache and its own upstream
		// latency and nothing else. Nil when the endpoint is switched off.
		Cache:    a.cacheObserver(),
		Upstream: a.upstreamObserver(),
	}
}

// cacheObserver and upstreamObserver hand a plugin the metrics surface, or
// nothing.
//
// Returning a typed nil inside a non-nil interface would be the easy mistake
// here: a plugin checking `if deps.Cache != nil` would then call through to a
// nil receiver. The methods are nil-safe, so it would work -- but relying on
// that makes every call site depend on a detail of another package. Nil in,
// nil out.
func (a *App) cacheObserver() plugins.CacheObserver {
	if a.metrics == nil {
		return nil
	}
	return a.metrics
}

func (a *App) upstreamObserver() plugins.UpstreamObserver {
	if a.metrics == nil {
		return nil
	}
	return a.metrics
}

// newPluginHTTPClient builds the client plugins use to reach upstream systems.
//
// Bounded rather than default: Go's http.DefaultClient has no timeout at all,
// so a hung upstream would pin a goroutine and a tool call indefinitely. The
// connection caps keep one plugin from exhausting file descriptors shared with
// the rest of the host.
//
// roots is nil unless an operator has added certificates, and nil is Go's own
// "use the system store" -- so a deployment that never needed one carries no
// pool and no branch at handshake time.
func newPluginHTTPClient(roots *x509.CertPool) *http.Client {
	var tlsConfig *tls.Config
	if roots != nil {
		// Only RootCAs is set. Everything else about the handshake -- the
		// minimum version, the cipher suites, whether the name is checked --
		// stays whatever Go's defaults are, because a company certificate is a
		// reason to trust one more issuer and not a reason to verify anything
		// less carefully.
		tlsConfig = &tls.Config{RootCAs: roots}
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: tlsConfig,
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
