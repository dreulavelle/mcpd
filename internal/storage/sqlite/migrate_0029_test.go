package sqlite

import (
	"context"
	"testing"
	"time"
)

// Two deployments on the same version number must have the same schema.
func TestMigrate0029_UpgradingMatchesAFreshDatabase(t *testing.T) {
	ctx := context.Background()

	fresh := openDBAt(t, "fresh29.db")
	if _, err := Migrate(ctx, fresh); err != nil {
		t.Fatalf("fresh migrate: %v", err)
	}

	upgraded := openDBAt(t, "upgraded29.db")
	applyThrough(t, upgraded, 28)
	if _, err := Migrate(ctx, upgraded); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	if got, want := schemaOf(t, upgraded), schemaOf(t, fresh); got != want {
		t.Errorf("an upgraded database does not match a fresh one\n--- upgraded ---\n%s\n--- fresh ---\n%s",
			got, want)
	}
}

/*
The backfill and the live upsert must produce identical rows for identical
calls.

They are two separate implementations of one rule: the migration hard-codes
eight CASE ranges, and `bandFor` walks `latencyBands`. Nothing keeps them in
step -- a future band change touches the Go table and nobody will remember the
frozen SQL -- and the migration is checksummed and forward-only, so a wrong
backfill can never be re-run against the hosts it has already touched. This is
the assertion that keeps them honest.

The durations are the boundaries themselves and one microsecond either side of
each, which is where an off-by-one between `<=` and `<` would show.
*/
func TestMigrate0029_BackfillMatchesTheLiveUpsert(t *testing.T) {
	ctx := context.Background()

	durations := []*int64{
		us(1), us(1_000), us(1_001), us(5_000), us(5_001), us(25_000), us(25_001),
		us(100_000), us(100_001), us(500_000), us(500_001), us(2_000_000),
		us(2_000_001), us(10_000_000), us(10_000_001), us(45_000_000),
		nil, // a refusal, which was never timed
	}

	// The ledger route: rows written before the migration, folded up by it.
	viaBackfill := openDBAt(t, "backfill29.db")
	applyThrough(t, viaBackfill, 28)
	ledger := NewToolCallStore(viaBackfill, time.Now)
	for i, d := range durations {
		outcome := "ok"
		if d == nil {
			outcome = "denied"
		}
		// Two hours, so the grouping is exercised rather than assumed.
		at := hour.Add(time.Duration(i%2) * time.Hour)
		if err := ledger.Record(ctx, ToolCall{
			At: at, Principal: "user:someone", Plugin: "echo", Tool: "say",
			Outcome: outcome, DurationUS: d,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Migrate(ctx, viaBackfill); err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	// The live route: the same calls through the code that runs from now on.
	viaUpsert := NewToolStatsStore(newTestDB(t))
	for i, d := range durations {
		outcome := "ok"
		if d == nil {
			outcome = "denied"
		}
		at := hour.Add(time.Duration(i%2) * time.Hour)
		if err := viaUpsert.RecordCall(ctx, at, "echo", "say", outcome, d); err != nil {
			t.Fatal(err)
		}
	}

	want := rollupRows(t, viaUpsert.db)
	got := rollupRows(t, viaBackfill)
	if len(got) == 0 {
		t.Fatal("the backfill produced nothing")
	}
	if len(got) != len(want) {
		t.Fatalf("backfill produced %d rows, the upsert %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d differs\n backfill: %v\n   upsert: %v", i, got[i], want[i])
		}
	}
}

// An empty ledger backfills nothing rather than one row of zeroes.
func TestMigrate0029_BackfillsNothingFromAnEmptyLedger(t *testing.T) {
	db := openDBAt(t, "emptybackfill29.db")
	applyThrough(t, db, 28)
	if _, err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if rows := rollupRows(t, db); len(rows) != 0 {
		t.Fatalf("got %d rows from an empty ledger", len(rows))
	}
}

// rollupRow is every column the two paths both write, in a comparable shape.
type rollupRow struct {
	bucket                         int64
	plugin, tool, outcome          string
	calls, timed, durationSum      int64
	durationMin, durationMax       int64
	b0, b1, b2, b3, b4, b5, b6, b7 int64
}

func rollupRows(t *testing.T, db *DB) []rollupRow {
	t.Helper()
	rows, err := db.Reader().Query(`
		SELECT bucket, plugin, tool, outcome, calls, timed, duration_sum,
		       COALESCE(duration_min, -1), COALESCE(duration_max, -1),
		       le_1ms, le_5ms, le_25ms, le_100ms, le_500ms, le_2s, le_10s, over_10s
		FROM tool_call_stats
		ORDER BY bucket, plugin, tool, outcome`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var out []rollupRow
	for rows.Next() {
		var r rollupRow
		if err := rows.Scan(&r.bucket, &r.plugin, &r.tool, &r.outcome,
			&r.calls, &r.timed, &r.durationSum, &r.durationMin, &r.durationMax,
			&r.b0, &r.b1, &r.b2, &r.b3, &r.b4, &r.b5, &r.b6, &r.b7); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
