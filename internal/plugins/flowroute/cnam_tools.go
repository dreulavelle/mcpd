package flowroute

import (
	"context"
	"net/url"
	"strconv"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tool for "what name shows up when this customer calls out".
//
// A CNAM record is submitted and then approved or rejected by the carriers
// that serve it, which takes days and can fail. So the interesting fields are
// not the name itself but whether it was approved and, when it was not, why --
// that is the answer to a customer asking why their business name still is not
// showing.

func (p *Plugin) registerCNAMTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_cnam_records",
		Title: "List caller-ID names",
		Description: "The caller-ID name records on the account, with whether " +
			"each was approved, when, and the reason if it was rejected. " +
			"get_number says which record a particular number uses.",
		Idempotent: true,
	}, p.listCNAMRecords)
}

// cnamAttrs is one caller-ID name record.
type cnamAttrs struct {
	ApprovalDatetime blank  `json:"approval_datetime"`
	CreationDatetime blank  `json:"creation_datetime"`
	IsApproved       bool   `json:"is_approved"`
	RejectionReason  blank  `json:"rejection_reason"`
	Value            string `json:"value"`
}

type cnamArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's Flowroute account, by business name or alias; needed when this instance serves more than one"`

	// A pointer so that "not mentioned" and "false" are different questions.
	// Flowroute filters on the parameter's presence, and defaulting it to
	// false would silently hide every approved record.
	Approved *bool `json:"approved,omitempty" jsonschema:"true for only approved records, false for only unapproved; omit for both"`
	Limit    int   `json:"limit,omitempty" jsonschema:"most records to return; the instance's ceiling applies"`
}

// CNAMRow is one caller-ID name record.
type CNAMRow struct {
	ID    string `json:"id"`
	Value string `json:"value"`
	// Approved is what decides whether this name is actually being shown.
	Approved        bool   `json:"approved"`
	CreatedAt       string `json:"created_at,omitempty"`
	ApprovedAt      string `json:"approved_at,omitempty"`
	RejectionReason string `json:"rejection_reason,omitempty"`
}

// CNAMResult is the caller-ID name list with its counts.
type CNAMResult struct {
	Customer string    `json:"customer"`
	Records  []CNAMRow `json:"records"`
	Count    int       `json:"count"`
	truncation
}

func (p *Plugin) listCNAMRecords(ctx context.Context, args cnamArgs) (CNAMResult, error) {
	a, err := p.customerFor(args.Customer)
	if err != nil {
		return CNAMResult{}, err
	}
	q := url.Values{}
	if args.Approved != nil {
		q.Set("is_approved", strconv.FormatBool(*args.Approved))
	}
	pg, err := a.client.list(ctx, "/v2/cnams", q, args.Limit)
	a.note(err, p.deps.Now())
	if err != nil {
		return CNAMResult{}, err
	}
	rows := make([]CNAMRow, 0, len(pg.items))
	for _, item := range pg.items {
		var at cnamAttrs
		if err := item.attrs(&at); err != nil {
			return CNAMResult{}, err
		}
		rows = append(rows, CNAMRow{
			ID:              item.ID.String(),
			Value:           at.Value,
			Approved:        at.IsApproved,
			CreatedAt:       at.CreationDatetime.String(),
			ApprovedAt:      at.ApprovalDatetime.String(),
			RejectionReason: at.RejectionReason.String(),
		})
	}
	rows, cut := bound(rows, pg.more)
	return CNAMResult{
		Customer: a.name, Records: rows, Count: len(rows), truncation: cut,
	}, nil
}
