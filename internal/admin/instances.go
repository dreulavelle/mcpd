package admin

import (
	"net/http"

	"github.com/spoked/mcpd/internal/auth"
)

// PluginTypeInfo is an integration this build has.
type PluginTypeInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Configurable reports whether the type declares any settings, so the
	// dashboard can say what adding one will ask for.
	Configurable bool `json:"configurable"`
}

// PluginInstanceInfo is one configured instance, mounted or not.
type PluginInstanceInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// FromFile marks an instance the dashboard cannot remove, because it
	// would return on the next start and look like the removal failed.
	FromFile bool `json:"from_file"`
	Enabled  bool `json:"enabled"`
	// Mounted reports whether it is serving now. An instance is not mounted
	// until every required setting has a value, so this being false alongside
	// Missing is the ordinary state of one nobody has finished configuring.
	Mounted bool `json:"mounted"`
	// Missing names the required settings still to be filled in. Empty on an
	// instance that is ready.
	Missing []string `json:"missing,omitempty"`
}

func (s *Server) handlePluginTypes(w http.ResponseWriter, r *http.Request) {
	types := []PluginTypeInfo{}
	if s.opts.PluginTypes != nil {
		types = s.opts.PluginTypes()
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"types": types, "count": len(types),
	})
}

func (s *Server) handleInstances(w http.ResponseWriter, r *http.Request) {
	list := []PluginInstanceInfo{}
	if s.opts.Instances != nil {
		list = s.opts.Instances(r.Context())
	}
	mounted := map[string]bool{}
	if s.opts.Manager != nil {
		for _, name := range s.opts.Manager.Names() {
			mounted[name] = true
		}
	}
	for i := range list {
		list[i].Mounted = mounted[list[i].Name]
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"instances": list, "count": len(list),
	})
}

type addInstanceRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (s *Server) handleAddInstance(w http.ResponseWriter, r *http.Request) {
	if s.opts.AddPlugin == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "plugins cannot be managed here")
		return
	}
	var req addInstanceRequest
	if !s.decode(w, r, &req) {
		return
	}
	actor := auth.FromContext(r.Context()).ID
	if err := s.opts.AddPlugin(r.Context(), actor, req.Name, req.Type); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("plugin instance added",
		"instance", req.Name, "type", req.Type, "by", actor)
	// Saying it is not yet running matters. A plugin is built at startup from
	// the settings it had then, so an instance added now appears in the list
	// and serves nothing until a restart -- which reads as a failure unless
	// something says otherwise.
	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"status":           "added",
		"restart_required": true,
		"note":             "Configure it below, then restart mcpd for it to start serving.",
	})
}

type setInstanceRequest struct {
	Enabled *bool `json:"enabled"`
}

func (s *Server) handleSetInstanceEnabled(w http.ResponseWriter, r *http.Request) {
	if s.opts.SetPluginEnabled == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "plugins cannot be managed here")
		return
	}
	var req setInstanceRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		s.writeError(w, r, http.StatusBadRequest, "enabled is required")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	name := r.PathValue("name")
	if err := s.opts.SetPluginEnabled(r.Context(), actor, name, *req.Enabled); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("plugin instance toggled",
		"instance", name, "enabled", *req.Enabled, "by", actor)
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "saved"})
}

func (s *Server) handleRemoveInstance(w http.ResponseWriter, r *http.Request) {
	if s.opts.RemovePlugin == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "plugins cannot be managed here")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	name := r.PathValue("name")
	if err := s.opts.RemovePlugin(r.Context(), actor, name); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("plugin instance removed", "instance", name, "by", actor)
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "removed"})
}
