package graylog

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// Events and event definitions are two halves of one question and are
// deliberately two tools.
//
// An event is something that happened. A definition is the rule that decides
// what counts as something happening. "Why was I not told" is answered by the
// second and not by the first, and an operator asking it has usually already
// looked at the first and found nothing -- which is exactly the case where a
// single tool returning only events reads as "nothing was wrong".

// registerEventTools adds the alerting surface.
func (p *Plugin) registerEventTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "search_events",
		Title: "Search events and alerts",
		Description: "What Graylog decided was worth raising: alerts that " +
			"fired and events that were recorded, newest first, with the rule " +
			"that raised each one named rather than left as an id.\n\n" +
			"Start here for 'is anything wrong' and 'what fired overnight'. " +
			"Never answered from cache. An empty result means nothing was " +
			"raised in the window -- which is not the same as nothing being " +
			"wrong, because a rule that does not exist raises nothing; " +
			"graylog_list_event_definitions says what is actually being watched.",
		Idempotent: true,
	}, p.searchEvents)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_event_definitions",
		Title: "List alert rules",
		Description: "The rules Graylog evaluates: what each one looks for, " +
			"how often, over what window, which streams, and whether it is " +
			"enabled and set to alert. This is the tool for 'what is being " +
			"watched', 'why did nothing fire', and 'is this rule switched on'.",
		Idempotent: true,
	}, p.listEventDefinitions)
}

// --- events ----------------------------------------------------------------

type eventsArgs struct {
	Query       string   `json:"query,omitempty" jsonschema:"narrows the events themselves, e.g. source:web-01; not the log query the rule runs"`
	Definitions []string `json:"event_definition_ids,omitempty" jsonschema:"only events raised by these rules, from graylog_list_event_definitions"`
	Priority    []string `json:"priority,omitempty" jsonschema:"only these priorities: 1 low, 2 normal, 3 high"`
	Alerts      string   `json:"alerts,omitempty" jsonschema:"include (default), only for alerts alone, or exclude to see non-alert events"`
	Limit       int      `json:"limit,omitempty" jsonschema:"most events to return"`
	Page        int      `json:"page,omitempty" jsonschema:"1-based page, for reading past the first limit"`
	SortBy      string   `json:"sort_by,omitempty" jsonschema:"field to sort on; default timestamp"`
	SortOrder   string   `json:"sort_order,omitempty" jsonschema:"asc or desc; default desc, newest first"`
	timeArgs
}

// eventsRequest is the body POST /api/events/search takes.
type eventsRequest struct {
	Page          int          `json:"page"`
	PerPage       int          `json:"per_page"`
	Timerange     timeRange    `json:"timerange"`
	Query         string       `json:"query"`
	Filter        eventsFilter `json:"filter"`
	SortBy        string       `json:"sort_by,omitempty"`
	SortDirection string       `json:"sort_direction,omitempty"`
}

type eventsFilter struct {
	Alerts           string   `json:"alerts,omitempty"`
	EventDefinitions []string `json:"event_definitions,omitempty"`
	Priority         []string `json:"priority,omitempty"`
}

// eventsResponse is what the API answers with. The context is the half that
// makes the events readable: it carries the titles of every definition and
// stream the events reference, so the ids in a row can be resolved without a
// request each.
type eventsResponse struct {
	Events []struct {
		Event eventDTO `json:"event"`
	} `json:"events"`
	TotalEvents int `json:"total_events"`
	Context     struct {
		EventDefinitions map[string]contextEntity `json:"event_definitions"`
		Streams          map[string]contextEntity `json:"streams"`
	} `json:"context"`
}

type contextEntity struct {
	Title string `json:"title"`
}

type eventDTO struct {
	ID                string         `json:"id"`
	Timestamp         string         `json:"timestamp"`
	EventDefinitionID string         `json:"event_definition_id"`
	Priority          int            `json:"priority"`
	Alert             bool           `json:"alert"`
	Message           string         `json:"message"`
	Source            string         `json:"source"`
	Key               string         `json:"key"`
	SourceStreams     []string       `json:"source_streams"`
	TimerangeStart    string         `json:"timerange_start"`
	TimerangeEnd      string         `json:"timerange_end"`
	Fields            map[string]any `json:"fields"`
}

