package cnmaestro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// Read tools are the plugin's largest surface, and also its prompt-injection
// channel: device names, client hostnames, SSIDs and alarm text all originate
// outside the trust boundary and flow into a model that also holds approval
// tools. Two mitigations run on everything returned here -- untrusted strings
// are sanitised of control and direction-override characters, and every result
// is bounded so a large estate cannot flood a context window.

// registerReadTools declares every read-only tool.
func (p *Plugin) registerReadTools(r *plugins.Registry) {
	type listDevicesIn struct {
		Network    string `json:"network,omitempty" jsonschema:"filter by network name"`
		Site       string `json:"site,omitempty" jsonschema:"filter by site name; requires network"`
		Status     string `json:"status,omitempty" jsonschema:"filter by status: online, offline, onboarding"`
		DeviceType string `json:"device_type,omitempty" jsonschema:"filter by type, e.g. wifi-enterprise, cnmatrix, ePMP"`
		Search     string `json:"search,omitempty" jsonschema:"free-text search over name and MAC"`
		Limit      int    `json:"limit,omitempty" jsonschema:"maximum devices to return, default 50"`
	}
	type listDevicesOut struct {
		Devices []Device `json:"devices"`
		Count   int      `json:"count"`
		Total   int      `json:"total_available"`
		// Truncated tells the model its view is partial. A capped list that
		// does not say so reads as complete, and a model would conclude a
		// device does not exist when it merely fell past the limit.
		Truncated bool   `json:"truncated"`
		Account   string `json:"managed_account"`
	}

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_devices",
		Title: "List devices",
		Description: "List devices managed by cnMaestro, optionally filtered by network, " +
			"site, status or type. Returns identity and status, not live statistics; " +
			"use cnmaestro_device_statistics for operating state.",
		Idempotent: true,
	}, func(ctx context.Context, in listDevicesIn) (listDevicesOut, error) {
		params := url.Values{}
		setIf(params, "network", in.Network)
		setIf(params, "status", in.Status)
		setIf(params, "type", in.DeviceType)
		setIf(params, "search", in.Search)
		if in.Site != "" {
			// The API rejects site without network, which would otherwise
			// surface as an opaque 422.
			if in.Network == "" {
				return listDevicesOut{}, fmt.Errorf(
					"site filtering requires a network; cnMaestro rejects site on its own")
			}
			params.Set("site", in.Site)
		}

		raw, paging, err := p.client.List(ctx, "/devices", params)
		if err != nil {
			return listDevicesOut{}, err
		}

		limit := boundLimit(in.Limit, 50, 200)
		devices := make([]Device, 0, min(len(raw), limit))
		for _, item := range raw[:min(len(raw), limit)] {
			var d Device
			if err := json.Unmarshal(item, &d); err != nil {
				continue
			}
			devices = append(devices, sanitizeDevice(d))
		}
		return listDevicesOut{
			Devices:   devices,
			Count:     len(devices),
			Total:     paging.Total,
			Truncated: len(raw) > len(devices) || paging.Total > len(raw),
			Account:   p.cfg.ManagedAccount,
		}, nil
	})

	type macIn struct {
		MAC string `json:"mac" jsonschema:"the device MAC address, e.g. AA:BB:CC:DD:EE:FF"`
	}

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "get_device",
		Title:       "Get a device",
		Description: "Get full detail for one device, including its configuration overrides.",
		Idempotent:  true,
	}, func(ctx context.Context, in macIn) (Device, error) {
		mac, err := normalizeMAC(in.MAC)
		if err != nil {
			return Device{}, err
		}
		var d Device
		if err := p.client.GetInto(ctx, "/devices/"+mac, nil, &d); err != nil {
			return Device{}, err
		}
		return sanitizeDevice(d), nil
	})

	type statsOut struct {
		MAC    string            `json:"mac"`
		Stats  json.RawMessage   `json:"statistics"`
		Radios []RadioStatistics `json:"radios,omitempty"`
	}

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "device_statistics",
		Title: "Device statistics",
		Description: "Live operating statistics for one device: uptime, CPU, memory, " +
			"client counts, and per-radio channel, power, noise floor and airtime. " +
			"This reports the channel a radio is OPERATING on, which can differ from " +
			"the configured override.",
		Idempotent: true,
	}, func(ctx context.Context, in macIn) (statsOut, error) {
		mac, err := normalizeMAC(in.MAC)
		if err != nil {
			return statsOut{}, err
		}
		var stats DeviceStatistics
		if err := p.client.GetInto(ctx, "/devices/"+mac+"/statistics", nil, &stats); err != nil {
			return statsOut{}, err
		}
		body, _ := json.Marshal(stats)
		return statsOut{MAC: mac, Stats: body, Radios: stats.Radios}, nil
	})

	type listClientsIn struct {
		MAC     string `json:"ap_mac,omitempty" jsonschema:"restrict to clients on one access point"`
		Network string `json:"network,omitempty" jsonschema:"filter by network name"`
		Site    string `json:"site,omitempty" jsonschema:"filter by site name; requires network"`
		Limit   int    `json:"limit,omitempty" jsonschema:"maximum clients to return, default 50"`
	}
	type listClientsOut struct {
		Clients   []WirelessClient `json:"clients"`
		Count     int              `json:"count"`
		Total     int              `json:"total_available"`
		Truncated bool             `json:"truncated"`
	}

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_clients",
		Title: "List wireless clients",
		Description: "List connected wireless clients, with signal strength, band and " +
			"the access point each is associated with.",
		Idempotent: true,
	}, func(ctx context.Context, in listClientsIn) (listClientsOut, error) {
		path := "/devices/clients"
		if in.MAC != "" {
			mac, err := normalizeMAC(in.MAC)
			if err != nil {
				return listClientsOut{}, err
			}
			path = "/devices/" + mac + "/clients"
		}
		params := url.Values{}
		setIf(params, "network", in.Network)
		if in.Site != "" {
			if in.Network == "" {
				return listClientsOut{}, fmt.Errorf(
					"site filtering requires a network; cnMaestro rejects site on its own")
			}
			params.Set("site", in.Site)
		}

		raw, paging, err := p.client.List(ctx, path, params)
		if err != nil {
			return listClientsOut{}, err
		}
		limit := boundLimit(in.Limit, 50, 200)
		clients := make([]WirelessClient, 0, min(len(raw), limit))
		for _, item := range raw[:min(len(raw), limit)] {
			var c WirelessClient
			if err := json.Unmarshal(item, &c); err != nil {
				continue
			}
			clients = append(clients, sanitizeClient(c))
		}
		return listClientsOut{
			Clients: clients, Count: len(clients), Total: paging.Total,
			Truncated: len(raw) > len(clients) || paging.Total > len(raw),
		}, nil
	})

	type alarmsIn struct {
		Severity string `json:"severity,omitempty" jsonschema:"filter by severity: critical, major, minor"`
		Network  string `json:"network,omitempty" jsonschema:"filter by network name"`
		History  bool   `json:"history,omitempty" jsonschema:"return cleared alarms instead of active ones"`
		Limit    int    `json:"limit,omitempty" jsonschema:"maximum alarms to return, default 50"`
	}
	type alarmsOut struct {
		Alarms    []Alarm `json:"alarms"`
		Count     int     `json:"count"`
		Total     int     `json:"total_available"`
		Truncated bool    `json:"truncated"`
	}

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "list_alarms",
		Title:       "List alarms",
		Description: "List active alarms, or cleared ones with history set.",
		Idempotent:  true,
	}, func(ctx context.Context, in alarmsIn) (alarmsOut, error) {
		path := "/alarms"
		if in.History {
			path = "/alarms/history"
		}
		params := url.Values{}
		setIf(params, "severity", in.Severity)
		setIf(params, "network", in.Network)

		raw, paging, err := p.client.List(ctx, path, params)
		if err != nil {
			return alarmsOut{}, err
		}
		limit := boundLimit(in.Limit, 50, 200)
		alarms := make([]Alarm, 0, min(len(raw), limit))
		for _, item := range raw[:min(len(raw), limit)] {
			var a Alarm
			if err := json.Unmarshal(item, &a); err != nil {
				continue
			}
			a.Message = sanitizeText(a.Message)
			a.Name = sanitizeText(a.Name)
			a.Source = sanitizeText(a.Source)
			alarms = append(alarms, a)
		}
		return alarmsOut{
			Alarms: alarms, Count: len(alarms), Total: paging.Total,
			Truncated: len(raw) > len(alarms) || paging.Total > len(raw),
		}, nil
	})

	type eventsIn struct {
		Network string `json:"network,omitempty" jsonschema:"filter by network name"`
		Limit   int    `json:"limit,omitempty" jsonschema:"maximum events to return, default 50"`
	}
	type eventsOut struct {
		Events    []Event `json:"events"`
		Count     int     `json:"count"`
		Truncated bool    `json:"truncated"`
	}

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "list_events",
		Title:       "List events",
		Description: "List recent events logged by cnMaestro.",
		Idempotent:  true,
	}, func(ctx context.Context, in eventsIn) (eventsOut, error) {
		params := url.Values{}
		setIf(params, "network", in.Network)

		raw, _, err := p.client.List(ctx, "/events", params)
		if err != nil {
			return eventsOut{}, err
		}
		limit := boundLimit(in.Limit, 50, 200)
		events := make([]Event, 0, min(len(raw), limit))
		for _, item := range raw[:min(len(raw), limit)] {
			var e Event
			if err := json.Unmarshal(item, &e); err != nil {
				continue
			}
			e.Message = sanitizeText(e.Message)
			e.Name = sanitizeText(e.Name)
			events = append(events, e)
		}
		return eventsOut{Events: events, Count: len(events), Truncated: len(raw) > len(events)}, nil
	})

	type emptyIn struct{}
	type topologyOut struct {
		Networks []Network `json:"networks"`
		Count    int       `json:"count"`
	}

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "list_networks",
		Title:       "List networks",
		Description: "List networks. Note that networks are addressed by NAME, not by a stable id.",
		Idempotent:  true,
	}, func(ctx context.Context, _ emptyIn) (topologyOut, error) {
		raw, _, err := p.client.List(ctx, "/networks", nil)
		if err != nil {
			return topologyOut{}, err
		}
		networks := make([]Network, 0, len(raw))
		for _, item := range raw {
			var n Network
			if err := json.Unmarshal(item, &n); err != nil {
				continue
			}
			n.Name = sanitizeText(n.Name)
			networks = append(networks, n)
		}
		return topologyOut{Networks: networks, Count: len(networks)}, nil
	})

	type sitesIn struct {
		Network string `json:"network" jsonschema:"the network name to list sites for"`
	}
	type sitesOut struct {
		Sites []Site `json:"sites"`
		Count int    `json:"count"`
	}

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "list_sites",
		Title:       "List sites",
		Description: "List the sites within a network.",
		Idempotent:  true,
	}, func(ctx context.Context, in sitesIn) (sitesOut, error) {
		if strings.TrimSpace(in.Network) == "" {
			return sitesOut{}, fmt.Errorf("network is required")
		}
		raw, _, err := p.client.List(ctx,
			"/networks/"+url.PathEscape(in.Network)+"/sites", nil)
		if err != nil {
			return sitesOut{}, err
		}
		sites := make([]Site, 0, len(raw))
		for _, item := range raw {
			var s Site
			if err := json.Unmarshal(item, &s); err != nil {
				continue
			}
			s.Name = sanitizeText(s.Name)
			sites = append(sites, s)
		}
		return sitesOut{Sites: sites, Count: len(sites)}, nil
	})

	type groupsOut struct {
		APGroups []APGroup `json:"ap_groups"`
		Count    int       `json:"count"`
	}

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_ap_groups",
		Title: "List AP groups",
		Description: "List Enterprise Wi-Fi AP groups. A device's ap_group must be " +
			"supplied whenever its configuration overrides are changed.",
		Idempotent: true,
	}, func(ctx context.Context, _ emptyIn) (groupsOut, error) {
		raw, _, err := p.client.List(ctx, "/wifi_enterprise/ap_groups", nil)
		if err != nil {
			return groupsOut{}, err
		}
		groups := make([]APGroup, 0, len(raw))
		for _, item := range raw {
			var g APGroup
			if err := json.Unmarshal(item, &g); err != nil {
				continue
			}
			g.Name = sanitizeText(g.Name)
			g.Description = sanitizeText(g.Description)
			groups = append(groups, g)
		}
		return groupsOut{APGroups: groups, Count: len(groups)}, nil
	})

	type wlansOut struct {
		WLANs []WLAN `json:"wlans"`
		Count int    `json:"count"`
	}

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "list_wlans",
		Title:       "List WLANs",
		Description: "List Enterprise Wi-Fi WLAN definitions.",
		Idempotent:  true,
	}, func(ctx context.Context, _ emptyIn) (wlansOut, error) {
		raw, _, err := p.client.List(ctx, "/wifi_enterprise/wlans", nil)
		if err != nil {
			return wlansOut{}, err
		}
		wlans := make([]WLAN, 0, len(raw))
		for _, item := range raw {
			var w WLAN
			if err := json.Unmarshal(item, &w); err != nil {
				continue
			}
			w.Name = sanitizeText(w.Name)
			w.SSID = sanitizeText(w.SSID)
			w.Description = sanitizeText(w.Description)
			wlans = append(wlans, w)
		}
		return wlansOut{WLANs: wlans, Count: len(wlans)}, nil
	})
}

