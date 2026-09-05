package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/mcpservers"
)

// MCPServerAPI is the slice of the application the remote-server pages need.
//
// A set of functions rather than a concrete type, for the same reason every
// other capability here is: the dashboard is a handler over an interface, and
// a test supplies the parts it cares about without building an App.
type MCPServerAPI struct {
	// List returns every imported server.
	List func(ctx context.Context) (any, error)
	// Tools returns one server's whole snapshot, in every state, so an
	// administrator can see what is pending as well as what is on.
	Tools func(ctx context.Context, name string) ([]mcpservers.Tool, error)
	// Import records a server from its server.json. It mounts nothing.
	Import func(ctx context.Context, actor, name string, document []byte) error
	// Remove forgets a server, its snapshot and its settings.
	Remove func(ctx context.Context, actor, name string) error
	// SetEnabled turns a server on or off.
	SetEnabled func(ctx context.Context, actor, name string, enabled bool) error
	// Discover asks the server what it offers and records the answer,
	// returning what changed.
	Discover func(ctx context.Context, actor, name string) (mcpservers.Diff, error)
	// Classify records a decision about one tool, guarded by the descriptor
	// hash the administrator was shown.
	Classify func(ctx context.Context, actor, server, tool, hash string, state mcpservers.ToolState) error
	// Schema returns the vendored server.json schema an import is judged by.
	Schema func() []byte
	// AddHeader declares a header this host must send that the published
	// document did not, and RemoveHeader withdraws one.
	AddHeader    func(ctx context.Context, actor, server, name, description string, secret bool) error
	RemoveHeader func(ctx context.Context, actor, server, name string) error
}

// addHeaderRequest is an operator saying what the publisher left out.
type addHeaderRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// A pointer, because the default is true and an absent field must not
	// read as false. A credential stored as a non-secret is rendered in a form
	// field to anybody who can open the page.
	Secret *bool `json:"secret"`
}

func (s *Server) handleAddMCPServerHeader(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCPServers.AddHeader == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "remote MCP servers cannot be managed here")
		return
	}
	var req addHeaderRequest
	if !s.decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		s.writeError(w, r, http.StatusBadRequest, "a header name is required")
		return
	}
	// Refused here rather than at the next dial, where the operator is no
	// longer looking at the field they typed it into.
	if err := mcpservers.CheckHeaderName(name); err != nil {
		s.writeError(w, r, http.StatusBadRequest,
			fmt.Sprintf("%q is not a usable HTTP header name", name))
		return
	}
	secret := true
	if req.Secret != nil {
		secret = *req.Secret
	}
	actor := auth.FromContext(r.Context()).ID
	if err := s.opts.MCPServers.AddHeader(r.Context(), actor,
		r.PathValue("name"), name, strings.TrimSpace(req.Description), secret); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, err, "That header could not be added.")
		return
	}
	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"status": "added",
		"note": "Fill the value in on the settings page, then run discovery " +
			"again to see what the server offers.",
	})
}

func (s *Server) handleRemoveMCPServerHeader(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCPServers.RemoveHeader == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "remote MCP servers cannot be managed here")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	if err := s.opts.MCPServers.RemoveHeader(r.Context(), actor,
		r.PathValue("name"), r.PathValue("header")); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, err, "That header could not be removed.")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "removed"})
}

func (s *Server) handleListMCPServers(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCPServers.List == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "remote MCP servers cannot be managed here")
		return
	}
	list, err := s.opts.MCPServers.List(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"servers": list})
}

// handleMCPSchema serves the vendored server.json schema.
//
// Read rather than admin: it is a public document, and showing an operator
// what their paste will be judged against is the difference between a refusal
// they can act on and one they argue with.
func (s *Server) handleMCPSchema(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCPServers.Schema == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "no schema is available")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.opts.MCPServers.Schema())
}

// maxDocumentBytes bounds an imported server.json. Generous next to any real
// document, and far short of what would make storing one a problem.
const maxDocumentBytes = 256 << 10

type importMCPServerRequest struct {
	// Name is the instance name: the endpoint path segment, the tool prefix,
	// and the entry in a credential's plugin list. It is not the document's
	// own reverse-DNS name, which is not a legal path segment.
	Name string `json:"name"`
	// Document is the server.json itself, as an object rather than a string,
	// so a dashboard can paste what it fetched without re-encoding it.
	Document json.RawMessage `json:"document"`
}

