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

	// PublicURL is the externally reachable base URL, used to build OAuth
	// protected-resource metadata. ChatGPT reads that document to discover
	// where to authenticate.
	PublicURL string

	// AuthorizationServer is the OAuth issuer URL advertised in the
	// protected-resource metadata. Empty when running with static tokens only.
	AuthorizationServer string

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

	// RFC 9728 protected-resource metadata. ChatGPT fetches this to discover
	// the authorization server for an endpoint.
	h.mux.HandleFunc("GET /.well-known/oauth-protected-resource", h.handleResourceMetadata)
	h.mux.HandleFunc("GET /.well-known/oauth-protected-resource/{path...}", h.handleResourceMetadata)

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

	h.mux.Handle("/mcp/{plugin}", h.authenticate(streamable))
	h.mux.Handle("/mcp/{plugin}/", h.authenticate(streamable))
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
	name := pluginFromContext(r.Context())
	if name == "" {
		return nil
	}
	mounted := h.opts.Manager.Lookup(name)
	if mounted == nil {
		return nil
	}
	return mounted.Server
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
		if name == "" {
			h.writeError(w, r, http.StatusNotFound, "unknown endpoint")
			return
		}

		token, ok := auth.BearerToken(r)
		if !ok {
			// The WWW-Authenticate challenge points the client at the
			// protected-resource metadata document, which is how an MCP
			// client discovers where to obtain a token.
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

		ctx = auth.WithPrincipal(ctx, principal)
		ctx = withPlugin(ctx, name)
		ctx = observability.WithLogger(ctx, log.With(
			"plugin", name, "principal", principal.ID))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// challenge emits a 401 with the RFC 9728 resource-metadata pointer.
func (h *Host) challenge(w http.ResponseWriter, r *http.Request) {
	if h.opts.PublicURL != "" {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(
			`Bearer resource_metadata=%q`,
			strings.TrimRight(h.opts.PublicURL, "/")+"/.well-known/oauth-protected-resource"))
	} else {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
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
