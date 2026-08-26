package cnmaestro

import (
	"context"
	"fmt"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// Alarms, alarm history and events: what an estate is complaining about, and
// what it did earlier.
//
// Three tools rather than one with a mode argument. An alarm that is current
// and one that cleared last week answer different questions -- "what is wrong
// now" against "what happened" -- and a model choosing between them by name is
// more reliable than one choosing by a flag it has to remember to set.

func (p *Plugin) registerAlarmTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_alarms",
		Title: "List active alarms",
		Description: "What is wrong in the estate right now: alarms that are " +
			"raised and not yet cleared, newest first. Filter by severity, " +
			"network, tower or site. For what has already cleared, use " +
			"cnmaestro_list_alarm_history instead.",
		Idempotent: true,
	}, p.listAlarms)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_alarm_history",
		Title: "List alarm history",
		Description: "Alarms over a period, including ones that have cleared. " +
			"Use this to see whether something is recurring rather than new. " +
			"Times are ISO 8601, and a range without them is whatever the API " +
			"defaults to, which is not the whole history.",
		Idempotent: true,
	}, p.listAlarmHistory)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_events",
		Title: "List events",
		Description: "The device and system event log: reboots, onboarding, " +
			"configuration changes, radio events. Alarms say what is wrong; " +
			"events say what happened. Filter by severity, event code, place " +
			"or time.",
		Idempotent: true,
	}, p.listEvents)
}

// AlarmsInput filters the active alarm list.
type AlarmsInput struct {
	Account  string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name from cnmaestro_list_managed_accounts, or Base Infrastructure for the main account; omit to use the configured default"`
	Severity string `json:"severity,omitempty" jsonschema:"critical, major or minor; omit for every severity"`
	Network  string `json:"network,omitempty" jsonschema:"limit to one network, by name"`
	Tower    string `json:"tower,omitempty" jsonschema:"limit to one tower, by name"`
	Site     string `json:"site,omitempty" jsonschema:"limit to one site, by name"`
}

// AlarmsOutput is a list of alarms.
//
// Alarms are passed through as raw records for the same reason devices are:
// what an alarm carries depends on the device type that raised it.
type AlarmsOutput struct {
	Alarms    []Record `json:"alarms"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listAlarms(ctx context.Context, in AlarmsInput) (AlarmsOutput, error) {
	severity, err := oneOf("severity", in.Severity, "critical", "major", "minor")
	if err != nil {
		return AlarmsOutput{}, err
	}

	account := p.cfg.Account(in.Account)
	params := accountParams(account)
	setIf(params, "severity", severity)
	setIf(params, "network", in.Network)
	setIf(params, "tower", in.Tower)
	setIf(params, "site", in.Site)

	page, note, err := p.collect(ctx, "/alarms", params, account,
		placeScope(in.Network, in.Tower, in.Site), "alarms",
		"severity, network, tower, or site")
	if err != nil {
		return AlarmsOutput{}, err
	}
	return AlarmsOutput{
		Alarms: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}

// AlarmHistoryInput filters the alarm history.
type AlarmHistoryInput struct {
	Account   string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name, or Base Infrastructure for the main account; omit to use the configured default"`
	Severity  string `json:"severity,omitempty" jsonschema:"critical, major or minor; omit for every severity"`
	State     string `json:"state,omitempty" jsonschema:"active for alarms still raised, cleared for ones that have resolved; omit for both"`
	StartTime string `json:"start_time,omitempty" jsonschema:"start of the period, ISO 8601 such as 2026-08-01T00:00:00Z"`
	StopTime  string `json:"stop_time,omitempty" jsonschema:"end of the period, ISO 8601"`
	Network   string `json:"network,omitempty" jsonschema:"limit to one network, by name"`
	Tower     string `json:"tower,omitempty" jsonschema:"limit to one tower, by name"`
	Site      string `json:"site,omitempty" jsonschema:"limit to one site, by name"`
}

