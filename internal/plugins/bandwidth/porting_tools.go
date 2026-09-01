package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// Porting: where a number is on its way in, and why it has not arrived.

func (p *Plugin) registerPortingTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_port_ins",
		Title: "List port-in orders",
		Description: "Port-in orders on this account — numbers being moved to " +
			"Bandwidth from another carrier. Narrow by status, by when the " +
			"order was created, or by a number on it. Start here for “where is " +
			"our port”, then use get_port_in for the order's history and notes.",
		Idempotent: true,
	}, p.listPortIns)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_port_in",
		Title: "Get one port-in order",
		Description: "One port-in order in full, and on request its status " +
			"history, the notes Bandwidth and the losing carrier have added, " +
			"and whether a letter of authorisation is on file. The history and " +
			"the notes are what actually answer “why was this rejected” — the " +
			"order itself only carries the current status.",
		Idempotent: true,
	}, p.getPortIn)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_bulk_port_ins",
		Title: "List bulk port-in orders",
		Description: "Bulk port-in orders, which carry many numbers under one " +
			"request and are tracked separately from ordinary port-ins. A " +
			"number that is not in list_port_ins may be on one of these.",
		Idempotent: true,
	}, p.listBulkPortIns)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_bulk_port_in",
		Title: "Get one bulk port-in order",
		Description: "One bulk port-in order, and on request the full list of " +
			"numbers on it with each number's own state.",
		Idempotent: true,
	}, p.getBulkPortIn)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_tollfree_port_validations",
		Title: "List toll-free porting validations",
		Description: "Toll-free portability checks that have been run on this " +
			"account. A toll-free number is validated before it can be ported, " +
			"and a failed validation is the usual reason a toll-free port never " +
			"starts. Give an id to read one.",
		Idempotent: true,
	}, p.listTollFreePortValidations)
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_port_outs",
		Title: "List port-out orders",
		Description: "Port-out orders — numbers being moved away from Bandwidth " +
			"to another carrier. Give an order id to read one.\n\n" +
			"This is the other half of \"why did this number stop working\", and " +
			"the half that is invisible from the port-in tools: a number that " +
			"has left is not a fault, it is a completed order somebody else " +
			"raised. Check here before treating a dead number as an outage.\n\n" +
			"Paging is by cursor rather than by number: page_token takes the " +
			"order id to start from, and \"1\" means the beginning. With no " +
			"filters the result covers the last two years.",
		Idempotent: true,
	}, p.listPortOuts)

}

