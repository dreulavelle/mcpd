package observium

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/spoked/mcpd/internal/plugins"
)

// Register declares the read surface.
//
// The tools are grouped by the question somebody asks rather than by the
// endpoint that answers it. Observium has around nineteen entity types, so
// storage, memory and processors arrive together as "capacity", and
// neighbours, addresses and VLANs as "topology". The grouping is the one a
// network engineer would use, which is the one a model asked a network
// question will reach for.
//
// It buys clarity rather than context. The saving it looks like it should make
// is not there -- a grouped tool's composite result carries an output schema
// larger by roughly what the extra tool entries would have cost -- and this
// plugin is the evidence: fourteen tools costing more than cnmaestro's
// seventeen. TestToolList_StaysWithinItsContextBudget holds the numbers.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_devices",
		Title: "List devices",
		Description: "Lists monitored devices and their current status. Start " +
			"here when you do not know what exists: nearly every other tool " +
			"takes a device_id from this one. Filter by status, operating " +
			"system, hardware, vendor, location or group rather than listing " +
			"everything -- a large estate is truncated, and the result says so.",
		Idempotent: true,
	}, p.listDevices)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_device",
		Title: "Get one device",
		Description: "Full detail for one device by id or hostname: hardware, " +
			"operating system, serial, uptime, location, contact and how it is " +
			"polled. Use it after observium_list_devices has named one.",
		Idempotent: true,
	}, p.getDevice)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_ports",
		Title: "List interfaces",
		Description: "Lists network interfaces with their administrative and " +
			"operational state, speed, and cumulative traffic and error " +
			"counters. Filter by device, state, or whether the port has errors. " +
			"The counters are totals since the counter last reset, not rates -- " +
			"a rate needs two readings, and Observium does not serve the " +
			"history as data. Use observium_get_graph_urls for the trend.",
		Idempotent: true,
	}, p.listPorts)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_sensors",
		Title: "Sensors and state indicators",
		Description: "Everything Observium measures or watches on a device. " +
			"Sensors are the readings that are numbers — temperature, voltage, " +
			"fan speed, power, humidity, current — with the thresholds they " +
			"are judged against. Status entries are the ones that are states: " +
			"a power supply present or absent, a fan ok or failed. Ask for " +
			"both when the question is whether a device is healthy; filter by " +
			"event to see only what is outside its limits.",
		Idempotent: true,
	}, p.listSensors)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_alerts",
		Title: "List current alerts",
		Description: "What is wrong now. Lists alert entries with their state, " +
			"the entity they are about, and when they last changed. Never " +
			"answered from cache. Filter by device or status; the default is " +
			"everything currently failed.",
		Idempotent: true,
	}, p.listAlerts)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_alert_history",
		Title: "Alert history",
		Description: "What went wrong and when, newest first. This is the only " +
			"history Observium serves as data -- everything else it records is " +
			"in RRD and comes out as an image. Filter by device, entity, or a " +
			"time window given as unix timestamps.",
		Idempotent: true,
	}, p.listAlertHistory)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_capacity",
		Title: "Storage, memory and processors",
		Description: "The three things that fill up: disk and filesystem usage, " +
			"memory pools, and processor load, each with its current percentage. " +
			"Ask for one device to see all three together, which is what " +
			"answering 'is this box healthy' actually needs.",
		Idempotent: true,
	}, p.getCapacity)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_topology",
		Title: "Neighbours, addresses and VLANs",
		Description: "How devices connect to each other and what they are " +
			"addressed as: discovered LLDP and CDP neighbours, configured IPv4 " +
			"and IPv6 addresses, and VLANs. Use it to trace what is on the " +
			"other end of a port. VLANs need a level 7 account and are omitted " +
			"with a note when the credential cannot read them.",
		Idempotent: true,
	}, p.getTopology)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_inventory",
		Title: "Hardware inventory",
		Description: "Physical hardware inside devices: modules, transceivers, " +
			"power supplies and fans, with model names and serial numbers. " +
			"Filter by device, model or serial to find where a part is fitted.",
		Idempotent: true,
	}, p.listInventory)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_maintenance_windows",
		Title: "Scheduled maintenance windows",
		Description: "Planned windows during which alerting is suppressed. " +
			"This is the first thing to check when an estate has something " +
			"visibly wrong and no alerts to show for it — the answer is often " +
			"that somebody scheduled the work. Needs a level 8 Observium " +
			"account; below that it reports that it could not read them rather " +
			"than reporting none.",
		Idempotent: true,
	}, p.listMaintenanceWindows)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_groups",
		Title: "List groups",
		Description: "The groups an operator has organised the estate into. " +
			"Several filters take a group name, and this is the only way to " +
			"learn what they are — guessing one returns an empty result that " +
			"reads like an empty estate.",
		Idempotent: true,
	}, p.listGroups)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_graph_urls",
		Title: "Graph image links",
		Description: "Links to Observium's rendered graphs for one entity over " +
			"a time window. These are PNG images: you cannot read the values " +
			"in them and must not describe what they show. Offer the links for " +
			"the person to open. This exists because Observium keeps its time " +
			"series in RRD and has no endpoint that returns them as numbers.",
		Idempotent: true,
	}, p.getGraphURLs)

	p.registerReadTools(r)
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
	// Checks is the alerting configuration, present only when a caller asked
	// for it. It answers "why did nothing tell me", which is a different
	// question from "what is wrong" and shares a tool because it shares a
	// domain.
	Checks []map[string]any `json:"alert_checkers,omitempty"`
	// Fields names what each item carries, once for the listing rather than
	// per item. A model that can see the field set can tell a field that is
	// absent because it was not returned from one that is absent because the
	// device does not have it.
	Fields []string `json:"fields,omitempty"`
	Note   string   `json:"note,omitempty"`
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
	// Said plainly, for the same reason truncation is: a row trimmed to the
	// fields that answer the question still looks like a whole row.
	if page.FieldsDropped > 0 {
		out.Fields = page.Fields
		out.Note = strings.TrimSpace(fmt.Sprintf(
			"%s Each %s carries the %d fields listed in \"fields\"; %d more "+
				"Observium holds were not returned. Ask about one by id for "+
				"its full record. Credentials are never returned, whichever "+
				"you ask for.",
			out.Note, strings.TrimSuffix(what, "s"), len(page.Fields),
			page.FieldsDropped))
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
	Type     string `json:"type,omitempty" jsonschema:"filter by device type, e.g. network, server, firewall"`
	Group    string `json:"group,omitempty" jsonschema:"filter by group name; observium_list_groups lists them"`
	Version  string `json:"version,omitempty" jsonschema:"filter by software version"`
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
	IfDescr   string `json:"name,omitempty" jsonschema:"filter by the interface's own name, e.g. GigabitEthernet0/1"`
	Group     string `json:"device_group,omitempty" jsonschema:"only interfaces on devices in this group"`
	Limit     int    `json:"limit,omitempty" jsonschema:"most interfaces to return"`
}

