package extremecloudiq

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// What is deployed, and how one of them is.

func (p *Plugin) registerDeviceTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_devices",
		Title: "List access points and switches",
		Description: "The devices this ExtremeCloud IQ account manages, with " +
			"whether each is connected. Narrow it with a site, a connection " +
			"state, or a hostname, serial number or MAC — an estate-wide " +
			"listing is truncated on any real network, and the result says how " +
			"many exist so a short answer is not read as a small estate. " +
			"Fields come back in the view you ask for; the default is enough " +
			"to identify a device and say whether it is up.",
		Idempotent: true,
	}, p.listDevices)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_device",
		Title: "Get one device",
		Description: "Everything about one access point or switch: its " +
			"details, where it is, and which network policy it runs. Name it " +
			"by serial number, MAC address, hostname or ExtremeCloud IQ id — " +
			"whichever is to hand. Use this rather than filtering the listing " +
			"when the question is about a single device.",
		Idempotent: true,
	}, p.getDevice)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_device_health",
		Title: "Get one device's health over a window",
		Description: "How one device has been behaving: processor and memory " +
			"over time, per-radio statistics — channel, utilisation, noise, " +
			"retries, clients — and the alarms it raised. This is the tool for " +
			"“that access point is slow”, where the device is up and the " +
			"listing says nothing is wrong. Also counts the client failures " +
			"this device saw — authentication, association and DHCP — which is " +
			"what separates a sick access point from a healthy one serving a " +
			"broken network. Covers the last day unless you name a window, and " +
			"says which window it covered.",
		Idempotent: true,
	}, p.getDeviceHealth)
}

// deviceViews are the field sets the API offers, and what each is for.
//
// Exposed rather than hidden because the choice is a real one: a device
// carries forty fields, FULL returns all of them, and a hundred devices in
// FULL is most of a conversation. The default is the narrow end.
var deviceViews = map[string]string{
	"basic":    "BASIC",
	"status":   "STATUS",
	"location": "LOCATION",
	"detail":   "DETAIL",
	"full":     "FULL",
}

// DevicesInput selects which devices to list.
type DevicesInput struct {
	Site      string `json:"site,omitempty" jsonschema:"name of a site building or floor from extremecloudiq_list_locations, to list only what is there"`
	Connected string `json:"connected,omitempty" jsonschema:"yes for only connected devices no for only disconnected; omit for both"`
	Hostname  string `json:"hostname,omitempty" jsonschema:"exact hostname of one device"`
	Serial    string `json:"serial,omitempty" jsonschema:"exact serial number of one device"`
	MAC       string `json:"mac,omitempty" jsonschema:"exact MAC address of one device"`
	View      string `json:"view,omitempty" jsonschema:"how many fields per device: basic (default) status location detail or full"`
	Limit     int    `json:"limit,omitempty" jsonschema:"most devices to return; the configured ceiling applies whatever this says"`
}

// DevicesOutput is a listing of devices.
type DevicesOutput struct {
	// Devices are rows as the API sent them, holding the fields the requested
	// view carries.
	Devices []Record `json:"devices"`
	// Returned is len(Devices), stated because a model counting a long list
	// gets it wrong and this is the number it is usually about to reason with.
	Returned int `json:"returned"`
	// Total is how many match, across every page. The number that makes a
	// truncated answer readable as one.
	Total     int    `json:"total"`
	Truncated bool   `json:"truncated,omitempty"`
	Reason    string `json:"truncation_reason,omitempty"`
	Note      string `json:"note,omitempty"`
}

func (p *Plugin) listDevices(ctx context.Context, in DevicesInput) (DevicesOutput, error) {
	if err := p.ready(); err != nil {
		return DevicesOutput{}, err
	}
	view, err := pickView("view", in.View, deviceViews, "basic")
	if err != nil {
		return DevicesOutput{}, err
	}

	params := url.Values{"views": {view}}
	switch strings.ToLower(strings.TrimSpace(in.Connected)) {
	case "", "any", "either":
	case "yes", "true", "connected":
		params.Set("connected", "true")
	case "no", "false", "disconnected":
		params.Set("connected", "false")
	default:
		return DevicesOutput{}, fmt.Errorf("extremecloudiq: connected is %q; it "+
			"is yes or no, or omitted for both", in.Connected)
	}
	setIf(params, "hostnames", in.Hostname)
	setIf(params, "sns", in.Serial)
	setIf(params, "macAddresses", in.MAC)

	var note string
	if site := strings.TrimSpace(in.Site); site != "" {
		id, where, err := p.locationID(ctx, site)
		if err != nil {
			return DevicesOutput{}, err
		}
		params.Set("locationIds", strconv.FormatInt(id, 10))
		note = "Devices at " + where + "."
	}

	got, err := p.client.Collect(ctx, "/devices", params,
		p.limit(in.Limit), 100, plugins.ResultBudget(1))
	p.note(err)
	if err != nil {
		return DevicesOutput{}, err
	}
	return DevicesOutput{
		Devices: got.Rows, Returned: len(got.Rows), Total: got.Total,
		Truncated: got.Truncated, Reason: got.Reason, Note: note,
	}, nil
}

