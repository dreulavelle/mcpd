package observium

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// Register declares the read surface.
//
// The tools are grouped by the question somebody asks rather than by the
// endpoint that answers it. Observium has around nineteen entity types and a
// tool per type would be a tool list longer than most conversations need, most
// of it never called -- so storage, memory and processors arrive together as
// "capacity", and neighbours, addresses and VLANs as "topology". The grouping
// is the one a network engineer would use, which is the one a model asked a
// network question will reach for.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "devices",
		Title: "List devices",
		Description: "Lists monitored devices and their current status. Start " +
			"here when you do not know what exists: nearly every other tool " +
			"takes a device_id from this one. Filter by status, operating " +
			"system, hardware, vendor, location or group rather than listing " +
			"everything -- a large estate is truncated, and the result says so.",
		Idempotent: true,
	}, p.listDevices)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "device",
		Title: "Get one device",
		Description: "Full detail for one device by id or hostname: hardware, " +
			"operating system, serial, uptime, location, contact and how it is " +
			"polled. Use it after observium_devices has named one.",
		Idempotent: true,
	}, p.getDevice)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "ports",
		Title: "List interfaces",
		Description: "Lists network interfaces with their administrative and " +
			"operational state, speed, and cumulative traffic and error " +
			"counters. Filter by device, state, or whether the port has errors. " +
			"The counters are totals since the counter last reset, not rates -- " +
			"a rate needs two readings, and Observium does not serve the " +
			"history as data. Use observium_graphs for the trend.",
		Idempotent: true,
	}, p.listPorts)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "sensors",
		Title: "List sensors",
		Description: "Lists environmental and electrical sensors with their " +
			"current reading and the thresholds Observium judges them against: " +
			"temperature, voltage, fan speed, power, humidity and current. " +
			"Filter by device, sensor class, or event state to find only what " +
			"is outside its limits.",
		Idempotent: true,
	}, p.listSensors)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "alerts",
		Title: "List current alerts",
		Description: "What is wrong now. Lists alert entries with their state, " +
			"the entity they are about, and when they last changed. Never " +
			"answered from cache. Filter by device or status; the default is " +
			"everything currently failed.",
		Idempotent: true,
	}, p.listAlerts)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "alert_history",
		Title: "Alert history",
		Description: "What went wrong and when, newest first. This is the only " +
			"history Observium serves as data -- everything else it records is " +
			"in RRD and comes out as an image. Filter by device, entity, or a " +
			"time window given as unix timestamps.",
		Idempotent: true,
	}, p.alertHistory)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "capacity",
		Title: "Storage, memory and processors",
		Description: "The three things that fill up: disk and filesystem usage, " +
			"memory pools, and processor load, each with its current percentage. " +
			"Ask for one device to see all three together, which is what " +
			"answering 'is this box healthy' actually needs.",
		Idempotent: true,
	}, p.capacity)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "topology",
		Title: "Neighbours, addresses and VLANs",
		Description: "How devices connect to each other and what they are " +
			"addressed as: discovered LLDP and CDP neighbours, configured IPv4 " +
			"and IPv6 addresses, and VLANs. Use it to trace what is on the " +
			"other end of a port. VLANs need a level 7 account and are omitted " +
			"with a note when the credential cannot read them.",
		Idempotent: true,
	}, p.topology)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "inventory",
		Title: "Hardware inventory",
		Description: "Physical hardware inside devices: modules, transceivers, " +
			"power supplies and fans, with model names and serial numbers. " +
			"Filter by device, model or serial to find where a part is fitted.",
		Idempotent: true,
	}, p.listInventory)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "graphs",
		Title: "Graph image links",
		Description: "Links to Observium's rendered graphs for one entity over " +
			"a time window. These are PNG images: you cannot read the values " +
			"in them and must not describe what they show. Offer the links for " +
			"the person to open. This exists because Observium keeps its time " +
			"series in RRD and has no endpoint that returns them as numbers.",
		Idempotent: true,
	}, p.graphURLs)

	return nil
}

// listResult is the shape every listing tool returns.
//
// Total and Truncated are on every one of them rather than only where
// truncation happened, because a field that appears only sometimes is one a
// model learns to ignore. Note carries the same fact in prose, for the same
// reason the operation view does: a model that reads only the items will
// answer as though it saw the estate.
type listResult struct {
	Items     []map[string]any `json:"items"`
	Count     int              `json:"count"`
	Total     int              `json:"total_matching,omitempty"`
	Truncated bool             `json:"truncated"`
	Note      string           `json:"note,omitempty"`
}

