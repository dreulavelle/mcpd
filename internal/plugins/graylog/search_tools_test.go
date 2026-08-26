package graylog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// The scripting API answers in columns and this package keeps it that way. A
// model handed columns it was not told about will read the first row as a
// header, so the columns are named and the rows stay positional.
func TestSearch_KeepsTheColumnarShape(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{
		"schema":[
			{"column_type":"field","type":"date","field":"timestamp","name":"field: timestamp"},
			{"column_type":"field","type":"string","field":"source","name":"field: source"}],
		"datarows":[["2026-08-26T11:00:00Z","web-01"],["2026-08-26T11:00:01Z","web-02"]],
		"metadata":{"effective_timerange":{"from":"2026-08-26T10:45:00Z","to":"2026-08-26T11:00:00Z","type":"absolute"}}}`))

	got, err := p.searchMessages(context.Background(), searchArgs{Streams: []string{"s1"}})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if want := []string{"timestamp", "source"}; fmt.Sprint(got.Columns) != fmt.Sprint(want) {
		t.Errorf("columns = %v, want %v", got.Columns, want)
	}
	if got.Returned != 2 {
		t.Errorf("returned = %d, want 2", got.Returned)
	}
	// The window Graylog says it searched, not the one that was asked for.
	// Only Graylog knows what a keyword resolved to, and a count without the
	// window it covers is a number with no unit.
	if !strings.Contains(got.Window, "2026-08-26T10:45:00Z") {
		t.Errorf("window = %q, want the effective range the API reported", got.Window)
	}
}

// A search with no streams named scans every index the credential can see.
// That is slow for everybody using the cluster, and the result says so rather
// than leaving it to be noticed in a graph later.
func TestSearch_SaysWhenItScannedEverything(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{"schema":[],"datarows":[],"metadata":{}}`))

	got, err := p.searchMessages(context.Background(), searchArgs{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(got.Note, "stream_ids") {
		t.Errorf("an unscoped search should say so: %q", got.Note)
	}
}

// A single log line can be a megabyte of stack trace, and one of those would
// fill a conversation on its own. It is cut rather than dropped -- the first
// few thousand characters are the ones somebody wants -- and the result says
// it happened so the message can be asked for on its own.
func TestSearch_CutsAnOversizedValue(t *testing.T) {
	huge := strings.Repeat("x", maxFieldChars+500)
	p := toolPlugin(t, func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(map[string]any{
			"schema":   []map[string]any{{"column_type": "field", "field": "message"}},
			"datarows": [][]any{{huge}},
			"metadata": map[string]any{},
		})
		_, _ = w.Write(body)
	})

	got, err := p.searchMessages(context.Background(), searchArgs{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got.ValuesCut != 1 {
		t.Fatalf("values_shortened = %d, want 1", got.ValuesCut)
	}
	value := got.Rows[0][0].(string)
	if len(value) > maxFieldChars+16 {
		t.Errorf("the value is %d characters; it should have been cut", len(value))
	}
	if !strings.Contains(got.Note, "cut short") {
		t.Errorf("the result should say a value was cut: %q", got.Note)
	}
}

// A value cut on the byte alone leaves a partial rune, which encodes as a
// replacement character -- so a message in any language but English ends in
// what looks like corrupted data rather than a value somebody cut on purpose.
func TestCutAt_DoesNotSplitARune(t *testing.T) {
	s := strings.Repeat("é", 100) // two bytes each
	got := cutAt(s, 101)          // deliberately mid-rune
	if len(got) != 100 {
		t.Errorf("cut to %d bytes, want 100 (backed up to the rune boundary)", len(got))
	}
	if !strings.HasSuffix(got, "é") {
		t.Errorf("the cut left a partial rune: %q", got[len(got)-3:])
	}
}

// A model shown twenty of two thousand matches and not told so will answer as
// though it saw them all.
func TestSearch_ReportsTruncation(t *testing.T) {
	rows := make([][]any, 40)
	for i := range rows {
		rows[i] = []any{"line"}
	}
	body, _ := json.Marshal(map[string]any{
		"schema":   []map[string]any{{"column_type": "field", "field": "message"}},
		"datarows": rows,
		"metadata": map[string]any{},
	})
	p := toolPlugin(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })

	got, err := p.searchMessages(context.Background(), searchArgs{Limit: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !got.Truncated || got.Returned != 5 {
		t.Fatalf("returned %d, truncated %v; want 5 and true", got.Returned, got.Truncated)
	}
	if got.Reason == "" {
		t.Error("truncation should say which ceiling stopped it")
	}
}

// An answer of nothing at all, because the one matching message was large, is
// worse than an answer of one large message.
func TestSearch_AlwaysReturnsAtLeastOneRow(t *testing.T) {
	big := strings.Repeat("y", maxFieldChars)
	body, _ := json.Marshal(map[string]any{
		"schema": []map[string]any{{"column_type": "field", "field": "message"}},
		"datarows": [][]any{{big}, {big}, {big}, {big}, {big}, {big}, {big}, {big},
			{big}, {big}, {big}, {big}, {big}, {big}, {big}, {big}, {big}, {big}},
		"metadata": map[string]any{},
	})
	p := toolPlugin(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })

	got, err := p.searchMessages(context.Background(), searchArgs{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got.Returned == 0 {
		t.Fatal("the whole answer was dropped for being large")
	}
	if !got.Truncated {
		t.Error("stopping early should be reported")
	}
}

// The search request has to carry what it was asked to carry. A dropped field
// here is silent: Graylog answers happily with the wrong columns, or the whole
// estate instead of one stream.
func TestSearch_SendsWhatItWasAsked(t *testing.T) {
	var got messagesRequest
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"schema":[],"datarows":[],"metadata":{}}`))
	})

	_, err := p.searchMessages(context.Background(), searchArgs{
		Query:     "level:ERROR",
		Streams:   []string{"s1", "", "  "},
		Fields:    []string{"timestamp", "level"},
		Limit:     7,
		Offset:    14,
		SortField: "timestamp",
		SortOrder: "ASC",
		timeArgs:  timeArgs{RangeSeconds: 300},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got.Query != "level:ERROR" || got.Size != 7 || got.From != 14 {
		t.Errorf("request = %+v, want the query, size and offset that were asked for", got)
	}
	// An empty entry in a list is what a model produces when it builds one
	// from something that was not there. Left in, it becomes a stream filter
	// matching nothing.
	if len(got.Streams) != 1 || got.Streams[0] != "s1" {
		t.Errorf("streams = %v, want the empty entries dropped", got.Streams)
	}
	if got.SortOrder != "asc" {
		t.Errorf("sort_order = %q, want it normalised to asc", got.SortOrder)
	}
	if got.Timerange.Range != 300 {
		t.Errorf("timerange = %+v, want the 300 seconds asked for", got.Timerange)
	}
}

func TestSearch_DefaultFieldsAreThree(t *testing.T) {
	var got messagesRequest
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"schema":[],"datarows":[],"metadata":{}}`))
	})

	if _, err := p.searchMessages(context.Background(), searchArgs{}); err != nil {
		t.Fatalf("search: %v", err)
	}
	if fmt.Sprint(got.Fields) != fmt.Sprint(defaultMessageFields) {
		t.Errorf("fields = %v, want %v", got.Fields, defaultMessageFields)
	}
}