type sensorsArgs struct {
	DeviceID int    `json:"device_id,omitempty" jsonschema:"only readings on this device"`
	Class    string `json:"class,omitempty" jsonschema:"sensor class: temperature, voltage, fanspeed, power, current, humidity"`
	Event    string `json:"event,omitempty" jsonschema:"filter by state: ok, warning, alert, or ignore"`
	Group    string `json:"group,omitempty" jsonschema:"only readings on devices in this group"`
	NoStatus bool   `json:"sensors_only,omitempty" jsonschema:"leave out the state indicators and return only numeric sensors"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most entries to return per kind"`
}

type alertsArgs struct {
	DeviceID   int    `json:"device_id,omitempty" jsonschema:"only alerts for this device"`
	Status     string `json:"status,omitempty" jsonschema:"failed, ok, or all; default failed"`
	EntityType string `json:"entity_type,omitempty" jsonschema:"only alerts about this kind of thing: device, port, sensor, storage"`
	EntityID   int    `json:"entity_id,omitempty" jsonschema:"only alerts about this entity, with entity_type"`
	Checks     bool   `json:"include_checks,omitempty" jsonschema:"also return the alert checkers that exist, which answers why something is not alerting"`
	Limit      int    `json:"limit,omitempty" jsonschema:"most alerts to return"`
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
	DeviceID int    `json:"device_id,omitempty" jsonschema:"only this device"`
	AF       string `json:"address_family,omitempty" jsonschema:"limit addresses to ipv4 or ipv6"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most entries per category"`
	VLANs    bool   `json:"include_vlans,omitempty" jsonschema:"include VLANs, which need a level 7 account"`
}