// --- helpers ---------------------------------------------------------------

func setIf(v url.Values, key, value string) {
	if strings.TrimSpace(value) != "" {
		v.Set(key, value)
	}
}

func boundLimit(requested, fallback, max int) int {
	if requested <= 0 {
		return fallback
	}
	if requested > max {
		return max
	}
	return requested
}

// normalizeMAC validates and canonicalises a MAC address.
//
// The API requires exactly 17 characters in colon-separated upper case, and
// rejecting a malformed value here produces a message a model can act on
// rather than an opaque 422 from upstream.
func normalizeMAC(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "-", ":")
	s = strings.ToUpper(s)

	// Accept an unseparated form and insert separators.
	if len(s) == 12 && !strings.Contains(s, ":") {
		var b strings.Builder
		for i := 0; i < 12; i += 2 {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(s[i : i+2])
		}
		s = b.String()
	}

	if len(s) != 17 {
		return "", fmt.Errorf("%q is not a MAC address; expected the form AA:BB:CC:DD:EE:FF", raw)
	}
	for i, r := range s {
		if (i+1)%3 == 0 {
			if r != ':' {
				return "", fmt.Errorf("%q is not a MAC address; expected colons as separators", raw)
			}
			continue
		}
		if !isHexDigit(r) {
			return "", fmt.Errorf("%q is not a MAC address; %q is not a hex digit", raw, r)
		}
	}
	return s, nil
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'A' && r <= 'F')
}

