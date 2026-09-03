package threecx

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "is the phone system all right".
//
// Almost every phone ticket is answered by one of these before anything else:
// whether the trunk is up, whether the handsets are registered, whether a
// service has stopped, and what the system has been complaining about.

func (p *Plugin) registerSystemTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_system_status",
		Title: "Get system status",
		Description: "Whether the phone system is healthy: how many extensions " +
			"and trunks are registered against how many exist, which trunks " +
			"are offline, which services are not running, calls in progress, " +
			"disk, backups, and the licence with its expiry. Use this first " +
			"for any phone problem; most are a trunk or a handset that is not " +
			"registered, and the concerns field says so in words.",
		Idempotent: true,
	}, p.getSystemStatus)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_services",
		Title: "List system services",
		Description: "The phone system's own services and whether each is " +
			"running. Reach for it when get_system_status reports a service " +
			"not running, to see which.",
		Idempotent: true,
	}, p.listServices)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_active_calls",
		Title: "List calls in progress",
		Description: "Calls on the phone system right now: who is talking to " +
			"whom and since when. Use it to answer whether anything is going " +
			"through at this moment.",
		Idempotent: true,
	}, p.listActiveCalls)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "search_events",
		Title: "Search the event log",
		Description: "The phone system's event log: failed registrations, trunk " +
			"status changes, licence warnings, service problems. Newest first. " +
			"Give a query to have the phone system search for lines mentioning " +
			"an extension, a trunk or a phrase from an error; narrow by " +
			"severity, source or time. Use it to find out when something " +
			"started, or why.",
		Idempotent: true,
	}, p.searchEvents)
}

// --- system status ----------------------------------------------------------

type statusArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
}

// SystemStatus is one answer to "is it healthy", with the arithmetic done.
type SystemStatus struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer  string `json:"customer"`
	FQDN      string `json:"fqdn"`
	Version   string `json:"version"`
	Activated bool   `json:"activated"`
	OS        string `json:"os,omitempty"`

	LicenseActive      bool   `json:"license_active"`
	Product            string `json:"product,omitempty"`
	Company            string `json:"company,omitempty"`
	LicenseExpires     string `json:"license_expires,omitempty"`
	MaintenanceExpires string `json:"maintenance_expires,omitempty"`
	MaxSimCalls        int    `json:"max_simultaneous_calls"`

	CallsActive          int      `json:"calls_active"`
	ExtensionsTotal      int      `json:"extensions_total"`
	ExtensionsRegistered int      `json:"extensions_registered"`
	TrunksTotal          int      `json:"trunks_total"`
	TrunksRegistered     int      `json:"trunks_registered"`
	TrunksOffline        []string `json:"trunks_offline"`
	ServicesNotRunning   []string `json:"services_not_running"`
	// SystemExtensionsUnregistered is 3CX's own flag for its internal
	// extensions -- parking, conference, the IVRs -- not being registered,
	// which is a service problem rather than a handset problem.
	SystemExtensionsUnregistered bool `json:"system_extensions_unregistered"`

	DiskUsedPercent int     `json:"disk_used_percent"`
	FreeDiskGB      float64 `json:"free_disk_gb"`
	BackupScheduled bool    `json:"backup_scheduled"`
	LastBackup      string  `json:"last_backup,omitempty"`
	AutoUpdate      bool    `json:"auto_update"`
	LastUpdate      string  `json:"last_update,omitempty"`

	RecordingQuotaReached bool `json:"recording_quota_reached"`
	VoicemailQuotaReached bool `json:"voicemail_quota_reached"`

	// Concerns says in words what the numbers say. "3 of 40 extensions
	// registered" is the finding; making a model derive it from two integers
	// is how it gets derived wrong.
	Concerns []string `json:"concerns"`
}

