package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"
)

// ToolStatsStore is the permanent record of how much and how fast.
//
// It holds sums, never rows: no principal, no arguments, no correlation id.
// That is what makes keeping it for ever reasonable, and it is the difference
// between this and the ledger beside it, which is pruned precisely because it
// does hold those things.
//
// Written on the same call path as the ledger rather than rebuilt from it by a
// batch job. A rollup derived from rows that get pruned is a rollup with a
// hole in it the first time the two schedules disagree, and reconciling them
// afterwards is guesswork.
type ToolStatsStore struct {
	db *DB
}

func NewToolStatsStore(db *DB) *ToolStatsStore { return &ToolStatsStore{db: db} }

// Bucket boundaries in microseconds, and the column each band counts into.
// Fixed rather than configurable: a boundary that moved would make two spans
// of the same table incomparable, which is the one thing this exists to allow.
var latencyBands = []struct {
	upto   int64
	column string
}{
	{1_000, "le_1ms"},
	{5_000, "le_5ms"},
	{25_000, "le_25ms"},
	{100_000, "le_100ms"},
	{500_000, "le_500ms"},
	{2_000_000, "le_2s"},
	{10_000_000, "le_10s"},
}

// bandFor names the column one duration belongs in.
func bandFor(us int64) string {
	for _, b := range latencyBands {
		if us <= b.upto {
			return b.column
		}
	}
	return "over_10s"
}

// hourOf truncates to the start of the hour the rollup is keyed on.
func hourOf(t time.Time) int64 { return t.UTC().Truncate(time.Hour).UnixMilli() }

