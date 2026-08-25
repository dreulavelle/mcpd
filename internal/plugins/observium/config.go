// Package observium integrates Observium, an SNMP-based network monitoring
// platform, over its /api/v0 REST API.
//
// Read-only. Observium's API can add, modify and delete devices, and a device
// deleted through it takes its history with it -- so the write surface is
// refused at the transport rather than merely left unimplemented. Adding
// mutations later means deliberately widening transport.go, which is the
// amount of friction that decision deserves against a live monitoring estate.
//
// See docs/observium.md for what the API does that a reader would not expect.
// Two things matter enough to repeat here:
//
// Collections are objects keyed by entity id, not arrays. GET /devices/
// answers {"status":"ok","count":2,"devices":{"277":{...},"278":{...}}}.
// Decoding that as a list fails, and decoding it as a map loses the ordering
// the caller asked for, so client.go turns it back into a slice sorted by the
// key the caller sorted on.
//
// Observium stores its time series in RRD and does not expose them as JSON.
// The only way out is graph.php, which renders PNGs. That is why there is a
// tool returning graph URLs and no tool returning trend data: the second one
// cannot be built against this API, and pretending otherwise would have the
// model inventing numbers it never saw.
package observium

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Defaults. Each is a judgement about an installation nobody has tuned yet.
const (
	// defaultPageSize is what one upstream page asks for. Observium caps
	// pagesize at 50000; asking for that in one request is a way to make a
	// modest installation time out rather than a way to go faster.
	defaultPageSize = 250

	// defaultMaxItems bounds what a single tool call accumulates. A large
	// estate has tens of thousands of ports, and an assistant asking for "all
	// ports" would otherwise pull more than a context window holds, slowly.
	defaultMaxItems = 500

	// defaultRPS bounds outbound calls. Observium is usually a single PHP
	// application over a MySQL database on someone's own hardware, which is a
	// much smaller thing than a vendor cloud, so this is deliberately modest.
	defaultRPS = 5.0

	// defaultTimeout bounds one upstream request. Observium answers a large
	// unfiltered listing by querying the whole table, which on a big estate is
	// slow rather than broken.
	defaultTimeout = 30 * time.Second

	// defaultStateTTL is how long a reading of current state may be reused.
	// Observium polls on a five-minute cycle by default, so a value read
	// twice inside thirty seconds came from the same poll either way.
	defaultStateTTL = 30 * time.Second

	// defaultInventoryTTL is how long a reading of how the estate is
	// *arranged* may be reused. Device lists, hardware inventory and VLANs
	// change when somebody changes them, not on a poll cycle.
	defaultInventoryTTL = 10 * time.Minute

	// defaultDBPort is MySQL's.
	defaultDBPort = 3306
)

// apiPrefix is the versioned root every call is made under. Observium has had
// exactly one API version; naming it here rather than scattering it means the
// day there is a second one is an edit in one place.
const apiPrefix = "/api/v0"

// Backend is how this instance reaches Observium.
//
// Two ways to read one product, and which is available is decided by the
// licence rather than by preference. The REST API is a subscription feature;
// on Community Edition there is no API at all, and the only way in is the
// database Observium writes to.
type Backend string

const (
	// BackendAPI reads the subscription-only REST API. Read-only is proved by
	// a transport that refuses every method but GET.
	BackendAPI Backend = "api"
	// BackendDatabase reads Observium's MySQL database directly, which is the
	// only option on Community Edition. Read-only is proved by the account's
	// own grants, checked at startup.
	BackendDatabase Backend = "database"
)

func (b Backend) Valid() bool {
	return b == BackendAPI || b == BackendDatabase
}

