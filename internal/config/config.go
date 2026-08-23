// Package config loads and validates the application configuration.
//
// Secrets are never stored here. Configuration carries secret *references* —
// an environment variable name or a systemd credential name — which are
// resolved at the point of use. A config struct that never holds a credential
// cannot leak one through a log line, an error message, or the admin API.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
	"strings"
)

// Config is the complete application configuration.
type Config struct {
	Server   Server   `yaml:"server"`
	Storage  Storage  `yaml:"storage"`
	Auth     Auth     `yaml:"auth"`
	Approval Approval `yaml:"approval"`
	Logging  Logging  `yaml:"logging"`
	// SecretKeyRef points at the key used to encrypt secrets stored in the
	// database. Without it, secrets cannot be set from the dashboard -- they
	// would have to be stored in the clear, which is worse than not offering
	// the feature.
	SecretKeyRef string `yaml:"secret_key_ref"`

	Tunnel  Tunnel                  `yaml:"tunnel"`
	Catalog Catalog                 `yaml:"catalog"`
	Plugins map[string]PluginConfig `yaml:"plugins"`
}

// Catalog says which public catalogues of MCP servers the dashboard browses.
//
// Both are on by default, and both are reached only when an administrator asks
// for a page -- nothing here is fetched at startup, so turning one off is
// about what an operator sees and where this host is willing to make a
// request, not about boot time. Turning both off leaves the catalogue
// endpoints answering "no server catalogue is configured", which is the right
// answer for a deployment that will not reach the internet at all.
type Catalog struct {
	// Official is the official MCP registry at registry.modelcontextprotocol.io.
	Official bool `yaml:"official"`
	// Docker is Docker's MCP catalogue, built from docker/mcp-registry.
	Docker bool `yaml:"docker"`
}

// Enabled reports whether any catalogue is switched on.
func (c Catalog) Enabled() bool { return c.Official || c.Docker }

// Tunnel configures OpenAI's Secure MCP Tunnel, which runs inside mcpd.
//
// It lets ChatGPT reach mcpd without an inbound port, public DNS, or a NAT
// rule: the connection is dialled outward and held open. Because it runs in
// process there is no HTTP request to authenticate, so what the tunnel may
// reach is decided here rather than by a bearer token.
type Tunnel struct {
	Enabled bool `yaml:"enabled"`

	// TunnelID comes from the OpenAI platform's Tunnels settings.
	TunnelID string `yaml:"tunnel_id"`

	// APIKeyRef is a secret reference to a *runtime* API key. An admin key is
	// only for creating and deleting tunnels and must not be used here.
	APIKeyRef string `yaml:"api_key_ref"`

	// Principal is the identity requests arriving through the tunnel act as.
	Principal string `yaml:"principal"`
	// Role is user or admin.
	Role string `yaml:"role"`
	// Plugins lists what the tunnel may reach, or ["*"]. This is the whole of
	// its authorization, so it is worth being specific.
	Plugins []string `yaml:"plugins"`

	// ControlPlaneBaseURL overrides the OpenAI endpoint. Leave empty.
	ControlPlaneBaseURL string `yaml:"control_plane_base_url"`

	// CheckForUpdates enables a daily check for a newer tunnel client release.
	//
	// It only reports. The client is compiled in, so updating means rebuilding
	// mcpd -- which is the point: the code that runs is the code that was
	// built and reviewed.
	CheckForUpdates bool `yaml:"check_for_updates"`

	// DiagnosticsAddr binds the tunnel client's own health and admin listener,
	// which reports OAuth discovery state mcpd cannot see: /readyz separates
	// "still discovering" from "discovery failed", and /api/oauth reports what
	// was actually discovered. Empty leaves it off, which is the default.
	//
	// Bind it to loopback. It is unauthenticated.
	DiagnosticsAddr string `yaml:"diagnostics_addr"`
}

// PluginsDir is where out-of-process plugins are discovered.
//
// Each subdirectory holds a plugin.json and its executable. It is a bind mount
// in the container image, so an integration can be added or upgraded without
// rebuilding mcpd.
func (c *Config) PluginsDir() string {
	if c.Storage.PluginsDir != "" {
		return c.Storage.PluginsDir
	}
	return filepath.Join(c.StorageDir(), "plugins")
}

// TLS configures HTTPS on the MCP listener.
//
// The dashboard listener is deliberately not covered. The two have different
// audiences and different exposure: an MCP client may be reached over a network
// segment worth encrypting, while the dashboard is served on an internal
// interface to an operator who can be given the CA instead.
type TLS struct {
	// Mode is "off" or "self-signed".
	//
	// Self-signed is the only option a private deployment has. A publicly
	// trusted certificate cannot be issued for an address like 192.168.1.10,
	// and a host that already had a real certificate did not need a tunnel.
	Mode string `yaml:"mode"`

	// Dir holds the certificate authority and server certificate. Empty puts
	// them beside the database, which is already the directory that has to
	// survive a restart.
	Dir string `yaml:"dir"`
}

