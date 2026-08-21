package cnmaestro

import (
	"context"
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
// Read tools only, and that is the design rather than a first instalment. The
// controllers this reaches run live networks, where the cost of a wrong write
// is measured in outage rather than in a bad answer -- so the integration does
// not have one to get wrong. readOnlyTransport enforces it below every tool
// registered here.
func (p *Plugin) Register(_ context.Context, r *plugins.Registry) error {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "networks",
		Title: "List networks",
		Description: "Lists networks. Start here when you do not know what " +
			"exists: network names are what most other filters take. Reads " +
			"every account this credential can see unless an account is named " +
			"here or one is configured as the default.",
		Idempotent: true,
	}, p.listNetworks)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "devices",
		Title: "List devices",
		Description: "Lists devices, newest state first. Filter by network, " +
			"site, tower, type, or whether the device is online, and name an " +
			"account to read one MSP tenant -- cnmaestro_managed_accounts " +
			"lists them. Prefer a filter to listing everything: a large estate " +
			"is truncated, and the result says so when it was.",
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
			"since the list omits fields this returns. Name the account the " +
			"device belongs to if reads are not pinned to one.",
		Idempotent: true,
	}, p.getDevice)

	return nil
}

// NetworksInput carries only the account, since networks are the top of the
// hierarchy and there is nothing above them to filter by.
type NetworksInput struct {
	Account string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name from cnmaestro_managed_accounts, or Base Infrastructure for the main account; omit to use the configured default"`
}

// NetworksOutput is a list of networks.
type NetworksOutput struct {
	Networks []Record `json:"networks"`
	Count    int      `json:"count"`
	// Total is what the API said exists, which differs from Count when a
	// result was truncated.
	Total int `json:"total,omitempty"`
	// Warnings are the API's own, passed through. It answers 200 with a
	// partial result when part of an estate is unreachable, and a caller that
	// drops these reports incomplete data as complete.
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	// Account is which account answered. Empty means none was named and the
	// result spans every account the credential can see, which is a different
	// statement from naming the main account.
	Account string `json:"account,omitempty"`
	Note    string `json:"note,omitempty"`
}

func (p *Plugin) listNetworks(ctx context.Context, in NetworksInput) (NetworksOutput, error) {
	account := p.cfg.Account(in.Account)
	page, err := p.client.List(ctx, "/networks", accountParams(account))
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
		Account:   account,
		Note:      spanningNote(account, false),
	}, nil
}

// DevicesInput filters a device listing.
//
// Every field is optional, and each maps to a documented query parameter. The
// model is told to prefer filters because an unfiltered estate is the case
// that gets truncated.
type DevicesInput struct {
	Account string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name from cnmaestro_managed_accounts, or Base Infrastructure for the main account; omit to use the configured default"`
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
	Devices   []Record `json:"devices"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	// Account is which account answered. Empty means none was named.
	Account string `json:"account,omitempty"`
	// Note explains a truncated result in words, since that is the one
	// outcome a model is likely to misread as "this is the whole estate" --
	// and carries the same warning about which accounts were in scope.
	Note string `json:"note,omitempty"`
}

func (p *Plugin) listDevices(ctx context.Context, in DevicesInput) (DevicesOutput, error) {
	account := p.cfg.Account(in.Account)
	params := accountParams(account)
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
		Account:   account,
	}
	// A hierarchy filter changes what an absent account means, so the two
	// notes are composed rather than one replacing the other.
	notes := make([]string, 0, 2)
	if page.Truncated {
		notes = append(notes, fmt.Sprintf(
			"Stopped at %d devices; the estate has more. Narrow with network, "+
				"site, tower, type, or search rather than treating this as the "+
				"whole estate.", len(page.Items)))
	}
	hierarchy := in.Network != "" || in.Site != "" || in.Tower != ""
	if n := spanningNote(account, hierarchy); n != "" {
		notes = append(notes, n)
	}
	out.Note = strings.Join(notes, " ")
	return out, nil
}

// ManagedAccountsInput takes no arguments.
type ManagedAccountsInput struct{}

// ManagedAccountsOutput is the authoritative tenant list.
type ManagedAccountsOutput struct {
	Accounts []Record `json:"accounts"`
	Count    int      `json:"count"`
	Warnings []string `json:"warnings,omitempty"`
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
	MAC     string `json:"mac" jsonschema:"the device's MAC address, with or without separators"`
	Account string `json:"account,omitempty" jsonschema:"which account the device belongs to: an MSP tenant name, or Base Infrastructure for the main account; omit to use the configured default"`
}

// DeviceOutput is one device's full record.
type DeviceOutput struct {
	Device   Record   `json:"device"`
	Warnings []string `json:"warnings,omitempty"`
}

func (p *Plugin) getDevice(ctx context.Context, in DeviceInput) (DeviceOutput, error) {
	mac := strings.TrimSpace(in.MAC)
	if !macPattern.MatchString(mac) {
		return DeviceOutput{}, fmt.Errorf(
			"%q is not a MAC address; expected something like AA:BB:CC:DD:EE:FF", in.MAC)
	}

	// The device record arrives as a single-element array on this endpoint,
	// not a bare object, so it is decoded as one and unwrapped.
	var records []Record
	warnings, err := p.client.Get(ctx, "/devices/"+url.PathEscape(mac),
		accountParams(p.cfg.Account(in.Account)), &records)
	p.note(err)
	if err != nil {
		return DeviceOutput{}, err
	}
	if len(records) == 0 {
		return DeviceOutput{}, fmt.Errorf("cnmaestro: no device with MAC %s", mac)
	}
	return DeviceOutput{Device: records[0], Warnings: warnings}, nil
}

// accountParams starts a parameter set with the account, or without it when
// there is none to send.
func accountParams(account string) url.Values {
	params := url.Values{}
	setIf(params, managedAccountKV, account)
	return params
}

// spanningNote says which accounts a result covered, when that is not what a
// reader would assume.
//
// Two cases, and they are opposites. Without a hierarchy filter an unnamed
// account spans the whole installation, which is easy to mistake for one
// tenant. With one, cnMaestro quietly answers from the main account alone --
// the same tool call, the same configuration, a narrower answer, and nothing
// in the response says so.
func spanningNote(account string, hierarchyFiltered bool) string {
	if account != "" {
		return ""
	}
	if hierarchyFiltered {
		return "No account was named and this request filters by network, " +
			"site, or tower, so cnMaestro answered from the main account " +
			"alone rather than every account. Name an account to read a " +
			"tenant's devices."
	}
	return "No account was named, so this spans every account the credential " +
		"can see. Each record's managed_account says which one it belongs to, " +
		"and an empty one means the main account."
}

// setIf adds a parameter only when it carries a value, so an omitted filter is
// absent rather than present and empty. The two are not the same to the API.
func setIf(params url.Values, key, value string) {
	if v := strings.TrimSpace(value); v != "" {
		params.Set(key, v)
	}
}