// sanitizeText cleans a string that originated outside the trust boundary.
//
// Device names, SSIDs, client hostnames and alarm text are all set by people
// and equipment on the far side of the API, and they flow into a model that
// also holds approval tools. Stripping control characters and direction
// overrides removes the mechanisms that make injected text look like something
// other than data.
func sanitizeText(s string) string {
	if s == "" {
		return ""
	}
	const maxLen = 256

	var b strings.Builder
	b.Grow(len(s))
	count := 0
	for _, r := range s {
		if count >= maxLen {
			b.WriteString("…")
			break
		}
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			// Newlines let injected text imitate a new message or section.
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// Other control characters carry no legitimate meaning here.
			continue
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			// Bidirectional overrides can make text render as something other
			// than what it contains.
			continue
		default:
			b.WriteRune(r)
		}
		count++
	}
	return strings.TrimSpace(b.String())
}

func sanitizeDevice(d Device) Device {
	d.Name = sanitizeText(d.Name)
	d.Description = sanitizeText(d.Description)
	d.Network = sanitizeText(d.Network)
	d.Site = sanitizeText(d.Site)
	d.Tower = sanitizeText(d.Tower)
	d.APGroup = sanitizeText(d.APGroup)
	return d
}

func sanitizeClient(c WirelessClient) WirelessClient {
	c.Name = sanitizeText(c.Name)
	c.SSID = sanitizeText(c.SSID)
	c.APName = sanitizeText(c.APName)
	c.Manufacturer = sanitizeText(c.Manufacturer)
	c.OS = sanitizeText(c.OS)
	c.Site = sanitizeText(c.Site)
	c.Network = sanitizeText(c.Network)
	return c
}
