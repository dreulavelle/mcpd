package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

var (
	tokenIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)
	validRoles     = []string{"user", "admin"}
	validRisks     = []string{"low", "medium", "high", "critical"}
	validModes     = []string{"static", "oauth", "mixed"}
)

// Validate checks the configuration for internal consistency.
//
// Every problem is collected before returning, so an operator fixing a config
// file sees all of the mistakes at once rather than discovering them one
// restart at a time.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	// --- server ---
	if strings.TrimSpace(c.Server.Listen) == "" {
		add("config: server.listen is required")
	}
	if c.Server.PublicURL != "" {
		u, err := url.Parse(c.Server.PublicURL)
		switch {
		case err != nil:
			add("config: server.public_url is not a valid URL: %v", err)
		case u.Host == "":
			add("config: server.public_url has no host")
		case u.Scheme == "https":
			// Fine.
		case u.Scheme == "http" && isPrivateHost(u.Hostname()):
			// Plaintext is permitted on loopback and private networks, which
			// is where development happens. Warnings() says out loud that the
			// bearer token travels in the clear.
		case u.Scheme == "http":
			// A publicly routable plaintext endpoint hands the bearer token to
			// anything on the path, and ChatGPT will not connect to one.
			add("config: server.public_url must use https for a public address "+
				"(got %q for host %q)", u.Scheme, u.Hostname())
		default:
			add("config: server.public_url must use http or https (got %q)", u.Scheme)
		}
	}
	switch c.Server.TLS.Mode {
	case "", "off", "self-signed":
	default:
		add("config: server.tls.mode must be off or self-signed (got %q)", c.Server.TLS.Mode)
	}

	if c.Server.TLS.Enabled() && c.Server.PublicURL != "" {
		if u, err := url.Parse(c.Server.PublicURL); err == nil && u.Scheme == "http" {
			add("config: server.public_url is http but the listener serves https; " +
				"clients would be sent to a port that does not speak plaintext")
		}
	}

	if c.Server.ShutdownTimeout <= 0 {
		add("config: server.shutdown_timeout must be positive")
	}
	if c.Server.FrontendEnabled {
		if strings.TrimSpace(c.Server.FrontendListen) == "" {
			add("config: server.frontend_listen is required when the dashboard is enabled")
		}
		if c.Server.FrontendListen == c.Server.Listen {
			// Sharing a port would put the dashboard and the MCP endpoint
			// behind the same exposure, which defeats the reason they are
			// separate listeners.
			add("config: server.frontend_listen and server.listen must differ")
		}
	}

	// --- storage ---
	if strings.TrimSpace(c.Storage.Path) == "" {
		add("config: storage.path is required")
	} else if !filepath.IsAbs(c.Storage.Path) {
		// A relative path resolves against the working directory, which
		// differs between a systemd unit and a shell. Requiring absolute
		// removes a class of "where did my database go" incidents.
		add("config: storage.path must be absolute (got %q)", c.Storage.Path)
	}
	if c.Storage.RelaxedDurability {
		// Not an error: it is legitimate for test environments. But it must be
		// a deliberate, visible choice.
		errs = append(errs, errRelaxedDurability)
	}

	// --- auth ---
	if c.Auth.Accounts.SessionTTL < 0 {
		add("config: auth.accounts.session_ttl cannot be negative")
	}
	errs = append(errs, c.validateTokens()...)

	// --- approval ---
	if r := c.Approval.InlineMaxRisk; r != "" && !slices.Contains(validRisks, r) {
		add("config: approval.inline_max_risk must be one of %v or empty (got %q)",
			validRisks, r)
	}
	for name, d := range map[string]time.Duration{
		"proposal_ttl": c.Approval.ProposalTTL,
		"approval_ttl": c.Approval.ApprovalTTL,
		"lease_ttl":    c.Approval.LeaseTTL,
	} {
		if d <= 0 {
			add("config: approval.%s must be positive", name)
		}
	}
	if c.Approval.ApprovalTTL > c.Approval.ProposalTTL {
		// Not fatal, but almost certainly a mistake: it means an approval
		// outlives the proposal window that produced it.
		add("config: approval.approval_ttl (%s) exceeds proposal_ttl (%s); "+
			"an approval would outlive the proposal it authorises",
			c.Approval.ApprovalTTL, c.Approval.ProposalTTL)
	}

	// --- tunnel ---
	//
	// The identity fields are checked whenever they are set, not only when the
	// file also enables the tunnel. A tunnel is normally turned on and given
	// its id from the dashboard, so a file that says enabled: false still
	// supplies the defaults every tunnel runs with -- and a stale value there
	// passed -check and then failed at connect time, which is the wrong end of
	// the process to find out.
	if strings.TrimSpace(c.Tunnel.Role) != "" && !slices.Contains(validRoles, c.Tunnel.Role) {
		add("config: tunnel.role must be one of %v (got %q)", validRoles, c.Tunnel.Role)
	}
	if c.Tunnel.Enabled {
		if strings.TrimSpace(c.Tunnel.APIKeyRef) == "" {
			add("config: tunnel.api_key_ref is required when the tunnel is enabled")
		} else if !strings.Contains(c.Tunnel.APIKeyRef, ":") {
			add("config: tunnel.api_key_ref must be a reference such as " +
				"env:OPENAI_TUNNEL_API_KEY, not the key itself")
		}
		if strings.TrimSpace(c.Tunnel.Principal) == "" {
			add("config: tunnel.principal is required; it names the identity requests " +
				"through the tunnel act as")
		}
		if len(c.Tunnel.Plugins) == 0 {
			add(`config: tunnel.plugins is empty; the tunnel would reach nothing. ` +
				`List plugins explicitly or use ["*"]`)
		}
		for _, name := range c.Tunnel.Plugins {
			if name == "*" {
				continue
			}
			if _, known := c.Plugins[name]; !known {
				add("config: tunnel.plugins names %q, which is not configured", name)
			}
		}
	}

	// A catalogue that authenticates every request is worth refusing to start
	// without its credentials, rather than mounting and answering every page
	// with a 401. The other three need nothing, so nothing is checked for them.
	if c.Catalog.PulseMCP {
		if strings.TrimSpace(c.Catalog.PulseMCPAPIKeyRef) == "" {
			add("config: catalog.pulsemcp_api_key_ref is required when " +
				"catalog.pulsemcp is on; PulseMCP's v0.1 API authenticates every request")
		} else if !strings.Contains(c.Catalog.PulseMCPAPIKeyRef, ":") {
			add("config: catalog.pulsemcp_api_key_ref must be a reference such as " +
				"env:PULSEMCP_API_KEY, not the key itself")
		}
		if strings.TrimSpace(c.Catalog.PulseMCPTenant) == "" {
			add("config: catalog.pulsemcp_tenant is required when catalog.pulsemcp is on")
		}
	}

	// --- plugins ---
	for name, p := range c.Plugins {
		if !pluginNamePattern.MatchString(name) {
			add("config: plugin name %q must match %s", name, pluginNamePattern)
		}
		if p.Required && !p.Enabled {
			add("config: plugin %q is marked required but not enabled", name)
		}
	}

	return joinNonFatal(errs)
}

var pluginNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// errRelaxedDurability is surfaced as a warning rather than a hard failure.
var errRelaxedDurability = errors.New(
	"config: storage.relaxed_durability is set; approvals may be lost on power failure")

// validateTokens checks the static credential declarations.
func (c *Config) validateTokens() []error {
	var errs []error
	seenID := make(map[string]bool)
	seenRef := make(map[string]string)

	for i, t := range c.Auth.StaticTokens {
		where := fmt.Sprintf("auth.static_tokens[%d]", i)
		if !tokenIDPattern.MatchString(t.ID) {
			errs = append(errs, fmt.Errorf("config: %s.id %q must match %s",
				where, t.ID, tokenIDPattern))
		}
		if seenID[t.ID] {
			errs = append(errs, fmt.Errorf("config: duplicate token id %q", t.ID))
		}
		seenID[t.ID] = true

		if strings.TrimSpace(t.SecretRef) == "" {
			errs = append(errs, fmt.Errorf("config: %s.secret_ref is required; "+
				"tokens are referenced, never written into the config file", where))
		} else if prev, dup := seenRef[t.SecretRef]; dup {
			// Two credentials sharing a secret are the same credential with
			// two names, which silently breaks revocation and audit.
			errs = append(errs, fmt.Errorf(
				"config: %s and %s share secret_ref %q", prev, where, t.SecretRef))
		} else {
			seenRef[t.SecretRef] = where
		}

		if strings.TrimSpace(t.Principal) == "" {
			errs = append(errs, fmt.Errorf("config: %s.principal is required", where))
		}
		if !slices.Contains(validRoles, t.Role) {
			errs = append(errs, fmt.Errorf("config: %s.role must be one of %v (got %q)",
				where, validRoles, t.Role))
		}
		if len(t.Plugins) == 0 {
			errs = append(errs, fmt.Errorf("config: %s.plugins is empty; "+
				`list plugins explicitly or use ["*"]`, where))
		}
		for _, name := range t.Plugins {
			if name == "*" {
				continue
			}
			if _, known := c.Plugins[name]; !known {
				errs = append(errs, fmt.Errorf(
					"config: %s grants plugin %q, which is not configured", where, name))
			}
		}
	}
	return errs
}

