package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spoked/mcpd/internal/plugins"
)

// The number inventory, and how it got there.

func (p *Plugin) registerInventoryTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_numbers",
		Title: "List numbers on the account",
		Description: "Numbers on this account. By default the ones in service; " +
			"ask for disconnected to see what has been given up. Narrow to one " +
			"site or one SIP peer when the question is “which numbers point " +
			"where”. Ask for totals when the question is only how many.",
		Idempotent: true,
	}, p.listNumbers)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "search_available_numbers",
		Title: "Search numbers available to order",
		Description: "Numbers Bandwidth currently has available, by area code, " +
			"prefix, state, city or pattern. This searches inventory that could " +
			"be ordered; it does not order anything and does not reserve " +
			"anything, so a number found here can be gone by the time somebody " +
			"asks for it.",
		Idempotent: true,
	}, p.searchAvailableNumbers)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_number_options",
		Title: "Get a number's options",
		Description: "The per-number settings on this account — call forwarding, " +
			"CNAM, and the rest. Give a number to read one, or omit it to list " +
			"every number that has options set.",
		Idempotent: true,
	}, p.getNumberOptions)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_orders",
		Title: "List number orders",
		Description: "Orders on this account and their state: by default the " +
			"ones that bought numbers, or ask for disconnects to see the ones " +
			"that gave numbers up. Give an order id to read one in full. " +
			"Port-ins are a third kind and are tracked separately, under " +
			"list_port_ins.",
		Idempotent: true,
	}, p.listOrders)
}

// NumbersInput selects which numbers to list.
type NumbersInput struct {
	State      string `json:"state,omitempty" jsonschema:"inservice (default) or disconnected"`
	SiteID     string `json:"site_id,omitempty" jsonschema:"list only numbers on this site"`
	SipPeerID  string `json:"sip_peer_id,omitempty" jsonschema:"list only numbers on this SIP peer; requires site_id"`
	TotalsOnly bool   `json:"totals_only,omitempty" jsonschema:"return only how many there are, not the numbers themselves"`
	Page       int    `json:"page,omitempty" jsonschema:"1-based page number"`
	Limit      int    `json:"limit,omitempty" jsonschema:"most numbers to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listNumbers(ctx context.Context, in NumbersInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	if in.SipPeerID != "" && in.SiteID == "" {
		return Listing{}, fmt.Errorf("bandwidth: a sip_peer_id needs the " +
			"site_id it belongs to — SIP peer ids are unique within a site, " +
			"not across the account")
	}
	account := p.client.AccountID()
	disconnected := in.State == "disconnected"

	var path string
	switch {
	case in.SipPeerID != "":
		path = fmt.Sprintf("/accounts/%s/sites/%s/sippeers/%s/tns",
			account, url.PathEscape(in.SiteID), url.PathEscape(in.SipPeerID))
		if in.TotalsOnly {
			path = fmt.Sprintf("/accounts/%s/sites/%s/sippeers/%s/totaltns",
				account, url.PathEscape(in.SiteID), url.PathEscape(in.SipPeerID))
		}
	case in.SiteID != "":
		path = fmt.Sprintf("/accounts/%s/sites/%s/inserviceNumbers", account, url.PathEscape(in.SiteID))
		if in.TotalsOnly {
			path = fmt.Sprintf("/accounts/%s/sites/%s/totaltns", account, url.PathEscape(in.SiteID))
		}
	case disconnected:
		path = fmt.Sprintf("/accounts/%s/discnumbers", account)
		if in.TotalsOnly {
			path += "/totals"
		}
	default:
		path = fmt.Sprintf("/accounts/%s/inserviceNumbers", account)
		if in.TotalsOnly {
			path += "/totals"
		}
	}

	limit := p.client.limit(in.Limit)
	q := url.Values{}
	if !in.TotalsOnly {
		q.Set("size", strconv.Itoa(limit))
		if in.Page > 0 {
			q.Set("page", strconv.Itoa(in.Page))
		}
	}

	rec, err := p.client.getXML(ctx, path, q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	if in.TotalsOnly {
		return Listing{Items: []Record{rec}, Returned: 1}, nil
	}
	items := listOf(rec, "TelephoneNumbers", "TelephoneNumber")
	if len(items) == 0 {
		items = listOf(rec, "", "TelephoneNumber")
	}
	return capped(items, limit), nil
}

// AvailableNumbersInput describes the numbers to look for.
type AvailableNumbersInput struct {
	AreaCode string `json:"area_code,omitempty" jsonschema:"three-digit area code such as 918"`
	Prefix   string `json:"prefix,omitempty" jsonschema:"six-digit NPA-NXX such as 918555"`
	State    string `json:"state,omitempty" jsonschema:"two-letter state code such as OK"`
	City     string `json:"city,omitempty" jsonschema:"city name; use with state"`
	Zip      string `json:"zip,omitempty" jsonschema:"five-digit ZIP code"`
	Contains string `json:"contains,omitempty" jsonschema:"digits the number must contain, such as 1234"`
	TollFree bool   `json:"toll_free,omitempty" jsonschema:"search toll-free inventory instead of local numbers"`
	Quantity int    `json:"quantity,omitempty" jsonschema:"how many numbers to return, up to the configured ceiling"`
}

func (p *Plugin) searchAvailableNumbers(ctx context.Context, in AvailableNumbersInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	q := url.Values{}
	set(q, "areaCode", in.AreaCode)
	set(q, "npaNxx", in.Prefix)
	set(q, "state", in.State)
	set(q, "city", in.City)
	set(q, "zip", in.Zip)
	set(q, "endsIn", in.Contains)
	if in.TollFree {
		q.Set("tollFreeWildCardPattern", "8**")
	}
	quantity := p.client.limit(in.Quantity)
	q.Set("quantity", strconv.Itoa(quantity))

	rec, err := p.client.getXML(ctx,
		fmt.Sprintf("/accounts/%s/availableNumbers", p.client.AccountID()), q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	items := listOf(rec, "TelephoneNumberList", "TelephoneNumber")
	if len(items) == 0 {
		items = listOf(rec, "", "TelephoneNumber")
	}
	return capped(items, quantity), nil
}

// NumberOptionsInput names one number, or none for a listing.
type NumberOptionsInput struct {
	PhoneNumber string `json:"phone_number,omitempty" jsonschema:"one number in 10-digit form such as 9195551234; omit to list every number with options set"`
	Limit       int    `json:"limit,omitempty" jsonschema:"most rows to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) getNumberOptions(ctx context.Context, in NumberOptionsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	base := fmt.Sprintf("/accounts/%s/tnoptions", p.client.AccountID())

	if in.PhoneNumber != "" {
		rec, err := p.client.getXML(ctx, base+"/"+url.PathEscape(in.PhoneNumber), nil)
		p.note(err, nil)
		if err != nil {
			return Listing{}, err
		}
		return Listing{Items: []Record{rec}, Returned: 1}, nil
	}

	rec, err := p.client.getXML(ctx, base, nil)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	return capped(listOf(rec, "", "TnOptionOrder"), p.client.limit(in.Limit)), nil
}

