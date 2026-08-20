package observability

import (
	"context"
	"sync"
	"time"
)

// Status is a component's health.
type Status string

const (
	StatusUp       Status = "up"
	StatusDegraded Status = "degraded"
	StatusDown     Status = "down"
)

// Check reports the health of one component.
type Check struct {
	Name    string        `json:"name"`
	Status  Status        `json:"status"`
	Message string        `json:"message,omitempty"`
	Latency time.Duration `json:"-"`
	// Critical marks a component whose failure makes the host unready. A
	// non-critical component can be down while the host still serves — a
	// single upstream API outage should not take the whole host out of
	// rotation.
	Critical bool `json:"critical"`
}

// CheckFunc produces a health report.
type CheckFunc func(ctx context.Context) Check

// HealthRegistry aggregates component checks.
//
// Liveness and readiness are kept distinct on purpose. Liveness answers "is
// this process functioning" and should almost never fail, because failing it
// tells an orchestrator to restart. Readiness answers "should traffic arrive
// right now", which a transient dependency outage legitimately changes.
type HealthRegistry struct {
	mu     sync.RWMutex
	checks map[string]CheckFunc
	order  []string
	// timeout bounds each individual check so one hung dependency cannot stall
	// the whole probe.
	timeout time.Duration
}

// NewHealthRegistry returns an empty registry.
func NewHealthRegistry(timeout time.Duration) *HealthRegistry {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &HealthRegistry{checks: make(map[string]CheckFunc), timeout: timeout}
}

// Register adds a component check, replacing any existing one of the same name.
func (h *HealthRegistry) Register(name string, fn CheckFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.checks[name]; !exists {
		h.order = append(h.order, name)
	}
	h.checks[name] = fn
}

// Report is the aggregated health of every registered component.
type Report struct {
	Status Status  `json:"status"`
	Checks []Check `json:"checks"`
}

// Readiness runs every check and aggregates the result.
//
// A failing critical component makes the whole report down. A failing
// non-critical one degrades it: the host keeps serving what still works.
func (h *HealthRegistry) Readiness(ctx context.Context) Report {
	h.mu.RLock()
	names := append([]string(nil), h.order...)
	fns := make([]CheckFunc, len(names))
	for i, n := range names {
		fns[i] = h.checks[n]
	}
	h.mu.RUnlock()

	report := Report{Status: StatusUp, Checks: make([]Check, 0, len(names))}
	for i, fn := range fns {
		cctx, cancel := context.WithTimeout(ctx, h.timeout)
		start := time.Now()
		c := fn(cctx)
		cancel()

		c.Latency = time.Since(start)
		if c.Name == "" {
			c.Name = names[i]
		}
		report.Checks = append(report.Checks, c)

		switch {
		case c.Status == StatusDown && c.Critical:
			report.Status = StatusDown
		case c.Status != StatusUp && report.Status == StatusUp:
			report.Status = StatusDegraded
		}
	}
	return report
}
