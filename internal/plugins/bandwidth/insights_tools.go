package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Insights: what the traffic adds up to, rather than what it was.

// voiceMetrics are the aggregates Bandwidth computes over voice traffic.
//
// The key is what a caller asks for and the value is the path segment. Two
// names for one thing, because the API's segments are readable but a model
// choosing between them benefits from being told the set exists rather than
// having to know it.
var voiceMetrics = map[string]string{
	"minutes":          "minutes-of-use",
	"completed":        "completed-calls",
	"failed":           "failed-calls",
	"connection_rate":  "connection-rates",
	"average_duration": "average-durations",
}

// metricNames is the set, in a fixed order, for a message that lists them.
var metricNames = []string{
	"minutes", "completed", "failed", "connection_rate", "average_duration",
}

// insightsHistory is how far back the Insights API keeps anything.
//
// Named because the failure it prevents is confusing: a window beyond it comes
// back empty rather than refused, which reads as "no traffic" instead of "not
// kept".
const insightsHistory = 365 * 24 * time.Hour

func (p *Plugin) registerInsightsTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "aggregate_calls",
		Title: "Aggregate voice traffic",
		Description: "What the voice traffic adds up to over a window, sliced " +
			"by time: minutes of use, completed and failed call counts, " +
			"connection rates, average durations. Use this for “how much” and " +
			"“is it getting worse” — list_calls returns individual calls and " +
			"cannot answer either. Narrow by number, direction or call type. " +
			"Bandwidth keeps one year; a window reaching further back comes " +
			"back empty rather than refused.",
		Idempotent: true,
	}, p.aggregateCalls)
}

// AggregateCallsInput selects the aggregate and the window.
type AggregateCallsInput struct {
	Account    string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	Metric     string `json:"metric" jsonschema:"which aggregate: minutes completed failed connection_rate or average_duration"`
	Since      string `json:"since,omitempty" jsonschema:"start of the window, as an ISO-8601 instant such as 2026-08-01T00:00:00Z"`
	Until      string `json:"until,omitempty" jsonschema:"end of the window, as an ISO-8601 instant"`
	To         string `json:"to,omitempty" jsonschema:"only traffic to this number, in E.164"`
	From       string `json:"from,omitempty" jsonschema:"only traffic from this number, in E.164"`
	Direction  string `json:"direction,omitempty" jsonschema:"INBOUND or OUTBOUND"`
	CallType   string `json:"call_type,omitempty" jsonschema:"LOCAL INTERSTATE INTRASTATE INTERNATIONAL TOLLFREE_IN TOLLFREE_OUT EMERGENCY OPERATOR INFORMATION or UNDETERMINED"`
	SubAccount string `json:"sub_account,omitempty" jsonschema:"only traffic on this sub-account"`
}

// AggregateOutput is one aggregate over a window.
type AggregateOutput struct {
	Metric string `json:"metric"`
	// Series is what the API returned, a slice per time bucket. Passed through
	// rather than modelled: the fields differ per metric, and a struct able to
	// hold all of them would be mostly empty whichever one was asked for.
	Series []Record `json:"series"`
	Note   string   `json:"note,omitempty"`
}

func (p *Plugin) aggregateCalls(ctx context.Context, in AggregateCallsInput) (AggregateOutput, error) {
	if err := p.ready(); err != nil {
		return AggregateOutput{}, err
	}
	segment, ok := voiceMetrics[strings.ToLower(strings.TrimSpace(in.Metric))]
	if !ok {
		return AggregateOutput{}, fmt.Errorf("bandwidth: %q is not an aggregate "+
			"this reads. Ask for one of: %s", in.Metric,
			strings.Join(metricNames, ", "))
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return AggregateOutput{}, err
	}

	// The Insights API takes its filters deepObject-style -- the comparison is
	// part of the parameter name rather than the value. Every one here is an
	// equality except the window, which is a pair of bounds.
	q := url.Values{}
	q.Set("accountId[eq]", account)
	set(q, "toPhoneNumber[eq]", in.To)
	set(q, "fromPhoneNumber[eq]", in.From)
	set(q, "direction[eq]", strings.ToUpper(in.Direction))
	// The enum is the underscore form. A caller writing TOLLFREE-OUT, which is
	// how Bandwidth's own prose spells it, is asking for the right thing in
	// the wrong dialect and should not be punished for it.
	set(q, "callType[eq]", strings.ReplaceAll(strings.ToUpper(in.CallType), "-", "_"))
	set(q, "subAccount[eq]", in.SubAccount)
	set(q, "timestamp[gte]", in.Since)
	set(q, "timestamp[lte]", in.Until)

	out := AggregateOutput{Metric: in.Metric}
	// Said before the call rather than after an empty answer, because an empty
	// answer is what the API gives for a window it does not keep, and "no
	// traffic" and "not kept" are different facts a caller cannot tell apart.
	if note := historyNote(in.Since, p.deps.Now()); note != "" {
		out.Note = note
	}

	// Insights answers in a {links, data, errors} envelope rather than with a
	// bare array. Decoding straight into a slice would have failed on every
	// successful response as well as every failed one; the 500 that exposed
	// this showed the envelope in its own error body.
	var env struct {
		Data any `json:"data"`
	}
	err = p.client.get(ctx, hostInsights, "/v1/monitors/voice/"+segment, q, &env)
	p.note(err, nil)
	if err != nil {
		return AggregateOutput{}, err
	}
	out.Series = seriesOf(env.Data)
	return out, nil
}

// seriesOf normalises the envelope's data into rows.
//
// The shape varies by monitor -- a list of buckets for most, a single object
// for an aggregate over one slice -- so it is taken as it comes and wrapped
// where it is not already a list.
func seriesOf(data any) []Record {
	switch v := data.(type) {
	case []any:
		out := make([]Record, 0, len(v))
		for _, item := range v {
			if rec, ok := item.(map[string]any); ok {
				out = append(out, rec)
			}
		}
		return out
	case map[string]any:
		return []Record{v}
	}
	return nil
}

// historyNote warns when a window reaches past what Insights keeps.
func historyNote(since string, now time.Time) string {
	if since == "" {
		return ""
	}
	from, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return ""
	}
	if now.Sub(from) <= insightsHistory {
		return ""
	}
	return "the window starts more than a year ago, and Insights keeps one " +
		"year: anything before that is absent rather than zero"
}
