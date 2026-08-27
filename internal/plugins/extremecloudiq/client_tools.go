package extremecloudiq

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// Who is connected.

func (p *Plugin) registerClientTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_clients",
		Title: "List connected clients",
		Description: "Who is connected right now, wireless or wired. Narrow it " +
			"with a device, a site, an SSID, or a search over hostnames, MAC " +
			"addresses, user names and IP addresses — an estate-wide listing " +
			"is truncated on any real network, and the result says how many " +
			"exist. Ask for the metrics view when the question is “why is this " +
			"connection bad”: it carries the health scores, signal and noise " +
			"rather than the identity fields.",
		Idempotent: true,
	}, p.listClients)
}

// clientViews are the field sets the API offers for a client.
var clientViews = map[string]string{
	"basic":    "BASIC",
	"status":   "STATUS",
	"detail":   "DETAIL",
	"location": "LOCATION",
	"metrics":  "METRICS",
	"full":     "FULL",
}

// ClientsInput selects whose clients to list.
type ClientsInput struct {
	Device     string `json:"device,omitempty" jsonschema:"one access point or switch by serial number MAC address hostname or id, to list only its clients"`
	Site       string `json:"site,omitempty" jsonschema:"name of a site building or floor from extremecloudiq_list_locations"`
	SSID       string `json:"ssid,omitempty" jsonschema:"exact SSID name, to list only clients on that wireless network"`
	Connection string `json:"connection,omitempty" jsonschema:"wireless or wired; omit for both"`
	Health     string `json:"health,omitempty" jsonschema:"healthy or poor, as ExtremeCloud IQ scores the connection; omit for both"`
	Search     string `json:"search,omitempty" jsonschema:"free text matched against hostname MAC address user name and IP address"`
	View       string `json:"view,omitempty" jsonschema:"how many fields per client: basic (default) status detail location metrics or full"`
	Limit      int    `json:"limit,omitempty" jsonschema:"most clients to return; the configured ceiling applies whatever this says"`
}

// ClientsOutput is a listing of connected clients.
type ClientsOutput struct {
	Clients   []Record `json:"clients"`
	Returned  int      `json:"returned"`
	Total     int      `json:"total"`
	Truncated bool     `json:"truncated,omitempty"`
	Reason    string   `json:"truncation_reason,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listClients(ctx context.Context, in ClientsInput) (ClientsOutput, error) {
	if err := p.ready(); err != nil {
		return ClientsOutput{}, err
	}
	view, err := pickView("view", in.View, clientViews, "basic")
	if err != nil {
		return ClientsOutput{}, err
	}

	params := url.Values{"views": {view}}
	setIf(params, "ssids", in.SSID)
	setIf(params, "searchString", in.Search)

	switch strings.ToLower(strings.TrimSpace(in.Connection)) {
	case "", "any", "both":
	case "wireless", "wifi", "wi-fi":
		params.Set("clientConnectionTypes", "1")
	case "wired", "ethernet":
		params.Set("clientConnectionTypes", "2")
	default:
		return ClientsOutput{}, fmt.Errorf("extremecloudiq: connection is %q; it "+
			"is wireless or wired, or omitted for both", in.Connection)
	}

	switch strings.ToLower(strings.TrimSpace(in.Health)) {
	case "", "any":
	case "healthy", "good":
		params.Set("clientHealthStatus", "1")
	case "poor", "bad", "unhealthy":
		params.Set("clientHealthStatus", "2")
	default:
		return ClientsOutput{}, fmt.Errorf("extremecloudiq: health is %q; it is "+
			"healthy or poor, or omitted for both", in.Health)
	}

	var notes []string
	if device := strings.TrimSpace(in.Device); device != "" {
		id, err := p.deviceID(ctx, device)
		if err != nil {
			return ClientsOutput{}, err
		}
		params.Set("deviceIds", strconv.FormatInt(id, 10))
		notes = append(notes, "Clients on device "+device+".")
	}
	if site := strings.TrimSpace(in.Site); site != "" {
		id, where, err := p.locationID(ctx, site)
		if err != nil {
			return ClientsOutput{}, err
		}
		params.Set("locationIds", strconv.FormatInt(id, 10))
		notes = append(notes, "Clients at "+where+".")
	}

	got, err := p.client.Collect(ctx, "/clients/active", params,
		p.limit(in.Limit), 100, plugins.ResultBudget(1))
	p.note(err)
	if err != nil {
		return ClientsOutput{}, err
	}
	return ClientsOutput{
		Clients: got.Rows, Returned: len(got.Rows), Total: got.Total,
		Truncated: got.Truncated, Reason: got.Reason,
		Note: strings.Join(notes, " "),
	}, nil
}