// eventRow is one event as a person would read it.
type eventRow struct {
	ID string `json:"id"`
	At string `json:"timestamp"`
	// Rule is the definition's title, and RuleID is what to pass back to
	// graylog_list_event_definitions. Both, because a title is what a person reads
	// and an id is what the next call takes.
	Rule     string `json:"rule"`
	RuleID   string `json:"rule_id"`
	Alert    bool   `json:"alert"`
	Priority string `json:"priority"`
	Message  string `json:"message,omitempty"`
	Source   string `json:"source,omitempty"`
	Key      string `json:"key,omitempty"`
	// Streams are titles where the response named them and ids where it did
	// not, so a row is never left holding an identifier with no meaning.
	Streams []string `json:"streams,omitempty"`
	// Covered is the window the rule was looking at when it fired, which is
	// not the same as when it fired -- an aggregation rule raises at the end
	// of a window it has been filling for an hour.
	Covered string         `json:"window_evaluated,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

type eventsResult struct {
	Events   []eventRow `json:"events"`
	Returned int        `json:"returned"`
	// Matching is what Graylog said the whole result set was, which is how a
	// caller learns their window held more than they were shown.
	Matching  int    `json:"total_matching"`
	Truncated bool   `json:"truncated,omitempty"`
	Window    string `json:"window_searched"`
	Note      string `json:"note,omitempty"`
}

func (p *Plugin) searchEvents(ctx context.Context, in eventsArgs) (eventsResult, error) {
	if err := p.ready(); err != nil {
		return eventsResult{}, err
	}

	alerts, err := alertsFilter(in.Alerts)
	if err != nil {
		return eventsResult{}, err
	}
	order, err := sortOrder(in.SortOrder)
	if err != nil {
		return eventsResult{}, err
	}
	window, err := p.cfg.resolve(in.timeArgs, p.deps.Now())
	if err != nil {
		return eventsResult{}, err
	}

	limit := in.Limit
	if limit <= 0 || limit > p.cfg.MaxItems {
		limit = p.cfg.MaxItems
	}
	page := in.Page
	if page <= 0 {
		page = 1
	}

	body := eventsRequest{
		Page:      page,
		PerPage:   limit,
		Timerange: window,
		Query:     strings.TrimSpace(in.Query),
		Filter: eventsFilter{
			Alerts:           alerts,
			EventDefinitions: cleanIDs(in.Definitions),
			Priority:         cleanIDs(in.Priority),
		},
		SortBy:        strings.TrimSpace(in.SortBy),
		SortDirection: order,
	}

	raw, err := p.client.Post(ctx, "/events/search", body)
	p.note(err)
	if err != nil {
		return eventsResult{}, err
	}

	var got eventsResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		return eventsResult{}, fmt.Errorf("graylog: the event search answered "+
			"with something that is not the API's result shape: %w", err)
	}

	out := eventsResult{
		Matching: got.TotalEvents,
		Window:   window.describe(),
		Events:   make([]eventRow, 0, len(got.Events)),
	}
	for _, entry := range got.Events {
		if len(out.Events) >= limit {
			out.Truncated = true
			break
		}
		out.Events = append(out.Events, entry.Event.row(got))
	}
	out.Returned = len(out.Events)
	// A page past the first is not truncation, but the caller still needs to
	// know there is more behind it.
	if got.TotalEvents > page*limit {
		out.Truncated = true
	}
	if out.Returned == 0 {
		out.Note = "Nothing was raised in this window. That is not the same as " +
			"nothing being wrong: a condition nobody wrote a rule for raises " +
			"no event. graylog_list_event_definitions says what is being watched."
	}
	return out, nil
}

// row renders one event, resolving the ids it carries against the response's
// own context.
func (e eventDTO) row(in eventsResponse) eventRow {
	rule := in.Context.EventDefinitions[e.EventDefinitionID].Title
	if rule == "" {
		// A definition that has since been deleted leaves events behind that
		// name it. Better to say so than to render an empty title beside a
		// real id.
		rule = "(rule no longer exists)"
	}

	streams := make([]string, 0, len(e.SourceStreams))
	for _, id := range e.SourceStreams {
		if title := in.Context.Streams[id].Title; title != "" {
			streams = append(streams, title)
			continue
		}
		streams = append(streams, id)
	}

	var covered string
	if e.TimerangeStart != "" && e.TimerangeEnd != "" {
		covered = e.TimerangeStart + " to " + e.TimerangeEnd
	}

	return eventRow{
		ID:       e.ID,
		At:       e.Timestamp,
		Rule:     rule,
		RuleID:   e.EventDefinitionID,
		Alert:    e.Alert,
		Priority: priorityName(e.Priority),
		Message:  e.Message,
		Source:   e.Source,
		Key:      e.Key,
		Streams:  streams,
		Covered:  covered,
		Fields:   e.Fields,
	}
}

// priorityName turns Graylog's number into the word its own UI shows.
//
// An unrecognised number is rendered as itself rather than mapped to the
// nearest word: a priority this does not know about is a fact worth passing
// through, and inventing a label for it would hide a version difference behind
// a plausible answer.
func priorityName(p int) string {
	switch p {
	case 1:
		return "low"
	case 2:
		return "normal"
	case 3:
		return "high"
	case 0:
		return ""
	}
	return strconv.Itoa(p)
}

// alertsFilter normalises the three values Graylog's filter takes.
func alertsFilter(in string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(in)) {
	case "":
		return "", nil
	case "include", "all":
		return "include", nil
	case "only", "alerts":
		return "only", nil
	case "exclude", "none":
		return "exclude", nil
	}
	return "", fmt.Errorf("graylog: alerts is %q; it is include, only, or exclude", in)
}

// --- event definitions -----------------------------------------------------

type definitionsArgs struct {
	Query string `json:"query,omitempty" jsonschema:"narrows by title or description"`
	Limit int    `json:"limit,omitempty" jsonschema:"most rules to return"`
	Page  int    `json:"page,omitempty" jsonschema:"1-based page"`
}

// definitionsResponse is the paginated listing GET /api/events/definitions
// answers with.
type definitionsResponse struct {
	Total       int               `json:"total"`
	Definitions []eventDefinition `json:"event_definitions"`
}

type eventDefinition struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	Alert       bool   `json:"alert"`
	State       string `json:"state"`
	Config      struct {
		Type           string   `json:"type"`
		Query          string   `json:"query"`
		Streams        []string `json:"streams"`
		SearchWithinMs int64    `json:"search_within_ms"`
		ExecuteEveryMs int64    `json:"execute_every_ms"`
	} `json:"config"`
	Notifications []struct {
		NotificationID string `json:"notification_id"`
	} `json:"notifications"`
}

// definitionRow is one rule flattened into the shape of the question people
// ask about it.
type definitionRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	// Enabled and Alerting are separate facts and are kept separate. A rule
	// can be running and recording events while notifying nobody, which is
	// the shape "why did nobody get told" usually turns out to have.
	Enabled  bool   `json:"enabled"`
	Alerting bool   `json:"raises_alert"`
	Priority string `json:"priority,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Query    string `json:"query,omitempty"`
	// StreamIDs rather than titles: this listing does not carry a context to
	// resolve them against, and inventing a request per rule to fill it in
	// would be one request per row. graylog_list_streams resolves them in one.
	StreamIDs []string `json:"stream_ids,omitempty"`
	Window    string   `json:"searches_over,omitempty"`
	Every     string   `json:"runs_every,omitempty"`
	// Notifications is a count rather than a list. Which notification is a
	// separate lookup; whether there is one at all is the fact that answers
	// the question, and zero here is the whole explanation for a rule that
	// fired and reached nobody.
	Notifications int `json:"notifications"`
}

