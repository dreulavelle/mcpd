package graylog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const eventsBody = `{
	"events":[{"event":{
		"id":"01H","timestamp":"2026-08-26T11:00:00.000Z",
		"event_definition_id":"def1","priority":3,"alert":true,
		"message":"Error rate above threshold","source":"graylog-01",
		"source_streams":["s1","s-gone"],
		"timerange_start":"2026-08-26T10:00:00.000Z",
		"timerange_end":"2026-08-26T11:00:00.000Z",
		"fields":{"count":"412"}}}],
	"total_events":1,
	"context":{
		"event_definitions":{"def1":{"id":"def1","title":"Error rate"}},
		"streams":{"s1":{"id":"s1","title":"Application logs"}}}}`

// An event carries ids: the rule that raised it and the streams it came from.
// Rendered as ids they are unreadable, and resolving them with a request each
// would be one call per row. The response's own context carries the titles, so
// they are resolved from it.
func TestEvents_ResolvesIdsFromTheResponseContext(t *testing.T) {
	p := toolPlugin(t, jsonOK(eventsBody))

	got, err := p.searchEvents(context.Background(), eventsArgs{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(got.Events))
	}
	row := got.Events[0]
	if row.Rule != "Error rate" {
		t.Errorf("rule = %q, want the title from the context", row.Rule)
	}
	// And the id is kept beside it, because a title is what a person reads
	// and an id is what the next call takes.
	if row.RuleID != "def1" {
		t.Errorf("rule_id = %q, want it kept alongside the title", row.RuleID)
	}
	if len(row.Streams) != 2 || row.Streams[0] != "Application logs" {
		t.Errorf("streams = %v, want the resolved title first", row.Streams)
	}
	// A stream the context did not name still gets a value. A row holding an
	// identifier with no meaning is worse than one holding the identifier.
	if row.Streams[1] != "s-gone" {
		t.Errorf("an unresolvable stream became %q, want the id itself", row.Streams[1])
	}
	if row.Priority != "high" {
		t.Errorf("priority = %q, want the word its own UI shows", row.Priority)
	}
	// When the rule was evaluating is not when it fired: an aggregation rule
	// raises at the end of a window it has been filling for an hour.
	if !strings.Contains(row.Covered, "10:00:00") {
		t.Errorf("window_evaluated = %q, want the window the rule looked at", row.Covered)
	}
}

// A definition that has since been deleted leaves events behind that name it.
// Better to say so than to render an empty title beside a real id.
func TestEvents_SaysWhenTheRuleIsGone(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{"events":[{"event":{"id":"1","event_definition_id":"gone"}}],
		"total_events":1,"context":{"event_definitions":{},"streams":{}}}`))

	got, err := p.searchEvents(context.Background(), eventsArgs{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !strings.Contains(got.Events[0].Rule, "no longer exists") {
		t.Errorf("rule = %q, want it to say the definition is gone", got.Events[0].Rule)
	}
}

// An empty event list is not an all-clear. A condition nobody wrote a rule for
// raises no event, and a model reading "no events" as "nothing is wrong" is
// the mistake this note exists to stop.
func TestEvents_EmptyIsNotAnAllClear(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{"events":[],"total_events":0,"context":{}}`))

	got, err := p.searchEvents(context.Background(), eventsArgs{})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if !strings.Contains(got.Note, "event_definitions") {
		t.Errorf("an empty result should point at what is being watched: %q", got.Note)
	}
}

func TestEvents_RefusesAnUnknownAlertsFilter(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{}`))
	if _, err := p.searchEvents(context.Background(), eventsArgs{Alerts: "maybe"}); err == nil {
		t.Fatal("an unknown alerts filter was accepted")
	}
}

// The events request has to carry the filter it was given. A dropped filter
// here is silent: the API answers happily with everything.
func TestEvents_SendsTheFilter(t *testing.T) {
	var got eventsRequest
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(`{"events":[],"total_events":0,"context":{}}`))
	})

	_, err := p.searchEvents(context.Background(), eventsArgs{
		Alerts:      "only",
		Definitions: []string{"def1"},
		Priority:    []string{"3"},
		Limit:       25,
		Page:        2,
	})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if got.Filter.Alerts != "only" {
		t.Errorf("alerts = %q, want only", got.Filter.Alerts)
	}
	if len(got.Filter.EventDefinitions) != 1 || got.Filter.Priority[0] != "3" {
		t.Errorf("filter = %+v, want the definitions and priority passed through", got.Filter)
	}
	if got.PerPage != 25 || got.Page != 2 {
		t.Errorf("paging = %d/%d, want 25 per page and page 2", got.PerPage, got.Page)
	}
}

const definitionsBody = `{"total":3,"event_definitions":[
	{"id":"d1","title":"Error rate","state":"ENABLED","alert":true,"priority":3,
	 "config":{"type":"aggregation-v1","query":"level:ERROR","streams":["s1"],
	           "search_within_ms":3600000,"execute_every_ms":300000},
	 "notifications":[{"notification_id":"n1"}]},
	{"id":"d2","title":"Disk full","state":"DISABLED","alert":true,"priority":2,
	 "config":{"type":"aggregation-v1"},"notifications":[{"notification_id":"n1"}]},
	{"id":"d3","title":"Audit trail","state":"ENABLED","alert":false,"priority":1,
	 "config":{"type":"aggregation-v1"},"notifications":[]}]}`

// "Enabled" and "raises an alert" are separate facts and are kept separate. A
// rule can be running and recording events while notifying nobody, which is
// the shape "why did nobody get told" usually turns out to have.
func TestEventDefinitions_CountsWhatIsSilent(t *testing.T) {
	p := toolPlugin(t, jsonOK(definitionsBody))

	got, err := p.listEventDefinitions(context.Background(), definitionsArgs{})
	if err != nil {
		t.Fatalf("eventDefinitions: %v", err)
	}
	if len(got.Rules) != 3 {
		t.Fatalf("rules = %d, want 3", len(got.Rules))
	}
	if got.Rules[0].Enabled != true || got.Rules[1].Enabled != false {
		t.Errorf("state did not become enabled/disabled: %+v", got.Rules)
	}
	if !strings.Contains(got.Note, "1 of these rules are disabled") {
		t.Errorf("a disabled rule should be counted in the note: %q", got.Note)
	}
	if !strings.Contains(got.Note, "without telling anybody") {
		t.Errorf("a rule with no notification should be counted: %q", got.Note)
	}
	// The window and interval are rendered rather than left in milliseconds,
	// because "3600000" is not a unit anybody reads.
	if got.Rules[0].Window == "" || got.Rules[0].Every == "" {
		t.Errorf("the rule's window and interval were not rendered: %+v", got.Rules[0])
	}
}

// An unrecognised priority is rendered as itself rather than mapped to the
// nearest word: inventing a label would hide a version difference behind a
// plausible answer.
func TestPriorityName_PassesThroughWhatItDoesNotKnow(t *testing.T) {
	if got := priorityName(9); got != "9" {
		t.Errorf("priorityName(9) = %q, want the number itself", got)
	}
	if got := priorityName(0); got != "" {
		t.Errorf("priorityName(0) = %q, want nothing", got)
	}
}
