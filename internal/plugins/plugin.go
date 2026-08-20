// Package plugins defines the host-owned contract that every integration
// implements. The interfaces live here, in the host, so that a plugin can never
// define a platform type — and so that adding an integration never requires
// editing the platform.
package plugins

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"
)

// namePattern constrains plugin names because a name becomes a URL path
// segment, a metrics label, a subject component, and a database value.
var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// Descriptor is a plugin's identity.
type Descriptor struct {
	// Name is the stable identifier. It forms the endpoint path
	// /mcp/<name> and prefixes every tool the plugin registers.
	Name string
	// Version is the plugin's own version, independent of the host's.
	Version string
	// Title is the human-readable label shown in clients and the dashboard.
	Title string
	// Description becomes the MCP server's instructions. Clients show it to
	// the model, so it should say what the plugin manages and what it will
	// refuse to do.
	Description string
}

// Validate checks a descriptor before registration.
func (d Descriptor) Validate() error {
	if !namePattern.MatchString(d.Name) {
		return fmt.Errorf("plugins: name %q must match %s", d.Name, namePattern)
	}
	if d.Version == "" {
		return fmt.Errorf("plugins: plugin %s requires a version", d.Name)
	}
	if d.Title == "" {
		return fmt.Errorf("plugins: plugin %s requires a title", d.Name)
	}
	return nil
}

// Endpoint returns the HTTP path this plugin is served at.
func (d Descriptor) Endpoint() string { return "/mcp/" + d.Name }

// Plugin is the minimum contract. A plugin exposing only read tools implements
// exactly these two methods.
//
// Lifecycle is deliberately absent: most plugins have no background work, and
// requiring them to implement empty Start and Shutdown methods would be noise.
// Plugins that do need lifecycle implement the optional interfaces below and
// the host discovers them by type assertion.
type Plugin interface {
	// Descriptor returns the plugin's identity. It must be constant.
	Descriptor() Descriptor

	// Register declares the plugin's tools and mutations. It is called once,
	// during startup, before the endpoint accepts traffic. It must not start
	// goroutines or dial upstream systems; use Starter for that.
	Register(ctx context.Context, r *Registry) error
}

// Starter is implemented by plugins with background work or upstream
// connections to establish.
type Starter interface {
	// Start is called after Register and before the endpoint opens. A plugin
	// marked required in configuration fails host startup if Start errors;
	// otherwise the plugin is marked unhealthy and the host continues.
	Start(ctx context.Context) error
}

// Stopper is implemented by plugins needing orderly teardown. Shutdown is
// called with a bounded context during host shutdown, in reverse registration
// order.
type Stopper interface {
	Shutdown(ctx context.Context) error
}

// Checker is implemented by plugins that can report on their upstream
// dependency. A plugin without it is assumed healthy once started.
type Checker interface {
	Check(ctx context.Context) Health
}

// HealthState is a plugin's readiness.
type HealthState string

const (
	// HealthyState means the plugin and its upstream are fully operational.
	HealthyState HealthState = "healthy"
	// DegradedState means the plugin is serving but impaired — for example
	// reads work while the upstream write API is unreachable.
	DegradedState HealthState = "degraded"
	// UnhealthyState means the plugin cannot serve.
	UnhealthyState HealthState = "unhealthy"
)

// Health is a plugin's self-report.
type Health struct {
	State HealthState `json:"state"`
	// Message is surfaced on the readiness endpoint and in the dashboard, so
	// it must never contain credentials, tokens, or full upstream URLs with
	// embedded secrets.
	Message string `json:"message,omitempty"`
	// CheckedAt records when the report was produced.
	CheckedAt time.Time `json:"checked_at"`
}

// Healthy returns a healthy report.
func Healthy() Health {
	return Health{State: HealthyState, CheckedAt: time.Now()}
}

// Degraded returns a degraded report.
func Degraded(msg string) Health {
	return Health{State: DegradedState, Message: msg, CheckedAt: time.Now()}
}

// Unhealthy returns an unhealthy report.
func Unhealthy(msg string) Health {
	return Health{State: UnhealthyState, Message: msg, CheckedAt: time.Now()}
}

// Store is a small namespaced key-value store backed by the host database. It
// is for plugin bookkeeping — a sync cursor, a cached upstream identifier — not
// for domain data, and not for anything that needs to be transactional with an
// operation.
type Store interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Put(ctx context.Context, key string, value []byte) error
	Delete(ctx context.Context, key string) error
}

// Publisher emits plugin-domain events. The host binds the
// mcp.plugin.<name>. prefix, so a plugin cannot publish outside its own
// namespace or forge another plugin's events.
type Publisher interface {
	// Publish queues an event under the plugin's namespace. subject is the
	// suffix, e.g. "ap.offline" becomes "mcp.plugin.cnmaestro.ap.offline".
	Publish(ctx context.Context, subject string, payload any) error
}

// SecretSource resolves a named secret. Secrets are fetched at the point of
// use and never stored in configuration structs, so that a config dump cannot
// leak a credential.
type SecretSource interface {
	// Secret returns the value for a name declared in configuration. The
	// returned value must not be logged or included in error messages.
	Secret(name string) (string, error)
}

// Deps is what a plugin receives at construction.
//
// Note what is deliberately absent: no operations service, no audit service,
// no database handle. A plugin that cannot reach operation state cannot
// corrupt it, which makes the guarantee structural rather than a convention
// that reviewers have to enforce.
type Deps struct {
	// Log is pre-tagged with the plugin name.
	Log *slog.Logger
	// Store is namespaced to this plugin.
	Store Store
	// Events is namespaced to this plugin.
	Events Publisher
	// Secrets resolves credentials declared in this plugin's config.
	Secrets SecretSource
	// HTTP is a client with bounded connections, sane timeouts, and a
	// transport that redacts credentials from error messages.
	HTTP *http.Client
	// Now is injectable so plugins can be tested deterministically.
	Now func() time.Time
}
