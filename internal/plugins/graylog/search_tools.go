package graylog

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// defaultMessageFields is what a search returns when the caller names no
// fields.
//
// Three, deliberately. Graylog stores whatever an application logged, which on
// a structured pipeline is dozens of fields per message, and returning all of
// them by default would make the cheapest question the most expensive answer.
// These three are what a person reads first, and graylog_list_message_fields exists so a
// caller can find out what else there is to ask for.
var defaultMessageFields = []string{"timestamp", "source", "message"}

// registerSearchTools adds the two tools this integration exists for.
func (p *Plugin) registerSearchTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "search_messages",
		Title: "Search log messages",
		Description: "Reads log messages matching a Graylog query. This is the " +
			"tool for 'what does the log say' -- errors around an incident, " +
			"what one host emitted, whether a request id appears at all.\n\n" +
			"The result is columnar: `columns` names each position and every " +
			"row is a list of values in that order, not an object. Read " +
			"rows[i][j] as columns[j].\n\n" +
			"Query syntax is Lucene: `level:ERROR`, `source:web-01 AND " +
			"status:500`, `\"connection refused\"`, `message:timeout*`. An " +
			"empty query matches everything in the window.\n\n" +
			"Always searches a bounded window; without one it uses the " +
			"installation's default, and the window it actually searched is " +
			"in every result. Name streams to narrow it -- a search across " +
			"every index is slow for everybody using that cluster. Use " +
			"graylog_aggregate_messages instead when the question is a count, a rate " +
			"or a top-N; pulling messages back to count them here wastes both " +
			"ends.",
		Idempotent: true,
	}, p.searchMessages)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "aggregate_messages",
		Title: "Count and summarise log messages",
		Description: "Groups and measures log messages without returning them: " +
			"errors per host, the busiest endpoints, a rate over time, the " +
			"95th percentile of a duration field.\n\n" +
			"Ask for this rather than graylog_search_messages whenever the answer is a " +
			"number or a ranking. It is computed on the cluster over every " +
			"matching message, so it is both cheaper and *more correct* than " +
			"counting a truncated page of results.\n\n" +
			"group_by names the fields to break the answer down by, outermost " +
			"first; metrics names what to measure in each group. A metric of " +
			"`count` with no field counts messages. Group by `timestamp` with " +
			"a timeunit to get a series over time.\n\n" +
			"The result is columnar: `columns` names each position and every " +
			"row is a list of values in that order.",
		Idempotent: true,
	}, p.aggregateMessages)
}

// --- search ----------------------------------------------------------------

type searchArgs struct {
	Query     string   `json:"query,omitempty" jsonschema:"Lucene query, e.g. level:ERROR AND source:web-01; empty matches everything in the window"`
	Streams   []string `json:"stream_ids,omitempty" jsonschema:"limit to these stream ids, from graylog_list_streams; strongly preferred, it is what stops a search scanning every index"`
	Fields    []string `json:"fields,omitempty" jsonschema:"message fields to return, in order; default timestamp, source, message. graylog_list_message_fields lists what exists"`
	Limit     int      `json:"limit,omitempty" jsonschema:"most messages to return"`
	Offset    int      `json:"offset,omitempty" jsonschema:"skip this many matches before returning any; for paging through a result"`
	SortField string   `json:"sort_by,omitempty" jsonschema:"field to sort on; default timestamp"`
	SortOrder string   `json:"sort_order,omitempty" jsonschema:"asc or desc; default desc, newest first"`
	timeArgs
}

// messagesRequest is the body POST /api/search/messages takes.
type messagesRequest struct {
	Query     string    `json:"query"`
	Streams   []string  `json:"streams,omitempty"`
	Fields    []string  `json:"fields"`
	From      int       `json:"from,omitempty"`
	Size      int       `json:"size"`
	Timerange timeRange `json:"timerange"`
	Sort      string    `json:"sort,omitempty"`
	SortOrder string    `json:"sort_order,omitempty"`
}