// resultOf renders a page, saying plainly when it is not the whole answer.
func resultOf(page Page, what string) listResult {
	out := listResult{
		Items:     page.Items,
		Count:     len(page.Items),
		Total:     page.Total,
		Truncated: page.Truncated,
	}
	if out.Items == nil {
		out.Items = []map[string]any{}
	}
	if page.Truncated {
		out.Note = fmt.Sprintf(
			"This is the first %d %s, not all of them%s. Narrow the filter "+
				"rather than reporting this as the whole estate.",
			len(page.Items), what, totalPhrase(page.Total))
	}
	return out
}

func totalPhrase(total int) string {
	if total <= 0 {
		return ""
	}
	return fmt.Sprintf(" — Observium reports %d matching", total)
}

// --- arguments -------------------------------------------------------------

type devicesArgs struct {
	Status   string `json:"status,omitempty" jsonschema:"filter by status: up, down, or disabled"`
	OS       string `json:"os,omitempty" jsonschema:"filter by operating system, e.g. ios, junos, linux"`
	Hostname string `json:"hostname,omitempty" jsonschema:"filter by hostname"`
	Location string `json:"location,omitempty" jsonschema:"filter by configured location"`
	Hardware string `json:"hardware,omitempty" jsonschema:"filter by hardware model"`
	Vendor   string `json:"vendor,omitempty" jsonschema:"filter by vendor"`
	Group    string `json:"group,omitempty" jsonschema:"filter by device group name"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most devices to return"`
}

type deviceArgs struct {
	DeviceID int    `json:"device_id,omitempty" jsonschema:"the device's numeric id"`
	Hostname string `json:"hostname,omitempty" jsonschema:"the device's hostname; either this or device_id"`
}

type portsArgs struct {
	DeviceID  int    `json:"device_id,omitempty" jsonschema:"only interfaces on this device"`
	Hostname  string `json:"hostname,omitempty" jsonschema:"only interfaces on this hostname"`
	State     string `json:"state,omitempty" jsonschema:"filter by operational state: up, down, admindown"`
	ErrorsSet bool   `json:"errors_only,omitempty" jsonschema:"only interfaces currently reporting errors"`
	Alerted   bool   `json:"alerted_only,omitempty" jsonschema:"only interfaces in an alerting state"`
	IfAlias   string `json:"description,omitempty" jsonschema:"filter by interface description or alias"`
	Limit     int    `json:"limit,omitempty" jsonschema:"most interfaces to return"`
}

type sensorsArgs struct {
	DeviceID int    `json:"device_id,omitempty" jsonschema:"only sensors on this device"`
	Class    string `json:"class,omitempty" jsonschema:"sensor class: temperature, voltage, fanspeed, power, current, humidity"`
	Event    string `json:"event,omitempty" jsonschema:"filter by state: ok, alert, warn, or ignore"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most sensors to return"`
}

type alertsArgs struct {
	DeviceID int    `json:"device_id,omitempty" jsonschema:"only alerts for this device"`
	Status   string `json:"status,omitempty" jsonschema:"failed, ok, delayed, suppressed, or all; default failed"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most alerts to return"`
}

type alertHistoryArgs struct {
	DeviceID int    `json:"device_id,omitempty" jsonschema:"only entries for this device"`
	From     int64  `json:"from,omitempty" jsonschema:"start of the window, as a unix timestamp"`
	To       int64  `json:"to,omitempty" jsonschema:"end of the window, as a unix timestamp"`
	Message  string `json:"message,omitempty" jsonschema:"filter by text in the log message"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most entries to return"`
}

type capacityArgs struct {
	DeviceID int `json:"device_id,omitempty" jsonschema:"only this device; strongly preferred, the unfiltered estate is large"`
	Limit    int `json:"limit,omitempty" jsonschema:"most entries per category"`
}

type topologyArgs struct {
	DeviceID int  `json:"device_id,omitempty" jsonschema:"only this device"`
	Limit    int  `json:"limit,omitempty" jsonschema:"most entries per category"`
	VLANs    bool `json:"include_vlans,omitempty" jsonschema:"include VLANs, which need a level 7 account"`
}

type inventoryArgs struct {
	DeviceID int    `json:"device_id,omitempty" jsonschema:"only hardware in this device"`
	Model    string `json:"model,omitempty" jsonschema:"filter by physical model name"`
	Serial   string `json:"serial,omitempty" jsonschema:"filter by serial number"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most entries to return"`
}

// --- handlers --------------------------------------------------------------