// joinNonFatal separates warnings from errors. Warnings are logged by the
// caller; only real errors abort startup.
func joinNonFatal(errs []error) error {
	var fatal []error
	for _, e := range errs {
		if errors.Is(e, errRelaxedDurability) {
			continue
		}
		fatal = append(fatal, e)
	}
	if len(fatal) == 0 {
		return nil
	}
	msgs := make([]string, len(fatal))
	for i, e := range fatal {
		msgs[i] = e.Error()
	}
	sort.Strings(msgs)
	return fmt.Errorf("configuration is invalid:\n  - %s", strings.Join(msgs, "\n  - "))
}

// Warnings returns non-fatal configuration concerns worth logging at startup.
func (c *Config) Warnings() []string {
	var out []string
	if c.Storage.RelaxedDurability {
		out = append(out, "storage.relaxed_durability is enabled: "+
			"approvals may be lost on power failure. Do not use this in production.")
	}
	if c.Server.PublicURL == "" {
		out = append(out, "server.public_url is unset: the dashboard cannot show "+
			"an address to connect a client to.")
	} else if u, err := url.Parse(c.Server.PublicURL); err == nil && u.Scheme == "http" {
		if !isLoopbackHost(u.Hostname()) {
			// Loopback never leaves the machine and needs no warning. Anything
			// else means the credential crosses a network.
			out = append(out, fmt.Sprintf(
				"server.public_url is plaintext http on %s: bearer tokens travel in the "+
					"clear to anything on that network. Acceptable on a trusted LAN; put "+
					"TLS in front before exposing it further. ChatGPT will not connect to "+
					"a plaintext endpoint.", u.Hostname()))
		}
	}
	if c.Metrics.Enabled && !c.Server.FrontendEnabled {
		// Not an error: a headless deployment is a legitimate choice, and
		// refusing to start over a metrics endpoint would be out of
		// proportion. But a scrape config pointing at a port that answers 404
		// is a monitoring gap somebody discovers during an incident.
		out = append(out, "metrics.enabled is set but server.frontend_enabled is not: "+
			"/metrics is served on the dashboard listener, so nothing is exposing it.")
	}
	if c.Metrics.Enabled && c.Metrics.Public {
		out = append(out, "metrics.public is set: /metrics is served without "+
			"authentication and names every plugin, every tool, and how long each "+
			"upstream takes. Only do this where the dashboard listener is already "+
			"reachable only from a monitoring network.")
	}
	return out
}

// isLoopbackHost reports whether a host resolves only to this machine, by
// name as well as by address.
func isLoopbackHost(host string) bool {
	lower := strings.ToLower(strings.Trim(host, "[]"))
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return true
	}
	ip := net.ParseIP(lower)
	return ip != nil && ip.IsLoopback()
}

// isPrivateHost reports whether a host is loopback or on a private network.
//
// Plaintext is acceptable there because the traffic does not cross a network
// the operator does not control. It is not a judgement that the traffic is
// safe -- Warnings() still says the token is in the clear -- only that the
// tradeoff is theirs to make on their own LAN.
func isPrivateHost(host string) bool {
	if host == "" {
		return false
	}
	// Named loopback, plus the .local and .internal suffixes used on LANs.
	lower := strings.ToLower(host)
	if lower == "localhost" ||
		strings.HasSuffix(lower, ".localhost") ||
		strings.HasSuffix(lower, ".local") ||
		strings.HasSuffix(lower, ".internal") {
		return true
	}

	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		// A public hostname, or one this check cannot classify. Fail closed.
		return false
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsUnspecified()
}

func sortStrings(s []string) { sort.Strings(s) }

// oauthMounted reports whether the built-in authorization server is served.
