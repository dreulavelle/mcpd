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
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/tunnel"
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
	Version    string
	// Audit reads the append-only trail.
	Audit AuditReader

	// Pruner removes history past its retention, or all of it. It is separate
	// from AuditReader because reading and removing are different rights.
	Pruner AuditPruner

	// PluginTypes lists the integrations this build has, so the dashboard can
	// offer them when someone adds an instance.
	PluginTypes func() []PluginTypeInfo
	// AddPlugin, RemovePlugin and SetPluginEnabled manage instances. They
	// record intent; mounting happens at startup, which is what the response
	// says rather than leaving someone to wonder why the tools never appear.
	AddPlugin        func(ctx context.Context, actor, name, typeName string) error
	RemovePlugin     func(ctx context.Context, actor, name string) error
	SetPluginEnabled func(ctx context.Context, actor, name string, enabled bool) error
	// Instances lists what is configured, mounted or not.
	Instances func(ctx context.Context) []PluginInstanceInfo

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

	// SessionTTL bounds a signed-in browser. Zero leaves the store's default.
	SessionTTL time.Duration

	// PublicURL is the address clients reach, used to render a connect URL an
	// operator can copy rather than assemble.
	PublicURL string

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

	// Directory manages tunnels in the OpenAI organisation. It is a function
	// because the admin key it needs is a setting, and a value captured at
	// startup would be the one the deployment began with rather than the one
	// an operator just saved.
	Directory func() *tunnel.Directory

	// Plugins names the mounted systems, so a tunnel can be assigned to one.
	Plugins func() []string

	// Settings is the runtime configuration store.
	Settings *settings.Store

	// Bootstrap describes the settings that cannot be edited here, so the UI
	// can show them read-only rather than pretending they do not exist.
	Bootstrap func() []BootstrapSetting
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
	// A missing verifier must deny everything rather than panic on the first
	// request. Failing open here would expose the whole dashboard.
	if opts.Verifier == nil {
		opts.Log.Error("dashboard has no token verifier configured; all requests will be refused")
		opts.Verifier = denyAllVerifier{}
	}
	s := &Server{opts: opts, mux: http.NewServeMux()}
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

	// Unauthenticated by necessity and harmless by nature: a CA certificate is
	// a public document, and requiring a sign-in to fetch it would mean
	// needing the browser to trust it before it can be trusted.
	s.mux.HandleFunc("GET /api/tls/ca", s.handleCACertificate)

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
	// Starting and stopping a tunnel changes what an external service can
	// reach, so it takes administrator rights rather than read.
	api("POST /api/tunnel/start", s.handleTunnelStart, auth.CapAdmin)
	api("POST /api/tunnel/stop", s.handleTunnelStop, auth.CapAdmin)
	// Managing tunnels reaches outside this deployment: creating one changes
	// the OpenAI organisation, and deleting one breaks every connector using
	// it, wherever those connectors are.
	api("POST /api/tunnels", s.handleCreateTunnel, auth.CapAdmin)
	api("POST /api/tunnels/{id}/assign", s.handleAssignTunnel, auth.CapAdmin)
	api("DELETE /api/tunnels/{id}", s.handleDeleteTunnel, auth.CapAdmin)
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
	// Accounts decide who can reach anything else here, so administering them
	// is an administrator's right.
	api("GET /api/users", s.handleListUsers, auth.CapAdmin)
	api("POST /api/users", s.handleCreateUser, auth.CapAdmin)
	api("PATCH /api/users/{id}", s.handleUpdateUser, auth.CapAdmin)
	api("DELETE /api/users/{id}", s.handleDeleteUser, auth.CapAdmin)

	// Everything else is the single-page application.
	s.mux.Handle("/", s.staticHandler())
}

