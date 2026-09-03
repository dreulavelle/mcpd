package threecx

import (
	"context"
	"fmt"
	"net/url"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for the things a call can land on that are not a person: ring
// groups, queues and digital receptionists. Each is read with its members
// expanded, because "who is in the sales group" is the question, and the
// member list is the answer.

func (p *Plugin) registerGroupTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_ring_groups",
		Title: "List ring groups",
		Description: "Ring groups: number, name, strategy, ring time, members, " +
			"and where an unanswered call goes.",
		Idempotent: true,
	}, p.listRingGroups)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_queues",
		Title: "List call queues",
		Description: "Call queues: number, name, strategy, ring and wait times, " +
			"agents, managers, and where an unanswered call goes.",
		Idempotent: true,
	}, p.listQueues)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_receptionists",
		Title: "List digital receptionists",
		Description: "Digital receptionists (IVRs): number, name, prompt, what " +
			"each key does, and where a caller who presses nothing ends up.",
		Idempotent: true,
	}, p.listReceptionists)
}

// member is a ring group member or a queue agent as 3CX lists them.
type member struct {
	Number     string `json:"Number"`
	Name       string `json:"Name"`
	SkillGroup string `json:"SkillGroup"`
}

func (m member) text() string {
	out := m.Number
	if m.Name != "" && m.Name != m.Number {
		out += " (" + m.Name + ")"
	}
	if m.SkillGroup != "" {
		out += " skill " + m.SkillGroup
	}
	return out
}

func memberTexts(members []member) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.text())
	}
	return out
}

// --- ring groups ---------------------------------------------------------------

type ringGroupsArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
}

// RingGroupRow is one ring group.
type RingGroupRow struct {
	Number      string   `json:"number"`
	Name        string   `json:"name"`
	Strategy    string   `json:"strategy,omitempty"`
	RingSeconds int      `json:"ring_seconds"`
	Registered  bool     `json:"registered"`
	Members     []string `json:"members"`
	NoAnswer    string   `json:"no_answer"`
}

// RingGroupsResult is the ring group list.
type RingGroupsResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer   string         `json:"customer"`
	RingGroups []RingGroupRow `json:"ring_groups"`
	Returned   int            `json:"returned"`
	truncation
}

func (p *Plugin) listRingGroups(ctx context.Context, args ringGroupsArgs) (RingGroupsResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return RingGroupsResult{}, err
	}
	type record struct {
		Number       string       `json:"Number"`
		Name         string       `json:"Name"`
		RingStrategy string       `json:"RingStrategy"`
		RingTime     int          `json:"RingTime"`
		IsRegistered bool         `json:"IsRegistered"`
		NoAnswer     *destination `json:"ForwardNoAnswer"`
		Members      []member     `json:"Members"`
	}
	q := url.Values{
		"$select":  {"Id,Number,Name,RingStrategy,RingTime,IsRegistered,ForwardNoAnswer"},
		"$expand":  {"Members($select=Id,Number,Name)"},
		"$orderby": {"Number"},
	}
	got, err := list[record](ctx, acct.client, "RingGroups", q, p.cfg.MaxItems)
	if err != nil {
		return RingGroupsResult{}, acct.call(err)
	}
	out := RingGroupsResult{RingGroups: make([]RingGroupRow, 0, len(got.Rows))}
	for _, g := range got.Rows {
		out.RingGroups = append(out.RingGroups, RingGroupRow{
			Number: g.Number, Name: g.Name, Strategy: g.RingStrategy, RingSeconds: g.RingTime,
			Registered: g.IsRegistered, Members: memberTexts(g.Members), NoAnswer: g.NoAnswer.text(),
		})
	}
	out.RingGroups, out.truncation = bound(out.RingGroups, got.Truncated)
	out.Returned = len(out.RingGroups)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// --- queues ----------------------------------------------------------------------

type queuesArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
}

// QueueRow is one call queue.
type QueueRow struct {
	Number         string   `json:"number"`
	Name           string   `json:"name"`
	Strategy       string   `json:"strategy,omitempty"`
	RingSeconds    int      `json:"ring_seconds"`
	MaxWaitSeconds int      `json:"max_wait_seconds"`
	MaxCallers     int      `json:"max_callers"`
	SLASeconds     int      `json:"sla_seconds,omitempty"`
	Registered     bool     `json:"registered"`
	Agents         []string `json:"agents"`
	Managers       []string `json:"managers"`
	NoAnswer       string   `json:"no_answer"`
}

// QueuesResult is the queue list.
type QueuesResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer string     `json:"customer"`
	Queues   []QueueRow `json:"queues"`
	Returned int        `json:"returned"`
	truncation
}

