package threecx

import (
	"context"
	"sync"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tool for "which businesses can I ask about here".
//
// It reads nothing from any phone system. What it reports is configuration --
// the names, the aliases, the address -- and what the last call to each one
// found, so a model can pick the right customer before spending a round trip
// on the wrong one.

func (p *Plugin) registerCustomerTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_customers",
		Title: "List customers",
		Description: "The businesses this instance serves, with aliases, address " +
			"and whether the last call to each worked. Set check to sign in to " +
			"each one now.",
		Idempotent: true,
	}, p.listCustomers)
}

type customersArgs struct {
	Check bool `json:"check,omitempty" jsonschema:"sign in to every customer's phone system now and report which answer; one request each"`
}

// CustomerRow is one business this instance serves.
type CustomerRow struct {
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Host    string   `json:"host"`
	// Reachable is what the last call found: true, false, or absent when
	// nothing has been asked of this customer yet.
	Reachable *bool  `json:"reachable,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// CustomersResult is the customer list.
type CustomersResult struct {
	Customers []CustomerRow `json:"customers"`
	Count     int           `json:"count"`
}

func (p *Plugin) listCustomers(ctx context.Context, args customersArgs) (CustomersResult, error) {
	if args.Check {
		// Together rather than in turn, and each bounded on its own: the point
		// is to say which of many phone systems is the one that will not
		// answer, and the slow one must not hide the rest.
		var wg sync.WaitGroup
		for _, a := range p.accounts {
			wg.Add(1)
			go func(a *account) {
				defer wg.Done()
				actx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
				defer cancel()
				_, err := a.client.Probe(actx)
				a.note(err)
			}(a)
		}
		wg.Wait()
	}
	out := CustomersResult{Customers: make([]CustomerRow, 0, len(p.accounts))}
	for _, a := range p.accounts {
		row := CustomerRow{Name: a.name, Aliases: a.aliases, Host: a.host}
		if row.Aliases == nil {
			row.Aliases = []string{}
		}
		a.mu.RLock()
		err, checked := a.lastErr, a.checked
		a.mu.RUnlock()
		if !checked.IsZero() {
			ok := err == nil
			row.Reachable = &ok
			if err != nil {
				row.LastError = plugins.Explain(err).Error()
			}
		}
		out.Customers = append(out.Customers, row)
	}
	out.Count = len(out.Customers)
	return out, nil
}
