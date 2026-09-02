package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/plugins"
)

// The work orders that change what a number says about itself, rather than
// where it routes: caller-ID name, directory listings, and the customer service
// records a port is built from.
//
// All three are order-shaped: raised, worked by somebody at a carrier, and
// completed or failed days later. So the useful question is almost never "what
// is the value" but "what happened to the request", which is why each of these
// reads an order rather than a setting.

func (p *Plugin) registerRecordTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_cnam_records",
		Title: "List caller-ID name (LIDB) orders",
		Description: "Orders that set the caller-ID name a number displays when " +
			"it calls out — the LIDB record. Give an order id to read one.\n\n" +
			"Answers \"why does this number show the wrong name\". The value " +
			"goes into a network database other carriers read on their own " +
			"schedule, so an order that completed and a name that has " +
			"propagated are different facts — only the first is visible here. " +
			"Bandwidth requires phone_number unless you give an order_id.",
		Idempotent: true,
	}, p.listCNAMRecords)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_directory_listings",
		Title: "List directory listing (DLDA) orders",
		Description: "Directory listing and directory assistance orders — " +
			"whether a number appears in 411 and the white pages, and under " +
			"what name and address. Give an order id to read one, with its " +
			"history.\n\n" +
			"For \"we cannot be found in 411\" or a request to go unlisted. Like " +
			"caller-ID name, this is a request to a third party rather than a " +
			"setting that takes effect when saved.",
		Idempotent: true,
	}, p.listDirectoryListings)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_csrs",
		Title: "List customer service record requests",
		Description: "Customer service record (CSR) requests — the query that " +
			"asks a losing carrier what it holds for a number: the account " +
			"name, service address and the full list of numbers on the " +
			"account. Give an order id to read one, with its notes.\n\n" +
			"A CSR is what a port is built from, and a port that keeps being " +
			"rejected for mismatched details is usually a port raised without " +
			"one. The notes carry what the losing carrier actually said.",
		Idempotent: true,
	}, p.listCSRs)
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_portout_passcodes",
		Title: "List port-out protection passcodes",
		Description: "The passcodes protecting numbers on this account from " +
			"being ported away, and which numbers carry one.\n\n" +
			"Read it to answer \"is this number protected\" and \"the losing " +
			"carrier is asking us for the PIN\". A number with no passcode is " +
			"portable by anyone who knows the account details, which is the " +
			"finding worth acting on.",
		// The passcode itself comes back, and it is the thing that stops a
		// competitor taking a customer's number. That makes seeing it the
		// privilege rather than a step towards one, which is exactly what this
		// field is for -- the tool changes nothing and is still not an
		// ordinary read.
		Capability: auth.CapAdmin,
		Idempotent: true,
	}, p.listPortoutPasscodes)

}