type maintenanceArgs struct {
	Active   bool `json:"active_only,omitempty" jsonschema:"only windows in effect right now"`
	Upcoming bool `json:"upcoming_only,omitempty" jsonschema:"only windows scheduled for the future"`
	Limit    int  `json:"limit,omitempty" jsonschema:"most windows to return"`
}

type groupsArgs struct {
	EntityType string `json:"entity_type,omitempty" jsonschema:"only groups of this kind of thing: device, port, sensor"`
	Members    bool   `json:"include_members,omitempty" jsonschema:"also list what is in each group"`
	Limit      int    `json:"limit,omitempty" jsonschema:"most groups to return"`
}

type inventoryArgs struct {
	DeviceID int    `json:"device_id,omitempty" jsonschema:"only hardware in this device"`
	OS       string `json:"os,omitempty" jsonschema:"only hardware in devices running this operating system"`
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
	setIf(q, FilterType, in.Type)
	setIf(q, "version", in.Version)
	setIf(q, "group", in.Group)

	page, err := p.fetch(ctx, EntityDevices, q, in.Limit)
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

	q := url.Values{}
	q.Set(FilterID, key)
	page, err := p.fetchFull(ctx, EntityDevices, q, 0)
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
	setIf(q, FilterIfAlias, in.IfAlias)
	setIf(q, FilterIfDescr, in.IfDescr)
	setIf(q, FilterDeviceGrp, in.Group)
	if in.ErrorsSet {
		q.Set("errors", "1")
	}
	if in.Alerted {
		q.Set("alerted", "1")
	}

	page, err := p.fetch(ctx, EntityPorts, q, in.Limit)
	if err != nil {
		return listResult{}, err
	}
	out := resultOf(page, "interfaces")
	out.Note = strings.TrimSpace(out.Note + " Fields ending _rate are per-second " +
		"figures Observium computed at the last poll; the bare counters beside " +
		"them are cumulative totals. poll_time says when that was, so a rate of " +
		"zero can be told apart from a rate nobody has recomputed lately.")
	return out, nil
}

// sensorResult keeps the two kinds of reading apart.
//
// A sensor carries a number judged against a threshold; a status entry carries
// a state from a fixed set. Concatenating them would invite a model to compare
// a temperature with "present", so they travel together and stay distinct.
type sensorResult struct {
	Sensors listResult `json:"sensors"`
	Status  listResult `json:"state_indicators"`
	Note    string     `json:"note,omitempty"`
}

