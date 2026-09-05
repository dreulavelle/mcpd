package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ToolCallStore is the record of who called what.
//
// Append-only in practice but not in guarantee: unlike the audit trail this is
// not hash-chained, and it is not evidence in the same sense. The audit trail
// answers "was this change authorised", which somebody may need to defend; this
// answers "what has this credential been doing", which somebody needs to read.
// Chaining it would cost a hash per read call and buy a property nobody is
// relying on.
type ToolCallStore struct {
	db  *DB
	now func() time.Time
}

func NewToolCallStore(db *DB, now func() time.Time) *ToolCallStore {
	return &ToolCallStore{db: db, now: now}
}

// ToolCall is one recorded call.
type ToolCall struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	Principal string    `json:"principal"`
	Role      string    `json:"role,omitempty"`
	Plugin    string    `json:"plugin"`
	Tool      string    `json:"tool"`
	Outcome   string    `json:"outcome"`
	// DurationUS is nil for a call that never ran, and so was never timed.
	// Not zero: a fast call and a refused one are different facts.
	DurationUS    *int64 `json:"duration_us,omitempty"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

// Record writes one call.
//
// The caller is expected to ignore the error beyond logging it: a tool call
// that worked must not be reported as failed because the bookkeeping did not.
func (s *ToolCallStore) Record(ctx context.Context, c ToolCall) error {
	at := c.At
	if at.IsZero() {
		at = s.now()
	}
	_, err := s.db.Writer().ExecContext(ctx, `
		INSERT INTO tool_calls (at, principal, role, plugin, tool, outcome, duration_us, correlation_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		at.UnixMilli(), c.Principal, c.Role, c.Plugin, c.Tool, c.Outcome,
		c.DurationUS, c.CorrelationID)
	if err != nil {
		return fmt.Errorf("sqlite: record tool call: %w", err)
	}
	return nil
}

// ToolCallFilter narrows a read. Every field is optional; the zero filter is
// "the most recent calls, whoever made them".
type ToolCallFilter struct {
	Principal string
	Plugin    string
	Tool      string
	Outcome   string
	Since     time.Time
	Limit     int
	// Before pages backwards by id rather than by time. Two calls can share a
	// millisecond, so a timestamp cursor would either skip one or repeat it.
	Before int64
}

