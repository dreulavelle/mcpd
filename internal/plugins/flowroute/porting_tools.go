package flowroute

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "why has this number not arrived yet".
//
// A port order is the long-running, status-bearing object in this account: it
// is raised, it sits with the losing carrier, it is rejected for a mismatched
// service address or accepted with a date, and somebody is asking about it the
// whole time. It is the nearest thing Flowroute has to a ticket.
//
// # A shape that could not be checked against a live account
//
// Flowroute's documentation shows the listing as a nested envelope -- a single
// `portorder_list` entity whose attributes carry `orders.data` -- rather than
// the flat array every other listing here returns, and the sample in the
// documentation is not valid JSON, so it cannot be read closely. The account
// this was built against has no port orders, so neither shape could be
// confirmed. Both are therefore accepted, and the fields are taken by name
// from the documentation rather than from a response anybody has seen.
//
// The consequence is deliberate: a field Flowroute sends under a name not
// listed below is *dropped*, and its name -- never its value -- is reported in
// `unmapped_fields`. So a mismatch shows up as a named gap that somebody can
// fix, rather than as a confidently empty answer.

func (p *Plugin) registerPortingTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_port_orders",
		Title: "List port orders",
		Description: "Port orders on the account and where each has got to. An " +
			"account with none answers with an empty list.",
		Idempotent: true,
	}, p.listPortOrders)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_port_order",
		Title: "Get one port order",
		Description: "One port order by its id, with its current status and when " +
			"that status last changed.",
		Idempotent: true,
	}, p.getPortOrder)
}

// portOrderAttrs is a port order as the documentation describes one.
type portOrderAttrs struct {
	Alias           blank           `json:"alias"`
	Status          blank           `json:"status"`
	GroupStatus     blank           `json:"group_status"`
	CompletionDate  blank           `json:"completion_date"`
	ModifiedDate    blank           `json:"modified_date"`
	CreatedDate     blank           `json:"created_date"`
	StatusUpdatedAt blank           `json:"status_updated_at"`
	Numbers         json.RawMessage `json:"numbers"`
}

// PortOrderRow is one port order.
type PortOrderRow struct {
	// Customer is set when this row is an answer in its own right; a row
	// inside a listing takes it from the listing instead.
	Customer string `json:"customer,omitempty"`
	ID       string `json:"id"`
	Alias    string `json:"alias,omitempty"`
	Status   string `json:"status,omitempty"`
	// GroupStatus is the status of the group a large port was split into,
	// which can differ from the order's own.
	GroupStatus     string   `json:"group_status,omitempty"`
	CreatedDate     string   `json:"created_date,omitempty"`
	ModifiedDate    string   `json:"modified_date,omitempty"`
	CompletionDate  string   `json:"completion_date,omitempty"`
	StatusUpdatedAt string   `json:"status_updated_at,omitempty"`
	Numbers         []string `json:"numbers,omitempty"`

	// UnmappedFields names the attributes Flowroute sent that this integration
	// does not know. Names only, never values: it exists so a shape that has
	// moved is visible, not so unknown content is passed to a reader.
	UnmappedFields []string `json:"unmapped_fields,omitempty"`
}

// PortOrdersResult is the port order list with its counts.
type PortOrdersResult struct {
	Customer string         `json:"customer"`
	Orders   []PortOrderRow `json:"orders"`
	Count    int            `json:"count"`
	truncation
}

type portOrdersArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's Flowroute account, by business name or alias; needed when this instance serves more than one"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most port orders to return; the instance's ceiling applies"`
}

func (p *Plugin) listPortOrders(ctx context.Context, args portOrdersArgs) (PortOrdersResult, error) {
	a, err := p.customerFor(args.Customer)
	if err != nil {
		return PortOrdersResult{}, err
	}
	pg, err := a.client.list(ctx, "/v2/portorders", nil, args.Limit)
	a.note(err, p.deps.Now())
	if err != nil {
		return PortOrdersResult{}, err
	}

	items := pg.items
	// The nested envelope: one entity of type portorder_list whose attributes
	// hold the orders. Unwrapped here so the caller sees the same rows either
	// way.
	if len(items) == 1 && items[0].Type == "portorder_list" {
		inner, err := unwrapPortOrderList(items[0])
		if err != nil {
			return PortOrdersResult{}, err
		}
		items = inner
	}

	rows := make([]PortOrderRow, 0, len(items))
	for _, item := range items {
		row, err := portOrderRow(item)
		if err != nil {
			return PortOrdersResult{}, err
		}
		rows = append(rows, row)
	}
	rows, cut := bound(rows, pg.more)
	return PortOrdersResult{
		Customer: a.name, Orders: rows, Count: len(rows), truncation: cut,
	}, nil
}

type portOrderArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's Flowroute account, by business name or alias; needed when this instance serves more than one"`
	ID       string `json:"id" jsonschema:"the port order id, as list_port_orders reports it"`
}

func (p *Plugin) getPortOrder(ctx context.Context, args portOrderArgs) (PortOrderRow, error) {
	a, err := p.customerFor(args.Customer)
	if err != nil {
		return PortOrderRow{}, err
	}
	id := strings.TrimSpace(args.ID)
	if id == "" || onlyDigits(id) != id {
		return PortOrderRow{}, fmt.Errorf(
			"flowroute: %q is not a port order id; they are numeric, and "+
				"list_port_orders reports them", args.ID)
	}

	doc, err := a.client.get(ctx, "/v2/portorders/"+id, nil)
	a.note(err, p.deps.Now())
	if err != nil {
		if isNotFound(err) {
			return PortOrderRow{}, fmt.Errorf(
				"flowroute: there is no port order %s on %s's account", id, a.name)
		}
		return PortOrderRow{}, err
	}
	item, err := doc.one()
	if err != nil {
		return PortOrderRow{}, err
	}
	row, err := portOrderRow(item)
	if err != nil {
		return PortOrderRow{}, err
	}
	row.Customer = a.name

	// The status lives on its own endpoint as well, and that one carries when
	// it last moved -- which is the field somebody chasing a stalled port
	// actually wants. A failure here is not fatal: the order was read, and an
	// answer without the timestamp beats no answer at all.
	if sdoc, serr := a.client.get(ctx, "/v2/portorders/"+id+"/status", nil); serr == nil {
		if sitem, err := sdoc.one(); err == nil {
			var sa portOrderAttrs
			if err := sitem.attrs(&sa); err == nil {
				if s := sa.Status.String(); s != "" {
					row.Status = s
				}
				if t := sa.StatusUpdatedAt.String(); t != "" {
					row.StatusUpdatedAt = t
				}
			}
		}
	}
	return row, nil
}

// unwrapPortOrderList reads the nested envelope the documentation shows.
func unwrapPortOrderList(item resource) ([]resource, error) {
	var wrapper struct {
		Orders struct {
			Data []resource `json:"data"`
		} `json:"orders"`
	}
	if err := item.attrs(&wrapper); err != nil {
		return nil, err
	}
	return wrapper.Orders.Data, nil
}

// knownPortOrderFields are the attribute names portOrderAttrs reads. Anything
// else is named in UnmappedFields.
var knownPortOrderFields = map[string]bool{
	"alias": true, "status": true, "group_status": true,
	"completion_date": true, "modified_date": true, "created_date": true,
	"status_updated_at": true, "numbers": true,
	// Present in the documentation's sample but already the entity's id.
	"id": true,
}

// portOrderRow renders one port order.
func portOrderRow(item resource) (PortOrderRow, error) {
	var a portOrderAttrs
	if err := item.attrs(&a); err != nil {
		return PortOrderRow{}, err
	}
	row := PortOrderRow{
		ID:              item.ID.String(),
		Alias:           a.Alias.String(),
		Status:          a.Status.String(),
		GroupStatus:     a.GroupStatus.String(),
		CreatedDate:     a.CreatedDate.String(),
		ModifiedDate:    a.ModifiedDate.String(),
		CompletionDate:  a.CompletionDate.String(),
		StatusUpdatedAt: a.StatusUpdatedAt.String(),
		Numbers:         numbersOf(a.Numbers),
	}

	// The documentation's sample carries the id inside attributes rather than
	// only beside them, so it is taken from there when the entity has none.
	if row.ID == "" {
		var withID struct {
			ID string `json:"id"`
		}
		if err := item.attrs(&withID); err == nil {
			row.ID = withID.ID
		}
	}

	var all map[string]json.RawMessage
	if err := item.attrs(&all); err == nil {
		for name := range all {
			if !knownPortOrderFields[name] {
				row.UnmappedFields = append(row.UnmappedFields, name)
			}
		}
		sortStrings(row.UnmappedFields)
	}
	return row, nil
}

// numbersOf reads the numbers on a port order, which the documentation shows
// as a list without saying whether its entries are strings or objects.
func numbersOf(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var strs []string
	if err := json.Unmarshal(raw, &strs); err == nil {
		out := make([]string, 0, len(strs))
		for _, s := range strs {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	var objs []struct {
		Number string `json:"number"`
		Value  string `json:"value"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(raw, &objs); err == nil {
		out := make([]string, 0, len(objs))
		for _, o := range objs {
			if n := firstNonBlank(o.Number, o.Value, o.ID); n != "" {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

// firstNonBlank returns the first value with something in it.
func firstNonBlank(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