// Enabled reports whether the listener should serve HTTPS.
func (t TLS) Enabled() bool { return t.Mode == "self-signed" }

// Server configures the HTTP listener.
type Server struct {
	// Listen is the bind address. Default is loopback: mcpd expects to sit
	// behind a reverse proxy that terminates TLS, so binding all interfaces by
	// default would expose it in plaintext.
	Listen string `yaml:"listen"`

	// PublicURL is the externally reachable base URL of the MCP endpoint --
	// the Listen address above, not the dashboard.
	//
	// It is what the dashboard renders as a connection address, so it must
	// match what clients actually use. A mismatch surfaces late and
	// confusingly, as a copied address that reaches the wrong listener.
	PublicURL string `yaml:"public_url"`

	// FrontendListen is the bind address for the admin dashboard.
	//
	// It is a separate listener from the MCP endpoint on purpose. The two have
	// different audiences and different exposure: the MCP endpoint is reached
	// by agents over a tunnel, while the dashboard is for operators and is
	// usually kept on an internal interface. Separating them means a firewall
	// rule can tell them apart.
	//
	// Binding below 1024 needs CAP_NET_BIND_SERVICE on Linux; the systemd unit
	// and Docker setup both handle it.
	FrontendListen string `yaml:"frontend_listen"`

	// FrontendEnabled turns the dashboard off entirely.
	FrontendEnabled bool `yaml:"frontend_enabled"`

	// TLS gives the MCP listener a certificate.
	TLS TLS `yaml:"tls"`

	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ReadTimeout       time.Duration `yaml:"read_timeout"`
	WriteTimeout      time.Duration `yaml:"write_timeout"`
	IdleTimeout       time.Duration `yaml:"idle_timeout"`
	ShutdownTimeout   time.Duration `yaml:"shutdown_timeout"`
	SessionTimeout    time.Duration `yaml:"session_timeout"`
}

// Storage configures the database.
type Storage struct {
	Path         string        `yaml:"path"`
	ReadPoolSize int           `yaml:"read_pool_size"`
	BusyTimeout  time.Duration `yaml:"busy_timeout"`

	// RelaxedDurability drops synchronous from FULL to NORMAL.
	//
	// Leave this false in any environment whose approvals matter. Under WAL,
	// NORMAL can lose the most recent transactions on power loss, and those
	// transactions authorise infrastructure changes.
	RelaxedDurability bool `yaml:"relaxed_durability"`

	// PluginsDir overrides where out-of-process plugins are discovered.
	// Defaults to a plugins directory beside the database.
	PluginsDir string `yaml:"plugins_dir"`
}

// Auth configures authentication.
//
// The two halves address different callers and are independent. People sign in
// to the dashboard with an email and password, which becomes a session;
// machines present a static token. Neither is a mode the other excludes.
type Auth struct {
	// StaticTokens are machine credentials. Each names a secret reference
	// rather than carrying the token itself.
	StaticTokens []StaticTokenConfig `yaml:"static_tokens"`

	// Accounts configures the people who sign in to the dashboard.
	Accounts Accounts `yaml:"accounts"`
}

// StaticTokenConfig declares one machine credential and what it may reach.
type StaticTokenConfig struct {
	// ID names the credential for audit and revocation.
	ID string `yaml:"id"`
	// SecretRef names where the token value is read from, e.g.
	// "env:MCPD_TOKEN_AGENT_A" or "credential:agent-a".
	SecretRef string `yaml:"secret_ref"`
	// Principal is the identity this credential asserts.
	Principal string `yaml:"principal"`
	// Role is user or admin.
	Role string `yaml:"role"`
	// Plugins lists the plugins this credential may reach, or ["*"].
	//
	// This is the per-agent scoping control: give an agent a token listing one
	// plugin and it can neither invoke nor enumerate any other.
	Plugins []string `yaml:"plugins"`
}

// Accounts configures the local identities that sign in to the dashboard.
//
// mcpd is no longer an OAuth authorization server. It reaches ChatGPT through
// the tunnel, which carries the connection and the credential both, so the
// authorize/token/consent machinery served no client that exists: signing
// someone in through a tunnel needs mcpd reachable from the public internet,
// which is the one thing a tunnel exists to avoid.
type Accounts struct {
	// SessionTTL bounds a signed-in browser.
	SessionTTL time.Duration `yaml:"session_ttl"`
}

// The first administrator is not configured here. An instance with no accounts
// offers to create one, and whoever does becomes administrator -- which puts
// the only password anyone types into a form rather than into a file, and
// removes the failure where an operator starts the host once with the password
// unset and has to clear a table to get back in.