// AlarmHistoryOutput is a list of alarms over a period.
type AlarmHistoryOutput struct {
	Alarms    []Record `json:"alarms"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listAlarmHistory(ctx context.Context, in AlarmHistoryInput) (AlarmHistoryOutput, error) {
	severity, err := oneOf("severity", in.Severity, "critical", "major", "minor")
	if err != nil {
		return AlarmHistoryOutput{}, err
	}
	state, err := oneOf("state", in.State, "active", "cleared")
	if err != nil {
		return AlarmHistoryOutput{}, err
	}
	start, err := isoTime("start_time", in.StartTime)
	if err != nil {
		return AlarmHistoryOutput{}, err
	}
	stop, err := isoTime("stop_time", in.StopTime)
	if err != nil {
		return AlarmHistoryOutput{}, err
	}

	account := p.cfg.Account(in.Account)
	params := accountParams(account)
	setIf(params, "severity", severity)
	setIf(params, "state", state)
	setIf(params, "start_time", start)
	setIf(params, "stop_time", stop)
	setIf(params, "network", in.Network)
	setIf(params, "tower", in.Tower)
	setIf(params, "site", in.Site)

	page, note, err := p.collect(ctx, "/alarms/history", params, account,
		placeScope(in.Network, in.Tower, in.Site), "alarms",
		"a shorter time range, severity, or a network")
	if err != nil {
		return AlarmHistoryOutput{}, err
	}
	return AlarmHistoryOutput{
		Alarms: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}

// EventsInput filters the event log.
type EventsInput struct {
	Account   string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name, or Base Infrastructure for the main account; omit to use the configured default"`
	Severity  string `json:"severity,omitempty" jsonschema:"critical, major, minor or notify; omit for every severity"`
	Code      string `json:"code,omitempty" jsonschema:"limit to one event code, when you already know which event you are looking for"`
	StartTime string `json:"start_time,omitempty" jsonschema:"start of the period, ISO 8601 such as 2026-08-01T00:00:00Z"`
	StopTime  string `json:"stop_time,omitempty" jsonschema:"end of the period, ISO 8601"`
	Network   string `json:"network,omitempty" jsonschema:"limit to one network, by name"`
	Tower     string `json:"tower,omitempty" jsonschema:"limit to one tower, by name"`
	Site      string `json:"site,omitempty" jsonschema:"limit to one site, by name"`
}

// EventsOutput is a list of events.
type EventsOutput struct {
	Events    []Record `json:"events"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listEvents(ctx context.Context, in EventsInput) (EventsOutput, error) {
	// notify exists here and nowhere else: it is the severity for events that
	// are ordinary activity rather than a problem.
	severity, err := oneOf("severity", in.Severity, "critical", "major", "minor", "notify")
	if err != nil {
		return EventsOutput{}, err
	}
	start, err := isoTime("start_time", in.StartTime)
	if err != nil {
		return EventsOutput{}, err
	}
	stop, err := isoTime("stop_time", in.StopTime)
	if err != nil {
		return EventsOutput{}, err
	}

	account := p.cfg.Account(in.Account)
	params := accountParams(account)
	setIf(params, "severity", severity)
	setIf(params, "code", in.Code)
	setIf(params, "start_time", start)
	setIf(params, "stop_time", stop)
	setIf(params, "network", in.Network)
	setIf(params, "tower", in.Tower)
	setIf(params, "site", in.Site)

	page, note, err := p.collect(ctx, "/events", params, account,
		placeScope(in.Network, in.Tower, in.Site), "events",
		"a shorter time range, a severity, or an event code")
	if err != nil {
		return EventsOutput{}, err
	}
	return EventsOutput{
		Events: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}

// oneOf checks a value against what the API accepts.
//
// Checked here rather than upstream because the API answers a wrong one with a
// 400 that does not name the choices, and a model that cannot see the choices
// guesses again.
func oneOf(field, value string, allowed ...string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", nil
	}
	for _, a := range allowed {
		if strings.EqualFold(v, a) {
			return a, nil
		}
	}
	return "", fmt.Errorf("%s must be one of %s; got %q",
		field, strings.Join(allowed, ", "), value)
}
