package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

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
		Name:  "list_orders",
		Title: "List number orders",
		Description: "Orders on this account and their state. Three kinds: " +
			"purchases bought numbers, disconnects gave numbers up, and " +
			"number_options changed a number's settings — assigning it to a " +
			"10DLC campaign, or setting SMS or CNAM. Give an order id to read " +
			"one in full. Port-ins are a fourth kind, tracked separately under " +
			"list_port_ins.",
		Idempotent: true,
	}, p.listOrders)
}

// NumbersInput selects which numbers to list.
type NumbersInput struct {
	Account    string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
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
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	if in.SipPeerID != "" && in.SiteID == "" {
		return Listing{}, fmt.Errorf("bandwidth: a sip_peer_id needs the " +
			"site_id it belongs to — SIP peer ids are unique within a site, " +
			"not across the account")
	}
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
		setPage(q, in.Page, limit)
	}

	rec, err := p.client.getXML(ctx, path, q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	if in.TotalsOnly {
		return Listing{Items: []Record{rec}, Returned: 1}, nil
	}
	items, note := collect(rec, "TelephoneNumbers", "TelephoneNumber")
	out := capped(items, limit)
	if out.Note == "" {
		out.Note = note
	}
	return out, nil
}

// AvailableNumbersInput describes the numbers to look for.
type AvailableNumbersInput struct {
	Account  string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
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
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
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
		fmt.Sprintf("/accounts/%s/availableNumbers", account), q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	items, note := collect(rec, "TelephoneNumberList", "TelephoneNumber")
	out := capped(items, quantity)
	if out.Note == "" {
		out.Note = note
	}
	return out, nil
}

// OrdersInput narrows a listing of orders, or names one.
type OrdersInput struct {
	Account string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	Kind    string `json:"kind,omitempty" jsonschema:"purchases (default), disconnects, or number_options"`
	OrderID string `json:"order_id,omitempty" jsonschema:"one order by its order id, to read it in full; not a phone number"`
	Status  string `json:"status,omitempty" jsonschema:"order status such as COMPLETE PARTIAL FAILED or BACKORDERED"`
	Page    int    `json:"page,omitempty" jsonschema:"1-based page number"`
	Limit   int    `json:"limit,omitempty" jsonschema:"most orders to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listOrders(ctx context.Context, in OrdersInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	if in.Kind == "disconnects" {
		// One disconnect order by id, which the listing cannot answer: a
		// disconnect that failed says why in its notes, and until now the only
		// readable thing was that it existed.
		if id := strings.TrimSpace(in.OrderID); id != "" {
			base := fmt.Sprintf("/accounts/%s/disconnects/%s", account, url.PathEscape(id))
			out, err := p.readOrder(ctx, base)
			if err != nil {
				return Listing{}, err
			}
			// Through getXML, which adds the Dashboard prefix, and not the
			// bare variant the 10DLC tools use: the notes live beside the
			// order, and a request for them at a path the allow-list does
			// not know is refused before it reaches the network.
			rec, err := p.client.getXML(ctx, base+"/notes", nil)
			if err != nil {
				out.Note = "the order was read; its notes were not: " + shortErr(err)
				return out, nil
			}
			notes, note := collect(rec, "Notes", "Note")
			if note != "" {
				out.Note = "notes: " + note
				return out, nil
			}
			out.Items = append(out.Items, notes...)
			out.Returned = len(out.Items)
			return out, nil
		}
		return p.listDisconnects(ctx, DisconnectsInput{
			Account: in.Account, Page: in.Page, Limit: in.Limit})
	}
	// TN option orders are orders like the others, and Bandwidth files them
	// under the same word. They were briefly a tool of their own that took a
	// phone number, which is what /tnoptions/{id} does not take: the id is the
	// order's, and passing a number got a 400 saying so.
	base := fmt.Sprintf("/accounts/%s/orders", account)
	element := "Order"
	if in.Kind == "number_options" {
		base = fmt.Sprintf("/accounts/%s/tnoptions", account)
		element = "TnOptionOrder"
	}

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
	setPage(q, in.Page, limit)

	rec, err := p.client.getXML(ctx, base, q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	items, note := collect(rec, "", element)
	out := capped(items, limit)
	if out.Note == "" {
		out.Note = note
	}
	return out, nil
}

// DisconnectsInput narrows a listing of disconnect orders.
//
// Reached through list_orders rather than advertised on its own: numbers
// arriving and numbers leaving are the same question with the arrow reversed,
// and two tools made a model choose between halves of one answer.
type DisconnectsInput struct {
	Account string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	Page    int    `json:"page,omitempty" jsonschema:"1-based page number"`
	Limit   int    `json:"limit,omitempty" jsonschema:"most orders to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listDisconnects(ctx context.Context, in DisconnectsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	limit := p.client.limit(in.Limit)
	q := url.Values{}
	setPage(q, in.Page, limit)

	rec, err := p.client.getXML(ctx,
		fmt.Sprintf("/accounts/%s/disconnects", account), q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	items, note := collect(rec, "", "DisconnectTelephoneNumberOrder")
	out := capped(items, limit)
	if out.Note == "" {
		out.Note = note
	}
	return out, nil
}
