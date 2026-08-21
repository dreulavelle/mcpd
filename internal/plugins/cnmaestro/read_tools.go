package cnmaestro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// macPattern is what cnMaestro accepts as a device identifier.
//
// Checked before the call rather than after, so a mistyped address is a clear
// message instead of a 404 that reads as "no such device" and invites the
// model to conclude the estate is missing something.
var macPattern = regexp.MustCompile(`^[0-9A-Fa-f]{2}([:-]?[0-9A-Fa-f]{2}){5}$`)

// Register implements plugins.Plugin.
//
// Read tools only. The write surface comes later, once these have been run
// against a real controller.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "networks",
		Title: "List networks",
		Description: "Lists the networks in this cnMaestro account. Start here " +
			"when you do not know what exists: network names are what most " +
			"other filters take.",
		Idempotent: true,
	}, p.listNetworks)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "devices",
		Title: "List devices",
		Description: "Lists devices, newest state first. Filter by network, " +
			"site, tower, type, or whether the device is online. Prefer a " +
			"filter to listing everything: a large estate is truncated, and " +
			"the result says so when it was.",
		Idempotent: true,
	}, p.listDevices)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "managed_accounts",
		Title: "List managed accounts",
		Description: "Lists the MSP managed accounts (tenants) in this " +
			"installation, with each one's status. Use this to find the exact " +
			"name to configure, since matching is case-sensitive, and to see " +
			"whether a tenant is disabled -- a disabled tenant can own visible " +
			"data and still reject every call that names it. Only available " +
			"when the account has the MSP feature.",
		Idempotent: true,
	}, p.listManagedAccounts)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "device",
		Title: "Get one device",
		Description: "Returns the full record for a single device by MAC " +
			"address. Use this after cnmaestro_devices has narrowed to one, " +
			"since the list omits fields this returns.",
		Idempotent: true,
	}, p.getDevice)

	return nil
}

// NetworksInput takes no arguments. Networks are the top of the hierarchy and
// there is nothing above them to filter by.
type NetworksInput struct{}

