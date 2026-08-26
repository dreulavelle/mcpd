package cnmaestro

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// Where things are, and how the wireless side is configured.
//
// Sites and towers hang off a network in the API's hierarchy, so both take a
// network name rather than being estate-wide listings. cnmaestro_list_networks is
// where that name comes from.

func (p *Plugin) registerTopologyTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_sites",
		Title: "List sites in a network",
		Description: "The sites within one network -- buildings, campuses, " +
			"whatever the estate calls a place. Site names are what the device " +
			"and alarm filters take. Get the network name from cnmaestro_list_networks.",
		Idempotent: true,
	}, p.listSites)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_towers",
		Title: "List towers in a network",
		Description: "The towers within one network, for fixed wireless estates " +
			"where devices are grouped by structure rather than by building. " +
			"Get the network name from cnmaestro_list_networks.",
		Idempotent: true,
	}, p.listTowers)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_wlans",
		Title: "List enterprise WLANs",
		Description: "The Wi-Fi networks configured for enterprise access points: " +
			"SSIDs, security, VLAN. This is configuration rather than state, " +
			"so it answers what should be on the air rather than what is.",
		Idempotent: true,
	}, p.listWLANs)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_ap_groups",
		Title: "List enterprise AP groups",
		Description: "The AP groups enterprise access points are configured " +
			"from. A device's behaviour comes from its group, so this is where " +
			"to look when one AP differs from its neighbours.",
		Idempotent: true,
	}, p.listAPGroups)
}

// SitesInput names the network to look inside.
type SitesInput struct {
	Network string `json:"network" jsonschema:"the network's name, from cnmaestro_list_networks"`
	Account string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name, or Base Infrastructure for the main account; omit to use the configured default"`
}

// SitesOutput is a list of sites.
type SitesOutput struct {
	Sites     []Record `json:"sites"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listSites(ctx context.Context, in SitesInput) (SitesOutput, error) {
	network, err := requiredName("network", in.Network)
	if err != nil {
		return SitesOutput{}, err
	}
	account := p.cfg.Account(in.Account)

	// The network is in the path, which is a hierarchy filter by any other
	// name: without an account the API answers from the main account alone.
	page, note, err := p.collect(ctx, "/networks/"+url.PathEscape(network)+"/sites",
		accountParams(account), account, scopeHierarchy, "sites", "an account")
	if err != nil {
		return SitesOutput{}, err
	}
	return SitesOutput{
		Sites: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}

// TowersInput names the network to look inside.
type TowersInput struct {
	Network string `json:"network" jsonschema:"the network's name, from cnmaestro_list_networks"`
	Account string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name, or Base Infrastructure for the main account; omit to use the configured default"`
}

// TowersOutput is a list of towers.
type TowersOutput struct {
	Towers    []Record `json:"towers"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listTowers(ctx context.Context, in TowersInput) (TowersOutput, error) {
	network, err := requiredName("network", in.Network)
	if err != nil {
		return TowersOutput{}, err
	}
	account := p.cfg.Account(in.Account)

	page, note, err := p.collect(ctx, "/networks/"+url.PathEscape(network)+"/towers",
		accountParams(account), account, scopeHierarchy, "towers", "an account")
	if err != nil {
		return TowersOutput{}, err
	}
	return TowersOutput{
		Towers: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}

// WLANsInput takes only the account.
type WLANsInput struct {
	Account string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name, or Base Infrastructure for the main account; omit to use the configured default"`
}

// WLANsOutput is a list of configured WLANs.
type WLANsOutput struct {
	WLANs     []Record `json:"wlans"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listWLANs(ctx context.Context, in WLANsInput) (WLANsOutput, error) {
	account := p.cfg.Account(in.Account)
	page, note, err := p.collect(ctx, "/wifi_enterprise/wlans",
		accountParams(account), account, scopeEstate, "WLANs", "an account")
	if err != nil {
		return WLANsOutput{}, err
	}
	return WLANsOutput{
		WLANs: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}

// APGroupsInput takes only the account.
type APGroupsInput struct {
	Account string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name, or Base Infrastructure for the main account; omit to use the configured default"`
}

// APGroupsOutput is a list of AP groups.
type APGroupsOutput struct {
	Groups    []Record `json:"ap_groups"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listAPGroups(ctx context.Context, in APGroupsInput) (APGroupsOutput, error) {
	account := p.cfg.Account(in.Account)
	page, note, err := p.collect(ctx, "/wifi_enterprise/ap_groups",
		accountParams(account), account, scopeEstate, "AP groups", "an account")
	if err != nil {
		return APGroupsOutput{}, err
	}
	return APGroupsOutput{
		Groups: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}

// requiredName rejects an empty path segment before it becomes a request to
// the collection above it, which would answer 200 with the wrong thing.
func requiredName(field, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", fmt.Errorf("%s is required; cnmaestro_list_networks lists the names", field)
	}
	return v, nil
}