// systemStatusFields is what SystemStatus is asked for. The singleton also
// carries LicenseKey and the system's IP addresses, which are not this
// integration's to hand out.
const systemStatusFields = "FQDN,Version,Activated,OS,MaxSimCalls,CallsActive," +
	"ExtensionsTotal,ExtensionsRegistered,TrunksTotal,TrunksRegistered," +
	"HasNotRunningServices,HasUnregisteredSystemExtensions,DiskUsage,FreeDiskSpace," +
	"BackupScheduled,LastBackupDateTime,AutoUpdateEnabled,LastSuccessfulUpdate," +
	"LicenseActive,ExpirationDate,MaintenanceExpiresAt,RecordingQuotaReached,VoicemailQuotaReached"

func (p *Plugin) getSystemStatus(ctx context.Context, args statusArgs) (SystemStatus, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return SystemStatus{}, err
	}

	var s struct {
		FQDN                            string  `json:"FQDN"`
		Version                         string  `json:"Version"`
		Activated                       bool    `json:"Activated"`
		OS                              string  `json:"OS"`
		MaxSimCalls                     int     `json:"MaxSimCalls"`
		CallsActive                     int     `json:"CallsActive"`
		ExtensionsTotal                 int     `json:"ExtensionsTotal"`
		ExtensionsRegistered            int     `json:"ExtensionsRegistered"`
		TrunksTotal                     int     `json:"TrunksTotal"`
		TrunksRegistered                int     `json:"TrunksRegistered"`
		HasNotRunningServices           bool    `json:"HasNotRunningServices"`
		HasUnregisteredSystemExtensions bool    `json:"HasUnregisteredSystemExtensions"`
		DiskUsage                       int     `json:"DiskUsage"`
		FreeDiskSpace                   float64 `json:"FreeDiskSpace"`
		BackupScheduled                 bool    `json:"BackupScheduled"`
		LastBackup                      string  `json:"LastBackupDateTime"`
		AutoUpdateEnabled               bool    `json:"AutoUpdateEnabled"`
		LastSuccessfulUpdate            string  `json:"LastSuccessfulUpdate"`
		LicenseActive                   bool    `json:"LicenseActive"`
		ExpirationDate                  string  `json:"ExpirationDate"`
		MaintenanceExpiresAt            string  `json:"MaintenanceExpiresAt"`
		RecordingQuotaReached           bool    `json:"RecordingQuotaReached"`
		VoicemailQuotaReached           bool    `json:"VoicemailQuotaReached"`
	}
	q := url.Values{"$select": {systemStatusFields}}
	if err := acct.client.get(ctx, "SystemStatus", q, &s); err != nil {
		return SystemStatus{}, acct.call(err)
	}

	out := SystemStatus{
		FQDN: s.FQDN, Version: s.Version, Activated: s.Activated, OS: s.OS,
		LicenseActive: s.LicenseActive, LicenseExpires: s.ExpirationDate,
		MaintenanceExpires: s.MaintenanceExpiresAt, MaxSimCalls: s.MaxSimCalls,
		CallsActive:     s.CallsActive,
		ExtensionsTotal: s.ExtensionsTotal, ExtensionsRegistered: s.ExtensionsRegistered,
		TrunksTotal: s.TrunksTotal, TrunksRegistered: s.TrunksRegistered,
		TrunksOffline: []string{}, ServicesNotRunning: []string{},
		SystemExtensionsUnregistered: s.HasUnregisteredSystemExtensions,
		DiskUsedPercent:              s.DiskUsage, FreeDiskGB: float64(int(s.FreeDiskSpace/1e8)) / 10,
		BackupScheduled: s.BackupScheduled, LastBackup: s.LastBackup,
		AutoUpdate: s.AutoUpdateEnabled, LastUpdate: s.LastSuccessfulUpdate,
		RecordingQuotaReached: s.RecordingQuotaReached, VoicemailQuotaReached: s.VoicemailQuotaReached,
		Concerns: []string{},
	}

	// The licence, because renewal dates are an MSP's problem and finding out
	// on the day it lapses is finding out too late. Fields are named so the
	// licence key itself never crosses the network. Degrades rather than
	// failing the call: a status without the product name still answers the
	// urgent question.
	var lic struct {
		ProductCode string `json:"ProductCode"`
		CompanyName string `json:"CompanyName"`
	}
	lq := url.Values{"$select": {"ProductCode,CompanyName"}}
	if err := acct.client.get(ctx, "LicenseStatus", lq, &lic); err == nil {
		out.Product, out.Company = lic.ProductCode, lic.CompanyName
	}

	// Trunk detail, because "1 of 2 registered" is only useful with the name
	// of the one that is not. Degrades for the same reason.
	if trunks, err := p.readTrunks(ctx, acct); err == nil {
		for _, t := range trunks {
			if !t.IsOnline && t.EnableInboundCalls || !t.IsOnline && t.EnableOutboundCalls {
				out.TrunksOffline = append(out.TrunksOffline, trunkLabel(t.Number, t.Gateway.Name))
			}
		}
	}
	if s.HasNotRunningServices {
		if services, err := p.readServices(ctx, acct); err == nil {
			for _, svc := range services {
				if !strings.EqualFold(svc.Status, "Running") {
					out.ServicesNotRunning = append(out.ServicesNotRunning, firstNonBlank(svc.DisplayName, svc.Name))
				}
			}
		}
	}

	out.Concerns = concerns(out)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// concerns says what is wrong, in the order somebody would want to hear it.
