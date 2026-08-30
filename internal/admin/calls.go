package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// The call ledger, over HTTP.
//
// Administrator, for the same reason the log is. A row names which systems were
// reached and by whom, which is a wider view than any one account's own work --
// an operator who may read the dashboard should not learn from it which
// credentials exist and what each one touches.

// CallLedger reads the record of who called what.
type CallLedger interface {
	Calls(ctx context.Context, f sqlite.ToolCallFilter) ([]sqlite.ToolCall, error)
	Callers(ctx context.Context, since time.Time) ([]sqlite.CallerSummary, error)
}

// handleListCalls returns recent calls, newest first.
func (s *Server) handleListCalls(w http.ResponseWriter, r *http.Request) {
	if s.opts.Calls == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host is not keeping a record of tool calls")
		return
	}

	q := r.URL.Query()
	filter := sqlite.ToolCallFilter{
		Principal: q.Get("principal"),
		Plugin:    q.Get("plugin"),
		Tool:      q.Get("tool"),
		Outcome:   q.Get("outcome"),
		Limit:     parseLimit(q.Get("limit"), 200, 1000),
	}
	if raw := q.Get("before"); raw != "" {
		// A bad cursor is ignored rather than refused: it can only come from a
		// link somebody edited, and the first page is a better answer than an
		// error about a parameter they did not know they were sending.
		if before, err := strconv.ParseInt(raw, 10, 64); err == nil {
			filter.Before = before
		}
	}
	if hours := parseLimit(q.Get("hours"), 0, 24*365); hours > 0 {
		filter.Since = time.Now().Add(-time.Duration(hours) * time.Hour)
	}

	calls, err := s.opts.Calls.Calls(r.Context(), filter)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}

	// The cursor for the next page, or empty on the last one. Computed here
	// rather than left to the browser, which would have to know that paging is
	// by id and not by time.
	var next string
	if len(calls) == filter.Limit && len(calls) > 0 {
		next = strconv.FormatInt(calls[len(calls)-1].ID, 10)
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"calls": calls,
		"count": len(calls),
		"next":  next,
	})
}

// handleListCallers summarises the ledger by who made the calls.
//
// The question this answers is "what has this credential been doing, and should
// it still exist" -- which is why it reports the plugins a caller actually
// reached rather than the ones it is permitted to reach. The gap between those
// two is the interesting part of a grant review, and only one of them is
// knowable from the grant.
func (s *Server) handleListCallers(w http.ResponseWriter, r *http.Request) {
	if s.opts.Calls == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host is not keeping a record of tool calls")
		return
	}

	days := parseLimit(r.URL.Query().Get("days"), 7, 3650)
	since := time.Now().Add(-time.Duration(days) * 24 * time.Hour)

	callers, err := s.opts.Calls.Callers(r.Context(), since)
	if err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"callers": callers,
		"count":   len(callers),
		"days":    days,
	})
}
