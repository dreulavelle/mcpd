package admin

import (
	"net/http"

	"github.com/spoked/mcpd/internal/auth"
)

// handleTestNotification sends one event to the configured address.
//
// It exists because the failure it catches is silent. Everything else here
// queues an event and never blocks a caller, so a mistyped address costs
// nothing at the moment it is typed and everything at the moment somebody
// needed to hear from this host. Pressing a button and being told what
// happened is the only way to find out before then.
func (s *Server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	if s.opts.NotifyTest == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"notifications are not configured here")
		return
	}
	if err := s.opts.NotifyTest(r.Context()); err != nil {
		// The receiver's own words, because they are what an operator needs:
		// a 404 means the webhook is gone, a 403 means the token is wrong,
		// and "it did not work" means neither.
		s.writeError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.opts.Log.InfoContext(r.Context(), "a test notification was sent",
		"principal", auth.FromContext(r.Context()).ID)

	s.writeJSON(w, r, http.StatusOK, map[string]string{
		"status": "sent",
		"note":   "If nothing arrives, the address answered but delivered nowhere.",
	})
}
