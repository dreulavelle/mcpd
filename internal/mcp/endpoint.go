package mcp

import (
	"encoding/json"
	"net/http"

	"github.com/spoked/mcpd/internal/observability"
)

// handleLive reports process liveness.
//
// It deliberately performs no dependency checks. Liveness failing tells an
// orchestrator to restart the process, and restarting mcpd does not fix an
// unreachable upstream API or a busy database — it just drops in-flight work.
func (h *Host) handleLive(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleReady reports whether the host should receive traffic.
//
// A degraded report still returns 200: the host is serving, just not
// everything. Only a failed critical component produces 503, which is what
// takes it out of a load balancer's rotation and what the shutdown sequence
// flips first.
func (h *Host) handleReady(w http.ResponseWriter, r *http.Request) {
	if h.opts.Health == nil {
		h.writeError(w, r, http.StatusServiceUnavailable, "health registry unavailable")
		return
	}
	report := h.opts.Health.Readiness(r.Context())

	status := http.StatusOK
	if report.Status == observability.StatusDown {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(report)
}