// Handler returns the fully wrapped dashboard handler.
func (s *Server) Handler() http.Handler {
	return observability.Correlate(s.opts.Log, s.securityHeaders(s.mux))
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
		Aggregate: s.connectURL("/mcp"),
		PerPlugin: s.connectURL("/mcp/{name}"),
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
	Available []tunnel.TunnelInfo `json:"available,omitempty"`
	// Problem explains why Available is missing, when it should not be.
	Problem string `json:"problem,omitempty"`
	// Missing names the credential needed to manage tunnels, when one is not
	// set. "The feature is off" is not something anyone can act on.
	Missing string `json:"missing,omitempty"`
	// Plugins names the systems a tunnel can be pointed at.
	Plugins []string `json:"plugins"`
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
	dir := s.directory()
	resp.Missing = dir.Missing()
	if dir.Available() {
		resp.CanManage = true
		// A listing failure is reported rather than fatal: the tunnels mcpd is
		// running are known locally and stay visible either way.
		if list, err := dir.List(r.Context()); err != nil {
			resp.Problem = err.Error()
		} else {
			resp.Available = list
			if ws := workspacesIn(list); len(ws) > 0 {
				resp.Workspaces = ws
			}
		}
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
			encoded, err := encodeTyped(field.Kind, value)
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

// encodeTyped converts a form string into the JSON its declared type implies,
// so a number is stored as a number rather than a quoted string.
func encodeTyped(kind settings.Kind, value string) (string, error) {
	switch kind {
	case settings.KindBool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return "", fmt.Errorf("expected true or false")
		}
		return strconv.FormatBool(b), nil

	case settings.KindInt, settings.KindDuration:
		n, err := strconv.Atoi(value)
		if err != nil {
			return "", fmt.Errorf("expected a whole number")
		}
		return strconv.Itoa(n), nil

	case settings.KindList:
		items := []string{}
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				items = append(items, trimmed)
			}
		}
		encoded, err := json.Marshal(items)
		if err != nil {
			return "", err
		}
		return string(encoded), nil

	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

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

	var all []operationDTO
	for _, plugin := range visible {
		ops, err := s.opts.Service.List(r.Context(), principal, plugin, states, limit)
		if err != nil {
			s.writeError(w, r, http.StatusInternalServerError, "could not list operations")
			return
		}
		for _, op := range ops {
			all = append(all, toDTO(op))
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
		"operation": toDTO(op),
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
	s.writeJSON(w, r, http.StatusOK, toDTO(op))
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

		tools := make([]toolDTO, 0, len(m.Registry.ToolNames()))
		for _, name := range m.Registry.ToolNames() {
			kind := "read"
			if proposeTools[name] {
				kind = "propose"
			}
			tools = append(tools, toolDTO{Name: name, Kind: kind})
		}
		for name := range proposeTools {
			tools = append(tools, toolDTO{Name: name, Kind: "propose"})
		}
		sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

		out = append(out, pluginDTO{
			Name: d.Name, Version: d.Version, Title: d.Title,
			Description: d.Description, Endpoint: d.Endpoint(),
			ConnectURL: s.connectURL(d.Endpoint()),
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
func (s *Server) connectURL(endpoint string) string {
	base := strings.TrimRight(s.opts.PublicURL, "/")
	if base == "" {
		return endpoint
	}
	return base + endpoint
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
	ID          string              `json:"id"`
	Plugin      string              `json:"plugin"`
	Action      string              `json:"action"`
	State       string              `json:"state"`
	Risk        string              `json:"risk"`
	Impact      string              `json:"impact"`
	Changes     []operations.Change `json:"changes,omitempty"`
	Target      any                 `json:"target,omitempty"`
	Before      any                 `json:"before,omitempty"`
	Desired     any                 `json:"desired,omitempty"`
	Observed    any                 `json:"observed,omitempty"`
	RequestedBy string              `json:"requested_by"`
	RequestedAt time.Time           `json:"requested_at"`
	ExpiresAt   time.Time           `json:"expires_at"`
	ApprovedBy  string              `json:"approved_by,omitempty"`
	ApprovedAt  *time.Time          `json:"approved_at,omitempty"`
	ExecuteBy   *time.Time          `json:"execute_by,omitempty"`
	TerminalAt  *time.Time          `json:"terminal_at,omitempty"`
	Verified    *bool               `json:"verified,omitempty"`
	Attempts    int                 `json:"attempts"`
	ErrorCode   string              `json:"error_code,omitempty"`
	ErrorDetail string              `json:"error_detail,omitempty"`
	Terminal    bool                `json:"terminal"`
}

func toDTO(op *operations.Operation) operationDTO {
	return operationDTO{
		ID: op.ID, Plugin: op.Plugin, Action: op.Action,
		State: op.State.String(), Risk: op.Risk.String(), Impact: op.Impact,
		Changes:     op.Changes,
		Target:      decodeJSON(op.Target),
		Before:      decodeJSON(op.Before),
		Desired:     decodeJSON(op.Desired),
		Observed:    decodeJSON(op.Observed),
		RequestedBy: op.RequestedBy, RequestedAt: op.RequestedAt,
		ExpiresAt: op.ExpiresAt, ApprovedBy: op.ApprovedBy,
		ApprovedAt: op.ApprovedAt, ExecuteBy: op.ApprovalExpiresAt,
		TerminalAt: op.TerminalAt, Verified: op.OutcomeVerified,
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

// directory returns the tunnel manager, never nil.
func (s *Server) directory() *tunnel.Directory {
	if s.opts.Directory == nil {
		return tunnel.NewDirectory("", "", "")
	}
	return s.opts.Directory()
}

// handleCreateTunnel makes a tunnel at OpenAI and points it at a system.
func (s *Server) handleCreateTunnel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Plugin    string `json:"plugin"`
		Workspace string `json:"workspace_id"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	dir := s.directory()
	if !dir.Available() {
		s.writeError(w, r, http.StatusBadRequest, "add "+dir.Missing()+" first")
		return
	}
	if err := s.checkPlugin(body.Plugin); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	created, err := dir.Create(r.Context(),
		tunnelName(body.Name, body.Plugin), "Created by mcpd", body.Workspace)
	if err != nil {
		s.writeError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	// Assigning is the point of creating: a tunnel nothing is bound to is an
	// object in someone's account doing nothing.
	if err := s.assign(r, created.ID, body.Plugin); err != nil {
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

// handleAssignTunnel points an existing tunnel at a system.
func (s *Server) handleAssignTunnel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Plugin string `json:"plugin"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	if err := s.checkPlugin(body.Plugin); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.assign(r, r.PathValue("id"), body.Plugin); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]string{"status": "assigned"})
}

// handleDeleteTunnel removes a tunnel from the organisation.
func (s *Server) handleDeleteTunnel(w http.ResponseWriter, r *http.Request) {
	dir := s.directory()
	if !dir.Available() {
		s.writeError(w, r, http.StatusBadRequest, "deleting a tunnel needs "+dir.Missing())
		return
	}
	id := r.PathValue("id")
	if err := dir.Delete(r.Context(), id); err != nil {
		s.writeError(w, r, http.StatusBadGateway, err.Error())
		return
	}
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

// assign records which system a tunnel serves.
func (s *Server) assign(r *http.Request, id, plugin string) error {
	if s.opts.Settings == nil {
		return fmt.Errorf("settings are unavailable")
	}
	key := settings.KeyTunnelID
	if plugin != "" {
		key = settings.PluginTunnelKey(plugin)
	}
	encoded, err := json.Marshal(id)
	if err != nil {
		return err
	}
	return s.opts.Settings.Apply(r.Context(), auth.FromContext(r.Context()).ID, []settings.Change{
		{Key: key, Value: string(encoded)},
	})
}

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
	ctx := r.Context()

	var changes []settings.Change
	keys := []string{settings.KeyTunnelID}
	if s.opts.Plugins != nil {
		for _, name := range s.opts.Plugins() {
			keys = append(keys, settings.PluginTunnelKey(name))
		}
	}
	for _, key := range keys {
		var current string
		if found, err := s.opts.Settings.GetJSON(ctx, key, &current); err != nil || !found {
			continue
		}
		if current == id {
			changes = append(changes, settings.Change{Key: key, Delete: true})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return s.opts.Settings.Apply(ctx, auth.FromContext(ctx).ID, changes)
}

// decode reads a small JSON body, reporting a failure to the client.
func (s *Server) decode(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(out); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "the request could not be read")
		return false
	}
	return true
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