// CNAMInput narrows a listing of caller-ID name orders, or names one.
type CNAMInput struct {
	Account     string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	OrderID     string `json:"order_id,omitempty" jsonschema:"one LIDB order by id; omit to list"`
	PhoneNumber string `json:"phone_number,omitempty" jsonschema:"the number to search orders for, in 10-digit form; required unless order_id is given"`
	Limit       int    `json:"limit,omitempty" jsonschema:"most orders to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listCNAMRecords(ctx context.Context, in CNAMInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	base := fmt.Sprintf("/accounts/%s/lidbs", account)

	if id := strings.TrimSpace(in.OrderID); id != "" {
		return p.readOrder(ctx, base+"/"+url.PathEscape(id))
	}

	tn := normaliseTN(in.PhoneNumber)
	if tn == "" {
		return Listing{}, fmt.Errorf("bandwidth: list_cnam_records needs a " +
			"phone_number to search by, or an order_id to read one order. " +
			"Bandwidth requires the number on this endpoint rather than " +
			"offering an unfiltered listing")
	}
	limit := p.client.limit(in.Limit)
	q := url.Values{}
	q.Set("tn", tn)
	setPage(q, 0, limit)
	return p.orderPage(ctx, base, q, "LidbOrderSummary", limit)
}

// DirectoryInput narrows a listing of directory listing orders, or names one.
type DirectoryInput struct {
	Account     string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	OrderID     string `json:"order_id,omitempty" jsonschema:"one DLDA order by id; omit to list"`
	PhoneNumber string `json:"phone_number,omitempty" jsonschema:"a number on the order, in 10-digit form"`
	WithHistory bool   `json:"with_history,omitempty" jsonschema:"also fetch the order's history; requires order_id"`
	Limit       int    `json:"limit,omitempty" jsonschema:"most orders to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listDirectoryListings(ctx context.Context, in DirectoryInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	base := fmt.Sprintf("/accounts/%s/dldas", account)

	if id := strings.TrimSpace(in.OrderID); id != "" {
		out, err := p.readOrder(ctx, base+"/"+url.PathEscape(id))
		if err != nil || !in.WithHistory {
			return out, err
		}
		rec, err := p.client.getXML(ctx, base+"/"+url.PathEscape(id)+"/history", nil)
		if err != nil {
			out.Note = "the order was read; its history was not: " + shortErr(err)
			return out, nil
		}
		hist, _ := collect(rec, "", "OrderHistory")
		out.Items = append(out.Items, hist...)
		out.Returned = len(out.Items)
		return out, nil
	}
	if in.WithHistory {
		return Listing{}, fmt.Errorf("bandwidth: with_history needs an order_id")
	}

	limit := p.client.limit(in.Limit)
	q := url.Values{}
	set(q, "tn", normaliseTN(in.PhoneNumber))
	setPage(q, 0, limit)
	return p.orderPage(ctx, base, q, "DldaOrderSummary", limit)
}

// CSRInput narrows a listing of customer service record requests, or names one.
type CSRInput struct {
	Account   string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	OrderID   string `json:"order_id,omitempty" jsonschema:"one CSR request by id; omit to list"`
	WithNotes bool   `json:"with_notes,omitempty" jsonschema:"also fetch the notes, where the losing carrier's answer is written; requires order_id"`
	Limit     int    `json:"limit,omitempty" jsonschema:"most requests to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listCSRs(ctx context.Context, in CSRInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	base := fmt.Sprintf("/accounts/%s/csrs", account)

	if id := strings.TrimSpace(in.OrderID); id != "" {
		out, err := p.readOrder(ctx, base+"/"+url.PathEscape(id))
		if err != nil || !in.WithNotes {
			return out, err
		}
		rec, err := p.client.getXML(ctx, base+"/"+url.PathEscape(id)+"/notes", nil)
		if err != nil {
			out.Note = "the request was read; its notes were not: " + shortErr(err)
			return out, nil
		}
		notes, _ := collect(rec, "Notes", "Note")
		out.Items = append(out.Items, notes...)
		out.Returned = len(out.Items)
		return out, nil
	}
	if in.WithNotes {
		return Listing{}, fmt.Errorf("bandwidth: with_notes needs an order_id")
	}

	limit := p.client.limit(in.Limit)
	q := url.Values{}
	setPage(q, 0, limit)
	return p.orderPage(ctx, base, q, "CsrResponse", limit)
}

// readOrder reads one order-shaped resource into a single-item listing.
func (p *Plugin) readOrder(ctx context.Context, path string) (Listing, error) {
	rec, err := p.client.getXML(ctx, path, nil)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	return Listing{Items: []Record{rec}, Returned: 1}, nil
}

// orderPage reads a page of an order collection.
func (p *Plugin) orderPage(ctx context.Context, path string, q url.Values,
	element string, limit int) (Listing, error) {

	rec, err := p.client.getXML(ctx, path, q)
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

// PasscodesInput narrows a listing of port-out protection passcodes.
type PasscodesInput struct {
	Account     string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	PhoneNumber string `json:"phone_number,omitempty" jsonschema:"one number, in 10-digit form; omit for every protected number on the account"`
	Limit       int    `json:"limit,omitempty" jsonschema:"most numbers to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listPortoutPasscodes(ctx context.Context, in PasscodesInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	limit := p.client.limit(in.Limit)
	q := url.Values{}
	set(q, "tn", normaliseTN(in.PhoneNumber))
	setPage(q, 0, limit)
	return p.orderPage(ctx, fmt.Sprintf("/accounts/%s/tnPortoutPasscodes", account),
		q, "TelephoneNumber", limit)
}
