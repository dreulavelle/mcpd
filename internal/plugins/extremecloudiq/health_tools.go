package extremecloudiq

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// What is unwell, rather than what exists.
//
// Everything here is a POST, and every one of them is a read. The filter is a
// list of site and device ids, which does not fit in a query string, so the
// API takes it in a body -- the same shape Graylog's searches have. See the
// comment at the top of transport.go for why that makes the allow-list the
// right guard rather than a weaker one.

func (p *Plugin) registerHealthTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_device_issues",
		Title: "List devices that are unwell",
		Description: "Which access points or switches are in trouble, and how. " +
			"Ask about health for the box itself — processor, memory, " +
			"temperature, PoE, power supplies, fans, reboots — or about " +
			"capacity for the link and the air: retries, packet loss, " +
			"interference, noise, throughput, congestion. Wired and wireless " +
			"are separate estates in ExtremeCloud IQ and this asks one at a " +
			"time. Unlike extremecloudiq_list_devices, which says whether a " +
			"device is up, this says whether an up device is coping.",
		Idempotent: true,
	}, p.listDeviceIssues)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_client_issues",
		Title: "List clients that are having trouble",
		Description: "Which clients are failing, and at which step. For " +
			"wireless: authentication, association, DHCP and roaming problems " +
			"with signal, noise and airtime beside them. For wired: the switch " +
			"and port each client is on, its VLAN, and whether that port is " +
			"erroring or congested — which is the only way this API will tell " +
			"you what a client is plugged into. Start here for “several people " +
			"in one area cannot get on”; use extremecloudiq_get_client_history " +
			"for one named client.",
		Idempotent: true,
	}, p.listClientIssues)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_sites_with_issues",
		Title: "List sites with problems",
		Description: "Triage across the estate: every site with its alert, " +
			"device health, client health and capacity picture, so a problem " +
			"can be placed before it is diagnosed. Name one site to also get " +
			"its five health scores — network, wifi, client, device and " +
			"services — each a number out of a hundred with the components " +
			"that made it.",
		Idempotent: true,
	}, p.listSitesWithIssues)
}

// DeviceIssuesInput selects which unwell devices to list.
type DeviceIssuesInput struct {
	Kind    string `json:"kind,omitempty" jsonschema:"wireless for access points (default) or wired for switches; they are separate estates and one call asks about one"`
	Concern string `json:"concern,omitempty" jsonschema:"health for the box itself — processor memory temperature PoE power supplies fans (default); capacity for the link and the air — retries packet loss interference throughput congestion"`
	Site    string `json:"site,omitempty" jsonschema:"name of a site building or floor from extremecloudiq_list_locations, to narrow to it"`
	Device  string `json:"device,omitempty" jsonschema:"one device by serial number MAC address hostname or id, to ask about it alone"`
	OnlyBad string `json:"only_unhealthy,omitempty" jsonschema:"yes to return only devices the platform has flagged; omit for every device with its numbers"`
	Limit   int    `json:"limit,omitempty" jsonschema:"most devices to return; the configured ceiling applies whatever this says"`
}