func (p *Plugin) listSensors(ctx context.Context, in sensorsArgs) (sensorResult, error) {
	q := url.Values{}
	setIDIf(q, FilterDeviceID, in.DeviceID)
	setIf(q, FilterMetric, in.Class)
	setIf(q, FilterEvent, in.Event)
	setIf(q, "group", in.Group)

	sensors, err := p.fetchWithin(ctx, EntitySensors, q, in.Limit, 2)
	if err != nil {
		return sensorResult{}, err
	}
	out := sensorResult{
		Sensors: resultOf(sensors, "sensors"),
		Status:  listResult{Items: []map[string]any{}},
	}

	if in.NoStatus {
		out.Status.Note = "Not requested."
		return out, nil
	}

	// A refusal here does not lose the sensors already fetched. Status is the
	// smaller half of the answer and an installation that cannot serve it can
	// still answer most of the question.
	status, err := p.fetchWithin(ctx, EntityStatus, q, in.Limit, 2)
	if err != nil {
		out.Status.Note = "State indicators could not be read: " + err.Error()
		return out, nil
	}
	out.Status = resultOf(status, "state indicators")
	return out, nil
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

	page, err := p.fetchWithin(ctx, EntityAlerts, q, in.Limit, 2)
	if err != nil {
		return listResult{}, err
	}
	out := resultOf(page, "alerts")
	if len(out.Items) == 0 && status == "failed" {
		out.Note = "Nothing is currently in a failed state. This is a live " +
			"read, not a cached one. If something is visibly wrong and nothing " +
			"is alerting, check observium_list_maintenance_windows for a window that is " +
			"suppressing it."
	}
	if in.Checks {
		checks, err := p.fetchWithin(ctx, EntityAlertChecks, url.Values{}, in.Limit, 2)
		switch {
		case err != nil:
			out.Note = strings.TrimSpace(out.Note +
				" The alert checkers could not be read: " + err.Error())
		default:
			out.Checks = resultOf(checks, "alert checkers").Items
		}
	}
	return out, nil
}

func (p *Plugin) listMaintenanceWindows(ctx context.Context, in maintenanceArgs) (listResult, error) {
	q := url.Values{}
	if in.Active {
		q.Set(FilterActive, "1")
	}
	if in.Upcoming {
		q.Set(FilterUpcoming, "1")
	}
	// Without the associations a window says a time and a name, and the
	// question being asked is which equipment it covers.
	q.Set("include_associations", "1")

	page, err := p.fetch(ctx, EntityMaintenance, q, in.Limit)
	if err != nil {
		// Level 8 gates this endpoint, so a refusal is a permission answer
		// rather than a failure -- and reporting "no windows" would be the
		// wrong answer to "why is nothing alerting".
		return listResult{
			Items: []map[string]any{},
			Note: "Maintenance windows could not be read, so this is not a " +
				"statement that there are none: " + err.Error(),
		}, nil
	}
	out := resultOf(page, "maintenance windows")
	if len(out.Items) == 0 {
		out.Note = "No maintenance window matches, so suppression is not the " +
			"reason anything is quiet."
	}
	return out, nil
}

func (p *Plugin) listGroups(ctx context.Context, in groupsArgs) (listResult, error) {
	q := url.Values{}
	setIf(q, FilterEntityType, in.EntityType)
	if in.Members {
		q.Set(FilterMembers, "1")
	}

	page, err := p.fetch(ctx, EntityGroups, q, in.Limit)
	if err != nil {
		return listResult{}, err
	}
	return resultOf(page, "groups"), nil
}

