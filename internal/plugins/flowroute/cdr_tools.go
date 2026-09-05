package flowroute

import (
	"context"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tool for "what happened to the call records somebody asked for".
//
// Flowroute does not serve call detail records as a query. An export job is
// requested, it is built in the background, and the result is downloaded when
// it is ready. Requesting one is a POST, so this integration does not: what it
// can do is say which jobs exist and where each one has got to, which is the
// question somebody has when an export they started in the portal has not
// arrived.

func (p *Plugin) registerCDRTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_cdr_exports",
		Title: "List call-record exports",
		Description: "The call-detail export jobs on the account and where each " +
			"has got to. Starting an export is a write, so it is done in " +
			"Flowroute Manage rather than here.",
		Idempotent: true,
	}, p.listCDRExports)
}

type cdrExportsArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's Flowroute account, by business name or alias; needed when this instance serves more than one"`

	Limit int `json:"limit,omitempty" jsonschema:"most export jobs to return; the instance's ceiling applies"`
}

// CDRExportRow is one export job.
//
// The attributes are carried through by name rather than mapped one by one:
// this is a job record whose fields are Flowroute's own vocabulary for its
// queue, none of them is a credential, and inventing names for them here would
// be a translation that has to be kept in step with theirs for no gain.
type CDRExportRow struct {
	ID string `json:"id"`
	// Status is lifted out because it is the only field anybody reads first.
	Status string         `json:"status,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
}

// CDRExportsResult is the export job list.
type CDRExportsResult struct {
	Customer string         `json:"customer"`
	Exports  []CDRExportRow `json:"exports"`
	Count    int            `json:"count"`
	truncation
}

func (p *Plugin) listCDRExports(ctx context.Context, args cdrExportsArgs) (CDRExportsResult, error) {
	a, err := p.customerFor(args.Customer)
	if err != nil {
		return CDRExportsResult{}, err
	}
	pg, err := a.client.list(ctx, "/v2/cdrs/exports", nil, args.Limit)
	a.note(err, p.deps.Now())
	if err != nil {
		return CDRExportsResult{}, err
	}
	rows := make([]CDRExportRow, 0, len(pg.items))
	for _, item := range pg.items {
		fields := map[string]any{}
		if err := item.attrs(&fields); err != nil {
			return CDRExportsResult{}, err
		}
		row := CDRExportRow{ID: item.ID.String(), Fields: fields}
		if s, ok := fields["status"].(string); ok {
			row.Status = s
			delete(fields, "status")
		}
		if len(fields) == 0 {
			row.Fields = nil
		}
		rows = append(rows, row)
	}
	rows, cut := bound(rows, pg.more)
	return CDRExportsResult{
		Customer: a.name, Exports: rows, Count: len(rows), truncation: cut,
	}, nil
}