// Calls returns matching calls, newest first.
func (s *ToolCallStore) Calls(ctx context.Context, f ToolCallFilter) ([]ToolCall, error) {
	var (
		where []string
		args  []any
	)
	add := func(clause string, value any) {
		where = append(where, clause)
		args = append(args, value)
	}
	if f.Principal != "" {
		add("principal = ?", f.Principal)
	}
	if f.Plugin != "" {
		add("plugin = ?", f.Plugin)
	}
	if f.Tool != "" {
		add("tool = ?", f.Tool)
	}
	if f.Outcome != "" {
		add("outcome = ?", f.Outcome)
	}
	if !f.Since.IsZero() {
		add("at >= ?", f.Since.UnixMilli())
	}
	if f.Before > 0 {
		add("id < ?", f.Before)
	}

	query := `SELECT id, at, principal, role, plugin, tool, outcome, duration_us, correlation_id
	            FROM tool_calls`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	// By id, not by time. Ordering by a millisecond timestamp leaves two calls
	// in the same millisecond in an order the database is free to change
	// between pages, which is how a paging reader sees a row twice.
	query += " ORDER BY id DESC LIMIT ?"

	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	args = append(args, limit)

	rows, err := s.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read tool calls: %w", err)
	}
	defer rows.Close()

	out := []ToolCall{}
	for rows.Next() {
		var (
			c        ToolCall
			at       int64
			duration sql.NullInt64
		)
		if err := rows.Scan(&c.ID, &at, &c.Principal, &c.Role, &c.Plugin, &c.Tool,
			&c.Outcome, &duration, &c.CorrelationID); err != nil {
			return nil, fmt.Errorf("sqlite: scan tool call: %w", err)
		}
		c.At = time.UnixMilli(at)
		if duration.Valid {
			c.DurationUS = &duration.Int64
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CallerSummary is one credential's activity, for the question "what has this
// been doing, and should I revoke it".
type CallerSummary struct {
	Principal string    `json:"principal"`
	Role      string    `json:"role,omitempty"`
	Calls     int64     `json:"calls"`
	Errors    int64     `json:"errors"`
	Denied    int64     `json:"denied"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Plugins are the instances this caller actually reached, which is not the
	// same as the ones it is permitted to reach. The gap between those two is
	// the interesting part of a grant review.
	Plugins []string `json:"plugins"`
}

// Callers summarises activity by principal since a point in time.
func (s *ToolCallStore) Callers(ctx context.Context, since time.Time) ([]CallerSummary, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT principal,
		       MAX(role),
		       COUNT(*),
		       SUM(CASE WHEN outcome = 'error' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN outcome = 'denied' THEN 1 ELSE 0 END),
		       MIN(at), MAX(at),
		       GROUP_CONCAT(DISTINCT plugin)
		  FROM tool_calls
		 WHERE at >= ?
		 GROUP BY principal
		 ORDER BY COUNT(*) DESC`, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("sqlite: summarise callers: %w", err)
	}
	defer rows.Close()

	out := []CallerSummary{}
	for rows.Next() {
		var (
			c                  CallerSummary
			first, last        int64
			plugins            string
			errCount, denCount int64
		)
		if err := rows.Scan(&c.Principal, &c.Role, &c.Calls, &errCount, &denCount,
			&first, &last, &plugins); err != nil {
			return nil, fmt.Errorf("sqlite: scan caller summary: %w", err)
		}
		c.Errors, c.Denied = errCount, denCount
		c.FirstSeen = time.UnixMilli(first)
		c.LastSeen = time.UnixMilli(last)
		if plugins != "" {
			c.Plugins = strings.Split(plugins, ",")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PruneToolCalls removes calls older than the cutoff, returning how many went.
//
// Deleted rather than archived. This is the highest-volume table mcpd writes --
// one row per read call -- and a control plane's data directory should not grow
// without bound because somebody left an agent running.
func (s *ToolCallStore) PruneToolCalls(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := s.db.Writer().ExecContext(ctx,
		`DELETE FROM tool_calls WHERE at < ?`, cutoff.UnixMilli())
	if err != nil {
		return 0, fmt.Errorf("sqlite: prune tool calls: %w", err)
	}
	removed, _ := result.RowsAffected()
	return removed, nil
}

// HourBucket is one step of a window: how the calls that started inside it
// ended. Present even when nothing happened, so a chart drawn from these has a
// bar per step rather than a gap that reads as a shorter day.
type HourBucket struct {
	At          time.Time `json:"at"`
	OK          int64     `json:"ok"`
	Error       int64     `json:"error"`
	Denied      int64     `json:"denied"`
	RateLimited int64     `json:"rate_limited"`
}

// PluginCalls is one system's share of a window.
type PluginCalls struct {
	Plugin string `json:"plugin"`
	Calls  int64  `json:"calls"`
	Errors int64  `json:"errors"`
}

// CallSummary is a window of the ledger seen two ways: along time, and by the
// system that was reached.
type CallSummary struct {
	Buckets []HourBucket  `json:"buckets"`
	Plugins []PluginCalls `json:"plugins"`
	Total   int64         `json:"total"`
	Errors  int64         `json:"errors"`
	Denied  int64         `json:"denied"`
}

// Summary counts the calls in [since, since+step*buckets) by step and by plugin.
//
// The bucket count is a parameter rather than something worked out from the
// clock: two reads of the clock either side of an hour boundary would answer
// with 24 buckets once and 25 the next minute, and a caller that promised its
// reader a fixed number of bars has no way to notice.
func (s *ToolCallStore) Summary(ctx context.Context, since time.Time, step time.Duration, buckets int) (CallSummary, error) {
	out := CallSummary{Buckets: []HourBucket{}, Plugins: []PluginCalls{}}
	if buckets <= 0 || step <= 0 {
		return out, nil
	}

	from := since.UnixMilli()
	width := step.Milliseconds()
	until := from + width*int64(buckets)

	out.Buckets = make([]HourBucket, buckets)
	for i := range out.Buckets {
		out.Buckets[i].At = since.Add(time.Duration(i) * step)
	}

	// Integer division on the millisecond stamp puts each call in a step. The
	// range is bounded at both ends so the totals below are the sum of the
	// bars: a row from a clock that ran ahead would otherwise be counted once
	// and drawn nowhere.
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT (at - ?) / ? AS bucket,
		       SUM(CASE WHEN outcome = 'ok' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN outcome = 'error' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN outcome = 'denied' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN outcome = 'rate_limited' THEN 1 ELSE 0 END)
		  FROM tool_calls
		 WHERE at >= ? AND at < ?
		 GROUP BY bucket`, from, width, from, until)
	if err != nil {
		return CallSummary{}, fmt.Errorf("sqlite: summarise calls by hour: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			index                       int64
			ok, failed, denied, limited int64
		)
		if err := rows.Scan(&index, &ok, &failed, &denied, &limited); err != nil {
			return CallSummary{}, fmt.Errorf("sqlite: scan call bucket: %w", err)
		}
		if index < 0 || index >= int64(buckets) {
			continue
		}
		b := &out.Buckets[index]
		b.OK, b.Error, b.Denied, b.RateLimited = ok, failed, denied, limited
		out.Total += ok + failed + denied + limited
		out.Errors += failed
		out.Denied += denied
	}
	if err := rows.Err(); err != nil {
		return CallSummary{}, fmt.Errorf("sqlite: summarise calls by hour: %w", err)
	}

	byPlugin, err := s.db.Reader().QueryContext(ctx, `
		SELECT plugin,
		       COUNT(*),
		       SUM(CASE WHEN outcome = 'error' THEN 1 ELSE 0 END)
		  FROM tool_calls
		 WHERE at >= ? AND at < ?
		 GROUP BY plugin
		 ORDER BY COUNT(*) DESC, plugin`, from, until)
	if err != nil {
		return CallSummary{}, fmt.Errorf("sqlite: summarise calls by plugin: %w", err)
	}
	defer byPlugin.Close()

	for byPlugin.Next() {
		var p PluginCalls
		if err := byPlugin.Scan(&p.Plugin, &p.Calls, &p.Errors); err != nil {
			return CallSummary{}, fmt.Errorf("sqlite: scan plugin summary: %w", err)
		}
		out.Plugins = append(out.Plugins, p)
	}
	return out, byPlugin.Err()
}
