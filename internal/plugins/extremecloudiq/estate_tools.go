package extremecloudiq

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// How the estate is arranged, and how it is doing.

func (p *Plugin) registerEstateTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_estate_summary",
		Title: "Get the state of the whole estate",
		Description: "One answer to “how is the network”: how many devices " +
			"exist, are managed and are connected; how many clients are on " +
			"wireless, wired and Thread; and how many alerts fired at each " +
			"severity over the window. Start here — it is one call, it is " +
			"small, and it says which of the other tools is worth making.",
		Idempotent: true,
	}, p.getEstateSummary)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_locations",
		Title: "List sites, buildings and floors",
		Description: "The location hierarchy, flattened: every site, building " +
			"and floor with its id and its full path. This is where the site " +
			"names the other tools take come from — pass one to " +
			"extremecloudiq_list_devices, list_clients or list_alerts to " +
			"narrow to it. Changes rarely, so it is answered from memory " +
			"between changes.",
		Idempotent: true,
	}, p.listLocations)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_network_policies",
		Title: "List network policies and SSIDs",
		Description: "How the wireless is configured: the network policies " +
			"this account defines and the SSIDs in it. This answers “what is " +
			"this network meant to be doing”, which is the question underneath " +
			"most “why can this client not connect”. It reads configuration, " +
			"not state — nothing here says whether anything is working.",
		Idempotent: true,
	}, p.listNetworkPolicies)
}

// EstateSummaryInput takes only a window, for the alert counts.
type EstateSummaryInput struct {
	timeArgs
}

