package extremecloudiq

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// What has gone wrong, and who changed something before it did.

func (p *Plugin) registerAlertTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_alerts",
		Title: "List alerts",
		Description: "Alerts ExtremeCloud IQ raised in a window, newest first, " +
			"with a count of how many there were at each severity. The counts " +
			"cover the whole window whatever the listing was truncated to, so " +
			"“how bad is it” is answered even when “what exactly happened” is " +
			"not. Covers the last day unless you name a window. Narrow it by " +
			"severity, by site, by whether somebody has acknowledged it, or " +
			"with a keyword.",
		Idempotent: true,
	}, p.listAlerts)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_audit_logs",
		Title: "List who changed what",
		Description: "Configuration changes made in ExtremeCloud IQ: who, " +
			"when, and what. This is the tool for “it worked yesterday” — a " +
			"policy pushed or an SSID edited an hour before the complaints " +
			"started is the answer more often than anything in the alerts. " +
			"Covers the last day unless you name a window, and at most 30 " +
			"days in one call, which is the API's own limit.",
		Idempotent: true,
	}, p.listAuditLogs)
}

// alertSeverities maps the words somebody uses to the ids the API takes.
//
// The API takes 1, 2 and 3 and documents them nowhere a model will read, so
// asking for "critical" is the only form worth exposing.
var alertSeverities = map[string]string{
	"critical": "1",
	"warning":  "2",
	"info":     "3",
}

// AlertsInput selects which alerts to list.
type AlertsInput struct {
	timeArgs
	Severity     string `json:"severity,omitempty" jsonschema:"critical warning or info; omit for all three"`
	Site         string `json:"site,omitempty" jsonschema:"name of a site from extremecloudiq_list_locations"`
	Acknowledged string `json:"acknowledged,omitempty" jsonschema:"yes for only acknowledged alerts no for only unacknowledged; omit for both"`
	Keyword      string `json:"keyword,omitempty" jsonschema:"free text matched against the summary severity and source"`
	Limit        int    `json:"limit,omitempty" jsonschema:"most alerts to return; the configured ceiling applies whatever this says"`
}