func concerns(s SystemStatus) []string {
	var out []string
	switch {
	case s.ExtensionsTotal > 0 && s.ExtensionsRegistered == 0:
		// Nothing registered at all is the loudest possible finding: a site
		// can have every trunk up, every service running and not one handset
		// able to ring.
		out = append(out, "no extensions are registered -- no handset or app on this system can make or take a call")
	case s.ExtensionsTotal > 0 && s.ExtensionsRegistered*2 < s.ExtensionsTotal:
		out = append(out, fmt.Sprintf("only %d of %d extensions are registered", s.ExtensionsRegistered, s.ExtensionsTotal))
	}
	switch {
	case s.TrunksTotal > 0 && s.TrunksRegistered == 0:
		out = append(out, "no trunks are registered -- no outside call can come in or go out")
	case len(s.TrunksOffline) > 0:
		out = append(out, fmt.Sprintf("%d of %d trunks are offline: %s",
			len(s.TrunksOffline), s.TrunksTotal, strings.Join(s.TrunksOffline, ", ")))
	case s.TrunksRegistered < s.TrunksTotal:
		out = append(out, fmt.Sprintf("%d of %d trunks are registered", s.TrunksRegistered, s.TrunksTotal))
	}
	if len(s.ServicesNotRunning) > 0 {
		out = append(out, "services not running: "+strings.Join(s.ServicesNotRunning, ", "))
	}
	if s.SystemExtensionsUnregistered {
		out = append(out, "some of the phone system's own internal extensions are not registered, which points at a service problem")
	}
	if !s.LicenseActive {
		out = append(out, "the licence is not active")
	}
	if !s.Activated {
		out = append(out, "the system is not activated")
	}
	if s.DiskUsedPercent >= 90 {
		out = append(out, fmt.Sprintf("disk is %d%% full", s.DiskUsedPercent))
	}
	if s.RecordingQuotaReached {
		out = append(out, "the recording quota is reached, so calls are no longer being recorded")
	}
	if s.VoicemailQuotaReached {
		out = append(out, "the voicemail quota is reached, so new messages are being refused")
	}
	if !s.BackupScheduled {
		out = append(out, "no backup is scheduled")
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func trunkLabel(number, name string) string {
	if name == "" || name == number {
		return number
	}
	return number + " (" + name + ")"
}

// --- services ---------------------------------------------------------------

type servicesArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
}

// ServiceRow is one of the phone system's own processes.
type ServiceRow struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Running  bool   `json:"running"`
	MemoryMB int    `json:"memory_mb"`
}