// EstateSummaryOutput is the whole estate in one small answer.
//
// Three reads behind one tool, and the composition is the point: each is a
// single object, and a model asked "how is the network" that had to make three
// calls to find out would make one and answer from a third of the picture.
type EstateSummaryOutput struct {
	// Window is the range the alert counts cover. The device and client
	// figures are as of now and carry no window, which is why this names what
	// it applies to rather than sitting at the top unqualified.
	Window string `json:"alert_window"`
	// Devices is the API's own count object: total, managed and connected.
	Devices Record `json:"devices,omitempty"`
	// Clients counts wireless, wired and Thread clients separately, as
	// ExtremeCloud IQ does.
	Clients Record `json:"clients,omitempty"`
	// AlertsBySeverity is how many fired in the window at each severity.
	AlertsBySeverity []Record `json:"alerts_by_severity,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
	Note             string   `json:"note,omitempty"`
}

func (p *Plugin) getEstateSummary(ctx context.Context, in EstateSummaryInput) (EstateSummaryOutput, error) {
	if err := p.ready(); err != nil {
		return EstateSummaryOutput{}, err
	}
	w, err := p.cfg.resolve(in.timeArgs, p.deps.Now())
	if err != nil {
		return EstateSummaryOutput{}, err
	}

	out := EstateSummaryOutput{Window: w.describe()}

	// Each part is best-effort and named when it fails. A token scoped away
	// from alerts should still be told how many devices are up, and a summary
	// that refuses entirely because one of three endpoints was unhappy is the
	// worst possible answer to "how is the network".
	var devices Record
	if err := p.client.GetInto(ctx, "/devices/stats", nil, &devices); err != nil {
		out.Warnings = append(out.Warnings, "could not count devices: "+err.Error())
	} else {
		out.Devices = devices
	}

	var clients Record
	if err := p.client.GetInto(ctx, "/clients/summary", nil, &clients); err != nil {
		out.Warnings = append(out.Warnings, "could not count clients: "+err.Error())
	} else {
		out.Clients = clients
	}

	counts := url.Values{}
	w.apply(counts)
	var alerts []Record
	if err := p.client.GetInto(ctx, "/alerts/count-by-SEVERITY", counts, &alerts); err != nil {
		out.Warnings = append(out.Warnings, "could not count alerts: "+err.Error())
	} else {
		out.AlertsBySeverity = alerts
	}

	p.note(nil)
	if len(out.Warnings) == 3 {
		// Every part failed, which is not a partial answer -- it is a broken
		// connection wearing the shape of one.
		return EstateSummaryOutput{}, fmt.Errorf("extremecloudiq: none of the "+
			"summary reads succeeded: %s", strings.Join(out.Warnings, "; "))
	}
	return out, nil
}

// LocationsInput narrows the hierarchy.
type LocationsInput struct {
	Search string `json:"search,omitempty" jsonschema:"case-insensitive substring of a name, to list only matching locations and their paths"`
	Limit  int    `json:"limit,omitempty" jsonschema:"most locations to return; the configured ceiling applies whatever this says"`
}

// LocationsOutput is the hierarchy, flattened.
type LocationsOutput struct {
	Locations []locationRow `json:"locations"`
	Returned  int           `json:"returned"`
	Truncated bool          `json:"truncated,omitempty"`
	Note      string        `json:"note,omitempty"`
}

// locationRow is one place in the hierarchy.
//
// Flattened rather than nested, and it is a real struct rather than a passed
// through record. Both for the same reason: a nested tree of forty fields per
// node is an output schema and a payload several times the size of this, and
// what a caller actually needs from a location is its name, what kind of thing
// it is, and where it sits.
type locationRow struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Type is SITE, BUILDING, FLOOR or the folder kinds above them.
	Type string `json:"type,omitempty"`
	// Path is the full name, which is what disambiguates two floors both
	// called "1" in different buildings.
	Path     string `json:"path,omitempty"`
	ParentID int64  `json:"parent_id,omitempty"`
}

func (p *Plugin) listLocations(ctx context.Context, in LocationsInput) (LocationsOutput, error) {
	if err := p.ready(); err != nil {
		return LocationsOutput{}, err
	}
	rows, err := p.locations(ctx)
	if err != nil {
		return LocationsOutput{}, err
	}

	search := strings.ToLower(strings.TrimSpace(in.Search))
	limit := p.limit(in.Limit)
	out := LocationsOutput{Locations: make([]locationRow, 0, len(rows))}
	for _, row := range rows {
		if search != "" && !strings.Contains(strings.ToLower(row.Name), search) &&
			!strings.Contains(strings.ToLower(row.Path), search) {
			continue
		}
		if len(out.Locations) >= limit {
			out.Truncated = true
			out.Note = fmt.Sprintf("stopped at %d locations; narrow it with a "+
				"search rather than raising the limit", limit)
			break
		}
		out.Locations = append(out.Locations, row)
	}
	out.Returned = len(out.Locations)
	if out.Returned == 0 && search != "" {
		out.Note = fmt.Sprintf("nothing here is called %q. The names are "+
			"whatever somebody typed in ExtremeCloud IQ; ask without a search "+
			"to see them all.", in.Search)
	}
	return out, nil
}

// NetworkPoliciesInput narrows the configuration listing.
type NetworkPoliciesInput struct {
	Search string `json:"search,omitempty" jsonschema:"partial network policy name"`
	Limit  int    `json:"limit,omitempty" jsonschema:"most policies and most SSIDs to return"`
}

// NetworkPoliciesOutput is the wireless configuration.
type NetworkPoliciesOutput struct {
	Policies []Record `json:"policies"`
	// SSIDs are every SSID this account defines, not only the ones the listed
	// policies use. Listed once rather than per policy: an SSID belongs to
	// several, and repeating it under each would be the same rows several
	// times over.
	SSIDs    []Record `json:"ssids,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Note     string   `json:"note,omitempty"`
}

func (p *Plugin) listNetworkPolicies(ctx context.Context, in NetworkPoliciesInput) (NetworkPoliciesOutput, error) {
	if err := p.ready(); err != nil {
		return NetworkPoliciesOutput{}, err
	}
	limit := p.limit(in.Limit)
	// Two collections in one result, so each gets half the budget.
	budget := plugins.ResultBudget(2)

	params := url.Values{"view": {"BASIC"}}
	setIf(params, "keyword", in.Search)
	got, err := p.client.Collect(ctx, "/network-policies", params, limit, 2000, budget)
	p.note(err)
	if err != nil {
		return NetworkPoliciesOutput{}, err
	}

	out := NetworkPoliciesOutput{Policies: got.Rows}
	if got.Truncated {
		out.Note = got.Reason
	}

	// Best-effort, and separate: SSIDs are a different scope, and a policy
	// listing without them is still an answer.
	ssids, err := p.client.Collect(ctx, "/ssids", nil, limit, 100, budget)
	if err != nil {
		out.Warnings = append(out.Warnings, "could not list SSIDs: "+err.Error())
	} else {
		out.SSIDs = ssids.Rows
	}
	return out, nil
}

