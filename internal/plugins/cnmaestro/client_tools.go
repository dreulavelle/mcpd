package cnmaestro

import (
	"context"
	"net/url"

	"github.com/spoked/mcpd/internal/plugins"
)

// Who is connected: wireless clients, wired clients, and the mesh peers that
// carry them.

func (p *Plugin) registerClientTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "clients",
		Title: "List connected clients",
		Description: "Wireless clients currently associated, across the estate " +
			"or on one access point. Name a device to ask about that AP's " +
			"clients, which is the form that stays small enough to read; the " +
			"estate-wide listing is truncated on any real network.",
		Idempotent: true,
	}, p.listClients)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "wired_clients",
		Title: "List wired clients",
		Description: "Clients connected over Ethernet rather than radio, which " +
			"is what a switch or an NSE reports. Separate from " +
			"cnmaestro_clients because the API keeps them apart.",
		Idempotent: true,
	}, p.listWiredClients)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "mesh_peers",
		Title: "List mesh peers",
		Description: "Mesh links between access points: which node is a base, " +
			"which is a client, and the state of the link between them. Use " +
			"this when a site is reachable but slow, since a degraded mesh " +
			"backhaul looks like a healthy AP.",
		Idempotent: true,
	}, p.listMeshPeers)
}

// ClientsInput selects whose clients to list.
type ClientsInput struct {
	Account string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name from cnmaestro_managed_accounts, or Base Infrastructure for the main account; omit to use the configured default"`
	Device  string `json:"device,omitempty" jsonschema:"MAC address of one access point, to list only its clients; omit for every client the account can see"`
	Type    string `json:"type,omitempty" jsonschema:"wireless, wired, or all; defaults to wireless"`
}

// ClientsOutput is a list of connected clients.
type ClientsOutput struct {
	Clients   []Record `json:"clients"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listClients(ctx context.Context, in ClientsInput) (ClientsOutput, error) {
	clientType, err := oneOf("type", in.Type, "wireless", "wired", "all")
	if err != nil {
		return ClientsOutput{}, err
	}

	path := "/devices/clients"
	if in.Device != "" {
		mac, err := deviceMAC(in.Device)
		if err != nil {
			return ClientsOutput{}, err
		}
		path = "/devices/" + url.PathEscape(mac) + "/clients"
	}

	account := p.cfg.Account(in.Account)
	params := accountParams(account)
	setIf(params, "client_type", clientType)

	// One access point's clients is a device-scoped read; the estate-wide
	// form is not.
	reach := scopeEstate
	if in.Device != "" {
		reach = scopeDevice
	}
	page, note, err := p.collect(ctx, path, params, account, reach, "clients",
		"a device MAC, to ask about one access point")
	if err != nil {
		return ClientsOutput{}, err
	}
	return ClientsOutput{
		Clients: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}

// WiredClientsInput takes only the account: the API offers no filters here.
type WiredClientsInput struct {
	Account string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name, or Base Infrastructure for the main account; omit to use the configured default"`
}

// WiredClientsOutput is a list of wired clients.
type WiredClientsOutput struct {
	Clients   []Record `json:"clients"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listWiredClients(ctx context.Context, in WiredClientsInput) (WiredClientsOutput, error) {
	account := p.cfg.Account(in.Account)
	page, note, err := p.collect(ctx, "/devices/wired_clients",
		accountParams(account), account, scopeEstate, "wired clients",
		"an account, since the API offers no other filter here")
	if err != nil {
		return WiredClientsOutput{}, err
	}
	return WiredClientsOutput{
		Clients: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}

// MeshPeersInput takes only the account.
type MeshPeersInput struct {
	Account string `json:"account,omitempty" jsonschema:"which account to read: an MSP tenant name, or Base Infrastructure for the main account; omit to use the configured default"`
}

// MeshPeersOutput is a list of mesh peers.
type MeshPeersOutput struct {
	Peers     []Record `json:"peers"`
	Count     int      `json:"count"`
	Total     int      `json:"total,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Account   string   `json:"account,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listMeshPeers(ctx context.Context, in MeshPeersInput) (MeshPeersOutput, error) {
	account := p.cfg.Account(in.Account)
	page, note, err := p.collect(ctx, "/devices/mesh/peers",
		accountParams(account), account, scopeEstate, "mesh peers",
		"an account, since the API offers no other filter here")
	if err != nil {
		return MeshPeersOutput{}, err
	}
	return MeshPeersOutput{
		Peers: page.Items, Count: len(page.Items), Total: page.Total,
		Warnings: page.Warnings, Truncated: page.Truncated,
		Account: account, Note: note,
	}, nil
}