// AlertsOutput is a listing of alerts and the shape of the whole window.
type AlertsOutput struct {
	// Window is the range actually covered. Reported on every call: a count of
	// alerts without the window it covers is a number with no unit.
	Window string `json:"window"`
	// CountsBySeverity covers the whole window, not the page. It is what makes
	// a truncated listing still answer "how bad is it".
	CountsBySeverity []Record `json:"counts_by_severity,omitempty"`
	Alerts           []Record `json:"alerts"`
	Returned         int      `json:"returned"`
	Total            int      `json:"total"`
	Truncated        bool     `json:"truncated,omitempty"`
	Reason           string   `json:"truncation_reason,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
	Note             string   `json:"note,omitempty"`
}

func (p *Plugin) listAlerts(ctx context.Context, in AlertsInput) (AlertsOutput, error) {
	if err := p.ready(); err != nil {
		return AlertsOutput{}, err
	}
	w, err := p.cfg.resolve(in.timeArgs, p.deps.Now())
	if err != nil {
		return AlertsOutput{}, err
	}

	params := url.Values{}
	w.apply(params)
	// Newest first. The API defaults to this and says so, but a default is a
	// thing that changes; an assistant reading the first ten of a thousand
	// alerts must be reading the ten that just happened.
	params.Set("sortField", "TIMESTAMP")
	params.Set("order", "DESC")
	setIf(params, "keyword", in.Keyword)

	if want := strings.ToLower(strings.TrimSpace(in.Severity)); want != "" {
		id, ok := alertSeverities[want]
		if !ok {
			return AlertsOutput{}, fmt.Errorf("extremecloudiq: severity is %q; "+
				"it is critical, warning or info", in.Severity)
		}
		params.Set("severityIds", id)
	}
	acknowledged, err := yesNo("acknowledged", in.Acknowledged)
	if err != nil {
		return AlertsOutput{}, err
	}
	if acknowledged != "" {
		params.Set("acknowledged", acknowledged)
	}

	out := AlertsOutput{Window: w.describe()}
	if site := strings.TrimSpace(in.Site); site != "" {
		id, where, err := p.locationID(ctx, site)
		if err != nil {
			return AlertsOutput{}, err
		}
		params.Set("siteId", strconv.FormatInt(id, 10))
		out.Note = "Alerts at " + where + "."
	}

	// The counts first, because they are the part that survives truncation.
	// Best-effort: a listing without them is still an answer, and failing the
	// whole call because a summary endpoint was unhappy would be the worse
	// trade.
	counts := url.Values{}
	w.apply(counts)
	if v := params.Get("acknowledged"); v != "" {
		counts.Set("acknowledged", v)
	}
	if v := params.Get("siteId"); v != "" {
		counts.Set("siteId", v)
	}
	var byseverity []Record
	if err := p.client.GetInto(ctx, "/alerts/count-by-SEVERITY", counts, &byseverity); err != nil {
		out.Warnings = append(out.Warnings, "could not count alerts by severity: "+err.Error())
	} else {
		out.CountsBySeverity = byseverity
	}

	got, err := p.client.Collect(ctx, "/alerts", params,
		p.limit(in.Limit), 100, plugins.ResultBudget(1))
	p.note(err)
	if err != nil {
		return AlertsOutput{}, err
	}
	out.Alerts, out.Returned, out.Total = got.Rows, len(got.Rows), got.Total
	out.Truncated, out.Reason = got.Truncated, got.Reason
	return out, nil
}

// AuditLogsInput selects which changes to list.
type AuditLogsInput struct {
	timeArgs
	Username string `json:"username,omitempty" jsonschema:"exact login name of one user, to list only their changes"`
	Keyword  string `json:"keyword,omitempty" jsonschema:"free text matched against the description of each change"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most entries to return; the configured ceiling applies whatever this says"`
}

// AuditLogsOutput is a listing of configuration changes.
type AuditLogsOutput struct {
	Window    string   `json:"window"`
	Entries   []Record `json:"entries"`
	Returned  int      `json:"returned"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated,omitempty"`
	Reason    string   `json:"truncation_reason,omitempty"`
}

func (p *Plugin) listAuditLogs(ctx context.Context, in AuditLogsInput) (AuditLogsOutput, error) {
	if err := p.ready(); err != nil {
		return AuditLogsOutput{}, err
	}
	w, err := p.cfg.resolve(in.timeArgs, p.deps.Now())
	if err != nil {
		return AuditLogsOutput{}, err
	}
	if err := withinAuditLimit(w); err != nil {
		return AuditLogsOutput{}, err
	}

	params := url.Values{}
	w.apply(params)
	params.Set("sortField", "TIMESTAMP")
	params.Set("sortOrder", "DESC")
	setIf(params, "username", in.Username)
	setIf(params, "keyword", in.Keyword)

	// 500 a page here rather than 100: audit rows are short and this is the
	// endpoint most likely to be walked over a wide window.
	got, err := p.client.Collect(ctx, "/logs/audit", params,
		p.limit(in.Limit), 500, plugins.ResultBudget(1))
	p.note(err)
	if err != nil {
		return AuditLogsOutput{}, err
	}
	return AuditLogsOutput{
		Window: w.describe(), Entries: got.Rows, Returned: len(got.Rows),
		Total: got.Total, Truncated: got.Truncated, Reason: got.Reason,
	}, nil
}

// yesNo turns a caller's word into the boolean the API takes, or an empty
// string for "they did not say".
//
// Three states rather than a Go bool, because "unacknowledged" and "either"
// are different questions and a zero value cannot hold both.
func yesNo(field, given string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(given)) {
	case "", "any", "either", "both":
		return "", nil
	case "yes", "true":
		return "true", nil
	case "no", "false":
		return "false", nil
	}
	return "", fmt.Errorf("extremecloudiq: %s is %q; it is yes or no, or "+
		"omitted for both", field, given)
}