// Config is the plugin's own configuration, from the `settings` block.
type Config struct {
	// Backend selects which of the two ways in this instance uses. It is not a
	// preference: the API is a subscription feature, so a Community Edition
	// installation has only one working answer.
	Backend Backend `yaml:"backend" json:"backend"`

	// BaseURL is the Observium web root -- the address someone types to reach
	// the UI, without /api/v0. On-premise and typically internal, so unlike a
	// vendor cloud there is no sensible default to offer.
	BaseURL string `yaml:"base_url" json:"base_url"`

	// Token is an API token from Profile > API tokens. Preferred over a
	// username and password: it is scoped to the permissions of the account
	// that made it, it can be issued read-only, and it can be revoked without
	// changing anybody's login.
	Token string `yaml:"token" json:"token"`

	// Username and Password are HTTP basic auth, which is what an installation
	// too old for API tokens has. Supported because refusing it would make
	// this plugin unusable on exactly the deployments most likely to be
	// running Observium at all, and because the API treats the two as
	// equivalent.
	//
	// Token wins when both are set. Two credentials with one obviously
	// preferred is not a choice worth making per request.
	Username string `yaml:"username" json:"username"`
	Password string `yaml:"password" json:"password"`

	// PageSize is how many entities one page asks for. It bounds a request,
	// not a tool call: a listing walks pages until MaxItems or the collection
	// is exhausted.
	PageSize int `yaml:"page_size" json:"page_size"`

	// MaxItems caps what one tool call accumulates. Reported in the result
	// when it bites, so a caller narrows their filter instead of silently
	// seeing part of an estate.
	MaxItems int `yaml:"max_items" json:"max_items"`

	// RequestsPerSecond bounds outbound calls. Walking pages is a loop, which
	// is the shape most likely to overrun a self-hosted upstream.
	RequestsPerSecond float64 `yaml:"requests_per_second" json:"requests_per_second"`

	// Timeout bounds a single upstream request.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`

	// StateCacheSeconds is how long a reading of current state -- sensors,
	// ports, alerts, processors -- may be answered from memory.
	//
	// Zero fetches every time. Keep it well under the poller's own interval:
	// the point is to stop three tools in one turn making the same call three
	// times, not to hold a picture of the network that has stopped being true.
	StateCacheSeconds int `yaml:"state_cache_seconds" json:"state_cache_seconds"`

	// InventoryCacheSeconds is how long a reading of how the estate is
	// arranged -- the device list, hardware inventory, VLANs, groups -- may be
	// answered from memory. These change when an operator changes them, so
	// they can be held far longer than anything the poller writes.
	InventoryCacheSeconds int `yaml:"inventory_cache_seconds" json:"inventory_cache_seconds"`

	// The database backend's connection. Unused when Backend is api, and the
	// settings form hides them -- but hiding is presentation, so Validate is
	// what actually decides they are required.
	DBHost string `yaml:"db_host" json:"db_host"`
	DBPort int    `yaml:"db_port" json:"db_port"`
	DBName string `yaml:"db_name" json:"db_name"`
	DBUser string `yaml:"db_user" json:"db_user"`
	// DBPassword is resolved like every other credential and, like the others,
	// is cleared off the Config the plugin retains.
	DBPassword string `yaml:"db_password" json:"db_password"`
}

// EffectiveBackend resolves the zero value.
//
// It resolves to the database rather than the API, which is the opposite of
// what the ordering of the constants suggests. The reason is that an empty
// backend belongs to an instance somebody configured before this setting
// existed -- and there were none, this shipping together -- or to one where
// the field was left alone. Defaulting to the API would put the more
// restrictive licence requirement behind a silent choice, so the default is
// the one every installation can actually run.
func (c Config) EffectiveBackend() Backend {
	if c.Backend == "" {
		return BackendDatabase
	}
	return c.Backend
}

