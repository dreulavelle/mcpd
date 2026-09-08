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
	Totals(ctx context.Context, since time.Time) ([]sqlite.ToolTotals, error)
	Series(ctx context.Context, since time.Time) ([]sqlite.Point, error)
	ByPlugin(ctx context.Context, since time.Time) ([]sqlite.PluginTotals, error)
	Span(ctx context.Context) (first, last time.Time, err error)
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

	// Recording says whether new calls are being rolled up at all, so a page
	// showing nothing can tell "nothing happened" from "nothing is kept".
	Recording bool `json:"recording"`

	// First and Last bound what is actually held, which is not the same as the
	// window asked for: a host three days old cannot answer for a month.
	First *time.Time `json:"first,omitempty"`
	Last  *time.Time `json:"last,omitempty"`

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

	out := StatisticsResponse{
		Window:            hours,
		Recording:         true,
		ResultBudgetBytes: observability.ResultBudgetBytes,
		WireMultiplier:    wireMultiplier,
		BytesPerToken:     bytesPerToken,
	}

	const failed = "The statistics could not be read."

	var err error
	if out.Tools, err = s.opts.Stats.Totals(ctx, since); err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, err, failed)
		return
	}
	if out.Plugins, err = s.opts.Stats.ByPlugin(ctx, since); err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, err, failed)
		return
	}
	if out.Series, err = s.opts.Stats.Series(ctx, since); err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, err, failed)
		return
	}

	first, last, err := s.opts.Stats.Span(ctx)
	if err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, err, failed)
		return
	}
	if !first.IsZero() {
		out.First, out.Last = &first, &last
	}

	s.writeJSON(w, r, http.StatusOK, out)
}