// OrdersInput narrows a listing of orders, or names one.
type OrdersInput struct {
	Kind    string `json:"kind,omitempty" jsonschema:"purchases (default) for orders that bought numbers, or disconnects for orders that gave them up"`
	OrderID string `json:"order_id,omitempty" jsonschema:"one order by id, to read it in full; purchases only"`
	Status  string `json:"status,omitempty" jsonschema:"order status such as COMPLETE PARTIAL FAILED or BACKORDERED"`
	Page    int    `json:"page,omitempty" jsonschema:"1-based page number"`
	Limit   int    `json:"limit,omitempty" jsonschema:"most orders to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listOrders(ctx context.Context, in OrdersInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	if in.Kind == "disconnects" {
		return p.listDisconnects(ctx, DisconnectsInput{Page: in.Page, Limit: in.Limit})
	}
	base := fmt.Sprintf("/accounts/%s/orders", p.client.AccountID())

	if in.OrderID != "" {
		rec, err := p.client.getXML(ctx, base+"/"+url.PathEscape(in.OrderID), nil)
		p.note(err, nil)
		if err != nil {
			return Listing{}, err
		}
		return Listing{Items: []Record{rec}, Returned: 1}, nil
	}

	limit := p.client.limit(in.Limit)
	q := url.Values{}
	set(q, "status", in.Status)
	q.Set("size", strconv.Itoa(limit))
	if in.Page > 0 {
		q.Set("page", strconv.Itoa(in.Page))
	}

	rec, err := p.client.getXML(ctx, base, q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	return capped(listOf(rec, "", "Order"), limit), nil
}

// DisconnectsInput narrows a listing of disconnect orders.
//
// Reached through list_orders rather than advertised on its own: numbers
// arriving and numbers leaving are the same question with the arrow reversed,
// and two tools made a model choose between halves of one answer.
type DisconnectsInput struct {
	Page  int `json:"page,omitempty" jsonschema:"1-based page number"`
	Limit int `json:"limit,omitempty" jsonschema:"most orders to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listDisconnects(ctx context.Context, in DisconnectsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	limit := p.client.limit(in.Limit)
	q := url.Values{}
	q.Set("size", strconv.Itoa(limit))
	if in.Page > 0 {
		q.Set("page", strconv.Itoa(in.Page))
	}

	rec, err := p.client.getXML(ctx,
		fmt.Sprintf("/accounts/%s/disconnects", p.client.AccountID()), q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	return capped(listOf(rec, "", "DisconnectTelephoneNumberOrder"), limit), nil
}
