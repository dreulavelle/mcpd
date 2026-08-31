package bandwidth

import (
	"context"
	"fmt"
	"net/url"

	"github.com/spoked/mcpd/internal/plugins"
)

// How the account is arranged, and what is registered for messaging.

func (p *Plugin) registerEstateTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_sites",
		Title: "List sites",
		Description: "Sites on this account — Bandwidth's term for a location " +
			"that numbers and SIP peers belong to. Give a site id to read one, " +
			"and ask for its SIP peers when the question is where calls " +
			"actually route.",
		Idempotent: true,
	}, p.listSites)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_sip_peers",
		Title: "List SIP peers on a site",
		Description: "SIP peers under one site: the hosts calls are delivered " +
			"to and accepted from. Give a peer id to read one, including the " +
			"messaging application bound to it.",
		Idempotent: true,
	}, p.listSipPeers)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_applications",
		Title: "List applications",
		Description: "Voice and messaging applications on this account — the " +
			"callback configuration numbers are attached to. Give an id to read " +
			"one, with the SIP peers using it. When a number stops delivering " +
			"messages this is usually where the answer is.",
		Idempotent: true,
	}, p.listApplications)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_e911_locations",
		Title: "List E911 locations",
		Description: "Registered emergency-service addresses on this account, " +
			"and the endpoints attached to them. This is the address emergency " +
			"services are given when someone dials 911 from a number here, so " +
			"a wrong one is the kind of wrong that matters.",
		Idempotent: true,
	}, p.listE911Locations)
}

// SitesInput names one site, or none for a listing.
type SitesInput struct {
	SiteID       string `json:"site_id,omitempty" jsonschema:"one site by id, to read it in full"`
	WithSipPeers bool   `json:"with_sip_peers,omitempty" jsonschema:"also fetch the SIP peers on the site; requires site_id"`
	Limit        int    `json:"limit,omitempty" jsonschema:"most sites to return; the configured ceiling applies whatever this says"`
}

// SiteOutput is one site or a listing of them, with SIP peers when asked for.
type SiteOutput struct {
	Sites    []Record `json:"sites"`
	Returned int      `json:"returned"`
	SipPeers []Record `json:"sip_peers,omitempty"`
	Note     string   `json:"note,omitempty"`
}

func (p *Plugin) listSites(ctx context.Context, in SitesInput) (SiteOutput, error) {
	if err := p.ready(); err != nil {
		return SiteOutput{}, err
	}
	if in.WithSipPeers && in.SiteID == "" {
		return SiteOutput{}, fmt.Errorf("bandwidth: with_sip_peers needs a " +
			"site_id — SIP peers belong to one site, and fetching them for " +
			"every site would be a call per site")
	}
	base := fmt.Sprintf("/accounts/%s/sites", p.client.AccountID())

	if in.SiteID == "" {
		rec, err := p.client.getXML(ctx, base, nil)
		p.note(err, nil)
		if err != nil {
			return SiteOutput{}, err
		}
		items := listOf(rec, "Sites", "Site")
		if len(items) == 0 {
			items = listOf(rec, "", "Site")
		}
		c := capped(items, p.client.limit(in.Limit))
		return SiteOutput{Sites: c.Items, Returned: c.Returned, Note: c.Note}, nil
	}

	one, err := p.client.getXML(ctx, base+"/"+url.PathEscape(in.SiteID), nil)
	p.note(err, nil)
	if err != nil {
		return SiteOutput{}, err
	}
	out := SiteOutput{Sites: []Record{one}, Returned: 1}

	if in.WithSipPeers {
		rec, err := p.client.getXML(ctx,
			base+"/"+url.PathEscape(in.SiteID)+"/sippeers", nil)
		if err != nil {
			out.Note = "the site was read; its SIP peers were not: " + err.Error()
		} else {
			peers := listOf(rec, "SipPeers", "SipPeer")
			if len(peers) == 0 {
				peers = listOf(rec, "", "SipPeer")
			}
			out.SipPeers = peers
		}
	}
	return out, nil
}

