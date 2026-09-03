package threecx

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tool for "what actually happened to that call".
//
// Most phone tickets are a disagreement about reality: the customer says they
// rang and nobody picked up, the client says no call ever arrived, somebody
// says the number goes to the wrong person. A call record settles it --
// whether the call reached the system at all, which extension it was offered
// to, whether it was answered, and for how long.

func (p *Plugin) registerHistoryTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "search_call_history",
		Title: "Search call records",
		Description: "Call records, newest first: who rang, who it reached, " +
			"whether it was answered and for how long. Filter by extension, by " +
			"phone number (partial matches count), by time window, or to " +
			"missed calls only. This is the tool for any question about " +
			"whether a call happened or what became of it. A call that passed " +
			"through several places appears once per leg.",
		Idempotent: true,
	}, p.searchCallHistory)
}

type callHistoryArgs struct {
	Customer   string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Extension  string `json:"extension,omitempty" jsonschema:"only calls involving this extension, either end"`
	Number     string `json:"number,omitempty" jsonschema:"only calls involving a phone number containing these digits, either end"`
	Since      string `json:"since,omitempty" jsonschema:"only calls at or after this time, as 2026-09-01T14:00:00Z or 2026-09-01"`
	Until      string `json:"until,omitempty" jsonschema:"only calls at or before this time"`
	MissedOnly bool   `json:"missed_only,omitempty" jsonschema:"only calls that were never answered"`
	Limit      int    `json:"limit,omitempty" jsonschema:"most records to return; defaults to 50"`
}

// CallRow is one leg of one call.
type CallRow struct {
	At       string `json:"at"`
	Ended    string `json:"ended,omitempty"`
	TalkTime string `json:"talk_time"`
	Answered bool   `json:"answered"`
	// From and To are "who rang" and "who it reached", rather than src and
	// dst, because the direction is what a person is actually reading for.
	From        string `json:"from"`
	FromName    string `json:"from_name,omitempty"`
	FromOutside bool   `json:"from_outside"`
	To          string `json:"to"`
	ToName      string `json:"to_name,omitempty"`
	ToOutside   bool   `json:"to_outside"`
	Direction   string `json:"direction"`
}

// CallHistoryResult is a page of call records.
type CallHistoryResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string    `json:"customer"`
	Calls    []CallRow `json:"calls"`
	Returned int       `json:"returned"`
	Answered int       `json:"answered"`
	Missed   int       `json:"missed"`
	truncation
}

// callFields is what a call record may contain. Deliberately no SrcRecId or
// DstRecId: the recording identifiers lead somewhere a summary has no business
// going.
const callFields = "SegmentId,SegmentStartTime,SegmentEndTime,CallTime,CallAnswered," +
	"SrcDn,SrcDisplayName,SrcCallerNumber,SrcExternal," +
	"DstDn,DstDisplayName,DstCallerNumber,DstExternal"

const defaultCalls = 50

func (p *Plugin) searchCallHistory(ctx context.Context, args callHistoryArgs) (CallHistoryResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return CallHistoryResult{}, err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultCalls
	}
	limit = p.limitOf(limit)

	// Built as OData filters so the PBX does the narrowing. Pulling everything
	// and filtering here would be slower, larger, and would put call records
	// this caller never asked for through the process.
	var filters []string
	if ext := strings.TrimSpace(args.Extension); ext != "" {
		lit := odataString(ext)
		filters = append(filters, fmt.Sprintf("(SrcDn eq %s or DstDn eq %s)", lit, lit))
	}
	if num := strings.TrimSpace(args.Number); num != "" {
		lit := odataString(num)
		filters = append(filters, fmt.Sprintf("(contains(SrcCallerNumber,%s) or contains(DstCallerNumber,%s))", lit, lit))
	}
	// Time bounds. The view refuses a comparison against a timestamp literal
	// with a bare 500 (see docs/3cx.md) but accepts the date() function, so the
	// window is pushed to the PBX at day granularity and the exact bound is
	// applied to what comes back. A caller asking for the last two hours gets
	// today's calls fetched and the earlier ones dropped here.
	since, haveSince, err := parseTime("since", args.Since)
	if err != nil {
		return CallHistoryResult{}, err
	}
	if haveSince {
		filters = append(filters, "date(SegmentStartTime) ge "+since.Format("2006-01-02"))
	}
	until, haveUntil, err := parseTime("until", args.Until)
	if err != nil {
		return CallHistoryResult{}, err
	}
	if haveUntil {
		filters = append(filters, "date(SegmentStartTime) le "+until.Format("2006-01-02"))
	}
	if args.MissedOnly {
		filters = append(filters, "CallAnswered eq false")
	}

	q := url.Values{"$select": {callFields}, "$orderby": {"SegmentStartTime desc"}}
	if len(filters) > 0 {
		q.Set("$filter", strings.Join(filters, " and "))
	}

	type record struct {
		Start     string `json:"SegmentStartTime"`
		End       string `json:"SegmentEndTime"`
		CallTime  string `json:"CallTime"`
		Answered  bool   `json:"CallAnswered"`
		SrcDn     string `json:"SrcDn"`
		SrcName   string `json:"SrcDisplayName"`
		SrcNumber string `json:"SrcCallerNumber"`
		SrcExt    bool   `json:"SrcExternal"`
		DstDn     string `json:"DstDn"`
		DstName   string `json:"DstDisplayName"`
		DstNumber string `json:"DstCallerNumber"`
		DstExt    bool   `json:"DstExternal"`
	}
	got, err := list[record](ctx, acct.client, "CallHistoryView", q, limit)
	if err != nil {
		return CallHistoryResult{}, acct.call(err)
	}

	out := CallHistoryResult{Calls: make([]CallRow, 0, len(got.Rows))}
	for _, c := range got.Rows {
		if start, err := time.Parse(time.RFC3339, c.Start); err == nil {
			if haveSince && start.Before(since) || haveUntil && start.After(until) {
				continue
			}
		}
		row := CallRow{
			At: c.Start, Ended: c.End, TalkTime: isoDuration(c.CallTime), Answered: c.Answered,
			From: firstNonBlank(c.SrcNumber, c.SrcDn), FromName: c.SrcName, FromOutside: c.SrcExt,
			To: firstNonBlank(c.DstNumber, c.DstDn), ToName: c.DstName, ToOutside: c.DstExt,
		}
		switch {
		case c.SrcExt && !c.DstExt:
			row.Direction = "inbound"
		case !c.SrcExt && c.DstExt:
			row.Direction = "outbound"
		case !c.SrcExt && !c.DstExt:
			row.Direction = "internal"
		default:
			row.Direction = "external"
		}
		if c.Answered {
			out.Answered++
		} else {
			out.Missed++
		}
		out.Calls = append(out.Calls, row)
	}
	out.Calls, out.truncation = bound(out.Calls, got.Truncated)
	out.Returned = len(out.Calls)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}