func (p *Plugin) listDevices(ctx context.Context, in devicesArgs) (listResult, error) {
	q := url.Values{}
	setIf(q, "status", in.Status)
	setIf(q, "os", in.OS)
	setIf(q, "hostname", in.Hostname)
	setIf(q, "location", in.Location)
	setIf(q, "hardware", in.Hardware)
	setIf(q, "vendor", in.Vendor)
	setIf(q, "group", in.Group)

	page, err := p.fetch(ctx, "/devices", "devices", q, in.Limit)
	if err != nil {
		return listResult{}, err
	}
	return resultOf(page, "devices"), nil
}

func (p *Plugin) getDevice(ctx context.Context, in deviceArgs) (listResult, error) {
	var key string
	switch {
	case in.DeviceID > 0:
		key = strconv.Itoa(in.DeviceID)
	case strings.TrimSpace(in.Hostname) != "":
		key = strings.TrimSpace(in.Hostname)
	default:
		return listResult{}, fmt.Errorf(
			"observium: name the device by device_id or hostname")
	}

	page, err := p.fetch(ctx, "/devices/"+url.PathEscape(key), "devices", url.Values{}, 0)
	if err != nil {
		return listResult{}, err
	}
	if len(page.Items) == 0 {
		return listResult{}, fmt.Errorf("observium: no device %q, or this "+
			"account cannot read it -- Observium reports the two the same way", key)
	}
	return resultOf(page, "devices"), nil
}

func (p *Plugin) listPorts(ctx context.Context, in portsArgs) (listResult, error) {
	q := url.Values{}
	setIDIf(q, "device_id", in.DeviceID)
	setIf(q, "hostname", in.Hostname)
	setIf(q, "state", in.State)
	setIf(q, "ifAlias", in.IfAlias)
	if in.ErrorsSet {
		q.Set("errors", "1")
	}
	if in.Alerted {
		q.Set("alerted", "1")
	}

	page, err := p.fetch(ctx, "/ports", "ports", q, in.Limit)
	if err != nil {
		return listResult{}, err
	}
	out := resultOf(page, "interfaces")
	out.Note = strings.TrimSpace(out.Note + " Traffic and error figures are " +
		"cumulative counters, not rates. Two readings are needed for a rate, " +
		"and this API does not serve the history.")
	return out, nil
}

func (p *Plugin) listSensors(ctx context.Context, in sensorsArgs) (listResult, error) {
	q := url.Values{}
	setIDIf(q, "device_id", in.DeviceID)
	setIf(q, "metric", in.Class)
	setIf(q, "event", in.Event)

	page, err := p.fetch(ctx, "/sensors", "sensors", q, in.Limit)
	if err != nil {
		return listResult{}, err
	}
	return resultOf(page, "sensors"), nil
}

func (p *Plugin) listAlerts(ctx context.Context, in alertsArgs) (listResult, error) {
	q := url.Values{}
	setIDIf(q, "device_id", in.DeviceID)
	// Defaulting to failed rather than all: "what is wrong" is the question
	// this tool exists for, and an unfiltered listing on a healthy estate is
	// thousands of rows saying nothing is wrong.
	status := strings.TrimSpace(in.Status)
	if status == "" {
		status = "failed"
	}
	q.Set("status", status)

	page, err := p.fetch(ctx, "/alerts", "alerts", q, in.Limit)
	if err != nil {
		return listResult{}, err
	}
	out := resultOf(page, "alerts")
	if len(out.Items) == 0 && status == "failed" {
		out.Note = "Nothing is currently in a failed state. This is a live " +
			"read, not a cached one."
	}
	return out, nil
}

func (p *Plugin) alertHistory(ctx context.Context, in alertHistoryArgs) (listResult, error) {
	q := url.Values{}
	setIDIf(q, "device_id", in.DeviceID)
	setIf(q, "message", in.Message)
	if in.From > 0 {
		q.Set("timestamp_from", strconv.FormatInt(in.From, 10))
	}
	if in.To > 0 {
		q.Set("timestamp_to", strconv.FormatInt(in.To, 10))
	}
	// Observium resolves entity ids to names only when asked, and a log line
	// naming "port 4821" is one a person cannot read.
	q.Set("expand_entities", "1")

	page, err := p.fetch(ctx, "/alert_log", "alert_log", q, in.Limit)
	if err != nil {
		return listResult{}, err
	}
	return resultOf(page, "log entries"), nil
}

// capacityResult keeps the three categories apart rather than concatenating
// them. A percentage means a different thing for a filesystem than for a
// processor, and a flat list invites a model to compare them.
type capacityResult struct {
	Storage    listResult `json:"storage"`
	Memory     listResult `json:"memory"`
	Processors listResult `json:"processors"`
	Note       string     `json:"note,omitempty"`
}

