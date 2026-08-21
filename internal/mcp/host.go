// Package mcp owns transport and routing. It maps an HTTP path to a plugin's
// MCP server and applies the middleware chain, and it deliberately knows
// nothing about any specific plugin.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/plugins"
)

// Options configures the host.
type Options struct {
	Log        *slog.Logger
	Manager    *plugins.Manager
	Verifier   auth.TokenVerifier
	Authorizer *auth.Authorizer
	Health     *observability.HealthRegistry

	// Plugins names the mounted plugins, so an authentication challenge can
	// say which scopes the endpoint being called actually needs.
	Plugins func() []string

	// PublicURL is the externally reachable base URL. It is what the
	// dashboard renders as a connection address.
	PublicURL string

	// SessionTimeout bounds idle MCP sessions. It has no effect in stateless
	// mode.
	SessionTimeout time.Duration
}

// Host serves every plugin endpoint plus the operational endpoints.
type Host struct {
	opts Options
	mux  *http.ServeMux
}

// NewHost builds the router. Each plugin gets its own path, its own MCP
// server, and its own authorization check, so a principal granted one plugin
// cannot enumerate or invoke another.
func NewHost(opts Options) (*Host, error) {
	if opts.Manager == nil {
		return nil, fmt.Errorf("mcp: host requires a plugin manager")
	}
	if opts.Verifier == nil {
		return nil, fmt.Errorf("mcp: host requires a token verifier")
	}
	if opts.Authorizer == nil {
		return nil, fmt.Errorf("mcp: host requires an authorizer")
	}
	h := &Host{opts: opts, mux: http.NewServeMux()}
	h.routes()
	return h, nil
}

func (h *Host) routes() {
	// Operational endpoints are unauthenticated by design: a load balancer or
	// systemd probe must reach them without credentials. They expose no
	// configuration and no plugin detail beyond aggregate state.
	h.mux.HandleFunc("GET /health/live", h.handleLive)
	h.mux.HandleFunc("GET /health/ready", h.handleReady)

	// One streamable HTTP handler serves every plugin. getServer resolves the
	// path segment to a plugin's MCP server, and returns nil when the caller
	// may not reach it — which the SDK renders as a clean protocol error
	// rather than leaking the plugin's existence.
	streamable := sdk.NewStreamableHTTPHandler(h.resolveServer, &sdk.StreamableHTTPOptions{
		// Stateless mode drops session affinity entirely, which is what keeps
		// a future second instance a configuration change rather than a
		// redesign. It also matches the sessionless direction of the current
		// spec revision.
		Stateless:      true,
		Logger:         h.opts.Log,
		SessionTimeout: h.opts.SessionTimeout,
	})

	// One endpoint per plugin. A credential scoped to one integration reaches
	// only that path; everything else is 404, so it cannot discover what else
	// is deployed.
	h.mux.Handle("/mcp/{plugin}", h.authenticate(streamable))
	h.mux.Handle("/mcp/{plugin}/", h.authenticate(streamable))

	// And one endpoint for everything the caller is granted.
	//
	// This exists because a transport may only be able to target a single
	// address -- OpenAI's tunnel binds one MCP server URL per tunnel, so
	// per-plugin endpoints would mean one tunnel per integration. Tool names
	// already carry their plugin prefix, so combining them cannot collide.
	//
	// It is not a way around scoping: the tool catalogue here is exactly the
	// plugins the presented credential grants, so a token limited to one
	// integration sees one integration's tools whichever path it uses.
	h.mux.Handle("/mcp", h.authenticate(streamable))
	h.mux.Handle("/mcp/", h.authenticate(streamable))
}

// ServeHTTP implements http.Handler.
func (h *Host) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// Handler returns the fully wrapped handler, including correlation and
// recovery. This is what the HTTP server should serve.
func (h *Host) Handler() http.Handler {
	return observability.Correlate(h.opts.Log, h.recover(h.mux))
}

// resolveServer maps a request to a plugin's MCP server.
//
// Authorization already ran in the authenticate middleware, which stores the
// resolved plugin in the request context. Returning nil here means the
// principal may not reach this endpoint.
func (h *Host) resolveServer(r *http.Request) *sdk.Server {
	ctx := r.Context()

	if name := pluginFromContext(ctx); name != "" {
		mounted := h.opts.Manager.Lookup(name)
		if mounted == nil {
			return nil
		}
		return mounted.Server
	}

	// The aggregate endpoint. The plugin set comes from the credential, not
	// from the path, so the catalogue is bounded by the grant either way.
	granted := grantedFromContext(ctx)
	if len(granted) == 0 {
		return nil
	}
	srv, err := h.opts.Manager.AggregateServer(granted)
	if err != nil {
		observability.Logger(ctx).Error("could not build the aggregate endpoint",
			"plugins", granted, "error", err)
		return nil
	}
	return srv
}