// DeviceInput names one device.
type DeviceInput struct {
	Device string `json:"device" jsonschema:"the device: its serial number MAC address hostname or ExtremeCloud IQ id; a value that is only digits is read as an id"`
}

// DeviceOutput is one device and the two things about it that live elsewhere.
//
// Three reads rather than one, because the API keeps them apart and the
// question does not: somebody asking about an access point wants to know where
// it is and what it is running, and being told to make two more calls for that
// is the shape this integration exists to avoid.
type DeviceOutput struct {
	Device Record `json:"device"`
	// Location is where ExtremeCloud IQ places it, or absent if it is not
	// placed. Absent rather than empty: an unplaced device is a real state and
	// an empty object reads like a failed read.
	Location Record `json:"location,omitempty"`
	// NetworkPolicy is the policy it runs, or absent if none is assigned.
	NetworkPolicy Record `json:"network_policy,omitempty"`
	// Warnings names a part that could not be read. The device itself is the
	// answer; a token without the scope for policies should still get it.
	Warnings []string `json:"warnings,omitempty"`
}

func (p *Plugin) getDevice(ctx context.Context, in DeviceInput) (DeviceOutput, error) {
	if err := p.ready(); err != nil {
		return DeviceOutput{}, err
	}
	id, err := p.deviceID(ctx, in.Device)
	if err != nil {
		return DeviceOutput{}, err
	}

	base := "/devices/" + strconv.FormatInt(id, 10)
	var device Record
	if err := p.client.GetInto(ctx, base, url.Values{"views": {"DETAIL"}}, &device); err != nil {
		p.note(err)
		return DeviceOutput{}, err
	}
	p.note(nil)

	out := DeviceOutput{Device: device}
	// The two extras are best-effort. They are separate endpoints with
	// separate scopes, and failing the whole answer because a token cannot
	// read policies would hide the device somebody asked about.
	var location Record
	if err := p.client.GetInto(ctx, base+"/location", nil, &location); err != nil {
		out.Warnings = append(out.Warnings, "could not read where this device is: "+err.Error())
	} else if len(location) > 0 {
		out.Location = location
	}
	var policy Record
	if err := p.client.GetInto(ctx, base+"/network-policy", nil, &policy); err != nil {
		out.Warnings = append(out.Warnings, "could not read this device's network policy: "+err.Error())
	} else if len(policy) > 0 {
		out.NetworkPolicy = policy
	}
	return out, nil
}

// DeviceHealthInput names one device and a window.
type DeviceHealthInput struct {
	Device string `json:"device" jsonschema:"the device: its serial number MAC address hostname or ExtremeCloud IQ id; a value that is only digits is read as an id"`
	timeArgs
	IntervalMinutes int `json:"interval_minutes,omitempty" jsonschema:"how coarsely to aggregate the processor and memory series, in minutes; the minimum and the default is 10"`
	Limit           int `json:"limit,omitempty" jsonschema:"most alarms and most samples to return"`
}