func (p *Plugin) listQueues(ctx context.Context, args queuesArgs) (QueuesResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return QueuesResult{}, err
	}
	type record struct {
		Number          string       `json:"Number"`
		Name            string       `json:"Name"`
		PollingStrategy string       `json:"PollingStrategy"`
		RingTimeout     int          `json:"RingTimeout"`
		MasterTimeout   int          `json:"MasterTimeout"`
		MaxCallers      int          `json:"MaxCallersInQueue"`
		SLATime         int          `json:"SLATime"`
		IsRegistered    bool         `json:"IsRegistered"`
		NoAnswer        *destination `json:"ForwardNoAnswer"`
		Agents          []member     `json:"Agents"`
		Managers        []member     `json:"Managers"`
	}
	q := url.Values{
		"$select":  {"Id,Number,Name,PollingStrategy,RingTimeout,MasterTimeout,MaxCallersInQueue,SLATime,IsRegistered,ForwardNoAnswer"},
		"$expand":  {"Agents($select=Id,Number,Name,SkillGroup),Managers($select=Id,Number,Name)"},
		"$orderby": {"Number"},
	}
	got, err := list[record](ctx, acct.client, "Queues", q, p.cfg.MaxItems)
	if err != nil {
		return QueuesResult{}, acct.call(err)
	}
	out := QueuesResult{Queues: make([]QueueRow, 0, len(got.Rows))}
	for _, qu := range got.Rows {
		out.Queues = append(out.Queues, QueueRow{
			Number: qu.Number, Name: qu.Name, Strategy: qu.PollingStrategy,
			RingSeconds: qu.RingTimeout, MaxWaitSeconds: qu.MasterTimeout, MaxCallers: qu.MaxCallers,
			SLASeconds: qu.SLATime, Registered: qu.IsRegistered,
			Agents: memberTexts(qu.Agents), Managers: memberTexts(qu.Managers), NoAnswer: qu.NoAnswer.text(),
		})
	}
	out.Queues, out.truncation = bound(out.Queues, got.Truncated)
	out.Returned = len(out.Queues)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// --- digital receptionists ------------------------------------------------------

type receptionistsArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
}

// ReceptionistRow is one digital receptionist.
type ReceptionistRow struct {
	Number         string `json:"number"`
	Name           string `json:"name"`
	Type           string `json:"type,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	Registered     bool   `json:"registered"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	OnTimeout      string `json:"on_timeout"`
	// Menu is what each key does: "1: Extension 100", "0: Queue 800".
	Menu []string `json:"menu"`
}

// ReceptionistsResult is the receptionist list.
type ReceptionistsResult struct {
	// Customer is the business this answer is about, so an answer can never be
	// read as another customer's.
	Customer      string            `json:"customer"`
	Receptionists []ReceptionistRow `json:"receptionists"`
	Returned      int               `json:"returned"`
	truncation
}

func (p *Plugin) listReceptionists(ctx context.Context, args receptionistsArgs) (ReceptionistsResult, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return ReceptionistsResult{}, err
	}
	type record struct {
		Number             string `json:"Number"`
		Name               string `json:"Name"`
		IVRType            string `json:"IVRType"`
		PromptFilename     string `json:"PromptFilename"`
		IsRegistered       bool   `json:"IsRegistered"`
		Timeout            int    `json:"Timeout"`
		TimeoutForwardType string `json:"TimeoutForwardType"`
		TimeoutForwardDN   string `json:"TimeoutForwardDN"`
		Forwards           []struct {
			Input       string `json:"Input"`
			ForwardType string `json:"ForwardType"`
			ForwardDN   string `json:"ForwardDN"`
		} `json:"Forwards"`
	}
	q := url.Values{
		"$select":  {"Id,Number,Name,IVRType,PromptFilename,IsRegistered,Timeout,TimeoutForwardType,TimeoutForwardDN"},
		"$expand":  {"Forwards($select=Id,Input,ForwardType,ForwardDN)"},
		"$orderby": {"Number"},
	}
	got, err := list[record](ctx, acct.client, "Receptionists", q, p.cfg.MaxItems)
	if err != nil {
		return ReceptionistsResult{}, acct.call(err)
	}
	out := ReceptionistsResult{Receptionists: make([]ReceptionistRow, 0, len(got.Rows))}
	for _, r := range got.Rows {
		row := ReceptionistRow{
			Number: r.Number, Name: r.Name, Type: r.IVRType, Prompt: r.PromptFilename,
			Registered: r.IsRegistered, TimeoutSeconds: r.Timeout,
			OnTimeout: forwardText(r.TimeoutForwardType, r.TimeoutForwardDN), Menu: []string{},
		}
		for _, f := range r.Forwards {
			row.Menu = append(row.Menu, fmt.Sprintf("%s: %s", f.Input, forwardText(f.ForwardType, f.ForwardDN)))
		}
		out.Receptionists = append(out.Receptionists, row)
	}
	out.Receptionists, out.truncation = bound(out.Receptionists, got.Truncated)
	out.Returned = len(out.Receptionists)
	acct.note(nil)
	out.Customer = acct.name
	return out, nil
}

// forwardText renders an IVR forward -- a kind and a number -- as one phrase.
func forwardText(kind, dn string) string {
	if kind == "" {
		return "None"
	}
	if dn == "" {
		return kind
	}
	return kind + " " + dn
}
