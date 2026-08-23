package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var (
	tokenIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)
	validRoles     = []string{"user", "admin"}
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
	//
	// Only the bind addresses, because only the bind addresses are here. What
	// the listeners advertise, whether they serve TLS and how patient they are
	// is validated by the settings schema, which is the one validator for the
	// keys it owns whether a value arrives from a form, the API, or the import
	// that runs once on upgrade.
	if strings.TrimSpace(c.Server.Listen) == "" {
		add("config: server.listen is required")
	}
	if strings.TrimSpace(c.Server.FrontendListen) == "" {
		add("config: server.frontend_listen is required")
	}
	if c.Server.FrontendListen == c.Server.Listen {
		// Sharing a port would put the dashboard and the MCP endpoint behind
		// the same exposure, which defeats the reason they are separate
		// listeners.
		add("config: server.frontend_listen and server.listen must differ")
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

	// --- auth ---
	errs = append(errs, c.validateTokens()...)

	// --- plugins ---
	for name, p := range c.Plugins {
		if !pluginNamePattern.MatchString(name) {
			add("config: plugin name %q must match %s", name, pluginNamePattern)
		}
		if p.Required && !p.Enabled {
			add("config: plugin %q is marked required but not enabled", name)
		}
	}

	return joinErrors(errs)
}

var pluginNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// reservedTokenIDPrefix is what a key created in the dashboard begins with.
//
// Spelled out here rather than imported from internal/auth/apikeys: config
// validation runs before anything is built and has no business depending on a
// store. The two are held together by a test that compares them, which is the
// only kind of duplication worth having.
const reservedTokenIDPrefix = "key_"

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

		// A key issued from the dashboard carries a generated id beginning
		// "key_", and both kinds of credential land in the same TokenID field
		// and the same audit column. Refusing the prefix here is what makes a
		// collision impossible rather than unlikely: identifiers are generated
		// on one side, so the only way the two namespaces could meet is an
		// operator choosing one, and that is refused where they can read the
		// reason. An entry naming a credential therefore names exactly one.
		if strings.HasPrefix(t.ID, reservedTokenIDPrefix) {
			errs = append(errs, fmt.Errorf(
				"config: %s.id %q begins with %q, which is reserved for keys "+
					"created in the dashboard; pick another id",
				where, t.ID, reservedTokenIDPrefix))
		}

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

// joinErrors reports every problem at once, so an operator fixing a file sees
// all of them rather than one restart at a time.
func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Error()
	}
	sort.Strings(msgs)
	return fmt.Errorf("configuration is invalid:\n  - %s", strings.Join(msgs, "\n  - "))
}

// Warnings returns the non-fatal concerns this file alone raises.
//
// It is short, because the file is short. Everything else worth saying at
// startup depends on values the settings store owns, and is said by
// Effective.Warnings once those have been read.
func (c *Config) Warnings() []string {
	var out []string
	if c.Metrics.Enabled && c.Metrics.Public {
		out = append(out, "metrics.public is set: /metrics is served without "+
			"authentication and names every plugin, every tool, and how long each "+
			"upstream takes. Only do this where the dashboard listener is already "+
			"reachable only from a monitoring network.")
	}
	return out
}

// Effective is the configuration after the settings store has had its say.
//
// It exists so the warnings that depend on a stored value read the stored
// value. Warning about what the file said would describe a deployment nobody
// is running.
type Effective struct {
	PublicURL         string
	FrontendEnabled   bool
	MetricsEnabled    bool
	RelaxedDurability bool
	TLSSelfSigned     bool
}

// Warnings returns non-fatal concerns worth logging at startup.
//
// Warnings rather than refusals, and that is the change worth naming. When
// these lived in the file a bad value could be fatal, because an operator
// fixes a file with an editor. They live in the database now, and refusing to
// start over one would take away the dashboard that is the only place to
// correct it. So mcpd comes up and says what is wrong.
func (e Effective) Warnings() []string {
	var out []string
	if e.RelaxedDurability {
		out = append(out, "relaxed durability is enabled: "+
			"approvals may be lost on power failure. Do not use this in production.")
	}
	switch u, err := url.Parse(e.PublicURL); {
	case e.PublicURL == "":
		out = append(out, "no public address is set: the dashboard cannot show "+
			"an address to connect a client to. Set it in Settings.")
	case err != nil || u.Host == "":
		out = append(out, fmt.Sprintf(
			"the public address %q is not a usable URL, so the address the "+
				"dashboard hands out will not work.", e.PublicURL))
	case u.Scheme == "http" && e.TLSSelfSigned:
		out = append(out, "the public address is http but the MCP listener serves "+
			"https: clients would be sent to a port that does not speak plaintext.")
	case u.Scheme == "http" && !isLoopbackHost(u.Hostname()):
		// Loopback never leaves the machine and needs no warning. Anything
		// else means the credential crosses a network.
		out = append(out, fmt.Sprintf(
			"the public address is plaintext http on %s: bearer tokens travel in the "+
				"clear to anything on that network. Acceptable on a trusted LAN; put "+
				"TLS in front before exposing it further. ChatGPT will not connect to "+
				"a plaintext endpoint.", u.Hostname()))
	}
	if e.MetricsEnabled && !e.FrontendEnabled {
		// Not an error: a headless deployment is a legitimate choice, and
		// refusing to start over a metrics endpoint would be out of
		// proportion. But a scrape config pointing at a port that answers 404
		// is a monitoring gap somebody discovers during an incident.
		out = append(out, "metrics.enabled is set but the dashboard is switched off: "+
			"/metrics is served on the dashboard listener, so nothing is exposing it.")
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

func sortStrings(s []string) { sort.Strings(s) }