// DeviceIssuesOutput is a listing of devices and how they are coping.
type DeviceIssuesOutput struct {
	// Kind and Concern are echoed because the fields in each row depend on
	// them entirely, and a model reading a row of PoE error slots needs to
	// know it asked the wired question.
	Kind    string `json:"kind"`
	Concern string `json:"concern"`
	// Devices are rows as the API sent them. The shape differs per kind and
	// concern; the note says what to read in them.
	Devices   []Record `json:"devices"`
	Returned  int      `json:"returned"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated,omitempty"`
	Reason    string   `json:"truncation_reason,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// gridFilter is the body every dashboard grid takes.
//
// One struct for all four, because they differ only in which fields they read
// and all four ignore the rest. Sent with omitempty throughout: an empty list
// and an absent one mean the same thing to this API, and sending `"site_ids":
// null` on a filter that means "everywhere" is a way to find out that they do
// not.
type gridFilter struct {
	SiteIDs   []int64 `json:"site_ids,omitempty"`
	DeviceIDs []int64 `json:"device_ids,omitempty"`
	// FilterField is the wired grids' way of asking for only the unwell. The
	// wireless grids use their own booleans instead, which is why this is not
	// one flag shared by all four.
	FilterField []string `json:"filter_field,omitempty"`
	// HasUsageCapacityIssues is the wireless capacity grid's equivalent.
	HasUsageCapacityIssues bool `json:"has_usage_capacity_issues,omitempty"`
}

// gridFor names the endpoint and the note for one combination.
func gridFor(kind, concern string) (path, note string) {
	switch {
	case kind == "wired" && concern == "health":
		return "/dashboard/wired/device-health/grid",
			"Rows carry cpu_usage, memory_usage, temperature and poe_usage, plus " +
				"the slots reporting a fault in temperature_error_slots, " +
				"poe_error_slots, fan_error_slots and psu_error_slots. An empty " +
				"slot string is a switch with nothing wrong in that category."
	case kind == "wired" && concern == "capacity":
		return "/dashboard/wired/usage-capacity/grid",
			"Rows carry total_bandwidth_utilized, throughput in and out, and " +
				"total_queue_congestion_pkts against total_queue_tx_pkts — the " +
				"ratio of those two is the congestion, not the raw count."
	case concern == "capacity":
		return "/dashboard/wireless/usage-capacity/grid",
			"Rows carry per-radio numbers: wifi0/1/2 are the radios, so " +
				"wifi0_retry_score, wifi0_packet_loss, wifi0_interference_score " +
				"and wifi0_noise all describe the same radio. healthy_clients " +
				"against unhealthy_clients says whether it is hurting anybody."
	default:
		return "/dashboard/wireless/device-health/grid",
			"Rows carry cpu_usage_percentage and memory_usage_percentage, plus " +
				"channel_change_count and wifi_reboots_count — an access point " +
				"changing channel repeatedly is interference rather than a fault, " +
				"and one rebooting repeatedly is the opposite."
	}
}

func (p *Plugin) listDeviceIssues(ctx context.Context, in DeviceIssuesInput) (DeviceIssuesOutput, error) {
	if err := p.ready(); err != nil {
		return DeviceIssuesOutput{}, err
	}
	kind, err := oneOf("kind", in.Kind, "wireless", "wireless", "wired")
	if err != nil {
		return DeviceIssuesOutput{}, err
	}
	concern, err := oneOf("concern", in.Concern, "health", "health", "capacity")
	if err != nil {
		return DeviceIssuesOutput{}, err
	}
	onlyBad, err := yesNo("only_unhealthy", in.OnlyBad)
	if err != nil {
		return DeviceIssuesOutput{}, err
	}

	path, note := gridFor(kind, concern)
	filter := gridFilter{}
	if onlyBad == "true" {
		if kind == "wired" {
			// The wired grids narrow by a named filter field; HEALTH is the
			// one that means "anything wrong", and the capacity grid has no
			// equivalent, so there it is left to the caller to read the
			// numbers.
			if concern == "health" {
				filter.FilterField = []string{"HEALTH"}
			}
		} else if concern == "capacity" {
			filter.HasUsageCapacityIssues = true
		}
		// The wireless health grid carries has_device_health_issue on each row
		// rather than taking a filter, so narrowing it is the caller reading
		// that field. Saying so beats sending a filter the endpoint ignores.
	}

	out := DeviceIssuesOutput{Kind: kind, Concern: concern, Note: note}
	var where []string
	if site := strings.TrimSpace(in.Site); site != "" {
		id, described, err := p.locationID(ctx, site)
		if err != nil {
			return DeviceIssuesOutput{}, err
		}
		filter.SiteIDs = []int64{id}
		where = append(where, "At "+described+".")
	}
	if device := strings.TrimSpace(in.Device); device != "" {
		id, err := p.deviceID(ctx, device)
		if err != nil {
			return DeviceIssuesOutput{}, err
		}
		filter.DeviceIDs = []int64{id}
		where = append(where, "For device "+device+".")
	}
	if len(where) > 0 {
		out.Note = strings.Join(where, " ") + " " + out.Note
	}

	got, err := p.client.CollectPost(ctx, path, nil, filter,
		p.limit(in.Limit), 100, plugins.ResultBudget(1))
	p.note(err)
	if err != nil {
		return DeviceIssuesOutput{}, err
	}
	out.Devices, out.Returned, out.Total = got.Rows, len(got.Rows), got.Total
	out.Truncated, out.Reason = got.Truncated, got.Reason
	return out, nil
}

// ClientIssuesInput selects which struggling clients to list.
type ClientIssuesInput struct {
	Connection string `json:"connection,omitempty" jsonschema:"wireless (default) or wired; they are separate estates and one call asks about one"`
	Site       string `json:"site,omitempty" jsonschema:"name of a site building or floor from extremecloudiq_list_locations"`
	Device     string `json:"device,omitempty" jsonschema:"one access point or switch by serial number MAC address hostname or id, to list only the clients on it"`
	Search     string `json:"search,omitempty" jsonschema:"free text matched against the row, for finding one client by hostname or user"`
	Limit      int    `json:"limit,omitempty" jsonschema:"most clients to return; the configured ceiling applies whatever this says"`
}

// ClientIssuesOutput is a listing of clients and where their connection is
// breaking.
type ClientIssuesOutput struct {
	Connection string   `json:"connection"`
	Clients    []Record `json:"clients"`
	Returned   int      `json:"returned"`
	Total      int      `json:"total"`
	Truncated  bool     `json:"truncated,omitempty"`
	Reason     string   `json:"truncation_reason,omitempty"`
	Note       string   `json:"note,omitempty"`
}

func (p *Plugin) listClientIssues(ctx context.Context, in ClientIssuesInput) (ClientIssuesOutput, error) {
	if err := p.ready(); err != nil {
		return ClientIssuesOutput{}, err
	}
	connection, err := oneOf("connection", in.Connection, "wireless", "wireless", "wired")
	if err != nil {
		return ClientIssuesOutput{}, err
	}

	path := "/dashboard/wireless/client-health/grid"
	note := "Rows carry has_authentication_issues, has_association_issues, " +
		"has_ip_address_issues and has_roaming_issues — those four are the " +
		"steps a connection goes through, and the one that is true is the step " +
		"that broke. snr, rssi and air_time say whether the radio is the cause."
	if connection == "wired" {
		path = "/dashboard/wired/client-health/grid"
		note = "Rows carry switch_name and port_number, which is the only place " +
			"this API says what a client is plugged into, with the vlan it " +
			"landed in. has_port_errors, has_port_congestions, " +
			"has_traffic_anomalies and has_ip_address_issues say what is wrong " +
			"with that port; total_port_errors is the count behind the first."
	}

	filter := gridFilter{}
	out := ClientIssuesOutput{Connection: connection, Note: note}
	var where []string
	if site := strings.TrimSpace(in.Site); site != "" {
		id, described, err := p.locationID(ctx, site)
		if err != nil {
			return ClientIssuesOutput{}, err
		}
		filter.SiteIDs = []int64{id}
		where = append(where, "At "+described+".")
	}
	if device := strings.TrimSpace(in.Device); device != "" {
		id, err := p.deviceID(ctx, device)
		if err != nil {
			return ClientIssuesOutput{}, err
		}
		filter.DeviceIDs = []int64{id}
		where = append(where, "On device "+device+".")
	}
	if len(where) > 0 {
		out.Note = strings.Join(where, " ") + " " + out.Note
	}

	params := url.Values{}
	setIf(params, "keyword", in.Search)

	got, err := p.client.CollectPost(ctx, path, params, filter,
		p.limit(in.Limit), 100, plugins.ResultBudget(1))
	p.note(err)
	if err != nil {
		return ClientIssuesOutput{}, err
	}
	out.Clients, out.Returned, out.Total = got.Rows, len(got.Rows), got.Total
	out.Truncated, out.Reason = got.Truncated, got.Reason
	return out, nil
}

// SitesInput narrows the triage listing.
type SitesInput struct {
	Site  string `json:"site,omitempty" jsonschema:"name of one site, which also fetches its five health scores; omit for every site"`
	Limit int    `json:"limit,omitempty" jsonschema:"most sites to return"`
}

// SitesOutput is the estate, placed.
type SitesOutput struct {
	Sites     []Record `json:"sites"`
	Returned  int      `json:"returned"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated,omitempty"`
	// Scores is present only when one site was named. Five numbers out of a
	// hundred with the components behind each, which is a different question
	// from the listing: the listing says where to look and this says how bad.
	Scores   map[string]Record `json:"health_scores,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
	Note     string            `json:"note,omitempty"`
}

// scorecards are the five scores a site carries, in the order somebody reads
// them: the overall picture first, then what it is made of.
var scorecards = []struct{ name, path string }{
	{"network", "networkHealth"},
	{"wifi", "wifiHealth"},
	{"client", "clientHealth"},
	{"device", "deviceHealth"},
	{"services", "servicesHealth"},
}

func (p *Plugin) listSitesWithIssues(ctx context.Context, in SitesInput) (SitesOutput, error) {
	if err := p.ready(); err != nil {
		return SitesOutput{}, err
	}
	filter := gridFilter{}
	var out SitesOutput
	var siteID int64
	if site := strings.TrimSpace(in.Site); site != "" {
		id, described, err := p.locationID(ctx, site)
		if err != nil {
			return SitesOutput{}, err
		}
		filter.SiteIDs, siteID = []int64{id}, id
		out.Note = "At " + described + "."
	}

	got, err := p.client.CollectPost(ctx, "/dashboard/sites-with-issues", nil, filter,
		p.limit(in.Limit), 100, plugins.ResultBudget(2))
	p.note(err)
	if err != nil {
		return SitesOutput{}, err
	}
	out.Sites, out.Returned, out.Total = got.Rows, len(got.Rows), got.Total
	out.Truncated = got.Truncated

	// The scores are per location, so they are only fetched when the caller
	// named one. Five requests for one site is a fair trade for the answer; a
	// hundred sites would be five hundred, which is why the estate-wide
	// listing does not carry them.
	if siteID == 0 {
		return out, nil
	}
	out.Scores = make(map[string]Record, len(scorecards))
	for _, card := range scorecards {
		var score Record
		path := "/network-scorecard/" + card.path + "/" + strconv.FormatInt(siteID, 10)
		if err := p.client.GetInto(ctx, path, nil, &score); err != nil {
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("could not read the %s score: %v", card.name, err))
			continue
		}
		out.Scores[card.name] = score
	}
	return out, nil
}

// oneOf validates a word against a small set, falling back to a default, and
// says what the words are when it does not recognise one.
//
// A word outside the vocabulary is refused rather than silently defaulted: a
// caller who asked for the wired estate and got the wireless one would report
// the absence of a switch as a fact.
func oneOf(field, given, fallback string, allowed ...string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(given))
	if want == "" {
		return fallback, nil
	}
	for _, ok := range allowed {
		if want == ok {
			return want, nil
		}
	}
	return "", fmt.Errorf("extremecloudiq: %s is %q; it is one of %s",
		field, given, strings.Join(allowed, ", "))
}
