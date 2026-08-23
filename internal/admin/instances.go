package admin

import (
	"net/http"
	"time"

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

// PluginDeclaration is what the configuration file says about an instance,
// shown read-only so an operator who does want to tidy their YAML can see
// exactly which entry to delete.
//
// Keys, never values. This is on a read-capability endpoint and a `settings:`
// block routinely holds a credential; the values are available through the
// settings API, which redacts secrets, and there is no reason to open a second
// path to them here.
type PluginDeclaration struct {
	Type     string `json:"type"`
	Enabled  bool   `json:"enabled"`
	Required bool   `json:"required"`
	// SettingsKeys names the fields the file sets, in the order the API
	// resolves them, so "the file also sets these" is visible without showing
	// what it sets them to.
	SettingsKeys []string `json:"settings_keys,omitempty"`
}

// PluginInstanceInfo is one configured instance, mounted or not.
type PluginInstanceInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Runtime is "builtin" for an integration this build carries and "mcp"
	// for a remote MCP server. The page needs it because the two are managed
	// in different places: one has a compiled-in type and a form, the other
	// has an imported document and a tool list to classify.
	Runtime string `json:"runtime,omitempty"`
	// FromFile marks an instance the configuration file declares. It can still
	// be removed here -- the removal is recorded in the database and overrides
	// the file, which is unchanged -- and the page says so rather than
	// implying the file was edited.
	FromFile bool `json:"from_file"`
	// Required is the file's `required: true`: the deployment saying this host
	// is meant not to run without the integration. Removing one takes an
	// explicit acknowledgement.
	Required bool `json:"required,omitempty"`
	Enabled  bool `json:"enabled"`
	// Removed marks a file-declared instance an administrator removed here.
	// It stays in this list, because somebody who removes the wrong thing has
	// to be able to find it again to restore it.
	Removed   bool      `json:"removed,omitempty"`
	RemovedBy string    `json:"removed_by,omitempty"`
	RemovedAt time.Time `json:"removed_at,omitzero"`
	// Declaration is the file's entry for this instance, when there is one.
	Declaration *PluginDeclaration `json:"declaration,omitempty"`
	// Mounted reports whether it is serving now. An instance is not mounted
	// until every required setting has a value, so this being false alongside
	// Missing is the ordinary state of one nobody has finished configuring.
	Mounted bool `json:"mounted"`
	// Missing names the required settings still to be filled in. Empty on an
	// instance that is ready.
	Missing []string `json:"missing,omitempty"`
	// Problem is why a configured instance is not serving -- a credential the
	// upstream rejected, most often. Without it, an instance that has every
	// field filled in and still will not mount says nothing about why.
	Problem string `json:"problem,omitempty"`
}

// StaleRemoval is a removal whose declaration is no longer in the
// configuration file: an operator removed a plugin here and later deleted the
// entry from their YAML.
//
// It is reported rather than discarded, because discarding one would let a
// host that started once with a truncated configuration file forget every
// removal and resurrect all of them on the next good deploy. Shown, it is a
// row somebody can deliberately forget.
type StaleRemoval struct {
	Name         string    `json:"name"`
	DeclaredType string    `json:"declared_type"`
	RemovedBy    string    `json:"removed_by"`
	RemovedAt    time.Time `json:"removed_at"`
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
	stale := []StaleRemoval{}
	if s.opts.StaleRemovals != nil {
		stale = s.opts.StaleRemovals(r.Context())
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"instances": list, "count": len(list), "stale_removals": stale,
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
	// Saying it is not yet running matters. An instance arrives with nothing
	// filled in, so it appears in the list and serves nothing until it has
	// what it needs -- which reads as a failure unless something says
	// otherwise. It mounts itself once the settings are saved.
	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"status": "added",
		"note":   "Configure it below. It starts serving as soon as it has what it needs.",
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
	// A query parameter rather than a body, because a DELETE carrying one is
	// awkward for every client that is not this dashboard. It says only that
	// the caller has seen the `required: true` warning; nothing else about the
	// removal changes with it.
	acknowledged := r.URL.Query().Get("acknowledge_required") == "true"
	if err := s.opts.RemovePlugin(r.Context(), actor, name, acknowledged); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("plugin instance removed", "instance", name, "by", actor)
	s.writeJSON(w, r, http.StatusOK, map[string]any{"status": "removed"})
}

func (s *Server) handleRestoreInstance(w http.ResponseWriter, r *http.Request) {
	if s.opts.RestorePlugin == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "plugins cannot be managed here")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	name := r.PathValue("name")
	if err := s.opts.RestorePlugin(r.Context(), actor, name); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("plugin instance restored", "instance", name, "by", actor)
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"status": "restored",
		"note": "It is back under whatever the configuration file declares. " +
			"It starts serving as soon as it has what it needs.",
	})
}