// SipPeersInput names the site, and optionally one peer on it.
type SipPeersInput struct {
	SiteID                string `json:"site_id" jsonschema:"the site the peers belong to, from list_sites"`
	SipPeerID             string `json:"sip_peer_id,omitempty" jsonschema:"one peer by id, to read it in full"`
	WithMessagingSettings bool   `json:"with_messaging_settings,omitempty" jsonschema:"also fetch the messaging application bound to the peer; requires sip_peer_id"`
	Limit                 int    `json:"limit,omitempty" jsonschema:"most peers to return; the configured ceiling applies whatever this says"`
}

// SipPeerOutput is one peer or a listing, with messaging settings when asked.
type SipPeerOutput struct {
	SipPeers          []Record `json:"sip_peers"`
	Returned          int      `json:"returned"`
	MessagingSettings Record   `json:"messaging_settings,omitempty"`
	Note              string   `json:"note,omitempty"`
}

func (p *Plugin) listSipPeers(ctx context.Context, in SipPeersInput) (SipPeerOutput, error) {
	if err := p.ready(); err != nil {
		return SipPeerOutput{}, err
	}
	if in.SiteID == "" {
		return SipPeerOutput{}, fmt.Errorf("bandwidth: a site_id is required; " +
			"SIP peer ids are unique within a site rather than across the account")
	}
	base := fmt.Sprintf("/accounts/%s/sites/%s/sippeers",
		p.client.AccountID(), url.PathEscape(in.SiteID))

	if in.SipPeerID == "" {
		rec, err := p.client.getXML(ctx, base, nil)
		p.note(err, nil)
		if err != nil {
			return SipPeerOutput{}, err
		}
		items := listOf(rec, "SipPeers", "SipPeer")
		if len(items) == 0 {
			items = listOf(rec, "", "SipPeer")
		}
		c := capped(items, p.client.limit(in.Limit))
		return SipPeerOutput{SipPeers: c.Items, Returned: c.Returned, Note: c.Note}, nil
	}

	one, err := p.client.getXML(ctx, base+"/"+url.PathEscape(in.SipPeerID), nil)
	p.note(err, nil)
	if err != nil {
		return SipPeerOutput{}, err
	}
	out := SipPeerOutput{SipPeers: []Record{one}, Returned: 1}

	if in.WithMessagingSettings {
		rec, err := p.client.getXML(ctx, base+"/"+url.PathEscape(in.SipPeerID)+
			"/products/messaging/applicationSettings", nil)
		if err != nil {
			out.Note = "the peer was read; its messaging settings were not: " + err.Error()
		} else {
			out.MessagingSettings = rec
		}
	}
	return out, nil
}

// ApplicationsInput names one application, or none for a listing.
type ApplicationsInput struct {
	ApplicationID    string `json:"application_id,omitempty" jsonschema:"one application by id, to read it in full"`
	WithAssociations bool   `json:"with_associations,omitempty" jsonschema:"also fetch the SIP peers using this application; requires application_id"`
	Limit            int    `json:"limit,omitempty" jsonschema:"most applications to return; the configured ceiling applies whatever this says"`
}

// ApplicationOutput is one application or a listing, with its users when asked.
type ApplicationOutput struct {
	Applications []Record `json:"applications"`
	Returned     int      `json:"returned"`
	// AssociatedSipPeers is who is actually using this application. An
	// application with none is configured and reaching nothing, which is a
	// state worth being able to see.
	AssociatedSipPeers []Record `json:"associated_sip_peers,omitempty"`
	Note               string   `json:"note,omitempty"`
}