// ServicesResult is the service list with the finding drawn out.
type ServicesResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer   string       `json:"customer"`
	Services   []ServiceRow `json:"services"`
	AllRunning bool         `json:"all_running"`
	NotRunning []string     `json:"not_running"`
}

type serviceRecord struct {
	Name        string  `json:"Name"`
	DisplayName string  `json:"DisplayName"`
	Status      string  `json:"Status"`
	MemoryUsed  float64 `json:"MemoryUsed"`
}

func (p *Plugin) readServices(ctx context.Context, acct *account) ([]serviceRecord, error) {
	q := url.Values{"$select": {"Name,DisplayName,Status,MemoryUsed"}}
	got, err := list[serviceRecord](ctx, acct.client, "Services", q, pageSize)
	if err != nil {
		return nil, err
	}
	return got.Rows, nil
}

func (p *Plugin) listServices(ctx context.Context, args servicesArgs) (ServicesResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return ServicesResult{}, err
	}
	services, err := p.readServices(ctx, acct)
	if err != nil {
		return ServicesResult{}, acct.call(err)
	}
	out := ServicesResult{Services: make([]ServiceRow, 0, len(services)), NotRunning: []string{}}
	for _, s := range services {
		running := strings.EqualFold(s.Status, "Running")
		name := firstNonBlank(s.DisplayName, s.Name)
		if !running {
			out.NotRunning = append(out.NotRunning, name)
		}
		out.Services = append(out.Services, ServiceRow{
			Name: name, Status: s.Status, Running: running,
			MemoryMB: int(s.MemoryUsed / (1 << 20)),
		})
	}
	out.AllRunning = len(out.NotRunning) == 0
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// --- active calls -----------------------------------------------------------

type activeCallsArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
}

// ActiveCallRow is one call in progress.
type ActiveCallRow struct {
	Caller      string `json:"caller"`
	Callee      string `json:"callee"`
	Status      string `json:"status"`
	Established string `json:"established,omitempty"`
	LastChange  string `json:"last_change,omitempty"`
}

// ActiveCallsResult is the calls in progress.
type ActiveCallsResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string          `json:"customer"`
	Calls    []ActiveCallRow `json:"calls"`
	Returned int             `json:"returned"`
	truncation
}

func (p *Plugin) listActiveCalls(ctx context.Context, args activeCallsArgs) (ActiveCallsResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return ActiveCallsResult{}, err
	}
	type record struct {
		Caller           string `json:"Caller"`
		Callee           string `json:"Callee"`
		Status           string `json:"Status"`
		EstablishedAt    string `json:"EstablishedAt"`
		LastChangeStatus string `json:"LastChangeStatus"`
	}
	q := url.Values{"$select": {"Id,Caller,Callee,Status,EstablishedAt,LastChangeStatus"}}
	got, err := list[record](ctx, acct.client, "ActiveCalls", q, p.cfg.MaxItems)
	if err != nil {
		return ActiveCallsResult{}, acct.call(err)
	}
	rows := make([]ActiveCallRow, 0, len(got.Rows))
	for _, c := range got.Rows {
		rows = append(rows, ActiveCallRow{
			Caller: c.Caller, Callee: c.Callee, Status: c.Status,
			Established: c.EstablishedAt, LastChange: c.LastChangeStatus,
		})
	}
	rows, cut := bound(rows, got.Truncated)
	acct.note(nil)
	return ActiveCallsResult{Customer: acct.name, Calls: rows, Returned: len(rows), truncation: cut}, nil
}

// --- event log --------------------------------------------------------------

type eventsArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Query    string `json:"query,omitempty" jsonschema:"text to look for, searched by the phone system: an extension, a trunk name, part of an error"`
	Type     string `json:"type,omitempty" jsonschema:"only this severity: Error, Warning or Info"`
	Source   string `json:"source,omitempty" jsonschema:"only events from a source whose name contains this, e.g. SIP Server"`
	Since    string `json:"since,omitempty" jsonschema:"only events at or after this time, as 2026-09-01T14:00:00Z or 2026-09-01"`
	Until    string `json:"until,omitempty" jsonschema:"only events at or before this time"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most events to return; defaults to 50"`
}