// RecordCall folds one call into its hour.
//
// `durationUS` is nil for a call that never ran. It is still counted in
// `calls`, because a refusal is a fact about the tool, but it contributes to
// nothing that would be divided by `timed` -- a host that refuses a great deal
// must not read as a host that is fast.
func (s *ToolStatsStore) RecordCall(ctx context.Context, at time.Time, plugin, tool, outcome string, durationUS *int64) error {
	if durationUS == nil {
		_, err := s.db.Writer().ExecContext(ctx, `
			INSERT INTO tool_call_stats (bucket, plugin, tool, outcome, calls)
			VALUES (?, ?, ?, ?, 1)
			ON CONFLICT(bucket, plugin, tool, outcome) DO UPDATE SET
				calls = calls + 1`,
			hourOf(at), plugin, tool, outcome)
		if err != nil {
			return fmt.Errorf("sqlite: roll up tool call: %w", err)
		}
		return nil
	}

	us := *durationUS
	// The band is a column name from a fixed table above, never anything a
	// caller supplies, so building the statement around it introduces nothing.
	band := bandFor(us)
	_, err := s.db.Writer().ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO tool_call_stats (
			bucket, plugin, tool, outcome,
			calls, timed, duration_sum, duration_min, duration_max, %s)
		VALUES (?, ?, ?, ?, 1, 1, ?, ?, ?, 1)
		ON CONFLICT(bucket, plugin, tool, outcome) DO UPDATE SET
			calls        = calls + 1,
			timed        = timed + 1,
			duration_sum = duration_sum + excluded.duration_sum,
			duration_min = MIN(COALESCE(duration_min, excluded.duration_min), excluded.duration_min),
			duration_max = MAX(COALESCE(duration_max, excluded.duration_max), excluded.duration_max),
			%s           = %s + 1`, band, band, band),
		hourOf(at), plugin, tool, outcome, us, us, us)
	if err != nil {
		return fmt.Errorf("sqlite: roll up tool call: %w", err)
	}
	return nil
}

// RecordResultSize folds the size of one answer into its hour.
//
// Separate from RecordCall because the observer reports the two separately:
// the size costs a marshal of the whole result and is measured only once a
// call has succeeded. Both land on the same row, since a sized call is by
// definition an `ok` one.
func (s *ToolStatsStore) RecordResultSize(ctx context.Context, at time.Time, plugin, tool string, bytes int64) error {
	_, err := s.db.Writer().ExecContext(ctx, `
		INSERT INTO tool_call_stats (bucket, plugin, tool, outcome, sized, bytes_sum, bytes_max)
		VALUES (?, ?, ?, 'ok', 1, ?, ?)
		ON CONFLICT(bucket, plugin, tool, outcome) DO UPDATE SET
			sized     = sized + 1,
			bytes_sum = bytes_sum + excluded.bytes_sum,
			bytes_max = MAX(COALESCE(bytes_max, excluded.bytes_max), excluded.bytes_max)`,
		hourOf(at), plugin, tool, bytes, bytes)
	if err != nil {
		return fmt.Errorf("sqlite: roll up result size: %w", err)
	}
	return nil
}

// ToolTotals is one tool's whole record over a span.
type ToolTotals struct {
	Plugin string `json:"plugin"`
	Tool   string `json:"tool"`

	Calls       int64 `json:"calls"`
	OK          int64 `json:"ok"`
	Errors      int64 `json:"errors"`
	Denied      int64 `json:"denied"`
	RateLimited int64 `json:"rate_limited"`

	// Timed is the denominator for every duration here, and is not Calls.
	Timed  int64  `json:"timed"`
	MeanUS int64  `json:"mean_us"`
	MinUS  *int64 `json:"min_us,omitempty"`
	MaxUS  *int64 `json:"max_us,omitempty"`
	P50US  *int64 `json:"p50_us,omitempty"`
	P95US  *int64 `json:"p95_us,omitempty"`

	// Sized is the denominator for the byte figures, for the same reason.
	Sized     int64  `json:"sized"`
	BytesSum  int64  `json:"bytes_sum"`
	MeanBytes int64  `json:"mean_bytes"`
	MaxBytes  *int64 `json:"max_bytes,omitempty"`

	// FirstSeen and LastSeen bound what this row is describing, so a tool that
	// stopped being called does not read as one that is merely quiet.
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	bands [8]int64
}

// Totals reports every tool with activity in the span, busiest first.
//
// `since` zero means everything ever recorded, which is the point of the table.
func (s *ToolStatsStore) Totals(ctx context.Context, since time.Time) ([]ToolTotals, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT plugin, tool,
		       SUM(calls),
		       SUM(CASE WHEN outcome = 'ok'           THEN calls ELSE 0 END),
		       SUM(CASE WHEN outcome = 'error'        THEN calls ELSE 0 END),
		       SUM(CASE WHEN outcome = 'denied'       THEN calls ELSE 0 END),
		       SUM(CASE WHEN outcome = 'rate_limited' THEN calls ELSE 0 END),
		       SUM(timed), SUM(duration_sum), MIN(duration_min), MAX(duration_max),
		       SUM(sized), SUM(bytes_sum), MAX(bytes_max),
		       SUM(le_1ms), SUM(le_5ms), SUM(le_25ms), SUM(le_100ms),
		       SUM(le_500ms), SUM(le_2s), SUM(le_10s), SUM(over_10s),
		       MIN(bucket), MAX(bucket)
		FROM tool_call_stats
		WHERE bucket >= ?
		GROUP BY plugin, tool
		ORDER BY SUM(calls) DESC, plugin, tool`,
		sinceMillis(since))
	if err != nil {
		return nil, fmt.Errorf("sqlite: tool totals: %w", err)
	}
	defer rows.Close()

	out := []ToolTotals{}
	for rows.Next() {
		var t ToolTotals
		var durSum int64
		var first, last int64
		if err := rows.Scan(&t.Plugin, &t.Tool,
			&t.Calls, &t.OK, &t.Errors, &t.Denied, &t.RateLimited,
			&t.Timed, &durSum, &t.MinUS, &t.MaxUS,
			&t.Sized, &t.BytesSum, &t.MaxBytes,
			&t.bands[0], &t.bands[1], &t.bands[2], &t.bands[3],
			&t.bands[4], &t.bands[5], &t.bands[6], &t.bands[7],
			&first, &last); err != nil {
			return nil, fmt.Errorf("sqlite: scan tool totals: %w", err)
		}
		if t.Timed > 0 {
			t.MeanUS = durSum / t.Timed
			t.P50US = quantile(t.bands, t.Timed, 0.50)
			t.P95US = quantile(t.bands, t.Timed, 0.95)
		}
		if t.Sized > 0 {
			t.MeanBytes = t.BytesSum / t.Sized
		}
		t.FirstSeen = time.UnixMilli(first).UTC()
		// The bucket names the start of its hour, so the span it describes
		// ends an hour later.
		t.LastSeen = time.UnixMilli(last).UTC().Add(time.Hour)
		out = append(out, t)
	}
	return out, rows.Err()
}

// quantile reads a percentile back off the bucket counts.
//
// The answer is the upper bound of the band the nth call falls in, not an
// interpolation inside it: the table records that a call was under 100ms and
// not where under, so a number between the bounds would be invented. It is
// therefore an upper estimate, which is the safe direction for a latency
// somebody is deciding a timeout from.
func quantile(bands [8]int64, total int64, q float64) *int64 {
	if total <= 0 {
		return nil
	}
	// Nearest rank: the qth percentile is the value at position ceil(q*n).
	// Truncating instead would put the 95th percentile of twenty calls at the
	// nineteenth, which is the same answer only when q*n is already whole.
	want := int64(math.Ceil(float64(total) * q))
	if want < 1 {
		want = 1
	}
	var seen int64
	for i, band := range bands {
		seen += band
		if seen >= want {
			if i < len(latencyBands) {
				v := latencyBands[i].upto
				return &v
			}
			// The overflow band has no upper bound to report.
			return nil
		}
	}
	return nil
}

