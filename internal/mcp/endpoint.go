package mcp

import (
	"encoding/json"
	"net/http"
	"strings"

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

// protectedResourceMetadata is the RFC 9728 document an MCP client fetches to
// discover where to authenticate. ChatGPT reads it during connector setup.
type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers,omitempty"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name,omitempty"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
}

// handleResourceMetadata serves the protected-resource document.
//
// It is unauthenticated by specification: a client must be able to read it in
// order to learn how to authenticate. It therefore carries no plugin names and
// no configuration, only the issuer pointer.
func (h *Host) handleResourceMetadata(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(h.opts.PublicURL, "/")
	if base == "" {
		h.writeError(w, r, http.StatusNotFound, "no public url configured")
		return
	}

	// The document describes the endpoint it was asked about, not the host.
	//
	// Each MCP endpoint is its own protected resource, and RFC 9728 puts that
	// resource's path after the well-known segment. Returning the same
	// identifier whatever was asked makes every endpoint look like the same
	// resource -- which breaks audience binding, and makes two tunnels serving
	// different endpoints indistinguishable to anything reading their
	// metadata.
	resource := base
	if suffix := strings.Trim(r.PathValue("path"), "/"); suffix != "" {
		resource = base + "/" + suffix
	}

	meta := protectedResourceMetadata{
		Resource:               resource,
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "mcpd",
	}
	// authorization_servers is omitted entirely when no authorization server
	// is mounted. A client reading this document -- OpenAI's tunnel-client
	// treats authorization_servers[0] as its only metadata target -- must be
	// able to tell "this resource takes a bearer token you already have" from
	// "go here to obtain one".
	if h.opts.AuthorizationServer != "" {
		meta.AuthorizationServers = []string{h.opts.AuthorizationServer}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_ = json.NewEncoder(w).Encode(meta)
}
