package config

import (
	"errors"
	"fmt"
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
	validRoles     = []string{"viewer", "operator", "approver", "admin"}
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
		case u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1":
			// ChatGPT will not connect to a plaintext endpoint, and a bearer
			// token over HTTP is a credential given away.
			add("config: server.public_url must use https (got %q)", u.Scheme)
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
	if !slices.Contains(validModes, c.Auth.Mode) {
		add("config: auth.mode must be one of %v (got %q)", validModes, c.Auth.Mode)
	}
	if c.Auth.Mode == "static" && len(c.Auth.StaticTokens) == 0 {
		add("config: auth.mode is static but no static_tokens are configured; " +
			"the host would be unreachable")
	}
	if c.Auth.Mode == "oauth" || c.Auth.Mode == "mixed" {
		// The issuer defaults to public_url, so one of the two must be set.
		// It ends up in metadata clients fetch, so a wrong value produces a
		// connector that fails only at the final redirect.
		if strings.TrimSpace(c.Auth.OAuth.Issuer) == "" &&
			strings.TrimSpace(c.Server.PublicURL) == "" {
			add("config: auth.oauth.issuer or server.public_url is required for mode %q; "+
				"clients need an absolute URL to reach the authorization endpoints", c.Auth.Mode)
		}
		if b := c.Auth.OAuth.Bootstrap; b.Username != "" && b.PasswordRef == "" {
			add("config: auth.oauth.bootstrap.password_ref is required when a bootstrap " +
				"username is set and auth.mode uses OAuth")
		}
	}
	errs = append(errs, c.validateTokens()...)

	// --- approval ---
	if r := c.Approval.RequireDistinctApproverAtOrAbove; r != "" && !slices.Contains(validRisks, r) {
		add("config: approval.require_distinct_approver_at_or_above must be one of %v (got %q)",
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
	if c.Approval.RequireDistinctApproverAtOrAbove != "" && c.Auth.Mode == "static" {
		out = append(out, fmt.Sprintf(
			"approval.require_distinct_approver_at_or_above is %q but auth.mode is static: "+
				"static tokens cannot distinguish principals, so operations at or above "+
				"that risk level will be refused rather than self-approved.",
			c.Approval.RequireDistinctApproverAtOrAbove))
	}
	if c.Server.PublicURL == "" {
		out = append(out, "server.public_url is unset: OAuth metadata will not be served, "+
			"so ChatGPT cannot discover how to authenticate.")
	}
	return out
}

func sortStrings(s []string) { sort.Strings(s) }