// DeviceHealthOutput is one device's behaviour over a window.
type DeviceHealthOutput struct {
	// DeviceID is echoed because the caller may have named the device by
	// serial or hostname, and every other tool here takes an id.
	DeviceID int64 `json:"device_id"`
	// Window is the range actually covered. Reported on every call: a caller
	// who named no window needs to know which one they were given.
	Window string `json:"window"`
	// Radios are per-interface statistics — channel, utilisation, noise,
	// retries, clients. Empty on a switch, which has none.
	Radios []Record `json:"radios,omitempty"`
	// CPUMemory is the processor and memory series, oldest first.
	CPUMemory []Record `json:"cpu_memory,omitempty"`
	// Alarms are what this device raised in the window.
	Alarms []Record `json:"alarms,omitempty"`
	// ClientFailures counts what went wrong for the clients on this device:
	// authentication failures, association failures, address problems, and how
	// many access points saw excessive packet loss per band. A device whose
	// own numbers are fine and whose clients are all failing authentication is
	// a RADIUS problem, not a hardware one -- and that distinction is not
	// visible in any of the three series above.
	ClientFailures Record   `json:"client_failures,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
	Note           string   `json:"note,omitempty"`
}

func (p *Plugin) getDeviceHealth(ctx context.Context, in DeviceHealthInput) (DeviceHealthOutput, error) {
	if err := p.ready(); err != nil {
		return DeviceHealthOutput{}, err
	}
	w, err := p.cfg.resolve(in.timeArgs, p.deps.Now())
	if err != nil {
		return DeviceHealthOutput{}, err
	}
	id, err := p.deviceID(ctx, in.Device)
	if err != nil {
		return DeviceHealthOutput{}, err
	}

	base := "/devices/" + strconv.FormatInt(id, 10)
	out := DeviceHealthOutput{DeviceID: id, Window: w.describe()}
	// Three collections in one result, so each gets a third of the budget --
	// a composite answer is still one tool result, and three collections each
	// bounded at the whole budget is a result three times past it. The
	// failure counts are a single small object and take no share.
	budget := plugins.ResultBudget(3)
	limit := p.limit(in.Limit)

	radios := url.Values{}
	w.apply(radios)
	var radioRows []Record
	if err := p.client.GetInto(ctx, base+"/interfaces/wifi", radios, &radioRows); err != nil {
		out.Warnings = append(out.Warnings, "could not read radio statistics: "+err.Error())
	} else {
		out.Radios = capRows(radioRows, limit, budget)
	}

	series := url.Values{}
	w.apply(series)
	interval := in.IntervalMinutes
	if interval < 10 {
		// The API's own minimum. Sent explicitly rather than omitted so the
		// series a caller gets is the one they can reason about the shape of.
		interval = 10
	}
	series.Set("interval", strconv.Itoa(interval))
	var cpuRows []Record
	if err := p.client.GetInto(ctx, base+"/history/cpu-mem", series, &cpuRows); err != nil {
		out.Warnings = append(out.Warnings, "could not read processor and memory history: "+err.Error())
	} else {
		out.CPUMemory = capRows(cpuRows, limit, budget)
	}

	alarms := url.Values{}
	w.apply(alarms)
	got, err := p.client.Collect(ctx, base+"/alarms", alarms, limit, 100, budget)
	if err != nil {
		out.Warnings = append(out.Warnings, "could not read alarms: "+err.Error())
	} else {
		out.Alarms = got.Rows
		if got.Truncated {
			out.Note = got.Reason
		}
	}

	issues := url.Values{"deviceId": {strconv.FormatInt(id, 10)}}
	w.apply(issues)
	var failures Record
	if err := p.client.GetInto(ctx, "/d360/device/issues", issues, &failures); err != nil {
		out.Warnings = append(out.Warnings, "could not count client failures: "+err.Error())
	} else if len(failures) > 0 {
		out.ClientFailures = failures
	}

	p.note(nil)
	if len(out.Radios) == 0 && len(out.CPUMemory) == 0 && len(out.Alarms) == 0 &&
		len(out.ClientFailures) == 0 && len(out.Warnings) == 0 {
		// Nothing wrong, and nothing to show. Said plainly, because three
		// empty collections read as a failed call.
		out.Note = "ExtremeCloud IQ holds no samples or alarms for this device " +
			"in " + w.describe() + ". A device that has just been onboarded, or " +
			"one that has been offline for the whole window, looks like this."
	}
	return out, nil
}

// deviceID resolves whatever a caller named into the id every endpoint takes.
//
// A model has whichever identifier was in front of it -- a serial from an
// asset list, a MAC from a switch table, a hostname from a ticket -- and
// ExtremeCloud IQ keys everything on a numeric id that appears nowhere else.
// Making the caller look it up first would be a round trip per question, and
// the failure when they guess wrong is a 404 that says the device does not
// exist.
func (p *Plugin) deviceID(ctx context.Context, named string) (int64, error) {
	name := strings.TrimSpace(named)
	if name == "" {
		return 0, fmt.Errorf("extremecloudiq: name a device — its serial " +
			"number, MAC address, hostname, or ExtremeCloud IQ id")
	}
	// All digits is read as an id, and the tool descriptions say so. Nothing
	// else here is purely numeric in practice -- an Extreme serial carries
	// letters and a MAC is written with colons -- but "read as an id" is
	// specified rather than inferred, because the one shape that would collide
	// is a MAC typed without separators and a caller who does that should be
	// told what happens rather than left to wonder.
	//
	// Locations do not get this shortcut: floors are called "1" in every
	// building anybody has named, so there the number is resolved as a name
	// too. See locationID.
	if id, err := strconv.ParseInt(name, 10, 64); err == nil && id > 0 {
		return id, nil
	}

	// Asked one filter at a time rather than all three at once.
	//
	// The API takes sns, macAddresses and hostnames as separate parameters and
	// does not document whether unlike filters are combined or alternated. If
	// they are combined -- which is how every other filter on this endpoint
	// behaves -- then sending all three would match nothing, and the failure
	// would be a device that plainly exists reporting as absent. That is the
	// worst answer available here, so it costs a round trip to not risk it: a
	// serial is tried first because it is the identifier somebody quoting one
	// usually has, and the loop stops at the first filter that finds anything.
	for _, filter := range []string{"sns", "macAddresses", "hostnames"} {
		params := url.Values{"views": {"BASIC"}, filter: {name}}
		got, err := p.client.Collect(ctx, "/devices", params, 5, 5, 0)
		if err != nil {
			p.note(err)
			return 0, err
		}
		switch len(got.Rows) {
		case 0:
			continue
		case 1:
			return recordID(got.Rows[0], name)
		default:
			return 0, fmt.Errorf("extremecloudiq: %q matches %d devices, so it "+
				"does not say which one. Use the serial number, which is unique",
				name, len(got.Rows))
		}
	}
	return 0, fmt.Errorf("extremecloudiq: no device here is called %q. It is "+
		"matched exactly against serial numbers, MAC addresses and hostnames — "+
		"a partial name will not find one. Use extremecloudiq_list_devices to "+
		"see what exists", name)
}

// recordID pulls the numeric id out of a row.
func recordID(row Record, named string) (int64, error) {
	switch v := row["id"].(type) {
	case float64:
		// JSON numbers decode as float64. An ExtremeCloud IQ id is well inside
		// what a float64 holds exactly, so this is lossless in practice; the
		// conversion is here rather than a json.Number decode because every
		// other field of these rows is passed through untouched.
		return int64(v), nil
	case string:
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			return id, nil
		}
	}
	return 0, fmt.Errorf("extremecloudiq: found %q but the API did not give it "+
		"a numeric id, so nothing else can be asked about it", named)
}

// limit applies the operator's ceiling to whatever the caller asked for.
//
// The ceiling wins in both directions: a caller asking for more gets the
// ceiling, and a caller asking for nothing gets it too. A caller asking for
// less gets what they asked for, because a model that wants five rows should
// not be sent two hundred.
func (p *Plugin) limit(want int) int {
	if want <= 0 || want > p.cfg.MaxItems {
		return p.cfg.MaxItems
	}
	return want
}

// capRows bounds a collection the API returned whole.
//
// Several endpoints here are not paginated at all -- they answer with an array
// and no envelope -- so the ceilings the page walk applies have to be applied
// afterwards instead. Same two ceilings, same order.
func capRows(rows []Record, limit, budget int) []Record {
	out := make([]Record, 0, len(rows))
	spent := 0
	for _, row := range rows {
		if limit > 0 && len(out) >= limit {
			break
		}
		cost := rowBytes(row)
		if budget > 0 && spent+cost > budget && len(out) > 0 {
			break
		}
		spent += cost
		out = append(out, row)
	}
	return out
}

// pickView turns a caller's word into the API's, or says what the words are.
func pickView(field, given string, allowed map[string]string, fallback string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(given))
	if want == "" {
		want = fallback
	}
	if view, ok := allowed[want]; ok {
		return view, nil
	}
	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	slices.Sort(names)
	return "", fmt.Errorf("extremecloudiq: %s is %q; it is one of %s",
		field, given, strings.Join(names, ", "))
}

// setIf adds a parameter only when the caller supplied one, so an empty filter
// is absent rather than sent as an empty string -- which several of these
// endpoints treat as a filter matching nothing.
func setIf(params url.Values, key, value string) {
	if v := strings.TrimSpace(value); v != "" {
		params.Set(key, v)
	}
}