type definitionsResult struct {
	Rules     []definitionRow `json:"rules"`
	Returned  int             `json:"returned"`
	Matching  int             `json:"total_matching"`
	Truncated bool            `json:"truncated,omitempty"`
	Note      string          `json:"note,omitempty"`
}

func (p *Plugin) listEventDefinitions(ctx context.Context, in definitionsArgs) (definitionsResult, error) {
	if err := p.ready(); err != nil {
		return definitionsResult{}, err
	}

	limit := in.Limit
	if limit <= 0 || limit > p.cfg.MaxItems {
		limit = p.cfg.MaxItems
	}
	page := in.Page
	if page <= 0 {
		page = 1
	}

	params := url.Values{}
	params.Set("page", strconv.Itoa(page))
	params.Set("per_page", strconv.Itoa(limit))
	if q := strings.TrimSpace(in.Query); q != "" {
		params.Set("query", q)
	}

	raw, err := p.client.Get(ctx, "/events/definitions", params)
	p.note(err)
	if err != nil {
		return definitionsResult{}, err
	}

	var got definitionsResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		return definitionsResult{}, fmt.Errorf("graylog: the event definition "+
			"listing answered with something unexpected: %w", err)
	}

	out := definitionsResult{
		Matching: got.Total,
		Rules:    make([]definitionRow, 0, len(got.Definitions)),
	}
	var disabled, silent int
	for _, def := range got.Definitions {
		if len(out.Rules) >= limit {
			out.Truncated = true
			break
		}
		row := def.row()
		if !row.Enabled {
			disabled++
		}
		if row.Enabled && row.Notifications == 0 {
			silent++
		}
		out.Rules = append(out.Rules, row)
	}
	out.Returned = len(out.Rules)
	if got.Total > page*limit {
		out.Truncated = true
	}

	// Two counts worth stating rather than leaving to be derived from the
	// rows, because they are the answer to the question this tool is usually
	// reached for and a model summarising twenty rules will not reliably
	// notice either.
	if disabled > 0 {
		out.Note = join(out.Note, fmt.Sprintf(
			"%d of these rules are disabled and are evaluating nothing.", disabled))
	}
	if silent > 0 {
		out.Note = join(out.Note, fmt.Sprintf(
			"%d are enabled but have no notification attached, so they record "+
				"events without telling anybody.", silent))
	}
	return out, nil
}

func (d eventDefinition) row() definitionRow {
	return definitionRow{
		ID:            d.ID,
		Title:         d.Title,
		Description:   d.Description,
		Enabled:       !strings.EqualFold(d.State, "DISABLED"),
		Alerting:      d.Alert,
		Priority:      priorityName(d.Priority),
		Kind:          d.Config.Type,
		Query:         d.Config.Query,
		StreamIDs:     d.Config.Streams,
		Window:        humanMillis(d.Config.SearchWithinMs),
		Every:         humanMillis(d.Config.ExecuteEveryMs),
		Notifications: len(d.Notifications),
	}
}

// humanMillis renders a rule's window the way its own form asks for it.
func humanMillis(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return humanSeconds(int(ms / 1000))
}
