package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/apikeys"
)

// Keys is the slice of the key store the dashboard needs.
//
// There is deliberately no method that reads a secret. The store has none
// either; the secret exists once, in the reply to the request that created it.
type Keys interface {
	List(ctx context.Context) ([]*apikeys.Key, error)
	ByID(ctx context.Context, id string) (*apikeys.Key, error)
	Create(ctx context.Context, actor string, req apikeys.CreateRequest) (*apikeys.Key, string, error)
	Update(ctx context.Context, actor, id string, req apikeys.UpdateRequest) (*apikeys.Key, error)
	Revoke(ctx context.Context, actor, id string) error
}

// keyView is what the dashboard sees. The secret is not a field here and there
// is no shape of this struct that carries one.
type keyView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	// Plugins is the key's own grant, exactly as stored, so an edit form can
	// round-trip it. Reaches is what it actually reaches once its groups are
	// unioned in, which is what a page should render.
	Plugins []string       `json:"plugins"`
	Reaches []string       `json:"reaches"`
	Groups  []groupRefView `json:"groups"`
	// Status is "active", "expired" or "revoked". An operator chasing a
	// connector that stopped working needs to know which; whoever presented
	// the credential is told only that it was not accepted.
	Status     string `json:"status"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	LastUsedAt string `json:"last_used_at,omitempty"`
	RevokedAt  string `json:"revoked_at,omitempty"`
	RevokedBy  string `json:"revoked_by,omitempty"`
}

func viewOfKey(k *apikeys.Key, now time.Time, reaches []string) keyView {
	v := keyView{
		ID:        k.ID,
		Name:      k.Name,
		Role:      string(k.Role),
		Plugins:   nonNil(k.Plugins),
		Reaches:   nonNil(reaches),
		Groups:    viewOfGroupRefs(k.Groups()),
		Status:    string(k.Status(now)),
		CreatedBy: k.CreatedBy,
		CreatedAt: k.CreatedAt.Format(time.RFC3339),
		RevokedBy: k.RevokedBy,
	}
	if k.ExpiresAt != nil {
		v.ExpiresAt = k.ExpiresAt.Format(time.RFC3339)
	}
	if k.LastUsedAt != nil {
		v.LastUsedAt = k.LastUsedAt.Format(time.RFC3339)
	}
	if k.RevokedAt != nil {
		v.RevokedAt = k.RevokedAt.Format(time.RFC3339)
	}
	return v
}

// reachOf asks what a key reaches, through the one function that answers it
// for anybody.
//
// It never computes an answer of its own. A server wired without the resolver,
// or a read that fails, yields nothing rather than the key's own grant:
// showing the direct grant and calling it the effective one would be the
// console disagreeing with the server about what a credential can do, which is
// the sort of disagreement nobody looks for.
func (s *Server) reachOf(r *http.Request, k *apikeys.Key) []string {
	if s.opts.KeyGrants == nil {
		s.opts.Log.Error("no way to resolve what a key reaches", "key", k.ID)
		return []string{}
	}
	reaches, err := s.opts.KeyGrants(r.Context(), k.ID)
	if err != nil {
		s.opts.Log.Error("could not resolve a key's grants", "key", k.ID, "error", err)
		return []string{}
	}
	return reaches
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	if s.opts.Keys == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "API keys are not configured")
		return
	}
	list, err := s.opts.Keys.List(r.Context())
	if err != nil {
		s.opts.Log.Error("could not list API keys", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not read the keys")
		return
	}
	now := time.Now()
	out := make([]keyView, len(list))
	for i, k := range list {
		out[i] = viewOfKey(k, now, s.reachOf(r, k))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"keys": out, "count": len(out)})
}

type createKeyRequest struct {
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	Plugins []string `json:"plugins"`
	Groups  []string `json:"groups"`
	// ExpiresAt is RFC 3339, or empty for a key that never expires.
	ExpiresAt string `json:"expires_at"`
}

// handleCreateKey issues a key and shows its secret, once.
//
// The secret is in this response and in no other. Nothing stores it, no
// endpoint reads it back, and the page that receives it says so before the
// dialog can be closed -- because "I will copy it later" is the mistake this
// shape exists to make impossible rather than merely discouraged.
func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if s.opts.Keys == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "API keys are not configured")
		return
	}
	var req createKeyRequest
	if !s.decode(w, r, &req) {
		return
	}
	create := apikeys.CreateRequest{
		Name:    req.Name,
		Role:    auth.Role(req.Role),
		Plugins: req.Plugins,
		Groups:  req.Groups,
	}
	if req.ExpiresAt != "" {
		at, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest,
				"expires_at must be a date and time, like 2027-01-31T00:00:00Z")
			return
		}
		create.ExpiresAt = &at
	}

	actor := auth.FromContext(r.Context()).ID
	key, secret, err := s.opts.Keys.Create(r.Context(), actor, create)
	if err != nil {
		// Every refusal from Create is a statement about the request -- an
		// unusable name, an unknown role, an expiry already past.
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	// The identifier and who issued it, never the secret. A log line carrying
	// one would put a working credential in whatever ships the logs.
	s.opts.Log.Info("API key created", "key", key.ID, "name", key.Name,
		"role", key.Role, "by", actor)

	loaded, err := s.opts.Keys.ByID(r.Context(), key.ID)
	if err != nil {
		loaded = key
	}
	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"key": viewOfKey(loaded, time.Now(), s.reachOf(r, loaded)),
		// The one time this field exists.
		"secret": secret,
	})
}

type updateKeyRequest struct {
	Name    *string   `json:"name,omitempty"`
	Role    *string   `json:"role,omitempty"`
	Plugins *[]string `json:"plugins,omitempty"`
	// ExpiresAt is RFC 3339 to set one, "" to clear it, and absent to leave it
	// alone. A pointer to a string is what tells the three apart.
	ExpiresAt *string `json:"expires_at,omitempty"`
}

func (s *Server) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	if s.opts.Keys == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "API keys are not configured")
		return
	}
	var req updateKeyRequest
	if !s.decode(w, r, &req) {
		return
	}
	update := apikeys.UpdateRequest{Name: req.Name, Plugins: req.Plugins}
	if req.Role != nil {
		role := auth.Role(*req.Role)
		update.Role = &role
	}
	if req.ExpiresAt != nil {
		var at *time.Time
		if *req.ExpiresAt != "" {
			parsed, err := time.Parse(time.RFC3339, *req.ExpiresAt)
			if err != nil {
				s.writeError(w, r, http.StatusBadRequest,
					"expires_at must be a date and time, like 2027-01-31T00:00:00Z")
				return
			}
			at = &parsed
		}
		update.ExpiresAt = &at
	}

	actor := auth.FromContext(r.Context()).ID
	key, err := s.opts.Keys.Update(r.Context(), actor, r.PathValue("id"), update)
	switch {
	case errors.Is(err, apikeys.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such key")
		return
	case errors.Is(err, apikeys.ErrRevoked):
		s.writeError(w, r, http.StatusConflict,
			"that key was revoked; create a new one instead")
		return
	case err != nil:
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	s.opts.Log.Info("API key re-scoped", "key", key.ID, "by", actor)
	s.writeJSON(w, r, http.StatusOK, viewOfKey(key, time.Now(), s.reachOf(r, key)))
}

// handleRevokeKey withdraws a key.
//
// It takes effect on the next request rather than the next restart: nothing
// about a key is cached, so the row the next call reads is this one.
func (s *Server) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	if s.opts.Keys == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "API keys are not configured")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	id := r.PathValue("id")
	err := s.opts.Keys.Revoke(r.Context(), actor, id)
	switch {
	case errors.Is(err, apikeys.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "no such key")
		return
	case errors.Is(err, apikeys.ErrAlreadyRevoked):
		s.writeError(w, r, http.StatusConflict, "that key is already revoked")
		return
	case err != nil:
		s.opts.Log.Error("could not revoke an API key", "key", id, "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not revoke the key")
		return
	}
	s.opts.Log.Info("API key revoked", "key", id, "by", actor)
	key, err := s.opts.Keys.ByID(r.Context(), id)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.writeJSON(w, r, http.StatusOK, viewOfKey(key, time.Now(), s.reachOf(r, key)))
}