// Two metrics over the same field are different columns, so the field alone
// would name them both the same thing.
func TestColumnNames_DistinguishesMetricsOverOneField(t *testing.T) {
	got := columnNames([]schemaColumn{
		{ColumnType: "grouping", Field: "source", Name: "grouping: source"},
		{ColumnType: "metric", Function: "avg", Field: "took_ms", Name: "metric: avg(took_ms)"},
		{ColumnType: "metric", Function: "max", Field: "took_ms", Name: "metric: max(took_ms)"},
	})
	want := []string{"source", "avg(took_ms)", "max(took_ms)"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("columns = %v, want %v", got, want)
	}
}

// A column this does not recognise still needs a position. An empty label
// would silently shift every value after it by one.
func TestColumnNames_NeverDropsAColumn(t *testing.T) {
	got := columnNames([]schemaColumn{
		{ColumnType: "something_new", Name: "whatever it is"},
		{ColumnType: "field", Field: "source"},
	})
	if len(got) != 2 {
		t.Fatalf("columns = %v, want two positions", got)
	}
	if got[1] != "source" {
		t.Errorf("the recognised column shifted to %q", got[1])
	}
}

// A model asked for an average writes "average" about as often as "avg", and a
// round trip to be told about three letters is a round trip wasted.
func TestBuildMetric_AcceptsTheObviousSpellings(t *testing.T) {
	for in, want := range map[string]string{
		"average": "avg", "avg": "avg", "AVG": "avg",
		"cardinality": "card", "std_dev": "stddev",
	} {
		got, err := buildMetric(in, "took_ms", 0, "")
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got.Function != want {
			t.Errorf("%q became %q, want %q", in, got.Function, want)
		}
	}
}

// An average of nothing is not a smaller question, it is one with no answer.
func TestBuildMetric_NeedsAFieldForEverythingButCount(t *testing.T) {
	if _, err := buildMetric("count", "", 0, ""); err != nil {
		t.Fatalf("count without a field should work: %v", err)
	}
	_, err := buildMetric("avg", "", 0, "")
	if err == nil {
		t.Fatal("avg with no field was accepted")
	}
	if !strings.Contains(err.Error(), "needs a field") {
		t.Errorf("the message should say what is missing, got: %v", err)
	}
}

// A function nobody recognises is refused with the list rather than passed
// upstream to come back as a 400 that does not say what was wrong with it.
func TestBuildMetric_RefusesAnUnknownFunctionWithTheList(t *testing.T) {
	_, err := buildMetric("median", "took_ms", 0, "")
	if err == nil {
		t.Fatal("an unknown function was accepted")
	}
	if !strings.Contains(err.Error(), "percentile") {
		t.Errorf("the refusal should list what is available, got: %v", err)
	}
}

func TestBuildMetric_PercentileNeedsAPercentile(t *testing.T) {
	if _, err := buildMetric("percentile", "took_ms", 0, ""); err == nil {
		t.Fatal("a percentile with no percentile was accepted")
	}
	got, err := buildMetric("percentile", "took_ms", 95, "")
	if err != nil {
		t.Fatalf("percentile 95: %v", err)
	}
	if got.Configuration["percentile"] != 95 {
		t.Errorf("configuration = %v, want the percentile in it", got.Configuration)
	}
}

// Graylog requires at least one metric, and the one somebody means when they
// only said "group by source" is how many there were.
func TestAggregate_DefaultsToACount(t *testing.T) {
	var got aggregateRequest
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"schema":[],"datarows":[],"metadata":{}}`))
	})

	_, err := p.aggregateMessages(context.Background(), aggregateArgs{})
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(got.Metrics) != 1 || got.Metrics[0].Function != "count" {
		t.Errorf("metrics = %+v, want a single count", got.Metrics)
	}
}
