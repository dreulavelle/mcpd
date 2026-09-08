package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/spoked/mcpd/internal/observability"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// StatsReader is the permanent rollup of every tool call this host has served.
//
// Distinct from CallLedger beside it, which reads individual rows and is
// pruned. This one holds sums that name nobody, so it answers questions about
// a year that the ledger cannot answer about a month.
type StatsReader interface {
	Read(ctx context.Context, since time.Time) (sqlite.Snapshot, error)
}

// StatisticsResponse is the whole page in one call.
//
// One request rather than four, because every part of it is a different cut of
// the same span and four requests could land either side of an hour boundary
// and disagree with each other on screen.
type StatisticsResponse struct {
	// Window is how far back this covers, in hours. Zero means everything the
	// host has ever recorded.
	Window int `json:"window_hours"`

	// First bounds what is actually held, which is not the window asked for: a
	// host three days old cannot answer for a month.
	First *time.Time `json:"first,omitempty"`

	// StrideSeconds is how wide one point of the series is, so a chart can say
	// what a bar covers rather than leaving it to be guessed from the count.
	StrideSeconds int `json:"stride_seconds"`

	Tools   []sqlite.ToolTotals   `json:"tools"`
	Plugins []sqlite.PluginTotals `json:"plugins"`
	Series  []sqlite.Point        `json:"series"`

	// ResultBudgetBytes is the ceiling a result is truncated at, so the page
	// can say which tools are answering against it rather than leaving a
	// number without a scale.
	ResultBudgetBytes int `json:"result_budget_bytes"`

	// WireMultiplier is what a byte of result costs a model's context. The
	// protocol carries a result as structured content and again as text, so a
	// page reporting context cost has to double it rather than quietly
	// reporting half.
	WireMultiplier int `json:"wire_multiplier"`

	// BytesPerToken is the divisor behind every token figure on the page. It
	// is an estimate and the page must say so: this host is the server, and
	// the token count belongs to a model it never speaks to.
	BytesPerToken int `json:"bytes_per_token"`
}

const (
	// maxWindowHours is two years, which is longer than any host has been
	// running and short enough that the bucket count stays a shape a chart can
	// draw.
	maxWindowHours = 24 * 730

	// wireMultiplier is the doubling above.
	wireMultiplier = 2

	// bytesPerToken is the usual rough figure for English text through a
	// byte-pair encoder. Deliberately a round number: a page that reported
	// 3.7 would be claiming a precision this host cannot have.
	bytesPerToken = 4
)

// handleStatistics reports what every tool has done, over a span.
func (s *Server) handleStatistics(w http.ResponseWriter, r *http.Request) {
	if s.opts.Stats == nil {
		// An empty surface rather than a failure, for the reason the
		// performance route gives: a host with nothing recorded should show an
		// empty page, not something the console has to draw as broken.
		s.writeJSON(w, r, http.StatusOK, StatisticsResponse{
			Tools: []sqlite.ToolTotals{}, Plugins: []sqlite.PluginTotals{},
			Series: []sqlite.Point{}, ResultBudgetBytes: observability.ResultBudgetBytes,
			WireMultiplier: wireMultiplier, BytesPerToken: bytesPerToken,
			StrideSeconds: int(time.Hour / time.Second),
		})
		return
	}
	ctx := r.Context()

	hours := 0
	if raw := r.URL.Query().Get("hours"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > maxWindowHours {
			s.writeError(w, r, http.StatusBadRequest,
				"The window has to be a number of hours between 0 and "+
					strconv.Itoa(maxWindowHours)+". Zero means everything kept.")
			return
		}
		hours = n
	}

	// Zero stays the zero time, which the store reads as "everything".
	var since time.Time
	if hours > 0 {
		since = time.Now().Add(-time.Duration(hours) * time.Hour)
	}

	snap, err := s.opts.Stats.Read(ctx, since)
	if err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, err,
			"The statistics could not be read.")
		return
	}

	out := StatisticsResponse{
		Window:            hours,
		Tools:             snap.Tools,
		Plugins:           snap.Plugins,
		Series:            snap.Series,
		StrideSeconds:     int(snap.Stride / time.Second),
		ResultBudgetBytes: observability.ResultBudgetBytes,
		WireMultiplier:    wireMultiplier,
		BytesPerToken:     bytesPerToken,
	}
	if !snap.First.IsZero() {
		first := snap.First
		out.First = &first
	}

	s.writeJSON(w, r, http.StatusOK, out)
}
