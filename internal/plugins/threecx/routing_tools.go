package threecx

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for "where does this number go".
//
// Inbound routing on 3CX is three objects that have to be read together. A
// trunk carries the DID numbers the carrier delivers on it, as a plain array of
// strings. An inbound rule says where a call to one of those numbers goes, and
// it is its own record keyed to the trunk. And the destination is a peer -- an
// extension, a queue, a ring group, a digital receptionist -- which 3CX numbers
// out of one plan, so a bare number says what it is on its own.

func (p *Plugin) registerRoutingTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_trunks",
		Title: "List trunks",
		Description: "The trunks the phone system carries calls on: provider, " +
			"host, whether each is registered, the DID numbers it answers on, " +
			"how many of those actually route somewhere, and any number that " +
			"appears on more than one trunk. Use it to find which trunk a " +
			"number belongs to, or whether calls can come in at all.",
		Idempotent: true,
	}, p.listTrunks)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_inbound_rules",
		Title: "List inbound rules",
		Description: "Where each DID number rings: the destination during office " +
			"hours, out of hours and on holidays, per trunk. This is the tool " +
			"for 'who gets calls to this number' and 'why does this number go " +
			"to the wrong place'. Narrow to one number or one trunk.",
		Idempotent: true,
	}, p.listInboundRules)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_outbound_rules",
		Title: "List outbound rules",
		Description: "How dialled numbers leave the phone system: each rule's " +
			"prefix and number-length match, which extensions and departments " +
			"it applies to, and the trunks it tries in order with any digits " +
			"stripped or prepended. This is the tool for 'why can't they dial " +
			"out' and 'which trunk does this call use'.",
		Idempotent: true,
	}, p.listOutboundRules)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "search_directory",
		Title: "Search everything that has a number",
		Description: "Finds what a number or name is on this phone system: an " +
			"extension, a queue, a ring group, a digital receptionist, a " +
			"conference room, a parking spot. 3CX numbers all of them out of " +
			"one plan, so this is how to tell what 800 is before reading it " +
			"with the tool for that kind of thing.",
		Idempotent: true,
	}, p.searchDirectory)
}

// --- trunks -------------------------------------------------------------------

// trunkRecord is a trunk as the phone system keeps it. Note where the name
// comes from: a trunk has no name of its own; it belongs to the Gateway it
// carries, along with the host it registers to.
type trunkRecord struct {
	ID                  int      `json:"Id"`
	Number              string   `json:"Number"`
	ExternalNumber      string   `json:"ExternalNumber"`
	Direction           string   `json:"Direction"`
	IsOnline            bool     `json:"IsOnline"`
	DidNumbers          []string `json:"DidNumbers"`
	SimultaneousCalls   int      `json:"SimultaneousCalls"`
	ConfigurationIssue  string   `json:"ConfigurationIssue"`
	EnableInboundCalls  bool     `json:"EnableInboundCalls"`
	EnableOutboundCalls bool     `json:"EnableOutboundCalls"`
	OutboundCallerID    string   `json:"OutboundCallerID"`
	Gateway             struct {
		Name string `json:"Name"`
		Host string `json:"Host"`
		Type string `json:"Type"`
	} `json:"Gateway"`
}

// trunkFields leaves out AuthID, AuthPassword, SeparateAuthId and Certificate,
// which are the trunk's registration credentials. Gateway is a complex value
// with the provider's name and host and nothing secret.
const trunkFields = "Id,Number,ExternalNumber,Direction,IsOnline,DidNumbers,SimultaneousCalls," +
	"ConfigurationIssue,EnableInboundCalls,EnableOutboundCalls,OutboundCallerID,Gateway"

func (p *Plugin) readTrunks(ctx context.Context, acct *account) ([]trunkRecord, error) {
	q := url.Values{"$select": {trunkFields}}
	got, err := list[trunkRecord](ctx, acct.client, "Trunks", q, p.cfg.MaxItems)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(got.Rows, func(a, b int) bool { return got.Rows[a].Number < got.Rows[b].Number })
	return got.Rows, nil
}

type trunksArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
}

// TrunkRow is one trunk with its numbers.
type TrunkRow struct {
	Number            string   `json:"number"`
	Name              string   `json:"name,omitempty"`
	Host              string   `json:"host,omitempty"`
	Type              string   `json:"type,omitempty"`
	Direction         string   `json:"direction,omitempty"`
	Online            bool     `json:"online"`
	InboundEnabled    bool     `json:"inbound_enabled"`
	OutboundEnabled   bool     `json:"outbound_enabled"`
	ExternalNumber    string   `json:"external_number,omitempty"`
	OutboundCallerID  string   `json:"outbound_caller_id,omitempty"`
	SimultaneousCalls int      `json:"simultaneous_calls"`
	Issue             string   `json:"configuration_issue,omitempty"`
	DIDs              []string `json:"dids"`
	DIDCount          int      `json:"did_count"`
	// RoutedDIDs is how many of the numbers have an inbound rule. "40 numbers,
	// 8 of them routed" is the difference between a trunk that works and one
	// that answers every call with the same default destination.
	RoutedDIDs int `json:"routed_dids"`
	// DuplicateDIDs appear on another trunk as well. A DID on two trunks is a
	// real misconfiguration -- the phone system matches whichever it finds --
	// and this is the only place anybody would see it.
	DuplicateDIDs []string `json:"duplicate_dids"`
}

// TrunksResult is the trunk list.
type TrunksResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string     `json:"customer"`
	Trunks   []TrunkRow `json:"trunks"`
	Returned int        `json:"returned"`
	Offline  int        `json:"offline"`
	truncation
}

func (p *Plugin) listTrunks(ctx context.Context, args trunksArgs) (TrunksResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return TrunksResult{}, err
	}
	trunks, err := p.readTrunks(ctx, acct)
	if err != nil {
		return TrunksResult{}, acct.call(err)
	}

	// Which of each trunk's numbers actually route somewhere. A phone system
	// that will not list its rules is not a failure here: the numbers are
	// still the answer to the question this tool was asked.
	routed := map[int]map[string]bool{}
	if rules, err := p.readInboundRules(ctx, acct); err == nil {
		for _, rule := range rules {
			if rule.TrunkDN == nil || !strings.EqualFold(rule.Condition, "BasedOnDID") {
				continue
			}
			if routed[rule.TrunkDN.ID] == nil {
				routed[rule.TrunkDN.ID] = map[string]bool{}
			}
			routed[rule.TrunkDN.ID][strings.TrimSpace(rule.Data)] = true
		}
	} else {
		p.deps.Log.DebugContext(ctx, "3cx would not list inbound rules; trunk routing counts are omitted", "error", err)
	}

	where := map[string][]string{}
	for _, t := range trunks {
		for _, did := range t.DidNumbers {
			where[did] = append(where[did], t.Number)
		}
	}

	out := TrunksResult{Trunks: make([]TrunkRow, 0, len(trunks))}
	for _, t := range trunks {
		row := TrunkRow{
			Number: t.Number, Name: t.Gateway.Name, Host: t.Gateway.Host, Type: t.Gateway.Type,
			Direction: t.Direction, Online: t.IsOnline,
			InboundEnabled: t.EnableInboundCalls, OutboundEnabled: t.EnableOutboundCalls,
			ExternalNumber: t.ExternalNumber, OutboundCallerID: t.OutboundCallerID,
			SimultaneousCalls: t.SimultaneousCalls, Issue: t.ConfigurationIssue,
			DIDs: t.DidNumbers, DIDCount: len(t.DidNumbers), DuplicateDIDs: []string{},
		}
		if row.DIDs == nil {
			row.DIDs = []string{}
		}
		for _, did := range t.DidNumbers {
			if routed[t.ID][did] {
				row.RoutedDIDs++
			}
			if len(where[did]) > 1 {
				row.DuplicateDIDs = append(row.DuplicateDIDs, did)
			}
		}
		if !t.IsOnline {
			out.Offline++
		}
		out.Trunks = append(out.Trunks, row)
	}
	out.Trunks, out.truncation = bound(out.Trunks, false)
	out.Returned = len(out.Trunks)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// --- inbound rules ---------------------------------------------------------------

