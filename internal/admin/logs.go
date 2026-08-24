package admin

import (
	"bytes"
	"fmt"
	"net/http"
	"time"
)

// heartbeat is how often a quiet stream says something.
//
// A log can be silent for a long time on a healthy host, and every layer
// between this process and the browser -- a reverse proxy, a load balancer,
// the browser itself -- is entitled to treat a silent connection as a dead
// one. A comment line costs two bytes and is discarded by the client, which is
// what SSE provides them for.
const heartbeat = 25 * time.Second

// handleLogStream sends the log as it happens.
//
// Server-sent events rather than a WebSocket. Nothing here travels upwards --
// the browser watches, it does not talk back -- and a WebSocket would buy a
// direction that is not used at the price of a dependency this build does not
// otherwise have. EventSource also reconnects on its own, which is the whole
// of the client-side reconnect logic that would otherwise have to be written
// and got right.
//
// What reaches this handler has already been through the same handler options
// the destination uses, so a value the redaction withholds from the file is
// not here to be sent.
func (s *Server) handleLogStream(w http.ResponseWriter, r *http.Request) {
	if s.opts.Logs == nil {
		s.writeError(w, r, http.StatusServiceUnavailable,
			"this host is not keeping a copy of its log")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, r, http.StatusInternalServerError,
			"this connection cannot stream")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	// Proxies that buffer would hold a line until they had a bufferful, which
	// on a quiet host is indistinguishable from the log having stopped.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	watch := s.opts.Logs.Watch()
	defer watch.Close()

	for _, line := range watch.Backlog {
		if !send(w, line) {
			return
		}
	}
	flusher.Flush()

	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case line, open := <-watch.Lines:
			if !open {
				return
			}
			// Reported before the line it precedes, so the gap appears where
			// it happened rather than at the end of the session.
			if n := watch.Dropped(); n > 0 {
				if _, err := fmt.Fprintf(w, "event: dropped\ndata: %d\n\n", n); err != nil {
					return
				}
			}
			if !send(w, line) {
				return
			}
			flusher.Flush()

		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": still here\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// send writes one line as an event.
//
// The trailing newline the handler wrote is removed rather than passed on: in
// this format a newline inside the payload ends the field, so leaving it there
// would have every event carry a stray empty line. Nothing else in a rendered
// record can contain one -- JSON escapes them -- so trimming the last is
// enough.
func send(w http.ResponseWriter, line []byte) bool {
	_, err := fmt.Fprintf(w, "data: %s\n\n", bytes.TrimRight(line, "\n"))
	return err == nil
}