func (p *Plugin) listAlertHistory(ctx context.Context, in alertHistoryArgs) (listResult, error) {

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

	page, err := p.fetch(ctx, EntityAlertLog, q, in.Limit)
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

func (p *Plugin) getCapacity(ctx context.Context, in capacityArgs) (capacityResult, error) {
	q := url.Values{}
	setIDIf(q, "device_id", in.DeviceID)

	// Three independent reads, so they wait on the upstream together rather
	// than one after another.
	//
	// The rate limiter still spaces the requests, which is why this is worth
	// anything at all: below one round trip per limiter interval it changes
	// nothing, and above it -- a large Observium answering a listing by
	// querying the whole table -- it is the difference between three latencies
	// and one. The group owns the goroutines, Wait is the join, and the derived
	// context stops the siblings when one fails.
	//
	// Sharing q is safe: Read copies what it needs into fresh parameters and
	// never writes to what it was given.
	var storage, memory, processors Page
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		storage, err = p.fetchWithin(gctx, EntityStorage, q, in.Limit, 3)
		return err
	})
	g.Go(func() (err error) {
		memory, err = p.fetchWithin(gctx, EntityMempools, q, in.Limit, 3)
		return err
	})
	g.Go(func() (err error) {
		processors, err = p.fetchWithin(gctx, EntityProcessors, q, in.Limit, 3)
		return err
	})
	if err := g.Wait(); err != nil {
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

func (p *Plugin) getTopology(ctx context.Context, in topologyArgs) (topologyResult, error) {
	q := url.Values{}
	setIDIf(q, "device_id", in.DeviceID)

	addrQuery := url.Values{}
	for k, v := range q {
		addrQuery[k] = v
	}
	setIf(addrQuery, FilterAF, in.AF)

	var neighbours, addresses, vlans Page
	var vlanErr error
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		neighbours, err = p.fetchWithin(gctx, EntityNeighbours, q, in.Limit, 3)
		return err
	})
	g.Go(func() (err error) {
		addresses, err = p.fetchWithin(gctx, EntityAddresses, addrQuery, in.Limit, 3)
		return err
	})
	if in.VLANs {
		// A permission failure here is not a failure of the call. VLANs need a
		// level 7 account, and a topology answer without them is still the
		// answer to most of the question -- so it degrades with a note rather
		// than losing the neighbours fetched beside it.
		//
		// Which is why this returns nil whatever happens: an error returned to
		// the group would cancel the derived context and take the neighbours
		// down with it. The failure is carried out instead, and read after
		// Wait, which is the barrier that makes that safe.
		g.Go(func() error {
			vlans, vlanErr = p.fetchWithin(gctx, EntityVLANs, q, in.Limit, 3)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return topologyResult{}, err
	}

	out := topologyResult{
		Neighbours: resultOf(neighbours, "neighbours"),
		Addresses:  resultOf(addresses, "addresses"),
		VLANs:      listResult{Items: []map[string]any{}},
	}

	if in.VLANs {
		switch {
		case vlanErr != nil:
			out.VLANs.Note = "VLANs could not be read: " + vlanErr.Error()
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
	setIf(q, "os", in.OS)
	setIf(q, "entPhysicalModelName", in.Model)
	setIf(q, "entPhysicalSerialNum", in.Serial)

	page, err := p.fetch(ctx, EntityInventory, q, in.Limit)
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
// fetch reads a listing: the summary view, because a listing is answering a
// question about many things and the fields that answer it are few.
func (p *Plugin) fetch(ctx context.Context, entity Entity, q url.Values, limit int) (Page, error) {
	return p.read(ctx, entity, q, limit, viewSummary, plugins.ResultBudget(1))
}

// fetchWithin is fetch for a tool whose answer carries several collections.
//
// The budget is one tool *result*, not one collection, so three collections
// each bounded at the whole of it is a result three times past it. Composite
// tools say how many they carry and each gets its share.
//
// This is the grouped-tool trade showing up a second time. Grouping is right
// for the reason it has always been right -- a model asked whether a box is
// healthy should not choose between three tools -- and the cost is that each
// part of the answer is shorter than it would be alone.
func (p *Plugin) fetchWithin(ctx context.Context, entity Entity, q url.Values, limit, collections int) (Page, error) {
	return p.read(ctx, entity, q, limit, viewSummary, plugins.ResultBudget(collections))
}

// fetchFull reads the whole record, for the tools that promise one named
// thing in full. Credentials are still withheld: "full detail" has never
// meant the SNMP community string, and a tool that returned it would be
// handing a model a live credential for a device it was asked to describe.
func (p *Plugin) fetchFull(ctx context.Context, entity Entity, q url.Values, limit int) (Page, error) {
	return p.read(ctx, entity, q, limit, viewFull, plugins.ResultBudget(1))
}

func (p *Plugin) read(ctx context.Context, entity Entity, q url.Values, limit int, v view, budget int) (Page, error) {
	if !p.configured {
		return Page{}, fmt.Errorf("observium: not configured yet — set its " +
			"connection details on the Plugins page")
	}
	if limit <= 0 || limit > p.cfg.MaxItems {
		limit = p.cfg.MaxItems
	}
	page, err := p.client.Read(ctx, entity, q, limit, v, budget)
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