type inboundRuleRecord struct {
	ID                  int          `json:"Id"`
	RuleName            string       `json:"RuleName"`
	Condition           string       `json:"Condition"`
	Data                string       `json:"Data"`
	CallType            string       `json:"CallType"`
	AlterOutOfOffice    bool         `json:"AlterDestinationDuringOutOfOfficeHours"`
	AlterHolidays       bool         `json:"AlterDestinationDuringHolidays"`
	OfficeHours         *destination `json:"OfficeHoursDestination"`
	OutOfOfficeHours    *destination `json:"OutOfOfficeHoursDestination"`
	HolidaysDestination *destination `json:"HolidaysDestination"`
	TrunkDN             *struct {
		ID     int    `json:"Id"`
		Number string `json:"Number"`
		Name   string `json:"Name"`
	} `json:"TrunkDN"`
}

const inboundRuleFields = "Id,RuleName,Condition,Data,CallType,AlterDestinationDuringOutOfOfficeHours," +
	"AlterDestinationDuringHolidays,OfficeHoursDestination,OutOfOfficeHoursDestination,HolidaysDestination"

func (p *Plugin) readInboundRules(ctx context.Context, acct *account) ([]inboundRuleRecord, error) {
	q := url.Values{
		"$select": {inboundRuleFields},
		"$expand": {"TrunkDN($select=Id,Number,Name)"},
	}
	got, err := list[inboundRuleRecord](ctx, acct.client, "InboundRules", q, p.cfg.MaxItems)
	if err != nil {
		return nil, err
	}
	return got.Rows, nil
}

type inboundRulesArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Number   string `json:"number,omitempty" jsonschema:"only rules for a DID containing these digits"`
	Trunk    string `json:"trunk,omitempty" jsonschema:"only rules on the trunk with this number or name"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most rules to return"`
}

// InboundRuleRow is where one number rings.
type InboundRuleRow struct {
	Trunk     string `json:"trunk"`
	Name      string `json:"name,omitempty"`
	DID       string `json:"did,omitempty"`
	Condition string `json:"condition"`
	CallType  string `json:"call_type,omitempty"`
	// The three destinations. Out-of-hours and holidays read "same as office
	// hours" where the rule does not alter them, because that is the answer
	// to the question rather than the field's value.
	OfficeHours      string `json:"office_hours"`
	OutOfOfficeHours string `json:"out_of_office_hours"`
	Holidays         string `json:"holidays"`
}

// InboundRulesResult is the routing table.
type InboundRulesResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string           `json:"customer"`
	Rules    []InboundRuleRow `json:"rules"`
	Returned int              `json:"returned"`
	truncation
}

func (p *Plugin) listInboundRules(ctx context.Context, args inboundRulesArgs) (InboundRulesResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return InboundRulesResult{}, err
	}
	rules, err := p.readInboundRules(ctx, acct)
	if err != nil {
		return InboundRulesResult{}, acct.call(err)
	}
	limit := p.limitOf(args.Limit)
	number := strings.TrimSpace(args.Number)
	trunk := strings.TrimSpace(args.Trunk)

	out := InboundRulesResult{Rules: []InboundRuleRow{}}
	truncated := false
	for _, r := range rules {
		if number != "" && !strings.Contains(r.Data, number) {
			continue
		}
		trunkNumber, trunkName := "", ""
		if r.TrunkDN != nil {
			trunkNumber, trunkName = r.TrunkDN.Number, r.TrunkDN.Name
		}
		if trunk != "" && !strings.EqualFold(trunk, trunkNumber) && !strings.EqualFold(trunk, trunkName) {
			continue
		}
		if len(out.Rules) >= limit {
			truncated = true
			break
		}
		row := InboundRuleRow{
			Trunk: trunkLabel(trunkNumber, trunkName), Name: r.RuleName, Condition: r.Condition,
			CallType: r.CallType, OfficeHours: r.OfficeHours.text(),
			OutOfOfficeHours: "same as office hours", Holidays: "same as office hours",
		}
		if strings.EqualFold(r.Condition, "BasedOnDID") {
			row.DID = strings.TrimSpace(r.Data)
		} else if r.Data != "" {
			row.DID = r.Data
		}
		if r.AlterOutOfOffice {
			row.OutOfOfficeHours = r.OutOfOfficeHours.text()
		}
		if r.AlterHolidays {
			row.Holidays = r.HolidaysDestination.text()
		}
		out.Rules = append(out.Rules, row)
	}
	sort.SliceStable(out.Rules, func(a, b int) bool {
		if out.Rules[a].Trunk != out.Rules[b].Trunk {
			return out.Rules[a].Trunk < out.Rules[b].Trunk
		}
		return out.Rules[a].DID < out.Rules[b].DID
	})
	out.Rules, out.truncation = bound(out.Rules, truncated)
	out.Returned = len(out.Rules)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// --- outbound rules ---------------------------------------------------------------

type outboundRulesArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
}

// OutboundRuleRow is one way a dialled number may leave.
type OutboundRuleRow struct {
	Name          string   `json:"name"`
	Priority      int      `json:"priority"`
	Prefix        string   `json:"prefix,omitempty"`
	NumberLengths string   `json:"number_lengths,omitempty"`
	Extensions    []string `json:"from_extensions"`
	Departments   []string `json:"from_departments"`
	// Routes are the trunks tried in order: "1: Provider X (strip 1, prepend 9)".
	Routes    []string `json:"routes"`
	Emergency bool     `json:"emergency"`
}

// OutboundRulesResult is the dialling plan.
type OutboundRulesResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string            `json:"customer"`
	Rules    []OutboundRuleRow `json:"rules"`
	Returned int               `json:"returned"`
	truncation
}

func (p *Plugin) listOutboundRules(ctx context.Context, args outboundRulesArgs) (OutboundRulesResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return OutboundRulesResult{}, err
	}
	type record struct {
		Name          string   `json:"Name"`
		Prefix        string   `json:"Prefix"`
		Priority      int      `json:"Priority"`
		NumberLengths string   `json:"NumberLengthRanges"`
		GroupNames    []string `json:"GroupNames"`
		Emergency     bool     `json:"EmergencyRule"`
		Routes        []struct {
			TrunkName   string `json:"TrunkName"`
			TrunkID     int    `json:"TrunkId"`
			StripDigits int    `json:"StripDigits"`
			Prepend     string `json:"Prepend"`
			Append      string `json:"Append"`
			CallerID    string `json:"CallerID"`
		} `json:"Routes"`
		DNRanges []struct {
			From string `json:"From"`
			To   string `json:"To"`
		} `json:"DNRanges"`
	}
	q := url.Values{
		"$select":  {"Id,Name,Prefix,Priority,NumberLengthRanges,GroupNames,Routes,DNRanges,EmergencyRule"},
		"$orderby": {"Priority"},
	}
	got, err := list[record](ctx, acct.client, "OutboundRules", q, p.cfg.MaxItems)
	if err != nil {
		return OutboundRulesResult{}, acct.call(err)
	}
	out := OutboundRulesResult{Rules: make([]OutboundRuleRow, 0, len(got.Rows))}
	for _, r := range got.Rows {
		row := OutboundRuleRow{
			Name: r.Name, Priority: r.Priority, Prefix: r.Prefix, NumberLengths: r.NumberLengths,
			Extensions: []string{}, Departments: r.GroupNames, Routes: []string{}, Emergency: r.Emergency,
		}
		if row.Departments == nil {
			row.Departments = []string{}
		}
		for _, dn := range r.DNRanges {
			if dn.From == dn.To || dn.To == "" {
				row.Extensions = append(row.Extensions, dn.From)
			} else {
				row.Extensions = append(row.Extensions, dn.From+"-"+dn.To)
			}
		}
		for i, rt := range r.Routes {
			if rt.TrunkID == 0 && rt.TrunkName == "" {
				continue
			}
			desc := fmt.Sprintf("%d: %s", i+1, firstNonBlank(rt.TrunkName, fmt.Sprint(rt.TrunkID)))
			var tweaks []string
			if rt.StripDigits > 0 {
				tweaks = append(tweaks, fmt.Sprintf("strip %d", rt.StripDigits))
			}
			if rt.Prepend != "" {
				tweaks = append(tweaks, "prepend "+rt.Prepend)
			}
			if rt.Append != "" {
				tweaks = append(tweaks, "append "+rt.Append)
			}
			if rt.CallerID != "" {
				tweaks = append(tweaks, "caller ID "+rt.CallerID)
			}
			if len(tweaks) > 0 {
				desc += " (" + strings.Join(tweaks, ", ") + ")"
			}
			row.Routes = append(row.Routes, desc)
		}
		out.Rules = append(out.Rules, row)
	}
	out.Rules, out.truncation = bound(out.Rules, got.Truncated)
	out.Returned = len(out.Rules)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// --- directory --------------------------------------------------------------------

type directoryArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Query    string `json:"query,omitempty" jsonschema:"a number or part of a name"`
	Type     string `json:"type,omitempty" jsonschema:"only this kind: Extension, Queue, RingGroup, IVR, Fax, Conference, Parking, SpecialMenu, RoutePoint"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most entries to return"`
}

// PeerRow is one thing with a number.
type PeerRow struct {
	Number string `json:"number"`
	Name   string `json:"name,omitempty"`
	Type   string `json:"type"`
	Hidden bool   `json:"hidden,omitempty"`
}

// DirectoryResult is the numbering plan, narrowed.
type DirectoryResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string    `json:"customer"`
	Entries  []PeerRow `json:"entries"`
	Total    int       `json:"total"`
	Returned int       `json:"returned"`
	truncation
}

var peerTypes = map[string]string{
	"extension": "Extension", "queue": "Queue", "ringgroup": "RingGroup", "ring_group": "RingGroup",
	"ivr": "IVR", "receptionist": "IVR", "fax": "Fax", "conference": "Conference",
	"parking": "Parking", "specialmenu": "SpecialMenu", "routepoint": "RoutePoint", "group": "Group",
}

func (p *Plugin) searchDirectory(ctx context.Context, args directoryArgs) (DirectoryResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return DirectoryResult{}, err
	}
	var filters []string
	if s := strings.TrimSpace(args.Query); s != "" {
		lit := odataString(s)
		filters = append(filters, fmt.Sprintf("(contains(Number,%s) or contains(Name,%s))", lit, lit))
	}
	if t := strings.TrimSpace(args.Type); t != "" {
		kind, ok := peerTypes[strings.ToLower(t)]
		if !ok {
			return DirectoryResult{}, fmt.Errorf("type %q is not a kind of number; it is one of Extension, Queue, RingGroup, IVR, Fax, Conference, Parking, SpecialMenu or RoutePoint", t)
		}
		filters = append(filters, "Type eq "+odataString(kind))
	}
	q := url.Values{"$select": {"Id,Number,Name,Type,Hidden"}, "$orderby": {"Number"}}
	if len(filters) > 0 {
		q.Set("$filter", strings.Join(filters, " and "))
	}
	type record struct {
		Number string `json:"Number"`
		Name   string `json:"Name"`
		Type   string `json:"Type"`
		Hidden bool   `json:"Hidden"`
	}
	got, err := list[record](ctx, acct.client, "Peers", q, p.limitOf(args.Limit))
	if err != nil {
		return DirectoryResult{}, acct.call(err)
	}
	out := DirectoryResult{Entries: make([]PeerRow, 0, len(got.Rows)), Total: got.Total}
	for _, r := range got.Rows {
		out.Entries = append(out.Entries, PeerRow{Number: r.Number, Name: r.Name, Type: r.Type, Hidden: r.Hidden})
	}
	if out.Total < 0 {
		out.Total = len(out.Entries)
	}
	out.Entries, out.truncation = bound(out.Entries, got.Truncated)
	out.Returned = len(out.Entries)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}