// withDefaults fills anything the operator left alone.
func (c *Config) withDefaults() {
	if c.PageSize <= 0 {
		c.PageSize = defaultPageSize
	}
	if c.MaxItems <= 0 {
		c.MaxItems = defaultMaxItems
	}
	if c.RequestsPerSecond <= 0 {
		c.RequestsPerSecond = defaultRPS
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.DBPort <= 0 {
		c.DBPort = defaultDBPort
	}
	if c.StateCacheSeconds == 0 {
		c.StateCacheSeconds = int(defaultStateTTL / time.Second)
	}
	if c.InventoryCacheSeconds == 0 {
		c.InventoryCacheSeconds = int(defaultInventoryTTL / time.Second)
	}
}

// Configured reports whether enough was supplied to reach Observium.
//
// What "enough" means depends on the backend, which is the whole reason this
// is one question rather than a set of booleans a caller has to combine
// correctly. A form that hides the fields for the other backend has not
// changed what is required -- it has only stopped showing it.
func (c Config) Configured() bool {
	switch c.EffectiveBackend() {
	case BackendDatabase:
		return c.DBHost != "" && c.DBName != "" && c.DBUser != ""
	default:
		return c.BaseURL != "" && (c.Token != "" || (c.Username != "" && c.Password != ""))
	}
}

// StateTTL and InventoryTTL turn the operator's seconds into durations.
func (c Config) StateTTL() time.Duration {
	return time.Duration(c.StateCacheSeconds) * time.Second
}

func (c Config) InventoryTTL() time.Duration {
	return time.Duration(c.InventoryCacheSeconds) * time.Second
}

// Validate rejects a configuration that cannot work.
//
// An unconfigured plugin is not an error: it mounts, its settings form has
// somewhere to live, and Check says what is missing. What is refused here is a
// configuration that is present and wrong, because that fails later, further
// away, and with a worse message.
func (c Config) Validate() error {
	if c.Backend != "" && !c.Backend.Valid() {
		return fmt.Errorf("observium: backend must be %q or %q, got %q",
			BackendAPI, BackendDatabase, c.Backend)
	}
	if c.EffectiveBackend() == BackendDatabase {
		return c.validateDatabase()
	}
	if c.BaseURL == "" {
		return nil
	}

	u, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil {
		return fmt.Errorf("observium: address %q is not a URL: %w", c.BaseURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("observium: address must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("observium: address %q names no host", c.BaseURL)
	}
	// The API root is appended, so an address that already carries it would
	// produce /api/v0/api/v0/devices. Saying so beats a 404 from a path
	// nobody meant to build.
	if strings.Contains(u.Path, apiPrefix) {
		return fmt.Errorf("observium: address should be the web root, not the "+
			"API path -- drop the %s from %q", apiPrefix, c.BaseURL)
	}
	if u.User != nil {
		return fmt.Errorf("observium: put credentials in the token or username " +
			"and password fields, not in the address; a URL with a password in " +
			"it ends up in logs")
	}

	if c.Token == "" && (c.Username == "") != (c.Password == "") {
		return fmt.Errorf("observium: a username needs a password and a password " +
			"needs a username; or use an API token instead of both")
	}
	if c.PageSize < 1 || c.PageSize > 50000 {
		return fmt.Errorf("observium: page_size must be between 1 and 50000, got %d", c.PageSize)
	}
	if c.MaxItems < 1 {
		return fmt.Errorf("observium: max_items must be at least 1, got %d", c.MaxItems)
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("observium: requests_per_second must be positive, got %v",
			c.RequestsPerSecond)
	}
	if c.StateCacheSeconds < 0 || c.InventoryCacheSeconds < 0 {
		return fmt.Errorf("observium: a cache duration cannot be negative")
	}
	return nil
}

// validateDatabase checks what the database backend needs.
//
// Separate from the API's checks rather than folded in with them, because the
// two share almost nothing: an address that is required for one is ignored by
// the other, and a message about the wrong half is worse than no message. The
// settings form hides the irrelevant fields, but hiding is presentation -- a
// value left over from a backend the operator switched away from is still
// stored, and this is what decides it does not matter.
func (c Config) validateDatabase() error {
	// Unconfigured is not an error. The plugin mounts, its settings form has
	// somewhere to live, and Check says what is missing.
	if c.DBHost == "" && c.DBName == "" && c.DBUser == "" {
		return nil
	}
	var missing []string
	if c.DBHost == "" {
		missing = append(missing, "host")
	}
	if c.DBName == "" {
		missing = append(missing, "database name")
	}
	if c.DBUser == "" {
		missing = append(missing, "username")
	}
	if len(missing) > 0 {
		return fmt.Errorf("observium: the database backend needs a %s",
			strings.Join(missing, ", a "))
	}
	if strings.Contains(c.DBHost, "://") {
		return fmt.Errorf("observium: the database host is a hostname or "+
			"address, not a URL -- drop the scheme from %q", c.DBHost)
	}
	if c.DBPort < 1 || c.DBPort > 65535 {
		return fmt.Errorf("observium: database port %d is not a port", c.DBPort)
	}
	if c.PageSize < 1 {
		return fmt.Errorf("observium: page_size must be at least 1, got %d", c.PageSize)
	}
	if c.MaxItems < 1 {
		return fmt.Errorf("observium: max_items must be at least 1, got %d", c.MaxItems)
	}
	if c.RequestsPerSecond <= 0 {
		return fmt.Errorf("observium: requests_per_second must be positive, got %v",
			c.RequestsPerSecond)
	}
	if c.StateCacheSeconds < 0 || c.InventoryCacheSeconds < 0 {
		return fmt.Errorf("observium: a cache duration cannot be negative")
	}
	return nil
}

// root returns the base URL with any trailing slash removed, so paths can be
// concatenated without producing a double separator.
func (c Config) root() string {
	return strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
}
