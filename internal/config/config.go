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

// Config is what the startup file still decides.
//
// It is deliberately small. Everything a running host can be told to do
// differently lives in the settings table, where a change is attributed and
// recorded; what is left here is what cannot: where the database is, the key
// that decrypts it, and where to bind. See docs/architecture.md, "Where
// configuration lives".
type Config struct {
	Server  Server  `yaml:"server"`
	Storage Storage `yaml:"storage"`
	Auth    Auth    `yaml:"auth"`
	// SecretKeyRef points at the key used to encrypt secrets stored in the
	// database. Without it, secrets cannot be set from the dashboard -- they
	// would have to be stored in the clear, which is worse than not offering
	// the feature.
	//
	// It cannot move into the database for the reason it exists: it is the
	// key the database's own secrets are encrypted under, and a lock does not
	// hold its own key.
	SecretKeyRef string `yaml:"secret_key_ref"`

	Metrics Metrics                 `yaml:"metrics"`
	Catalog Catalog                 `yaml:"catalog"`
	Plugins map[string]PluginConfig `yaml:"plugins"`

	// legacy is what the file says about keys that have moved. Nothing reads
	// it to run; the first start after an upgrade reads it to import, and
	// every start after that reads it to warn.
	legacy *Legacy
}

// Legacy returns what the file still says about the settings that have moved.
func (c *Config) Legacy() *Legacy {
	if c.legacy == nil {
		return &Legacy{sources: map[string]string{}}
	}
	return c.legacy
}

// Metrics configures the Prometheus endpoint.
//
// It is served on the *dashboard* listener, not the MCP one, and that is the
// decision worth writing down. /health/ready is unauthenticated because a load
// balancer in front of the MCP port has to reach it without a credential.
// Nothing in front of that port needs metrics, and metrics say a great deal
// more: which integrations are mounted, what each of their tools is called,
// how long a named upstream takes to answer, and how often calls fail. The MCP
// listener is the one reached by a third party through a tunnel, so a series
// naming every plugin and tool must not be on it. The dashboard listener has
// the audience this is for -- operators, on an internal interface -- and is
// already where the rest of the operational detail lives.
//
// Because it is behind the dashboard's own gate it takes `read`, which a
// scraper satisfies with a static token exactly as any other machine caller
// does. Public drops that for a deployment where the port is already fenced
// off to a monitoring network and a token is one more thing to rotate.
type Metrics struct {
	// Enabled builds the collectors and serves GET /metrics. On by default:
	// what it exposes is bounded by the same listener the dashboard is on, and
	// a host nobody can see the state of is worse to operate.
	Enabled bool `yaml:"enabled"`

	// Public serves /metrics without authentication.
	//
	// Off by default and deliberately not the recommendation. It exists
	// because a Prometheus scraped from inside a private network is a common
	// shape and refusing to support it would only produce a token pasted into
	// a scrape config and never rotated.
	Public bool `yaml:"public"`
}

// Catalog says which public catalogues of MCP servers the dashboard browses.
//
// Four of them, and each is reached only when an administrator asks for a
// page -- nothing here is fetched at startup, so turning one off is about what
// an operator sees and where this host is willing to make a request, not about
// boot time. Turning all of them off leaves the catalogue endpoints answering
// "no server catalogue is configured", which is the right answer for a
// deployment that will not reach the internet at all.
//
// Three are on by default and one is not, and the line between them is whether
// the source is any use without a credential. Official, Docker and Smithery
// are all readable by anybody: Smithery's *servers* need a key to dial, but its
// registry does not need one to browse, so an operator with no Smithery account
// still gets ten thousand descriptions and a search over them and is told which
// ones would ask for a key. PulseMCP's v0.1 API authenticates every request, so
// a deployment without a key gets a page of 401s rather than a catalogue --
// which is a worse thing to default to than an absence.
type Catalog struct {
	// Official is the official MCP registry at registry.modelcontextprotocol.io.
	Official bool `yaml:"official"`
	// Docker is Docker's MCP catalogue, built from docker/mcp-registry.
	Docker bool `yaml:"docker"`
	// Smithery is Smithery's registry at registry.smithery.ai.
	Smithery bool `yaml:"smithery"`
	// PulseMCP is PulseMCP's v0.1 sub-registry. Off unless configured; see
	// the credentials below.
	PulseMCP bool `yaml:"pulsemcp"`

	// PulseMCPAPIKeyRef is a secret reference -- env:, credential: or file: --
	// to the key PulseMCP issues. A reference rather than a value, like every
	// other credential in this file, so that a token cannot be pasted into
	// something that ends up in version control.
	PulseMCPAPIKeyRef string `yaml:"pulsemcp_api_key_ref"`
	// PulseMCPTenant is the tenant id that accompanies the key. Not a secret
	// -- it identifies rather than authenticates -- so it is written plainly.
	PulseMCPTenant string `yaml:"pulsemcp_tenant"`
}

