package flowroute

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "what does this customer hold, and everything about one
// number".
//
// A Flowroute account has no idea which business it is for -- that is what the
// customer row on the Plugins page supplies -- and it has no idea which of a
// business's sites or departments a number serves either. The two fields
// somebody has usually written that into, the alias and the note, are carried
// on every row. They are free text and often empty; that is worth returning
// rather than hiding, because an empty alias is itself the answer to "why can
// nobody tell what this number is for".

func (p *Plugin) registerNumberTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_numbers",
		Title: "List numbers",
		Description: "Telephone numbers on one customer's Flowroute account, with " +
			"alias, type, rate centre and status. Narrow by digits with " +
			"starts_with, contains or ends_with, or by exact alias.",
		Idempotent: true,
	}, p.listNumbers)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_number",
		Title: "Get one number",
		Description: "One number in full: alias and note, whether messaging and " +
			"CNAM lookup are on, its costs, and the inbound routes it rings on. " +
			"Says when a number has no primary route, which is why calls to it " +
			"go nowhere.",
		Idempotent: true,
	}, p.getNumber)
}

// --- listing ----------------------------------------------------------------

type numbersArgs struct {
	Customer   string `json:"customer,omitempty" jsonschema:"which customer's Flowroute account, by business name or alias; needed when this instance serves more than one"`
	StartsWith string `json:"starts_with,omitempty" jsonschema:"only numbers beginning with these digits, such as 1206 for a Seattle area code"`
	Contains   string `json:"contains,omitempty" jsonschema:"only numbers containing these digits anywhere"`
	EndsWith   string `json:"ends_with,omitempty" jsonschema:"only numbers ending in these digits"`
	// Alias is an exact match upstream, not a search. Said in the schema
	// because a model that assumes otherwise gets an empty list and reads it
	// as "this customer has no numbers".
	Alias string `json:"alias,omitempty" jsonschema:"only numbers whose alias is exactly this; not a partial match"`
	Limit int    `json:"limit,omitempty" jsonschema:"most numbers to return; the instance's ceiling applies"`
}

// numberAttrs is what a listing carries for one number.
type numberAttrs struct {
	Alias              blank  `json:"alias"`
	CNAMLookupsEnabled bool   `json:"cnam_lookups_enabled"`
	ISOCountry         string `json:"iso_country"`
	NumberType         string `json:"number_type"`
	RateCenter         string `json:"rate_center"`
	State              string `json:"state"`
	Status             string `json:"status"`
	Tier               string `json:"tier"`
	Value              string `json:"value"`
}

// NumberRow is one number as a list shows it.
type NumberRow struct {
	Number string `json:"number"`
	// Formatted is the same number written the way a person reads one, so an
	// answer can be quoted without the reader parsing eleven digits.
	Formatted string `json:"formatted"`
	Alias     string `json:"alias,omitempty"`
	Type      string `json:"type,omitempty"`
	Status    string `json:"status,omitempty"`
	// RateCenter and State are where the number is homed, which is what
	// decides whether a caller is charged long distance for reaching it.
	RateCenter  string `json:"rate_center,omitempty"`
	State       string `json:"state,omitempty"`
	Country     string `json:"country,omitempty"`
	CNAMLookups bool   `json:"cnam_lookups_enabled"`
}

// NumbersResult is the number list with its counts.
type NumbersResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string      `json:"customer"`
	Numbers  []NumberRow `json:"numbers"`
	Count    int         `json:"count"`
	truncation
}

func (p *Plugin) listNumbers(ctx context.Context, args numbersArgs) (NumbersResult, error) {
	a, err := p.customerFor(args.Customer)
	if err != nil {
		return NumbersResult{}, err
	}

	q := url.Values{}
	// The three digit filters are Flowroute's own, applied upstream rather
	// than over a page this side, so a large account narrows before it pages.
	for _, f := range []struct {
		key, value string
	}{
		{"starts_with", args.StartsWith},
		{"contains", args.Contains},
		{"ends_with", args.EndsWith},
	} {
		if strings.TrimSpace(f.value) == "" {
			continue
		}
		digits := onlyDigits(f.value)
		if digits == "" {
			return NumbersResult{}, fmt.Errorf(
				"flowroute: %s %q has no digits in it", f.key, f.value)
		}
		q.Set(f.key, digits)
	}
	// Whole-value only: Flowroute matches an alias exactly, so "Acme" does not
	// find "Acme Dental front desk".
	if alias := strings.TrimSpace(args.Alias); alias != "" {
		q.Set("alias", alias)
	}

	pg, err := a.client.list(ctx, "/v2/numbers", q, args.Limit)
	a.note(err, p.deps.Now())
	if err != nil {
		return NumbersResult{}, err
	}

	rows := make([]NumberRow, 0, len(pg.items))
	for _, item := range pg.items {
		var at numberAttrs
		if err := item.attrs(&at); err != nil {
			return NumbersResult{}, err
		}
		rows = append(rows, NumberRow{
			Number:      numberOf(item, at.Value),
			Formatted:   display(numberOf(item, at.Value)),
			Alias:       at.Alias.String(),
			Type:        at.NumberType,
			Status:      at.Status,
			RateCenter:  at.RateCenter,
			State:       at.State,
			Country:     at.ISOCountry,
			CNAMLookups: at.CNAMLookupsEnabled,
		})
	}
	rows, cut := bound(rows, pg.more)
	return NumbersResult{
		Customer: a.name, Numbers: rows, Count: len(rows), truncation: cut,
	}, nil
}

// --- one number -------------------------------------------------------------

type numberArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's Flowroute account, by business name or alias; needed when this instance serves more than one"`
	Number   string `json:"number" jsonschema:"the telephone number, with the country code: 12065550100 or +1 206 555 0100"`
}

