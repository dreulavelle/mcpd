package observium

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The rest of Observium's read surface, grouped the way tools.go groups the
// first part of it: by the question somebody asks, not by the endpoint that
// answers it.
//
// Five endpoints arrive here as two tools. Billing is one question whether the
// meter is traffic or power; counters, printer supplies and probes are all
// "what else is this device reporting", and they are grouped the way capacity
// groups storage, memory and processors.
//
// The reason is that a model asked one question should not have to choose
// between five tools that answer it. It is *not* that grouping saves context,
// which is what this comment used to say: measured, it does not. A grouped
// tool returns a composite result, and the output schema derived from that
// result grows by about what the extra tool entries would have cost. See
// TestToolList_StaysWithinItsContextBudget in internal/app, which is where
// that number lives now.

// registerReadTools adds the second half of the read surface.
func (p *Plugin) registerReadTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_bills",
		Title: "Traffic and power bills",
		Description: "What the estate is metered for: traffic bills and power " +
			"bills, each with its current-period usage against its allowance. " +
			"Ask for one by id to get its closed periods and the ports or " +
			"power sources it accumulates. Use it for questions about " +
			"commitment, overage and what a circuit has actually carried this " +
			"period -- not for live throughput, which is observium_list_ports.",
		Idempotent: true,
	}, p.listBills)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_counters_supplies_probes",
		Title: "Status, counters, printer supplies and probes",
		Description: "The readings no other tool carries: arbitrary counters, " +
			"printer consumable levels and response-time probes. Pass a " +
			"device_id -- unfiltered this spans three collections across the " +
			"whole estate, each truncated independently. Temperature, voltage, " +
			"fans, power and a device's enumerated conditions are all on " +
			"observium_list_sensors.",
		Idempotent: true,
	}, p.listCountersSuppliesProbes)

}

// --- billing ---------------------------------------------------------------

type billingArgs struct {
	BillID   int    `json:"bill_id,omitempty" jsonschema:"one traffic bill by id; adds its closed periods and the ports it meters"`
	PowerID  int    `json:"power_bill_id,omitempty" jsonschema:"one power bill by id; adds its closed periods and the sources it meters"`
	Kind     string `json:"kind,omitempty" jsonschema:"limit to one meter: traffic or power; default both"`
	Live     bool   `json:"live,omitempty" jsonschema:"recompute current-period usage from raw samples instead of returning cached totals; slower on the far end"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most bills to return per meter"`
	Detailed bool   `json:"include_history,omitempty" jsonschema:"include closed billing periods; only when a bill_id or power_bill_id is given"`
}

// billingResult keeps the two meters apart. A gigabyte and a kilowatt-hour are
// not comparable, and one list holding both invites a model to add them up.
type billingResult struct {
	Traffic listResult `json:"traffic_bills"`
	Power   listResult `json:"power_bills"`
	// History and Entities are populated only for a named bill: they are
	// per-bill sub-collections, and fetching them for a listing would be one
	// request per row.
	History  *listResult `json:"history,omitempty"`
	Entities *listResult `json:"metered_entities,omitempty"`
	Note     string      `json:"note,omitempty"`
}

func (p *Plugin) listBills(ctx context.Context, in billingArgs) (billingResult, error) {
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if kind != "" && kind != "traffic" && kind != "power" {
		return billingResult{}, fmt.Errorf(
			"observium: kind is %q; it is traffic, power, or left empty for both", in.Kind)
	}
	if in.Detailed && in.BillID <= 0 && in.PowerID <= 0 {
		return billingResult{}, fmt.Errorf(
			"observium: include_history needs a bill_id or a power_bill_id -- " +
				"closed periods are per bill, and fetching them for every bill " +
				"would be one request each")
	}

	var out billingResult

	if kind != "power" {
		q := url.Values{}
		setIDIf(q, FilterID, in.BillID)
		if in.Live {
			q.Set("live", "1")
		}
		page, err := p.fetchWithin(ctx, EntityBills, q, in.Limit, billCollections(in))
		if err != nil {
			return billingResult{}, err
		}
		out.Traffic = resultOf(page, "traffic bills")
	}

	if kind != "traffic" {
		q := url.Values{}
		setIDIf(q, FilterID, in.PowerID)
		if in.Live {
			q.Set("live", "1")
		}
		page, err := p.fetchWithin(ctx, EntityPowerBills, q, in.Limit, billCollections(in))
		if err != nil {
			return billingResult{}, err
		}
		out.Power = resultOf(page, "power bills")
	}

	if in.Detailed {
		sub, err := p.billDetail(ctx, in)
		if err != nil {
			return billingResult{}, err
		}
		out.History, out.Entities = sub.history, sub.entities
	}

	if in.Live {
		out.Note = "Usage was recomputed from raw samples for this call, so it " +
			"is current rather than as of the last billing run."
	}
	return out, nil
}

