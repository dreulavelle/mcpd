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
)

// Config is the complete application configuration.
type Config struct {
	Server   Server                  `yaml:"server"`
	Storage  Storage                 `yaml:"storage"`
	Auth     Auth                    `yaml:"auth"`
	Approval Approval                `yaml:"approval"`
	Logging  Logging                 `yaml:"logging"`
	Plugins  map[string]PluginConfig `yaml:"plugins"`
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

// Server configures the HTTP listener.
type Server struct {
	// Listen is the bind address. Default is loopback: mcpd expects to sit
	// behind a reverse proxy that terminates TLS, so binding all interfaces by
	// default would expose it in plaintext.
	Listen string `yaml:"listen"`

	// PublicURL is the externally reachable base URL of the MCP endpoint --
	// the Listen address above, not the dashboard.
	//
	// It identifies this resource in OAuth metadata and is what the dashboard
	// renders as a connection address, so it must match what clients actually
	// use. A mismatch surfaces late and confusingly: as a connector handshake
	// that fails at the redirect, or a copied address that reaches the wrong
	// listener.
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
type Auth struct {
	// Mode is "static", "oauth", or "mixed".
	Mode string `yaml:"mode"`

	// StaticTokens are machine credentials. Each names a secret reference
	// rather than carrying the token itself.
	StaticTokens []StaticTokenConfig `yaml:"static_tokens"`

	// OAuth configures the resource-server side used by ChatGPT.
	OAuth OAuth `yaml:"oauth"`
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
	// Role is viewer, operator, approver, or admin.
	Role string `yaml:"role"`
	// Plugins lists the plugins this credential may reach, or ["*"].
	//
	// This is the per-agent scoping control: give an agent a token listing one
	// plugin and it can neither invoke nor enumerate any other.
	Plugins []string `yaml:"plugins"`
}

// OAuth configures the built-in authorization server.
//
// mcpd is its own authorization server. The alternative is requiring an
// operator to run Keycloak or buy Auth0 before ChatGPT can reach their own
// network gear, which is a steep prerequisite for a single-VM deployment.
type OAuth struct {
	// Issuer is the authorization server's base URL. It defaults to
	// server.public_url, which is almost always correct since both describe
	// the same host.
	Issuer string `yaml:"issuer"`

	AccessTokenTTL  time.Duration `yaml:"access_token_ttl"`
	RefreshTokenTTL time.Duration `yaml:"refresh_token_ttl"`
	AuthCodeTTL     time.Duration `yaml:"auth_code_ttl"`
	SessionTTL      time.Duration `yaml:"session_ttl"`

	// AllowDynamicRegistration enables RFC 7591. ChatGPT uses it when Client
	// ID Metadata Documents are unavailable.
	//
	// Open registration is safe here because registering confers nothing on
	// its own: a client still cannot obtain a token without a user completing
	// the consent flow, and the resulting token is bounded by that user's own
	// role and plugin grants.
	AllowDynamicRegistration bool `yaml:"allow_dynamic_registration"`

	// AllowCIMD enables Client ID Metadata Documents, which supersede dynamic
	// registration in the 2026-07-28 MCP revision.
	AllowCIMD bool `yaml:"allow_cimd"`

	// Bootstrap provisions the first administrator when no users exist. It is
	// ignored once any identity is present.
	Bootstrap Bootstrap `yaml:"bootstrap"`
}

// Bootstrap describes the initial administrator.
type Bootstrap struct {
	Username string `yaml:"username"`
	// PasswordRef is a secret reference, never a password.
	PasswordRef string `yaml:"password_ref"`
}

// Approval configures the risk policy.
type Approval struct {
	// RequireDistinctApproverAtOrAbove is the risk level from which the
	// requester may not also approve. Empty disables the rule.
	//
	// Note that this fails closed: if the authentication mode cannot
	// distinguish principals, operations at or above this level are refused
	// rather than self-approved.
	RequireDistinctApproverAtOrAbove string `yaml:"require_distinct_approver_at_or_above"`

	// ProposalTTL bounds how long a proposal awaits approval.
	ProposalTTL time.Duration `yaml:"proposal_ttl"`
	// ApprovalTTL bounds how long an approval remains executable. Without it,
	// an approval granted weeks ago could still execute against a network that
	// has since changed.
	ApprovalTTL time.Duration `yaml:"approval_ttl"`
	// LeaseTTL bounds an execution claim before the reaper reclaims it.
	LeaseTTL time.Duration `yaml:"lease_ttl"`
}

// Logging configures output.
type Logging struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// PluginConfig is the host-level configuration common to every plugin.
type PluginConfig struct {
	Enabled bool `yaml:"enabled"`
	// Required determines whether a startup failure in this plugin fails the
	// host, or only marks the plugin unhealthy.
	Required bool `yaml:"required"`
	// Settings holds plugin-specific configuration, decoded by the plugin.
	Settings map[string]any `yaml:"settings"`
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
			Mode: "static",
			OAuth: OAuth{
				AccessTokenTTL:           time.Hour,
				RefreshTokenTTL:          30 * 24 * time.Hour,
				AuthCodeTTL:              time.Minute,
				SessionTTL:               30 * time.Minute,
				AllowDynamicRegistration: true,
				AllowCIMD:                true,
			},
		},
		Approval: Approval{
			RequireDistinctApproverAtOrAbove: "high",
			ProposalTTL:                      30 * time.Minute,
			ApprovalTTL:                      15 * time.Minute,
			LeaseTTL:                         2 * time.Minute,
		},
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

// StorageDir returns the directory holding the database.
func (c *Config) StorageDir() string { return filepath.Dir(c.Storage.Path) }