// Approval configures the risk policy.
type Approval struct {

	// ProposalTTL bounds how long a proposal awaits approval.
	ProposalTTL time.Duration `yaml:"proposal_ttl"`
	// ApprovalTTL bounds how long an approval remains executable. Without it,
	// an approval granted weeks ago could still execute against a network that
	// has since changed.
	ApprovalTTL time.Duration `yaml:"approval_ttl"`
	// LeaseTTL bounds an execution claim before the reaper reclaims it.
	LeaseTTL time.Duration `yaml:"lease_ttl"`

	// InlineMaxRisk is the highest risk a user may approve from a single
	// yes/no prompt raised by their client.
	//
	// Above it the shortcut is withheld, not the decision: the assistant has
	// to show the change in full and be told explicitly before approving it.
	// Either way the person decides in the conversation. Sending them to a
	// separate console to approve is how a gate becomes an obstacle, and an
	// obstacle is what people route around.
	//
	// Empty withholds the prompt for everything, which is the strictest
	// setting rather than a disabled one.
	InlineMaxRisk string `yaml:"inline_max_risk"`
}

// Logging configures output.
type Logging struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// PluginConfig is the host-level configuration common to every plugin.
type PluginConfig struct {
	// Type names the integration this is an instance of. It defaults to the
	// key, so a single instance is configured by naming it after its type and
	// nothing else changes.
	//
	// Separating the two is what lets one integration be configured more than
	// once. Two Synology devices are two plugins as far as the host is
	// concerned -- two endpoints, two entries in a credential's plugin list,
	// two connectors, and a history that says which one acted -- because the
	// name is already the identity everywhere downstream.
	Type string `yaml:"type"`

	Enabled bool `yaml:"enabled"`
	// Required determines whether a startup failure in this plugin fails the
	// host, or only marks the plugin unhealthy.
	Required bool `yaml:"required"`
	// Settings holds plugin-specific configuration, decoded by the plugin.
	Settings map[string]any `yaml:"settings"`
}

// ResolvedType returns the integration this instance is of.
//
// An unset type means the instance is named after its type, which is the
// ordinary single-instance case and what every configuration written before
// instances existed looks like.
func (p PluginConfig) ResolvedType(name string) string {
	if t := strings.TrimSpace(p.Type); t != "" {
		return t
	}
	return name
}

// Load reads, expands, and validates a configuration file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	// Environment overrides layer over the file, so a container image can vary
	// a handful of settings without a rewritten config.
	if err := cfg.applyEnvOverrides(); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Default returns the configuration mcpd runs with when nothing is specified.
func Default() *Config {
	return &Config{
		Server: Server{
			// Loopback by default. mcpd expects a reverse proxy in front of
			// it, and binding publicly by default would serve MCP in plaintext.
			Listen:            "127.0.0.1:8080",
			FrontendListen:    ":80",
			FrontendEnabled:   true,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      120 * time.Second,
			IdleTimeout:       120 * time.Second,
			ShutdownTimeout:   30 * time.Second,
			SessionTimeout:    10 * time.Minute,
		},
		Storage: Storage{
			Path:        "/var/lib/mcpd/mcpd.db",
			BusyTimeout: 5 * time.Second,
		},
		Auth: Auth{
			Accounts: Accounts{
				// Long enough that a working day does not require signing in
				// twice, short enough that an unattended browser does not stay
				// signed in indefinitely.
				SessionTTL: 12 * time.Hour,
			},
		},
		Approval: Approval{
			// Routine changes are confirmed in the conversation; anything
			// weightier goes to the dashboard.
			InlineMaxRisk: "medium",
			ProposalTTL:   30 * time.Minute,
			ApprovalTTL:   15 * time.Minute,
			LeaseTTL:      2 * time.Minute,
		},
		Tunnel: Tunnel{CheckForUpdates: true},
		// Both catalogues on. They cost nothing until a page asks for one,
		// and an operator who wants only the official registry says so rather
		// than discovering that the second one existed and was off.
		Catalog: Catalog{Official: true, Docker: true},
		Logging: Logging{Level: "info", Format: "json"},
		Plugins: map[string]PluginConfig{},
	}
}

// EnabledPlugins returns the names of plugins switched on, sorted for stable
// startup ordering.
func (c *Config) EnabledPlugins() []string {
	var out []string
	for name, p := range c.Plugins {
		if p.Enabled {
			out = append(out, name)
		}
	}
	sortStrings(out)
	return out
}

// TLSDir returns the directory holding the certificate authority.
func (c *Config) TLSDir() string {
	if c.Server.TLS.Dir != "" {
		return c.Server.TLS.Dir
	}
	return filepath.Join(c.StorageDir(), "tls")
}

// StorageDir returns the directory holding the database.
func (c *Config) StorageDir() string { return filepath.Dir(c.Storage.Path) }