type billDetail struct {
	history  *listResult
	entities *listResult
}

// billDetail fetches the two sub-collections a named bill has.
//
// The sub-paths are built here rather than added to apiPaths because they are
// not entity collections in their own right: there is no "all bill histories"
// to read, only the history of one bill.
func (p *Plugin) billDetail(ctx context.Context, in billingArgs) (billDetail, error) {
	base, id := "/bills", in.BillID
	if in.PowerID > 0 {
		base, id = "/power_bills", in.PowerID
	}

	history, err := p.readPath(ctx,
		fmt.Sprintf("%s/%d/history", base, id), "history", in.Limit)
	if err != nil {
		return billDetail{}, err
	}
	entities, err := p.readPath(ctx,
		fmt.Sprintf("%s/%d/entities", base, id), "entities", in.Limit)
	if err != nil {
		return billDetail{}, err
	}

	h := resultOf(history, "closed periods")
	e := resultOf(entities, "metered entities")
	return billDetail{history: &h, entities: &e}, nil
}

// --- indicators ------------------------------------------------------------

type indicatorsArgs struct {
	DeviceID int    `json:"device_id,omitempty" jsonschema:"only this device; strongly preferred, four collections across the estate is a lot"`
	Only     string `json:"only,omitempty" jsonschema:"limit to one: counters, supplies, or probes"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most entries per category"`
}

type indicatorsResult struct {
	Counters listResult `json:"counters"`
	Supplies listResult `json:"printer_supplies"`
	Probes   listResult `json:"probes"`
	Note     string     `json:"note,omitempty"`
}

func (p *Plugin) listCountersSuppliesProbes(ctx context.Context, in indicatorsArgs) (indicatorsResult, error) {
	only := strings.ToLower(strings.TrimSpace(in.Only))
	switch only {
	case "", "counters", "supplies", "probes":
	default:
		return indicatorsResult{}, fmt.Errorf(
			"observium: only is %q; it is counters, supplies, probes, or left "+
				"empty for all three", in.Only)
	}

	q := url.Values{}
	setIDIf(q, "device_id", in.DeviceID)

	var out indicatorsResult
	categories := []struct {
		name   string
		entity Entity
		what   string
		into   *listResult
	}{
		{"counters", EntityCounters, "counters", &out.Counters},
		{"supplies", EntityPrinterSupply, "printer supplies", &out.Supplies},
		{"probes", EntityProbes, "probes", &out.Probes},
	}
	for _, c := range categories {
		if only != "" && only != c.name {
			continue
		}
		page, err := p.fetchWithin(ctx, c.entity, q, in.Limit, 3)
		if err != nil {
			return indicatorsResult{}, err
		}
		*c.into = resultOf(page, c.what)
	}

	if in.DeviceID <= 0 && only == "" {
		out.Note = "This spans every device and three collections, each " +
			"truncated independently, so none of them is a complete picture. " +
			"Pass a device_id, or narrow with only."
	}
	return out, nil
}

// billCollections counts what one billing answer carries, so the byte budget
// is divided by the number of collections that will actually be in it rather
// than by the most there could ever be.
func billCollections(in billingArgs) int {
	n := 2 // both meters, unless one was asked for
	if kind := strings.ToLower(strings.TrimSpace(in.Kind)); kind != "" {
		n = 1
	}
	if in.Detailed {
		n += 2 // the named bill's closed periods and metered entities
	}
	return n
}

// readPath reads a sub-collection that belongs to one named entity.
//
// The path is built by the caller rather than looked up in apiPaths, because
// these are not entity collections in their own right: there is no "every
// bill's history" to read, only the history of one bill. They take the full
// view -- these rows are small, nobody has chosen a field set for them, and
// narrowing to a set nobody checked is the mistake this package has already
// made once.
func (p *Plugin) readPath(ctx context.Context, path, key string, limit int) (Page, error) {
	if !p.configured {
		return Page{}, fmt.Errorf("observium: not configured yet — set its " +
			"connection details on the Plugins page")
	}
	if limit <= 0 || limit > p.cfg.MaxItems {
		limit = p.cfg.MaxItems
	}

	client := p.client
	if limit < p.cfg.MaxItems {
		capped := *client
		capped.cfg.MaxItems = limit
		client = &capped
	}
	page, err := client.Get(ctx, path, key, url.Values{})
	p.note(err)
	if err != nil {
		return Page{}, err
	}
	page.Items = copyItems(page.Items)
	return page, nil
}