// Point is one hour of the whole host, for a trend line.
type Point struct {
	At     time.Time `json:"at"`
	Calls  int64     `json:"calls"`
	OK     int64     `json:"ok"`
	Failed int64     `json:"failed"`
	Timed  int64     `json:"timed"`
	MeanUS int64     `json:"mean_us"`
	Bytes  int64     `json:"bytes"`
}

// Series reports the host hour by hour, oldest first.
func (s *ToolStatsStore) Series(ctx context.Context, since time.Time) ([]Point, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT bucket,
		       SUM(calls),
		       SUM(CASE WHEN outcome = 'ok' THEN calls ELSE 0 END),
		       SUM(CASE WHEN outcome <> 'ok' THEN calls ELSE 0 END),
		       SUM(timed), SUM(duration_sum), SUM(bytes_sum)
		FROM tool_call_stats
		WHERE bucket >= ?
		GROUP BY bucket
		ORDER BY bucket`,
		sinceMillis(since))
	if err != nil {
		return nil, fmt.Errorf("sqlite: tool series: %w", err)
	}
	defer rows.Close()

	out := []Point{}
	for rows.Next() {
		var p Point
		var at, durSum int64
		if err := rows.Scan(&at, &p.Calls, &p.OK, &p.Failed, &p.Timed, &durSum, &p.Bytes); err != nil {
			return nil, fmt.Errorf("sqlite: scan tool series: %w", err)
		}
		p.At = time.UnixMilli(at).UTC()
		if p.Timed > 0 {
			p.MeanUS = durSum / p.Timed
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PluginTotals is one plugin's whole record, summed across its tools.
type PluginTotals struct {
	Plugin   string `json:"plugin"`
	Tools    int64  `json:"tools"`
	Calls    int64  `json:"calls"`
	OK       int64  `json:"ok"`
	Timed    int64  `json:"timed"`
	MeanUS   int64  `json:"mean_us"`
	BytesSum int64  `json:"bytes_sum"`
}

// ByPlugin reports each plugin, busiest first.
func (s *ToolStatsStore) ByPlugin(ctx context.Context, since time.Time) ([]PluginTotals, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT plugin, COUNT(DISTINCT tool), SUM(calls),
		       SUM(CASE WHEN outcome = 'ok' THEN calls ELSE 0 END),
		       SUM(timed), SUM(duration_sum), SUM(bytes_sum)
		FROM tool_call_stats
		WHERE bucket >= ?
		GROUP BY plugin
		ORDER BY SUM(calls) DESC, plugin`,
		sinceMillis(since))
	if err != nil {
		return nil, fmt.Errorf("sqlite: plugin totals: %w", err)
	}
	defer rows.Close()

	out := []PluginTotals{}
	for rows.Next() {
		var p PluginTotals
		var durSum int64
		if err := rows.Scan(&p.Plugin, &p.Tools, &p.Calls, &p.OK, &p.Timed, &durSum, &p.BytesSum); err != nil {
			return nil, fmt.Errorf("sqlite: scan plugin totals: %w", err)
		}
		if p.Timed > 0 {
			p.MeanUS = durSum / p.Timed
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Span reports the first and last hour the table holds anything for.
//
// What it is for is saying how far back the numbers go. "Since this host was
// first used" is a different claim from "since the retention window", and a
// page that shows lifetime totals has to be able to say which.
func (s *ToolStatsStore) Span(ctx context.Context) (first, last time.Time, err error) {
	var lo, hi sql.NullInt64
	if err := s.db.Reader().QueryRowContext(ctx,
		`SELECT MIN(bucket), MAX(bucket) FROM tool_call_stats`).Scan(&lo, &hi); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("sqlite: stats span: %w", err)
	}
	if !lo.Valid {
		return time.Time{}, time.Time{}, nil
	}
	return time.UnixMilli(lo.Int64).UTC(), time.UnixMilli(hi.Int64).UTC().Add(time.Hour), nil
}

// sinceMillis turns a zero time into "everything", rather than 1970.
func sinceMillis(since time.Time) int64 {
	if since.IsZero() {
		return 0
	}
	return since.UTC().Truncate(time.Hour).UnixMilli()
}