// authenticate verifies the bearer credential and checks that the principal is
// granted the plugin named in the path.
//
// Both checks happen before the MCP handler runs, so an unauthorized caller
// never reaches tool dispatch and cannot enumerate a plugin's tools.
func (h *Host) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		log := observability.Logger(ctx)

		name := r.PathValue("plugin")
		token, ok := auth.BearerToken(r)
		if !ok {
			h.challenge(w, r)
			return
		}

		principal, err := h.opts.Verifier.Verify(ctx, token, r)
		if err != nil {
			log.Warn("authentication failed",
				"plugin", name,
				"scheme", h.opts.Verifier.Scheme(),
				"token_fingerprint", auth.Fingerprint(token),
				"remote", clientIP(r))
			h.challenge(w, r)
			return
		}

		ctx = auth.WithPrincipal(ctx, principal)

		if name == "" {
			// The aggregate endpoint: resolve the credential's grants to the
			// plugins that are actually mounted.
			granted := h.opts.Authorizer.VisiblePlugins(principal, h.opts.Manager.Names())
			if !principal.Can(auth.CapRead) || len(granted) == 0 {
				log.Warn("aggregate access denied",
					"principal", principal.ID, "granted", granted)
				h.writeError(w, r, http.StatusNotFound, "unknown endpoint")
				return
			}
			ctx = withGranted(ctx, granted)
			ctx = observability.WithLogger(ctx, log.With(
				"endpoint", "aggregate", "principal", principal.ID))
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// A principal that exists but is not granted this plugin gets 404
		// rather than 403: whether a plugin is mounted is itself information,
		// and an agent scoped to one integration has no business learning
		// which others are deployed.
		if d := h.opts.Authorizer.AuthorizeEndpoint(principal, name); !d.Allowed {
			log.Warn("endpoint access denied",
				"plugin", name, "principal", principal.ID,
				"code", d.Code, "reason", d.Reason)
			h.writeError(w, r, http.StatusNotFound, "unknown endpoint")
			return
		}
		if h.opts.Manager.Lookup(name) == nil {
			h.writeError(w, r, http.StatusNotFound, "unknown endpoint")
			return
		}

		ctx = withPlugin(ctx, name)
		ctx = observability.WithLogger(ctx, log.With(
			"plugin", name, "principal", principal.ID))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// challenge emits a 401 asking for a bearer credential.
//
// It carries no resource_metadata pointer. mcpd is no longer an authorization
// server, so there is nowhere for a client to be sent to obtain a token: the
// tunnel authenticates itself to the control plane, and a machine caller is
// issued a static token out of band. A pointer to a document describing no
// authorization server would only send a client somewhere that cannot help it.
func (h *Host) challenge(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	h.writeError(w, r, http.StatusUnauthorized, "authentication required")
}

// recover turns a panic in a handler into a 500 rather than a dropped
// connection, and logs it with the correlation ID so it can be traced.
func (h *Host) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				observability.Logger(r.Context()).Error("panic serving request",
					"path", r.URL.Path, "panic", fmt.Sprint(v))
				h.writeError(w, r, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// writeError emits a minimal JSON error. The body carries the correlation ID
// so a caller can quote it in a support request, and nothing else: error
// detail belongs in the logs, where only operators can read it.
func (h *Host) writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":          msg,
		"correlation_id": observability.CorrelationID(r.Context()),
	})
}

// clientIP extracts a best-effort client address for logging. It reads the
// direct peer only: X-Forwarded-For is caller-controlled and trusting it would
// let a caller forge the address recorded against a failed authentication.
func clientIP(r *http.Request) string {
	host, _, found := strings.Cut(r.RemoteAddr, ":")
	if !found {
		return r.RemoteAddr
	}
	return host
}

type pluginKey struct{}

// withPlugin records the resolved plugin so the SDK's getServer callback can
// read it without re-parsing the path or re-running authorization.
func withPlugin(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, pluginKey{}, name)
}

func pluginFromContext(ctx context.Context) string {
	name, _ := ctx.Value(pluginKey{}).(string)
	return name
}

type grantedKey struct{}

// withGranted records the plugins the aggregate endpoint may expose for this
// request, resolved from the credential rather than the path.
func withGranted(ctx context.Context, names []string) context.Context {
	return context.WithValue(ctx, grantedKey{}, names)
}

func grantedFromContext(ctx context.Context) []string {
	names, _ := ctx.Value(grantedKey{}).([]string)
	return names
}

// pluginFromPath returns the plugin an endpoint serves, or "" for the
// aggregate endpoint and anything else.
func pluginFromPath(path string) string {
	rest, ok := strings.CutPrefix(strings.Trim(path, "/"), "mcp/")
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, "/")
	return name
}
