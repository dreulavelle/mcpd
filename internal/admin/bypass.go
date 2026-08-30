package admin

import (
	"context"
	"net/http"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
)

// Bypasses opens, reads and closes the windows in which this host stops asking.
type Bypasses interface {
	Active(ctx context.Context) (*operations.Bypass, error)
	List(ctx context.Context, limit int) ([]*operations.Bypass, error)
	Open(ctx context.Context, actor string, minutes int, plugin string, ceiling operations.RiskLevel, reason string) (*operations.Bypass, error)
	RevokeAll(ctx context.Context, actor string) (int64, error)
	Approved(ctx context.Context) (map[string]int64, error)
}

// bypassView is one window as the dashboard reads it.
type bypassView struct {
	ID        string    `json:"id"`
	Plugin    string    `json:"plugin,omitempty"`
	Ceiling   string    `json:"ceiling"`
	Reason    string    `json:"reason,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	// Active is computed rather than stored: a window closes by the clock, and
	// a field written when it was opened would keep saying "open" afterwards.
	Active bool `json:"active"`
	// SecondsLeft drives a countdown without the browser having to agree with
	// this host about the time.
	SecondsLeft int `json:"seconds_left"`
	// Approved is how many changes this window let through, counted from the
	// operations that record it as their authority.
	Approved int64 `json:"approved"`
}

type openBypassRequest struct {
	Minutes int    `json:"minutes"`
	Plugin  string `json:"plugin"`
	Ceiling string `json:"ceiling"`
	Reason  string `json:"reason"`
}

func (s *Server) bypassStatus(ctx context.Context) (map[string]any, error) {
	active, err := s.opts.Bypasses.Active(ctx)
	if err != nil {
		return nil, err
	}
	recent, err := s.opts.Bypasses.List(ctx, 20)
	if err != nil {
		return nil, err
	}
	approved, err := s.opts.Bypasses.Approved(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	views := make([]bypassView, 0, len(recent))
	for _, b := range recent {
		views = append(views, viewBypass(b, now, approved[b.ID]))
	}

	out := map[string]any{
		"active":      active != nil,
		"recent":      views,
		"max_minutes": operations.MaxBypassMinutes,
	}
	if active != nil {
		out["current"] = viewBypass(active, now, approved[active.ID])
	}
	return out, nil
}

func viewBypass(b *operations.Bypass, now time.Time, approved int64) bypassView {
	return bypassView{
		ID: b.ID, Plugin: b.Plugin, Ceiling: string(b.Ceiling), Reason: b.Reason,
		CreatedBy: b.CreatedBy, CreatedAt: b.CreatedAt, ExpiresAt: b.ExpiresAt,
		Active:      b.Active(now),
		SecondsLeft: int(b.Remaining(now).Seconds()),
		Approved:    approved,
	}
}

// handleBypassStatus reports whether anything is unsupervised right now.
//
// Read rather than admin, and deliberately: everything else about a bypass is
// an administrator's, but "is this host approving changes without asking
// anyone" is a fact every operator who can see the approval queue should be
// able to check. A window nobody can see is worse than no window.
func (s *Server) handleBypassStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.Bypasses == nil {
		s.writeError(w, r, http.StatusNotImplemented, "bypasses are not configured here")
		return
	}
	status, err := s.bypassStatus(r.Context())
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusOK, status)
}

// handleOpenBypass starts a window.
func (s *Server) handleOpenBypass(w http.ResponseWriter, r *http.Request) {
	if s.opts.Bypasses == nil {
		s.writeError(w, r, http.StatusNotImplemented, "bypasses are not configured here")
		return
	}
	var req openBypassRequest
	if !s.decode(w, r, &req) {
		return
	}
	if req.Reason == "" {
		// Required, because the point of a window that ends is that somebody
		// can read back what it was for. An unexplained one is a widened rule
		// with a timer.
		s.writeError(w, r, http.StatusBadRequest,
			"say what this is for; it is recorded against every change the window lets through")
		return
	}

	actor := auth.FromContext(r.Context()).ID
	b, err := s.opts.Bypasses.Open(r.Context(), actor, req.Minutes, req.Plugin,
		operations.RiskLevel(req.Ceiling), req.Reason)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// Warn, because for as long as this is open the host is approving changes
	// nobody is being asked about.
	s.opts.Log.WarnContext(r.Context(), "approval bypass opened",
		"bypass", b.ID, "principal", actor, "plugin", b.Plugin,
		"ceiling", b.Ceiling, "expires", b.ExpiresAt, "reason", b.Reason)

	s.writeJSON(w, r, http.StatusCreated, viewBypass(b, time.Now(), 0))
}

// handleRevokeBypasses closes every open window.
func (s *Server) handleRevokeBypasses(w http.ResponseWriter, r *http.Request) {
	if s.opts.Bypasses == nil {
		s.writeError(w, r, http.StatusNotImplemented, "bypasses are not configured here")
		return
	}
	actor := auth.FromContext(r.Context()).ID
	closed, err := s.opts.Bypasses.RevokeAll(r.Context(), actor)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.opts.Log.WarnContext(r.Context(), "approval bypasses revoked",
		"principal", actor, "closed", closed)

	s.writeJSON(w, r, http.StatusOK, map[string]any{"closed": closed})
}