func (p *Plugin) searchMessages(ctx context.Context, in searchArgs) (tableResult, error) {
	if err := p.ready(); err != nil {
		return tableResult{}, err
	}

	order, err := sortOrder(in.SortOrder)
	if err != nil {
		return tableResult{}, err
	}
	if in.Offset < 0 {
		return tableResult{}, fmt.Errorf("graylog: offset cannot be negative, got %d", in.Offset)
	}
	window, err := p.cfg.resolve(in.timeArgs, p.deps.Now())
	if err != nil {
		return tableResult{}, err
	}

	limit := in.Limit
	if limit <= 0 || limit > p.cfg.MaxMessages {
		limit = p.cfg.MaxMessages
	}
	fields := in.Fields
	if len(fields) == 0 {
		fields = defaultMessageFields
	}

	body := messagesRequest{
		Query:     strings.TrimSpace(in.Query),
		Streams:   cleanIDs(in.Streams),
		Fields:    fields,
		From:      in.Offset,
		Size:      limit,
		Timerange: window,
		Sort:      strings.TrimSpace(in.SortField),
		SortOrder: order,
	}

	raw, err := p.client.Post(ctx, "/search/messages", body)
	p.note(err)
	if err != nil {
		return tableResult{}, err
	}

	// The limit is enforced again on the way out even though it was sent
	// upstream. Belt and braces is not the reason: the row budget in
	// decodeTable is the ceiling that Size cannot express, because Graylog is
	// counting messages and the thing that has to be bounded is characters.
	out, err := decodeTable(raw, limit, window)
	if err != nil {
		return tableResult{}, err
	}
	if len(body.Streams) == 0 {
		out.Note = join(out.Note, "This searched every stream the credential "+
			"can see. Naming stream_ids makes it faster and the answer easier "+
			"to trust.")
	}
	return out, nil
}

// --- aggregate -------------------------------------------------------------

type aggregateArgs struct {
	Query   string   `json:"query,omitempty" jsonschema:"Lucene query narrowing what is counted; empty counts everything in the window"`
	Streams []string `json:"stream_ids,omitempty" jsonschema:"limit to these stream ids, from graylog_list_streams"`
	GroupBy []struct {
		Field    string `json:"field" jsonschema:"field to break the answer down by"`
		Limit    int    `json:"limit,omitempty" jsonschema:"most distinct values of this field to return"`
		TimeUnit string `json:"timeunit,omitempty" jsonschema:"bucket size when grouping a date field, e.g. 1h, 5m, 1d"`
	} `json:"group_by,omitempty" jsonschema:"fields to group by, outermost first; empty measures the whole result as one group"`
	Metrics []struct {
		Function   string `json:"function" jsonschema:"count, avg, min, max, sum, latest, stddev, variance, percentile, card"`
		Field      string `json:"field,omitempty" jsonschema:"field to measure; required for everything except count"`
		Percentile int    `json:"percentile,omitempty" jsonschema:"which percentile, 1-99, when function is percentile"`
		Sort       string `json:"sort,omitempty" jsonschema:"sort the rows by this metric: asc or desc"`
	} `json:"metrics,omitempty" jsonschema:"what to measure in each group; default is a message count"`
	Limit int `json:"limit,omitempty" jsonschema:"most rows to return"`
	timeArgs
}

// aggregateRequest is the body POST /api/search/aggregate takes.
type aggregateRequest struct {
	Query     string        `json:"query"`
	Streams   []string      `json:"streams,omitempty"`
	Timerange timeRange     `json:"timerange"`
	GroupBy   []groupBySpec `json:"group_by,omitempty"`
	Metrics   []metricSpec  `json:"metrics"`
}

type groupBySpec struct {
	Field    string `json:"field"`
	Limit    int    `json:"limit,omitempty"`
	TimeUnit string `json:"timeunit,omitempty"`
}

type metricSpec struct {
	Function      string         `json:"function"`
	Field         string         `json:"field,omitempty"`
	Sort          string         `json:"sort,omitempty"`
	Configuration map[string]any `json:"configuration,omitempty"`
}

// aggregations are the functions Graylog computes. Named here so a typo is
// refused with the list rather than passed upstream to come back as a 400 that
// does not say what was wrong with it.
//
// Everything but count needs a field: an average of nothing is not a smaller
// question, it is one with no answer.
var aggregations = map[string]bool{
	"count": true, "avg": true, "min": true, "max": true, "sum": true,
	"latest": true, "stddev": true, "variance": true, "percentile": true,
	"card": true, "sumofsquares": true,
}

// aliases are the spellings somebody reasonably writes for a function Graylog
// names differently. A model asked for an average will write "average" about
// as often as "avg", and a round trip to be told about three letters is a
// round trip wasted.
var aliases = map[string]string{
	"average":        "avg",
	"mean":           "avg",
	"stddev":         "stddev",
	"std_dev":        "stddev",
	"cardinality":    "card",
	"distinct":       "card",
	"unique":         "card",
	"sum_of_squares": "sumofsquares",
	"p":              "percentile",
}