// numberDetailAttrs is the fuller record a single read carries.
type numberDetailAttrs struct {
	numberAttrs
	InboundRate      float64 `json:"inbound_rate"`
	MessagingEnabled bool    `json:"messaging_enabled"`
	MonthlyCost      float64 `json:"monthly_cost"`
	Note             blank   `json:"note"`
	RateType         string  `json:"rate_type"`
	SetupCost        float64 `json:"setup_cost"`
}

// RouteRef is an inbound route as a number's record points at it.
type RouteRef struct {
	ID    string `json:"id"`
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
	Alias string `json:"alias,omitempty"`
}

// NumberDetail is one number in full.
type NumberDetail struct {
	Customer    string  `json:"customer"`
	Number      string  `json:"number"`
	Formatted   string  `json:"formatted"`
	Alias       string  `json:"alias,omitempty"`
	Note        string  `json:"note,omitempty"`
	Type        string  `json:"type,omitempty"`
	Status      string  `json:"status,omitempty"`
	RateCenter  string  `json:"rate_center,omitempty"`
	State       string  `json:"state,omitempty"`
	Country     string  `json:"country,omitempty"`
	RateType    string  `json:"rate_type,omitempty"`
	Tier        string  `json:"tier,omitempty"`
	CNAMLookups bool    `json:"cnam_lookups_enabled"`
	Messaging   bool    `json:"messaging_enabled"`
	MonthlyCost float64 `json:"monthly_cost"`
	SetupCost   float64 `json:"setup_cost"`
	InboundRate float64 `json:"inbound_rate"`

	// PrimaryRoute is where calls to this number are sent. Absent means there
	// is no route at all, which is the usual cause of a number that rings
	// nowhere -- so it is called out rather than left as a missing field.
	PrimaryRoute  *RouteRef `json:"primary_route,omitempty"`
	FailoverRoute *RouteRef `json:"failover_route,omitempty"`
	// E911AddressID and CNAMRecordID are the ids of the related records, for
	// get_e911_address and list_cnam_records to look up.
	E911AddressID string `json:"e911_address_id,omitempty"`
	CNAMRecordID  string `json:"cnam_record_id,omitempty"`

	// Notes are the things worth saying about this number that are not fields
	// on it: chiefly that it has nowhere to ring.
	Notes []string `json:"notes,omitempty"`
}

func (p *Plugin) getNumber(ctx context.Context, args numberArgs) (NumberDetail, error) {
	a, err := p.customerFor(args.Customer)
	if err != nil {
		return NumberDetail{}, err
	}
	number, err := e164(args.Number)
	if err != nil {
		return NumberDetail{}, err
	}

	doc, err := a.client.get(ctx, "/v2/numbers/"+number, nil)
	a.note(err, p.deps.Now())
	if err != nil {
		if isNotFound(err) {
			return NumberDetail{}, fmt.Errorf("flowroute: %s is not on %s's account",
				display(number), a.name)
		}
		return NumberDetail{}, err
	}
	item, err := doc.one()
	if err != nil {
		return NumberDetail{}, err
	}
	var at numberDetailAttrs
	if err := item.attrs(&at); err != nil {
		return NumberDetail{}, err
	}

	// The routes arrive in the document's `included` array rather than nested
	// under the number, so they are indexed by id before being looked up.
	routes := map[string]RouteRef{}
	for _, inc := range doc.Included {
		if inc.Type != "route" {
			continue
		}
		var ra routeAttrs
		if err := inc.attrs(&ra); err != nil {
			return NumberDetail{}, err
		}
		routes[inc.ID.String()] = RouteRef{
			ID:    inc.ID.String(),
			Type:  ra.RouteType,
			Value: ra.Value.String(),
			Alias: ra.Alias.String(),
		}
	}

	out := NumberDetail{
		Customer:    a.name,
		Number:      numberOf(item, at.Value),
		Formatted:   display(numberOf(item, at.Value)),
		Alias:       at.Alias.String(),
		Note:        at.Note.String(),
		Type:        at.NumberType,
		Status:      at.Status,
		RateCenter:  at.RateCenter,
		State:       at.State,
		Country:     at.ISOCountry,
		RateType:    at.RateType,
		Tier:        at.Tier,
		CNAMLookups: at.CNAMLookupsEnabled,
		Messaging:   at.MessagingEnabled,
		MonthlyCost: at.MonthlyCost,
		SetupCost:   at.SetupCost,
		InboundRate: at.InboundRate,

		E911AddressID: item.related("e911_address"),
		CNAMRecordID:  item.related("cnam_preset"),
	}
	if id := item.related("primary_route"); id != "" {
		r := routes[id]
		if r.ID == "" {
			r = RouteRef{ID: id}
		}
		out.PrimaryRoute = &r
	}
	if id := item.related("failover_route"); id != "" {
		r := routes[id]
		if r.ID == "" {
			r = RouteRef{ID: id}
		}
		out.FailoverRoute = &r
	}

	if out.PrimaryRoute == nil {
		out.Notes = append(out.Notes, "This number has no primary route, so an "+
			"inbound call to it has nowhere to go.")
	}
	if out.E911AddressID == "" {
		out.Notes = append(out.Notes, "No emergency address is assigned to this "+
			"number, so a 911 call from it carries no location.")
	}
	return out, nil
}

// numberOf prefers the id, which is the number Flowroute keys on, and falls
// back to the value attribute. They are the same string in every response
// seen; the fallback is for a listing that omits one of them.
func numberOf(r resource, value string) string {
	if id := strings.TrimSpace(r.ID.String()); id != "" {
		return id
	}
	return value
}

// onlyDigits keeps the digits of a caller's prefix.
func onlyDigits(in string) string {
	var b strings.Builder
	for _, r := range in {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