// Enabled reports whether any catalogue is switched on.
func (c Catalog) Enabled() bool {
	return c.Official || c.Docker || c.Smithery || c.PulseMCP
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
	// Dir holds the certificate authority and server certificate. Empty puts
	// them beside the database, which is already the directory that has to
	// survive a restart.
	//
	// Whether a certificate is served at all is a setting, not a file key:
	// see settings.KeyServerTLSMode. This stays because it is a path, and a
	// path beside the database belongs with the database's own.
	Dir string `yaml:"dir"`
}

// Server configures the two HTTP listeners.
//
// Only the bind addresses are here, and that is a judgement rather than a
// technical limit. A bad bind address stored in the database locks an operator
// out of the interface they would fix it on; the file is the recovery path,
// and it is only a recovery path if it is the authority. Everything else about
// the listeners -- the address they advertise, whether they serve TLS, how
// patient they are -- is a setting.
type Server struct {
	// Listen is the bind address. Default is loopback: mcpd expects to sit
	// behind a reverse proxy that terminates TLS, so binding all interfaces by
	// default would expose it in plaintext.
	Listen string `yaml:"listen"`

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

	// TLS says where the certificate authority is kept.
	TLS TLS `yaml:"tls"`

	// SessionTimeout bounds an idle MCP session.
	SessionTimeout time.Duration `yaml:"session_timeout"`
}

// Storage says where the database is.
//
// Where, and nothing else. How it is opened -- how long a statement waits for
// a lock, whether durability is relaxed -- is a setting, read out of the
// database itself before the pools are configured. Where the file is cannot
// be, for the obvious reason.
type Storage struct {
	Path         string `yaml:"path"`
	ReadPoolSize int    `yaml:"read_pool_size"`

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
	//
	// They stay in the file because their values do: each names a reference
	// resolved from the environment or from systemd, which is a deployment's
	// own arrangement rather than something the dashboard can offer. Keys
	// issued from the dashboard are the other kind of bearer credential and
	// live in the database, digest only.
	StaticTokens []StaticTokenConfig `yaml:"static_tokens"`
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
	// Read separately, from the same bytes, because these are not
	// configuration any more: they are what an upgrade imports and what a
	// startup warning names. Keeping them out of Config is what makes "the
	// file is ignored for these keys" true by construction rather than by
	// discipline.
	legacy, err := parseLegacy(raw)
	if err != nil {
		return nil, err
	}
	cfg.legacy = legacy

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
			Listen:         "127.0.0.1:8080",
			FrontendListen: ":80",
			SessionTimeout: 10 * time.Minute,
		},
		Storage: Storage{Path: "/var/lib/mcpd/mcpd.db"},
		// The three that need no credential are on. They cost nothing until a
		// page asks for one, and an operator who wants only the official
		// registry says so rather than discovering that the others existed and
		// were off. PulseMCP is off because it cannot answer without a key it
		// has to be issued by hand; see Catalog.
		// Docker is off. Its catalogue is 317 servers of which this host can
		// import 29: the rest are container-packaged, and mcpd only accepts a
		// server that publishes a remote endpoint. On, it contributed about
		// two rows per page while every other catalogue contributed ten, which
		// read as a bug in the paging rather than as a catalogue full of
		// things that cannot be installed here. Still implemented, and still
		// one line in config.yaml for a deployment that wants it.
		Catalog: Catalog{Official: true, Smithery: true},
		Metrics: Metrics{Enabled: true},
		Plugins: map[string]PluginConfig{},
		legacy:  &Legacy{sources: map[string]string{}},
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