func (p *Plugin) capacity(ctx context.Context, in capacityArgs) (capacityResult, error) {
	q := url.Values{}
	setIDIf(q, "device_id", in.DeviceID)

	storage, err := p.fetch(ctx, "/storage", "storage", q, in.Limit)
	if err != nil {
		return capacityResult{}, err
	}
	memory, err := p.fetch(ctx, "/mempools", "mempools", q, in.Limit)
	if err != nil {
		return capacityResult{}, err
	}
	processors, err := p.fetch(ctx, "/processors", "processors", q, in.Limit)
	if err != nil {
		return capacityResult{}, err
	}

	out := capacityResult{
		Storage:    resultOf(storage, "filesystems"),
		Memory:     resultOf(memory, "memory pools"),
		Processors: resultOf(processors, "processors"),
	}
	if in.DeviceID <= 0 {
		out.Note = "This spans every device, so each category is truncated " +
			"independently and none of them is a complete picture. Pass a " +
			"device_id to see one machine fully."
	}
	return out, nil
}

type topologyResult struct {
	Neighbours listResult `json:"neighbours"`
	Addresses  listResult `json:"addresses"`
	VLANs      listResult `json:"vlans"`
	Note       string     `json:"note,omitempty"`
}

func (p *Plugin) topology(ctx context.Context, in topologyArgs) (topologyResult, error) {
	q := url.Values{}
	setIDIf(q, "device_id", in.DeviceID)

	neighbours, err := p.fetch(ctx, "/neighbours", "neighbours", q, in.Limit)
	if err != nil {
		return topologyResult{}, err
	}
	addresses, err := p.fetch(ctx, "/address", "addresses", q, in.Limit)
	if err != nil {
		return topologyResult{}, err
	}

	out := topologyResult{
		Neighbours: resultOf(neighbours, "neighbours"),
		Addresses:  resultOf(addresses, "addresses"),
		VLANs:      listResult{Items: []map[string]any{}},
	}

	if in.VLANs {
		// A permission failure here is not a failure of the call. VLANs need a
		// level 7 account, and a topology answer without them is still the
		// answer to most of the question -- so it degrades with a note rather
		// than losing the neighbours that were already fetched.
		vlans, err := p.fetch(ctx, "/vlans", "vlans", q, in.Limit)
		switch {
		case err != nil:
			out.VLANs.Note = "VLANs could not be read: " + err.Error()
		default:
			out.VLANs = resultOf(vlans, "VLANs")
		}
	} else {
		out.VLANs.Note = "Not requested. Pass include_vlans to fetch them; " +
			"they need a level 7 Observium account."
	}
	return out, nil
}

func (p *Plugin) listInventory(ctx context.Context, in inventoryArgs) (listResult, error) {
	q := url.Values{}
	setIDIf(q, "device_id", in.DeviceID)
	setIf(q, "entPhysicalModelName", in.Model)
	setIf(q, "entPhysicalSerialNum", in.Serial)

	page, err := p.fetch(ctx, "/inventory", "inventory", q, in.Limit)
	if err != nil {
		return listResult{}, err
	}
	return resultOf(page, "hardware entries"), nil
}

// --- plumbing --------------------------------------------------------------

// fetch applies a per-call limit over the configured ceiling and runs the read.
//
// A caller may narrow what they get back but never widen it past what the
// operator configured: max_items is a bound on what one answer may pull into a
// conversation, and an argument that could raise it would not be a bound.
func (p *Plugin) fetch(ctx context.Context, path, key string, q url.Values, limit int) (Page, error) {
	if !p.configured {
		return Page{}, fmt.Errorf("observium: not configured yet — set the " +
			"address and a credential on the Plugins page")
	}

	client := p.client
	if limit > 0 && limit < p.cfg.MaxItems {
		// A shallow copy with a tighter ceiling. The cache, limiter and HTTP
		// client are shared by pointer, so this costs nothing and keeps the
		// per-call bound out of the client's own state where a concurrent
		// call would see it.
		narrowed := *client
		narrowed.cfg.MaxItems = limit
		client = &narrowed
	}

	page, err := client.Get(ctx, path, key, q)
	p.note(err)
	return page, err
}

// setIf adds a filter only when it was given, so an empty argument does not
// become a filter matching the empty string.
func setIf(q url.Values, key, value string) {
	if v := strings.TrimSpace(value); v != "" {
		q.Set(key, v)
	}
}

func setIDIf(q url.Values, key string, id int) {
	if id > 0 {
		q.Set(key, strconv.Itoa(id))
	}
}