// PortInsInput narrows a listing of port-in orders.
type PortInsInput struct {
	Account       string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	Status        string `json:"status,omitempty" jsonschema:"order status, such as PENDING SUBMITTED FOE EXCEPTION COMPLETE or CANCELLED"`
	PhoneNumber   string `json:"phone_number,omitempty" jsonschema:"a number on the order, in 10-digit form such as 9195551234"`
	CreatedAfter  string `json:"created_after,omitempty" jsonschema:"earliest order creation date, as YYYY-MM-DD"`
	CreatedBefore string `json:"created_before,omitempty" jsonschema:"latest order creation date, as YYYY-MM-DD"`
	Page          int    `json:"page,omitempty" jsonschema:"1-based page number; the Dashboard pages rather than using a token"`
	Limit         int    `json:"limit,omitempty" jsonschema:"most orders to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listPortIns(ctx context.Context, in PortInsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	limit := p.client.limit(in.Limit)
	q := url.Values{}
	set(q, "status", in.Status)
	set(q, "tn", in.PhoneNumber)
	set(q, "startDate", in.CreatedAfter)
	set(q, "endDate", in.CreatedBefore)
	setPage(q, in.Page, limit)

	rec, err := p.client.getXML(ctx,
		fmt.Sprintf("/accounts/%s/portins", account), q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	items, note := collect(rec, "", "LnpOrderSummary")
	out := capped(items, limit)
	if out.Note == "" {
		out.Note = note
	}
	return out, nil
}

// PortInInput names one port-in order and says how much of it to read.
type PortInInput struct {
	Account        string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	OrderID        string `json:"order_id" jsonschema:"the port-in order id, as returned by list_port_ins"`
	WithHistory    bool   `json:"with_history,omitempty" jsonschema:"also fetch the order's status history, which is what shows when and why it changed"`
	WithNotes      bool   `json:"with_notes,omitempty" jsonschema:"also fetch the notes on the order, where a rejection reason is usually written"`
	WithAuthLetter bool   `json:"with_auth_letter,omitempty" jsonschema:"also check whether a letter of authorisation is on file"`
}

// PortInOutput is one order and whatever else was asked for.
type PortInOutput struct {
	Order   Record   `json:"order"`
	History []Record `json:"history,omitempty"`
	Notes   []Record `json:"notes,omitempty"`
	// AuthLetters lists the letters of authorisation on file. The documents
	// themselves are never fetched: they are scans, often of a signature.
	AuthLetters []Record `json:"auth_letters,omitempty"`
	// Note names any part that could not be read, so a partial answer is not
	// mistaken for a complete one.
	Note string `json:"note,omitempty"`
}

func (p *Plugin) getPortIn(ctx context.Context, in PortInInput) (PortInOutput, error) {
	if err := p.ready(); err != nil {
		return PortInOutput{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return PortInOutput{}, err
	}
	if in.OrderID == "" {
		return PortInOutput{}, fmt.Errorf("bandwidth: a port-in order id is required")
	}
	base := fmt.Sprintf("/accounts/%s/portins/%s",
		account, url.PathEscape(in.OrderID))

	order, err := p.client.getXML(ctx, base, nil)
	p.note(err, nil)
	if err != nil {
		return PortInOutput{}, err
	}
	out := PortInOutput{Order: order}

	// Each extra is best effort and named when it fails. A history that cannot
	// be read must not turn an order that was read into an error -- the order
	// is what the caller asked for, and the rest is enrichment.
	var missing []string
	if in.WithHistory {
		if rec, err := p.client.getXML(ctx, base+"/history", nil); err != nil {
			missing = append(missing, "history ("+err.Error()+")")
		} else {
			out.History, _ = collect(rec, "", "OrderHistory")
		}
	}
	if in.WithNotes {
		if rec, err := p.client.getXML(ctx, base+"/notes", nil); err != nil {
			missing = append(missing, "notes ("+err.Error()+")")
		} else {
			out.Notes, _ = collect(rec, "", "Note")
		}
	}
	if in.WithAuthLetter {
		q := url.Values{"documentType": {"LOA"}}
		if rec, err := p.client.getXML(ctx, base+"/loas", q); err != nil {
			missing = append(missing, "letter of authorisation ("+err.Error()+")")
		} else {
			out.AuthLetters, _ = collect(rec, "", "FileData")
		}
	}
	if len(missing) > 0 {
		out.Note = "the order was read; these were not: " + joinAnd(missing)
	}
	return out, nil
}

// BulkPortInsInput narrows a listing of bulk port-in orders.
type BulkPortInsInput struct {
	Account string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	Status  string `json:"status,omitempty" jsonschema:"order status"`
	Page    int    `json:"page,omitempty" jsonschema:"1-based page number"`
	Limit   int    `json:"limit,omitempty" jsonschema:"most orders to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listBulkPortIns(ctx context.Context, in BulkPortInsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	limit := p.client.limit(in.Limit)
	q := url.Values{}
	set(q, "status", in.Status)
	setPage(q, in.Page, limit)

	rec, err := p.client.getXML(ctx,
		fmt.Sprintf("/accounts/%s/bulkPortins", account), q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	items, note := collect(rec, "", "BulkPortinSummary")
	out := capped(items, limit)
	if out.Note == "" {
		out.Note = note
	}
	return out, nil
}

// BulkPortInInput names one bulk order.
type BulkPortInInput struct {
	Account     string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	OrderID     string `json:"order_id" jsonschema:"the bulk port-in order id, as returned by list_bulk_port_ins"`
	WithNumbers bool   `json:"with_numbers,omitempty" jsonschema:"also fetch every number on the order and its own state"`
}

// BulkPortInOutput is one bulk order and, on request, its numbers.
type BulkPortInOutput struct {
	Order   Record   `json:"order"`
	Numbers []Record `json:"numbers,omitempty"`
	Note    string   `json:"note,omitempty"`
}

func (p *Plugin) getBulkPortIn(ctx context.Context, in BulkPortInInput) (BulkPortInOutput, error) {
	if err := p.ready(); err != nil {
		return BulkPortInOutput{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return BulkPortInOutput{}, err
	}
	if in.OrderID == "" {
		return BulkPortInOutput{}, fmt.Errorf("bandwidth: a bulk port-in order id is required")
	}
	base := fmt.Sprintf("/accounts/%s/bulkPortins/%s",
		account, url.PathEscape(in.OrderID))

	order, err := p.client.getXML(ctx, base, nil)
	p.note(err, nil)
	if err != nil {
		return BulkPortInOutput{}, err
	}
	out := BulkPortInOutput{Order: order}

	if in.WithNumbers {
		if rec, err := p.client.getXML(ctx, base+"/tnList", nil); err != nil {
			out.Note = "the order was read; its number list was not: " + err.Error()
		} else {
			out.Numbers, _ = collect(rec, "", "TelephoneNumber")
		}
	}
	return out, nil
}

// TollFreeValidationInput reads one validation or lists them.
type TollFreeValidationInput struct {
	Account      string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	ValidationID string `json:"validation_id,omitempty" jsonschema:"one validation by id; omit to list them"`
	Limit        int    `json:"limit,omitempty" jsonschema:"most validations to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listTollFreePortValidations(ctx context.Context, in TollFreeValidationInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	base := fmt.Sprintf("/accounts/%s/tollFreePortingValidations", account)

	if in.ValidationID != "" {
		rec, err := p.client.getXML(ctx, base+"/"+url.PathEscape(in.ValidationID), nil)
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
	items, note := collect(rec, "", "TollFreePortingValidation")
	out := capped(items, p.client.limit(in.Limit))
	if out.Note == "" {
		out.Note = note
	}
	return out, nil
}

// joinAnd joins a list the way a sentence does.
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	out := ""
	for i, s := range items[:len(items)-1] {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out + " and " + items[len(items)-1]
}

// PortOutsInput narrows a listing of port-out orders, or names one.
type PortOutsInput struct {
	Account     string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	OrderID     string `json:"order_id,omitempty" jsonschema:"one port-out order by id; omit to list"`
	Status      string `json:"status,omitempty" jsonschema:"order status, such as complete pending or cancelled"`
	PhoneNumber string `json:"phone_number,omitempty" jsonschema:"a number on the order, in 10-digit form such as 9195551234"`
	// PageToken is a cursor rather than an index. Bandwidth's own parameter is
	// called "page" and takes the port-out id to start from, with "1" as the
	// convention for the beginning -- so an integer page number, which every
	// other listing here takes, would be silently wrong past the first page.
	PageToken string `json:"page_token,omitempty" jsonschema:"the order id to start the page from; omit or pass 1 for the first page"`
	Limit     int    `json:"limit,omitempty" jsonschema:"most orders to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listPortOuts(ctx context.Context, in PortOutsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	base := fmt.Sprintf("/accounts/%s/portouts", account)

	if id := strings.TrimSpace(in.OrderID); id != "" {
		rec, err := p.client.getXML(ctx, base+"/"+url.PathEscape(id), nil)
		p.note(err, nil)
		if err != nil {
			return Listing{}, err
		}
		return Listing{Items: []Record{rec}, Returned: 1}, nil
	}

	limit := p.client.limit(in.Limit)
	q := url.Values{}
	// Both are required by the endpoint rather than optional, so they are set
	// unconditionally and the cursor defaults to the documented "1".
	page := strings.TrimSpace(in.PageToken)
	if page == "" {
		page = "1"
	}
	q.Set("page", page)
	q.Set("size", strconv.Itoa(limit))
	set(q, "status", in.Status)
	set(q, "tn", in.PhoneNumber)

	rec, err := p.client.getXML(ctx, base, q)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	items, note := collect(rec, "", "lnpPortInfoForGivenStatus")
	out := capped(items, limit)
	if out.Note == "" {
		out.Note = note
	}
	if len(items) >= limit {
		if out.Note != "" {
			out.Note += " "
		}
		out.Note += "paging here is by cursor: pass the last order's id as " +
			"page_token to continue."
	}
	return out, nil
}