func (p *Plugin) listApplications(ctx context.Context, in ApplicationsInput) (ApplicationOutput, error) {
	if err := p.ready(); err != nil {
		return ApplicationOutput{}, err
	}
	if in.WithAssociations && in.ApplicationID == "" {
		return ApplicationOutput{}, fmt.Errorf("bandwidth: with_associations " +
			"needs an application_id")
	}
	base := fmt.Sprintf("/accounts/%s/applications", p.client.AccountID())

	if in.ApplicationID == "" {
		rec, err := p.client.getXML(ctx, base, nil)
		p.note(err, nil)
		if err != nil {
			return ApplicationOutput{}, err
		}
		items := listOf(rec, "Applications", "Application")
		if len(items) == 0 {
			items = listOf(rec, "", "Application")
		}
		c := capped(items, p.client.limit(in.Limit))
		return ApplicationOutput{Applications: c.Items, Returned: c.Returned, Note: c.Note}, nil
	}

	one, err := p.client.getXML(ctx, base+"/"+url.PathEscape(in.ApplicationID), nil)
	p.note(err, nil)
	if err != nil {
		return ApplicationOutput{}, err
	}
	out := ApplicationOutput{Applications: []Record{one}, Returned: 1}

	if in.WithAssociations {
		rec, err := p.client.getXML(ctx,
			base+"/"+url.PathEscape(in.ApplicationID)+"/associatedsippeers", nil)
		if err != nil {
			out.Note = "the application was read; its associations were not: " + err.Error()
		} else {
			peers := listOf(rec, "AssociatedSipPeers", "AssociatedSipPeer")
			if len(peers) == 0 {
				peers = listOf(rec, "", "AssociatedSipPeer")
			}
			out.AssociatedSipPeers = peers
		}
	}
	return out, nil
}

// E911Input selects what to read from the emergency-calling records.
type E911Input struct {
	LocationID    string `json:"location_id,omitempty" jsonschema:"one location by id, to read it in full"`
	WithEndpoints bool   `json:"with_endpoints,omitempty" jsonschema:"also list the endpoints attached to locations"`
	Limit         int    `json:"limit,omitempty" jsonschema:"most locations to return; the configured ceiling applies whatever this says"`
}

// E911Output is the emergency-calling picture for this account.
type E911Output struct {
	Locations []Record `json:"locations"`
	Returned  int      `json:"returned"`
	Endpoints []Record `json:"endpoints,omitempty"`
	Note      string   `json:"note,omitempty"`
}

func (p *Plugin) listE911Locations(ctx context.Context, in E911Input) (E911Output, error) {
	if err := p.ready(); err != nil {
		return E911Output{}, err
	}
	base := fmt.Sprintf("/accounts/%s/e911s", p.client.AccountID())

	var out E911Output
	if in.LocationID != "" {
		one, err := p.client.getXML(ctx,
			base+"/locations/"+url.PathEscape(in.LocationID), nil)
		p.note(err, nil)
		if err != nil {
			return E911Output{}, err
		}
		out = E911Output{Locations: []Record{one}, Returned: 1}
	} else {
		rec, err := p.client.getXML(ctx, base+"/locations", nil)
		p.note(err, nil)
		if err != nil {
			return E911Output{}, err
		}
		items := listOf(rec, "Locations", "Location")
		if len(items) == 0 {
			items = listOf(rec, "", "Location")
		}
		c := capped(items, p.client.limit(in.Limit))
		out = E911Output{Locations: c.Items, Returned: c.Returned, Note: c.Note}
	}

	if in.WithEndpoints {
		rec, err := p.client.getXML(ctx, base, nil)
		if err != nil {
			out.Note = appendNote(out.Note, "the endpoints were not read: "+err.Error())
		} else {
			eps := listOf(rec, "Endpoints", "Endpoint")
			if len(eps) == 0 {
				eps = listOf(rec, "", "Endpoint")
			}
			out.Endpoints = eps
		}
	}
	return out, nil
}

// appendNote joins two notes without losing either.
func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}