func (p *Plugin) aggregateMessages(ctx context.Context, in aggregateArgs) (tableResult, error) {
	if err := p.ready(); err != nil {
		return tableResult{}, err
	}

	window, err := p.cfg.resolve(in.timeArgs, p.deps.Now())
	if err != nil {
		return tableResult{}, err
	}
	limit := in.Limit
	if limit <= 0 || limit > p.cfg.MaxItems {
		limit = p.cfg.MaxItems
	}

	groups := make([]groupBySpec, 0, len(in.GroupBy))
	for _, g := range in.GroupBy {
		field := strings.TrimSpace(g.Field)
		if field == "" {
			return tableResult{}, fmt.Errorf("graylog: a group_by entry names no field")
		}
		if g.Limit < 0 {
			return tableResult{}, fmt.Errorf("graylog: group_by %s has a negative limit", field)
		}
		groups = append(groups, groupBySpec{
			Field:    field,
			Limit:    g.Limit,
			TimeUnit: strings.TrimSpace(g.TimeUnit),
		})
	}

	metrics := make([]metricSpec, 0, len(in.Metrics))
	for _, m := range in.Metrics {
		spec, err := buildMetric(m.Function, m.Field, m.Percentile, m.Sort)
		if err != nil {
			return tableResult{}, err
		}
		metrics = append(metrics, spec)
	}
	if len(metrics) == 0 {
		// Graylog requires at least one metric, and the one somebody means
		// when they only said "group by source" is how many there were.
		metrics = append(metrics, metricSpec{Function: "count"})
	}

	body := aggregateRequest{
		Query:     strings.TrimSpace(in.Query),
		Streams:   cleanIDs(in.Streams),
		Timerange: window,
		GroupBy:   groups,
		Metrics:   metrics,
	}

	raw, err := p.client.Post(ctx, "/search/aggregate", body)
	p.note(err)
	if err != nil {
		return tableResult{}, err
	}
	return decodeTable(raw, limit, window)
}

// buildMetric validates one measurement and renders it the way the API wants.
func buildMetric(function, field string, percentile int, sort string) (metricSpec, error) {
	name := strings.ToLower(strings.TrimSpace(function))
	if name == "" {
		return metricSpec{}, fmt.Errorf("graylog: a metric names no function; "+
			"it is one of %s", knownAggregations())
	}
	if canonical, ok := aliases[name]; ok {
		name = canonical
	}
	if !aggregations[name] {
		return metricSpec{}, fmt.Errorf("graylog: %q is not a function this "+
			"understands; it is one of %s", function, knownAggregations())
	}

	field = strings.TrimSpace(field)
	if name == "count" {
		// count over a named field counts messages *having* that field, which
		// is a legitimate and different question, so it is passed through
		// rather than refused.
	} else if field == "" {
		return metricSpec{}, fmt.Errorf("graylog: %s needs a field to measure; "+
			"only count works without one", name)
	}

	order, err := sortOrder(sort)
	if err != nil {
		return metricSpec{}, err
	}

	spec := metricSpec{Function: name, Field: field, Sort: order}
	if name == "percentile" {
		if percentile < 1 || percentile > 99 {
			return metricSpec{}, fmt.Errorf("graylog: percentile needs a "+
				"percentile between 1 and 99, got %d", percentile)
		}
		spec.Configuration = map[string]any{"percentile": percentile}
	}
	return spec, nil
}

func knownAggregations() string {
	names := make([]string, 0, len(aggregations))
	for name := range aggregations {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// --- shared ----------------------------------------------------------------

// sortOrder normalises a direction, refusing anything else.
//
// Empty is left empty rather than defaulted, so the API's own default applies
// and this package is not quietly asserting an ordering it did not choose.
func sortOrder(in string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "":
		return "", nil
	case "asc", "ascending":
		return "asc", nil
	case "desc", "descending":
		return "desc", nil
	}
	return "", fmt.Errorf("graylog: sort order is %q; it is asc or desc", in)
}

// cleanIDs drops the empty entries a model produces when it builds a list from
// something that was not there, so an empty string does not become a stream
// filter matching nothing.
func cleanIDs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, id := range in {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// join appends a sentence to a note that may already have one.
func join(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + " " + add
}
