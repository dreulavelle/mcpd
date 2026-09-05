package flowroute

import (
	"context"
	"fmt"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "where would an ambulance be sent".
//
// These records are the address emergency services are given for a call from
// one of these numbers, so they are the records in this account with the
// worst failure mode. A number with no address assigned, or one carrying the
// address of an office the customer moved out of, is invisible until the day
// it matters.

func (p *Plugin) registerE911Tools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_e911_addresses",
		Title: "List emergency addresses",
		Description: "The emergency-calling addresses held on the account, with " +
			"the name and label each was filed under. get_number says which one " +
			"a particular number uses.",
		Idempotent: true,
	}, p.listE911Addresses)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_e911_address",
		Title: "Get one emergency address",
		Description: "One emergency-calling address in full, by its id. " +
			"get_number reports the id for a number.",
		Idempotent: true,
	}, p.getE911Address)
}

// e911Attrs is one emergency address.
type e911Attrs struct {
	AddressType       string `json:"address_type"`
	AddressTypeNumber blank  `json:"address_type_number"`
	Alias             blank  `json:"alias"`
	City              string `json:"city"`
	Country           string `json:"country"`
	FirstName         string `json:"first_name"`
	Label             string `json:"label"`
	LastName          string `json:"last_name"`
	State             string `json:"state"`
	StreetName        string `json:"street_name"`
	StreetNumber      string `json:"street_number"`
	Zip               string `json:"zip"`
}

// E911Row is one emergency address.
type E911Row struct {
	// Customer is set when this row is an answer in its own right; a row
	// inside a listing takes it from the listing instead.
	Customer string `json:"customer,omitempty"`
	ID       string `json:"id"`
	Label    string `json:"label,omitempty"`
	Alias    string `json:"alias,omitempty"`
	Name     string `json:"name,omitempty"`
	// Address is the whole address on one line, which is how somebody checks
	// it against what the customer told them.
	Address string `json:"address"`
	City    string `json:"city,omitempty"`
	State   string `json:"state,omitempty"`
	Zip     string `json:"zip,omitempty"`
	Country string `json:"country,omitempty"`
}

// E911Result is the emergency address list with its counts.
type E911Result struct {
	Customer  string    `json:"customer"`
	Addresses []E911Row `json:"addresses"`
	Count     int       `json:"count"`
	truncation
}

type e911ListArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's Flowroute account, by business name or alias; needed when this instance serves more than one"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most addresses to return; the instance's ceiling applies"`
}

func (p *Plugin) listE911Addresses(ctx context.Context, args e911ListArgs) (E911Result, error) {
	a, err := p.customerFor(args.Customer)
	if err != nil {
		return E911Result{}, err
	}
	pg, err := a.client.list(ctx, "/v2/e911s", nil, args.Limit)
	a.note(err, p.deps.Now())
	if err != nil {
		return E911Result{}, err
	}
	rows := make([]E911Row, 0, len(pg.items))
	for _, item := range pg.items {
		var at e911Attrs
		if err := item.attrs(&at); err != nil {
			return E911Result{}, err
		}
		rows = append(rows, e911Row(item.ID.String(), at))
	}
	rows, cut := bound(rows, pg.more)
	return E911Result{
		Customer: a.name, Addresses: rows, Count: len(rows), truncation: cut,
	}, nil
}

type e911GetArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's Flowroute account, by business name or alias; needed when this instance serves more than one"`
	ID       string `json:"id" jsonschema:"the emergency address id, as get_number or list_e911_addresses reports it"`
}

func (p *Plugin) getE911Address(ctx context.Context, args e911GetArgs) (E911Row, error) {
	a, err := p.customerFor(args.Customer)
	if err != nil {
		return E911Row{}, err
	}
	id := strings.TrimSpace(args.ID)
	if id == "" || onlyDigits(id) != id {
		return E911Row{}, fmt.Errorf(
			"flowroute: %q is not an emergency address id; they are numeric, and "+
				"list_e911_addresses reports them", args.ID)
	}
	doc, err := a.client.get(ctx, "/v2/e911s/"+id, nil)
	a.note(err, p.deps.Now())
	if err != nil {
		if isNotFound(err) {
			return E911Row{}, fmt.Errorf(
				"flowroute: there is no emergency address %s on %s's account", id, a.name)
		}
		return E911Row{}, err
	}
	item, err := doc.one()
	if err != nil {
		return E911Row{}, err
	}
	var at e911Attrs
	if err := item.attrs(&at); err != nil {
		return E911Row{}, err
	}
	row := e911Row(item.ID.String(), at)
	row.Customer = a.name
	return row, nil
}

// e911Row renders one address.
func e911Row(id string, a e911Attrs) E911Row {
	parts := []string{strings.TrimSpace(a.StreetNumber + " " + a.StreetName)}
	if t := strings.TrimSpace(a.AddressType + " " + a.AddressTypeNumber.String()); t != "" {
		parts = append(parts, t)
	}
	street := strings.Join(nonEmpty(parts), ", ")
	full := strings.Join(nonEmpty([]string{street, a.City, strings.TrimSpace(a.State + " " + a.Zip), a.Country}), ", ")
	return E911Row{
		ID:      id,
		Label:   a.Label,
		Alias:   a.Alias.String(),
		Name:    strings.TrimSpace(a.FirstName + " " + a.LastName),
		Address: full,
		City:    a.City,
		State:   a.State,
		Zip:     a.Zip,
		Country: a.Country,
	}
}

// nonEmpty drops the blanks from a slice of address parts.
func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