// NetworksOutput is a list of networks.
type NetworksOutput struct {
	Networks []json.RawMessage `json:"networks"`
	Count    int               `json:"count"`
	// Total is what the API said exists, which differs from Count when a
	// result was truncated.
	Total int `json:"total,omitempty"`
	// Warnings are the API's own, passed through. It answers 200 with a
	// partial result when part of an estate is unreachable, and a caller that
	// drops these reports incomplete data as complete.
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (p *Plugin) listNetworks(ctx context.Context, _ NetworksInput) (NetworksOutput, error) {
	page, err := p.client.List(ctx, "/networks", nil)
	p.note(err)
	if err != nil {
		return NetworksOutput{}, err
	}
	return NetworksOutput{
		Networks:  page.Items,
		Count:     len(page.Items),
		Total:     page.Total,
		Warnings:  page.Warnings,
		Truncated: page.Truncated,
	}, nil
}

// DevicesInput filters a device listing.
//
// Every field is optional, and each maps to a documented query parameter. The
// model is told to prefer filters because an unfiltered estate is the case
// that gets truncated.
type DevicesInput struct {
	Network string `json:"network,omitempty" jsonschema:"limit to one network, by name"`
	Site    string `json:"site,omitempty" jsonschema:"limit to one site, by name"`
	Tower   string `json:"tower,omitempty" jsonschema:"limit to one tower, by name"`
	Type    string `json:"type,omitempty" jsonschema:"limit to one device type, such as cnmatrix, wifi-enterprise, pmp, or nse"`
	Online  string `json:"online,omitempty" jsonschema:"true for only online devices, false for only offline; omit for both"`
	Search  string `json:"search,omitempty" jsonschema:"free-text match on name, MAC, or serial"`
}

// DevicesOutput is a list of devices.
//
// Devices are passed through as raw records rather than decoded into a struct.
// The API returns a oneOf across device types — cnmatrix, cnwave60, enterprise
// Wi-Fi, NSE and more — each with its own fields and a type discriminator.
// There is no common shape, and inventing one would drop whatever the caller
// actually asked about.
type DevicesOutput struct {
	Devices   []json.RawMessage `json:"devices"`
	Count     int               `json:"count"`
	Total     int               `json:"total,omitempty"`
	Warnings  []string          `json:"warnings,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
	// Note explains a truncated result in words, since that is the one
	// outcome a model is likely to misread as "this is the whole estate".
	Note string `json:"note,omitempty"`
}

func (p *Plugin) listDevices(ctx context.Context, in DevicesInput) (DevicesOutput, error) {
	params := url.Values{}
	setIf(params, "network", in.Network)
	setIf(params, "site", in.Site)
	setIf(params, "tower", in.Tower)
	setIf(params, "type", in.Type)
	setIf(params, "search", in.Search)

	if raw := strings.TrimSpace(in.Online); raw != "" {
		switch strings.ToLower(raw) {
		case "true", "false":
			params.Set("online", strings.ToLower(raw))
		default:
			return DevicesOutput{}, fmt.Errorf(
				"online must be true or false, got %q", in.Online)
		}
	}

	page, err := p.client.List(ctx, "/devices", params)
	p.note(err)
	if err != nil {
		return DevicesOutput{}, err
	}

	out := DevicesOutput{
		Devices:   page.Items,
		Count:     len(page.Items),
		Total:     page.Total,
		Warnings:  page.Warnings,
		Truncated: page.Truncated,
	}
	if page.Truncated {
		out.Note = fmt.Sprintf(
			"Stopped at %d devices; the estate has more. Narrow with network, "+
				"site, tower, type, or search rather than treating this as the "+
				"whole estate.", len(page.Items))
	}
	return out, nil
}

// ManagedAccountsInput takes no arguments.
type ManagedAccountsInput struct{}

// ManagedAccountsOutput is the authoritative tenant list.
type ManagedAccountsOutput struct {
	Accounts []json.RawMessage `json:"accounts"`
	Count    int               `json:"count"`
	Warnings []string          `json:"warnings,omitempty"`
	// Note carries the reserved name, because it is not in the list and is
	// the value a single-account installation actually needs.
	Note string `json:"note"`
}

func (p *Plugin) listManagedAccounts(ctx context.Context, _ ManagedAccountsInput) (ManagedAccountsOutput, error) {
	page, err := p.client.List(ctx, "/msp/managed_accounts", nil)
	p.note(err)
	if err != nil {
		return ManagedAccountsOutput{}, err
	}
	return ManagedAccountsOutput{
		Accounts: page.Items,
		Count:    len(page.Items),
		Warnings: page.Warnings,
		Note: fmt.Sprintf("These are MSP tenants. The Main Account is named %q, "+
			"which is a reserved value and does not appear in this list.", MainAccount),
	}, nil
}

// DeviceInput names one device.
type DeviceInput struct {
	MAC string `json:"mac" jsonschema:"the device's MAC address, with or without separators"`
}

// DeviceOutput is one device's full record.
type DeviceOutput struct {
	Device   json.RawMessage `json:"device"`
	Warnings []string        `json:"warnings,omitempty"`
}

func (p *Plugin) getDevice(ctx context.Context, in DeviceInput) (DeviceOutput, error) {
	mac := strings.TrimSpace(in.MAC)
	if !macPattern.MatchString(mac) {
		return DeviceOutput{}, fmt.Errorf(
			"%q is not a MAC address; expected something like AA:BB:CC:DD:EE:FF", in.MAC)
	}

	// The device record arrives as a single-element array on this endpoint,
	// not a bare object, so it is decoded as one and unwrapped.
	var records []json.RawMessage
	warnings, err := p.client.Get(ctx, "/devices/"+url.PathEscape(mac), nil, &records)
	p.note(err)
	if err != nil {
		return DeviceOutput{}, err
	}
	if len(records) == 0 {
		return DeviceOutput{}, fmt.Errorf("cnmaestro: no device with MAC %s", mac)
	}
	return DeviceOutput{Device: records[0], Warnings: warnings}, nil
}

// setIf adds a parameter only when it carries a value, so an omitted filter is
// absent rather than present and empty. The two are not the same to the API.
func setIf(params url.Values, key, value string) {
	if v := strings.TrimSpace(value); v != "" {
		params.Set(key, v)
	}
}
