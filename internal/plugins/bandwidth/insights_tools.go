package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
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
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "search_call_events",
		Title: "Search individual calls in Insights",
		Description: "Individual calls as Insights recorded them, with the " +
			"detail the voice API does not carry: the SIP response code the " +
			"call ended on, which side hung up, the call type and result.\n\n" +
			"This is the tool for one bad call. list_calls says a call happened " +
			"and how it was set up; this says how it ended and who ended it, " +
			"which is what separates a customer hanging up from a carrier " +
			"rejecting the call. Narrow by either number, by result, or by SIP " +
			"response code when somebody has already quoted one.\n\n" +
			"Insights keeps roughly a year. Older than that returns nothing, " +
			"which is not the same as the call not having happened.",
		Idempotent: true,
	}, p.searchCallEvents)

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

// CallEventsInput narrows a search of individual call records.
//
// A curated subset of what the endpoint accepts. It also takes hangUpSource,
// callType, subAccount and several more; every parameter is paid for in the
// tool list of every conversation whether it is used or not, so these are the
// ones somebody troubleshooting a call actually reaches for.
type CallEventsInput struct {
	Account     string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	CallID      string `json:"call_id,omitempty" jsonschema:"one call by its id"`
	FromNumber  string `json:"from_number,omitempty" jsonschema:"the calling number, in 10-digit or E.164 form"`
	ToNumber    string `json:"to_number,omitempty" jsonschema:"the called number, in 10-digit or E.164 form"`
	Direction   string `json:"direction,omitempty" jsonschema:"inbound or outbound"`
	Result      string `json:"result,omitempty" jsonschema:"how the call ended, such as completed or failed"`
	SIPCode     string `json:"sip_response_code,omitempty" jsonschema:"the SIP response the call ended on, such as 486 or 503"`
	StartedIn   string `json:"started_after,omitempty" jsonschema:"earliest call start, RFC 3339 such as 2026-09-01T00:00:00Z"`
	StartedTill string `json:"started_before,omitempty" jsonschema:"latest call start, RFC 3339"`
	Limit       int    `json:"limit,omitempty" jsonschema:"most calls to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) searchCallEvents(ctx context.Context, in CallEventsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	limit := p.client.limit(in.Limit)

	q := url.Values{}
	q.Set("accountId", account)
	set(q, "callId", in.CallID)
	set(q, "callingNumber", in.FromNumber)
	set(q, "calledNumber", in.ToNumber)
	set(q, "callDirection", in.Direction)
	set(q, "callResult", in.Result)
	set(q, "sipResponseCode", in.SIPCode)
	// Insights will not take a bare timestamp on a time field: it requires a
	// comparison operator on the front, and answers a plain RFC 3339 value with
	// "startTime must use one of the following filter operators: lt, lte, gt,
	// gte." So the operator is supplied here rather than being something every
	// caller has to know -- started_after means gte and started_before means
	// lte, which is what those words mean.
	//
	// Both narrow startTime rather than one narrowing endTime: the caller asked
	// when the call *started*, and endTime is when it finished, which is a
	// different question and would quietly exclude a long call still in flight
	// at the window's edge.
	addFilter(q, "startTime", "gte", in.StartedIn)
	addFilter(q, "startTime", "lte", in.StartedTill)
	// "limit", not "pageSize". Insights rejects an unknown parameter outright
	// rather than ignoring it, so a wrong guess here fails the whole call --
	// which is better than being silently unpaginated, and is how this was
	// caught on the first live request.
	q.Set("limit", strconv.Itoa(limit))

	// Decoded loosely: this endpoint is JSON and its envelope is not documented
	// alongside the parameters, so the payload is found rather than assumed.
	var body map[string]any
	if err := p.client.get(ctx, hostInsights, "/api/v1/voice/calls", q, &body); err != nil {
		p.note(err, nil)
		return Listing{}, err
	}
	p.note(nil, nil)

	items := jsonRecords(body)
	out := capped(items, limit)
	if len(items) == 0 {
		// An empty result here has three causes and the response cannot tell
		// them apart. The third is the one worth naming: unlike the Dashboard,
		// which refuses an unauthorised read with a 403, Insights appears to
		// answer with an empty set -- so "no calls" and "this credential cannot
		// see calls" arrive identically. Reported as unknown rather than as
		// nothing, because a model shown an empty list will say the calls did
		// not happen.
		out.Note = "no calls matched, which may not mean there were none. " +
			"Insights keeps about a year, so an older window is empty rather " +
			"than absent; a number is matched as Bandwidth stored it, so try " +
			"the other of 10-digit and E.164; and a credential without voice " +
			"rights appears to receive an empty set here rather than a refusal. " +
			"If list_calls on the same account answers 403, this emptiness is " +
			"probably that."
	}
	return out, nil
}

// addFilter adds a comparison-filtered query value, leaving one that already
// carries an operator alone.
//
// A caller who knows the API may pass "gt:2026-01-01T00:00:00Z" deliberately,
// and prefixing that again would produce "gte:gt:..." and a refusal that reads
// like their timestamp was malformed.
func addFilter(q url.Values, key, op, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	for _, known := range []string{"lt:", "lte:", "gt:", "gte:"} {
		if strings.HasPrefix(value, known) {
			q.Add(key, value)
			return
		}
	}
	q.Add(key, op+":"+value)
}

// jsonRecords finds the list of records in a JSON envelope whose shape is not
// documented.
//
// The same problem the XML side solves with collect, and nearly the same
// answer -- with one correction learned the hard way. An earlier version fell
// through to "any array of objects at the top level" when it did not recognise
// a payload key, and on the first live call that found the envelope's own
// "links" array and returned a page link as though it were a call. One record,
// two fields, href and rel: a result that is not empty and not right, which is
// the worst of the three available outcomes.
//
// So a payload key that is present decides the answer even when it is null --
// null data means no calls, not "keep looking" -- and the envelope's own
// bookkeeping is never a candidate.
func jsonRecords(body map[string]any) []Record {
	for _, key := range []string{"data", "calls", "results", "items", "content"} {
		raw, ok := body[key]
		if !ok {
			continue
		}
		if list, ok := raw.([]any); ok {
			return asRecords(list)
		}
		// Present but not a list -- null, most often, which is how this API
		// spells an empty result. That is an answer.
		return nil
	}
	// No key this recognises. Any array of objects will do, except the parts
	// of the envelope that are never the payload.
	for key, v := range body {
		switch key {
		case "links", "errors", "error", "page", "meta":
			continue
		}
		if list, ok := v.([]any); ok {
			if recs := asRecords(list); len(recs) > 0 {
				return recs
			}
		}
	}
	return nil
}

func asRecords(list []any) []Record {
	out := make([]Record, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, Record(m))
		}
	}
	return out
}