// EventRow is one event log entry, with its message filled in.
type EventRow struct {
	At      string `json:"at"`
	Type    string `json:"type"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
	Group   string `json:"group,omitempty"`
}

// EventsResult is a page of the event log.
type EventsResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string     `json:"customer"`
	Events   []EventRow `json:"events"`
	Returned int        `json:"returned"`
	truncation
}

// defaultEvents is how many events come back when nobody says.
const defaultEvents = 50

func (p *Plugin) searchEvents(ctx context.Context, args eventsArgs) (EventsResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return EventsResult{}, err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = defaultEvents
	}
	limit = p.limitOf(limit)

	q := url.Values{
		"$select":  {"Id,TimeGenerated,Type,Source,Message,Params,GroupName"},
		"$orderby": {"TimeGenerated desc"},
	}
	if s := strings.TrimSpace(args.Query); s != "" {
		q.Set("$search", s)
	}
	var filters []string
	if t := strings.TrimSpace(args.Type); t != "" {
		kind, ok := eventTypes[strings.ToLower(t)]
		if !ok {
			return EventsResult{}, fmt.Errorf("type %q is not a severity; it is Error, Warning or Info", t)
		}
		filters = append(filters, "Type eq "+odataString(kind))
	}
	if src := strings.TrimSpace(args.Source); src != "" {
		filters = append(filters, "contains(Source,"+odataString(src)+")")
	}
	since, err := odataTime("since", args.Since)
	if err != nil {
		return EventsResult{}, err
	}
	if since != "" {
		filters = append(filters, "TimeGenerated ge "+since)
	}
	until, err := odataTime("until", args.Until)
	if err != nil {
		return EventsResult{}, err
	}
	if until != "" {
		filters = append(filters, "TimeGenerated le "+until)
	}
	if len(filters) > 0 {
		q.Set("$filter", strings.Join(filters, " and "))
	}

	type record struct {
		At      string   `json:"TimeGenerated"`
		Type    string   `json:"Type"`
		Source  string   `json:"Source"`
		Message string   `json:"Message"`
		Params  []string `json:"Params"`
		Group   string   `json:"GroupName"`
	}
	got, err := list[record](ctx, acct.client, "EventLogs", q, limit)
	if err != nil {
		return EventsResult{}, acct.call(err)
	}
	rows := make([]EventRow, 0, len(got.Rows))
	for _, e := range got.Rows {
		rows = append(rows, EventRow{
			At: e.At, Type: e.Type, Source: e.Source, Group: e.Group,
			Message: fillTemplate(e.Message, e.Params),
		})
	}
	rows, cut := bound(rows, got.Truncated)
	acct.note(nil)
	return EventsResult{Customer: acct.name, Events: rows, Returned: len(rows), truncation: cut}, nil
}

var eventTypes = map[string]string{"error": "Error", "warning": "Warning", "info": "Info"}

// placeholder matches 3CX's message templates: %1$s, %2$s and so on, one-based.
var placeholder = regexp.MustCompile(`%(\d+)\$s`)

// fillTemplate puts the parameters back into the message they came out of.
//
// 3CX stores log lines as templates with the values alongside them, so the raw
// record reads "Trunk %1$s has changed status to %2$s" and says nothing about
// which trunk or which status. A placeholder with nothing to fill it is left as
// it was rather than blanked, because "Trunk  has changed status to " looks
// like a system with an empty trunk name instead of a record we could not
// complete.
func fillTemplate(message string, params []string) string {
	if message == "" || len(params) == 0 {
		return message
	}
	return placeholder.ReplaceAllStringFunc(message, func(match string) string {
		n, err := strconv.Atoi(placeholder.FindStringSubmatch(match)[1])
		if err != nil || n < 1 || n > len(params) {
			return match
		}
		return params[n-1]
	})
}