func (s *Server) handleImportMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCPServers.Import == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "remote MCP servers cannot be managed here")
		return
	}
	// A body limit of its own. The shared one is 8 KiB, which is right for a
	// dashboard form and wrong for the one endpoint whose entire job is
	// pasting a document -- a published server.json carrying packages, icons
	// and _meta goes past it, and the operator gets "the request could not be
	// read" with nothing saying what was too big.
	var req importMCPServerRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDocumentBytes)).
		Decode(&req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, fmt.Sprintf(
			"the document could not be read; it must be JSON and at most %d KiB",
			maxDocumentBytes>>10))
		return
	}
	if len(req.Document) == 0 {
		s.writeError(w, r, http.StatusBadRequest, "a server.json document is required")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	if err := s.opts.MCPServers.Import(r.Context(), actor, req.Name, req.Document); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, err,
			"That document could not be imported. Check it is the server.json for this server.")
		return
	}
	s.opts.Log.Info("remote MCP server imported", "server", req.Name, "by", actor)
	// Saying what has and has not happened matters more here than anywhere
	// else on this page: importing records how to reach a server and nothing
	// about what it offers, and an operator who expects tools to appear will
	// otherwise read the empty list as a failure.
	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"status": "imported",
		"note": "Fill in what it needs, then run discovery. Nothing is served " +
			"until you enable the tools you want.",
	})
}

type setMCPServerRequest struct {
	Enabled *bool `json:"enabled"`
}

func (s *Server) handleSetMCPServerEnabled(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCPServers.SetEnabled == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "remote MCP servers cannot be managed here")
		return
	}
	var req setMCPServerRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		s.writeError(w, r, http.StatusBadRequest, "enabled is required")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	name := r.PathValue("name")
	if err := s.opts.MCPServers.SetEnabled(r.Context(), actor, name, *req.Enabled); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, err, "That change could not be saved.")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "saved"})
}

func (s *Server) handleRemoveMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCPServers.Remove == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "remote MCP servers cannot be managed here")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	name := r.PathValue("name")
	if err := s.opts.MCPServers.Remove(r.Context(), actor, name); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, err, "That server could not be removed.")
		return
	}
	s.opts.Log.Info("remote MCP server removed", "server", name, "by", actor)
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "removed"})
}

func (s *Server) handleDiscoverMCPServer(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCPServers.Discover == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "remote MCP servers cannot be managed here")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	name := r.PathValue("name")
	diff, err := s.opts.MCPServers.Discover(r.Context(), actor, name)
	if err != nil {
		// A discovery that fails is nearly always the far end being down or a
		// credential the far end refused, which is the operator's to fix
		// rather than a fault in this host.
		s.writeError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"status": "discovered",
		"diff":   diff,
		"note": "New tools arrive pending and are not served. Enable the ones " +
			"you want; a tool whose description or schema changed has to be " +
			"read and enabled again.",
	})
}

func (s *Server) handleMCPServerTools(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCPServers.Tools == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "remote MCP servers cannot be managed here")
		return
	}
	tools, err := s.opts.MCPServers.Tools(r.Context(), r.PathValue("name"))
	if err != nil {
		s.writeProblem(w, r, http.StatusNotFound, err, "That server's tools could not be read.")
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"tools": tools, "count": len(tools),
	})
}

type classifyToolRequest struct {
	State string `json:"state"`
	// DescriptorHash is the descriptor the administrator actually read. It is
	// required, and it is part of the guard in SQL rather than a check before
	// it: a decision is about a description and a schema, and if a discovery
	// replaced them the decision was about something else.
	DescriptorHash string `json:"descriptor_hash"`
}

func (s *Server) handleClassifyMCPTool(w http.ResponseWriter, r *http.Request) {
	if s.opts.MCPServers.Classify == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "remote MCP servers cannot be managed here")
		return
	}
	var req classifyToolRequest
	if !s.decode(w, r, &req) {
		return
	}
	actor := auth.FromContext(r.Context()).ID
	server := r.PathValue("name")
	tool := r.PathValue("tool")
	err := s.opts.MCPServers.Classify(r.Context(), actor, server, tool,
		req.DescriptorHash, mcpservers.ToolState(req.State))
	if err != nil {
		s.writeProblem(w, r, http.StatusConflict, err,
			"That tool could not be classified. Read it again -- its description or schema may have changed.")
		return
	}
	s.opts.Log.Info("remote MCP tool classified",
		"server", server, "tool", tool, "state", req.State, "by", actor)
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "saved"})
}
