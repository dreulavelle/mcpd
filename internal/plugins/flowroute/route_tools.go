package flowroute

import (
	"context"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "where does a call to this number actually go".
//
// A route is the record that decides which of a customer's systems answers,
// and it is the first thing to look at when calls stop arriving. Flowroute
// keeps routes as their own objects rather than as fields on a number, so a
// route can exist with nothing pointed at it and a number can exist with no
// route -- both of which are silent, and both of which are outages.

func (p *Plugin) registerRouteTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_routes",
		Title: "List inbound routes",
		Description: "The inbound routes on the account: the hosts, URIs and " +
			"numbers that calls can be sent to. get_number says which route a " +
			"particular number uses.",
		Idempotent: true,
	}, p.listRoutes)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_edge_strategies",
		Title: "List edge strategies",
		Description: "The regions Flowroute can send traffic from, with the " +
			"firewall rules and NAPTR record each one needs. Read this when a " +
			"customer's firewall is dropping calls from an address they do not " +
			"expect.",
		Idempotent: true,
	}, p.listEdgeStrategies)
}

// routeAttrs is one inbound route.
type routeAttrs struct {
	Alias          blank  `json:"alias"`
	EdgeStrategyID blank  `json:"edge_strategy_id"`
	RouteType      string `json:"route_type"`
	Value          blank  `json:"value"`
}

type routesArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's Flowroute account, by business name or alias; needed when this instance serves more than one"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most routes to return; the instance's ceiling applies"`
}

// RouteRow is one inbound route.
type RouteRow struct {
	ID    string `json:"id"`
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
	Alias string `json:"alias,omitempty"`
	// EdgeStrategyID is the region this route's traffic is sent from, when one
	// was chosen. Empty means Flowroute's default.
	EdgeStrategyID string `json:"edge_strategy_id,omitempty"`
}

// RoutesResult is the route list with its counts.
type RoutesResult struct {
	Customer string     `json:"customer"`
	Routes   []RouteRow `json:"routes"`
	Count    int        `json:"count"`
	truncation
}

func (p *Plugin) listRoutes(ctx context.Context, args routesArgs) (RoutesResult, error) {
	a, err := p.customerFor(args.Customer)
	if err != nil {
		return RoutesResult{}, err
	}
	pg, err := a.client.list(ctx, "/v2/routes", nil, args.Limit)
	a.note(err, p.deps.Now())
	if err != nil {
		return RoutesResult{}, err
	}
	rows := make([]RouteRow, 0, len(pg.items))
	for _, item := range pg.items {
		var at routeAttrs
		if err := item.attrs(&at); err != nil {
			return RoutesResult{}, err
		}
		rows = append(rows, RouteRow{
			ID:             item.ID.String(),
			Type:           at.RouteType,
			Value:          at.Value.String(),
			Alias:          at.Alias.String(),
			EdgeStrategyID: at.EdgeStrategyID.String(),
		})
	}
	rows, cut := bound(rows, pg.more)
	return RoutesResult{
		Customer: a.name, Routes: rows, Count: len(rows), truncation: cut,
	}, nil
}

// --- edge strategies --------------------------------------------------------

type edgeArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's Flowroute account, by business name or alias; needed when this instance serves more than one"`
}

// edgeAttrs is one edge strategy.
type edgeAttrs struct {
	Description   string `json:"description"`
	FirewallRules string `json:"firewall_rules"`
	Name          string `json:"name"`
	NAPTR         string `json:"naptr"`
}

// EdgeStrategyRow is one region traffic can be sent from.
type EdgeStrategyRow struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// FirewallRules is the address range a customer's firewall has to permit,
	// which is the whole reason to read this.
	FirewallRules string `json:"firewall_rules,omitempty"`
	NAPTR         string `json:"naptr,omitempty"`
}

// EdgeStrategiesResult is the edge strategy list.
type EdgeStrategiesResult struct {
	Customer       string            `json:"customer"`
	EdgeStrategies []EdgeStrategyRow `json:"edge_strategies"`
	Count          int               `json:"count"`
	truncation
}

func (p *Plugin) listEdgeStrategies(ctx context.Context, args edgeArgs) (EdgeStrategiesResult, error) {
	a, err := p.customerFor(args.Customer)
	if err != nil {
		return EdgeStrategiesResult{}, err
	}
	// Not paged: this is a short fixed list of Flowroute's regions rather than
	// anything that grows with the account. It is the same list for every
	// customer, but it is read with that customer's credential rather than
	// borrowing another's -- a tool that answered from whichever account
	// happened to be first would be reading one customer to answer about
	// another, however harmless the content.
	doc, err := a.client.get(ctx, "/v2/routes/edge_strategies", nil)
	a.note(err, p.deps.Now())
	if err != nil {
		return EdgeStrategiesResult{}, err
	}
	items, err := doc.many()
	if err != nil {
		return EdgeStrategiesResult{}, err
	}
	rows := make([]EdgeStrategyRow, 0, len(items))
	for _, item := range items {
		var at edgeAttrs
		if err := item.attrs(&at); err != nil {
			return EdgeStrategiesResult{}, err
		}
		rows = append(rows, EdgeStrategyRow{
			ID:            item.ID.String(),
			Name:          at.Name,
			Description:   at.Description,
			FirewallRules: at.FirewallRules,
			NAPTR:         at.NAPTR,
		})
	}
	rows, cut := bound(rows, false)
	return EdgeStrategiesResult{
		Customer: a.name, EdgeStrategies: rows, Count: len(rows), truncation: cut,
	}, nil
}