// locationNode is one node of the tree as the API sends it.
type locationNode struct {
	ID         int64          `json:"id"`
	ParentID   int64          `json:"parent_id"`
	Name       string         `json:"name"`
	UniqueName string         `json:"unique_name"`
	Type       string         `json:"type"`
	Children   []locationNode `json:"children"`
}

// locations reads the hierarchy and flattens it.
//
// One read for the whole tree rather than a page per level: it is small, it
// changes rarely, and it is held in the cache between changes -- so the site
// name a caller passes to another tool costs nothing to resolve.
func (p *Plugin) locations(ctx context.Context) ([]locationRow, error) {
	var tree []locationNode
	if err := p.client.GetInto(ctx, "/locations/tree", nil, &tree); err != nil {
		p.note(err)
		return nil, err
	}
	p.note(nil)

	var out []locationRow
	var walk func(nodes []locationNode, prefix string)
	walk = func(nodes []locationNode, prefix string) {
		for _, n := range nodes {
			path := n.UniqueName
			if path == "" {
				// Not every deployment fills unique_name in. Building it from
				// the walk is what keeps two floors both called "1" tellable
				// apart, which is the entire reason the path is here.
				path = n.Name
				if prefix != "" {
					path = prefix + " / " + n.Name
				}
			}
			out = append(out, locationRow{
				ID: n.ID, Name: n.Name, Type: n.Type, Path: path, ParentID: n.ParentID,
			})
			walk(n.Children, path)
		}
	}
	walk(tree, "")
	return out, nil
}

// locationID resolves whatever a caller named into the id the filters take.
//
// Every location filter in this API is a numeric id, and a model has a name --
// from a ticket, from a question, from the listing tool. Resolving it here
// costs one cached read and turns "devices at Springfield" into something that
// works; leaving it to the caller turns it into a wrong answer, because an id
// guessed wrong is a filter that silently matches nothing rather than an
// error.
//
// A number is *not* short-circuited as an id, which it is for a device. Floors
// are called "1" and "2" in every building anybody has ever named, so a
// numeric value here is at least as likely to be a name as an id -- and
// reading "1" as the id of a site called Springfield would answer confidently
// about the wrong place with nothing in the answer saying so. Both readings
// are gathered and an ambiguity is refused with the candidates named.
//
// An exact name or path wins over a substring, because a floor called "1" must
// not be beaten by a building whose path merely contains it.
func (p *Plugin) locationID(ctx context.Context, named string) (int64, string, error) {
	name := strings.TrimSpace(named)
	if name == "" {
		return 0, "", fmt.Errorf("extremecloudiq: name a site, building or " +
			"floor, or omit it to cover the whole estate")
	}
	rows, err := p.locations(ctx)
	if err != nil {
		return 0, "", err
	}

	asID, numeric := int64(0), false
	if id, err := strconv.ParseInt(name, 10, 64); err == nil && id > 0 {
		asID, numeric = id, true
	}
	want := strings.ToLower(name)

	var exact, partial []locationRow
	for _, row := range rows {
		switch {
		case numeric && row.ID == asID:
			exact = append(exact, row)
		case strings.EqualFold(row.Name, name), strings.EqualFold(row.Path, name):
			exact = append(exact, row)
		case strings.Contains(strings.ToLower(row.Path), want):
			partial = append(partial, row)
		}
	}
	found := exact
	if len(found) == 0 {
		found = partial
	}

	switch len(found) {
	case 0:
		return 0, "", fmt.Errorf("extremecloudiq: no site, building or floor "+
			"here is called %q. Use extremecloudiq_list_locations to see the "+
			"names this account actually uses", named)
	case 1:
		return found[0].ID, describeLocation(found[0]), nil
	default:
		names := make([]string, 0, len(found))
		for _, row := range found {
			if len(names) == 5 {
				names = append(names, fmt.Sprintf("and %d more", len(found)-5))
				break
			}
			names = append(names, describeLocation(row))
		}
		return 0, "", fmt.Errorf("extremecloudiq: %q matches %d locations (%s), "+
			"so it does not say which one. Use the full path, which is unique",
			named, len(found), strings.Join(names, "; "))
	}
}

// describeLocation names a location the way a person would, for a note or an
// ambiguity message.
func describeLocation(row locationRow) string {
	if row.Path != "" && !strings.EqualFold(row.Path, row.Name) {
		return row.Path
	}
	return row.Name
}
