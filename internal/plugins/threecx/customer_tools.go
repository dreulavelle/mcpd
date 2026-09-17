package threecx

import (
	"context"
	"strings"
	"sync"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tool for "which businesses can I ask about here, and what do they run".
//
// It reads nothing from any phone system. What it reports is configuration --
// the names, the aliases, the addresses -- and what the last call to each one
// found, so a model can pick the right phone system before spending a round
// trip on the wrong one.
//
// The answer is nested rather than flat, and that is the point of it: a
// business carries the phone systems it owns, so "does this customer have more
// than one" is a field rather than something to infer from names that happen
// to look alike. A flat list of "Acme HQ" and "Acme Branch" reads as two
// businesses to anything that has not been told otherwise, which is exactly
// the mistake this tool exists to stop.

func (p *Plugin) registerCustomerTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_customers",
		Title: "List customers",
		Description: "The businesses this instance serves and the phone systems each " +
			"one runs, with aliases, addresses and whether the last call to each " +
			"worked. A business with more than one system lists them under systems, " +
			"and tools about that business need system set to one of their ids. Set " +
			"check to sign in to every system now.",
		Idempotent: true,
	}, p.listCustomers)
}

type customersArgs struct {
	Check bool `json:"check,omitempty" jsonschema:"sign in to every phone system now and report which answer; one request each"`
}

// SystemRow is one phone system: one address, one sign-in, one thing a tool
// can be pointed at.
type SystemRow struct {
	// ID is what to pass as `system`. Derived from the name, so it is stable
	// for as long as the name is.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Aliases are the other names this one answers to. They are the system's
	// rather than the business's, which matters once a business has two: "hq"
	// belongs to one of them.
	Aliases []string `json:"aliases"`
	// Host is the address without its https scheme, which is the form 3CX
	// itself reports and the form somebody types. An http address keeps its
	// scheme, because that one is the exception. It is here because it is
	// often the only thing that tells two of a customer's systems apart to
	// somebody who knows the estate.
	Host string `json:"host"`
	// Reachable is what the last call found: true, false, or absent when
	// nothing has been asked of this phone system yet.
	Reachable *bool  `json:"reachable,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// CustomerRow is one business this instance serves, and the phone systems it
// owns.
type CustomerRow struct {
	// ID is the business's identifier, for an answer that wants to name it
	// without spelling prose back.
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	// Systems is every phone system this business owns, and is the field that
	// says whether a tool call needs to name one. Never empty.
	Systems []SystemRow `json:"systems"`
	// SystemCount is len(Systems), stated rather than counted, because
	// "does this customer have more than one" is the question this tool
	// exists to answer and a model should not have to derive it.
	SystemCount int `json:"system_count"`
	// Host, Reachable and LastError describe the business's one phone system,
	// and are absent when it has more than one.
	//
	// They are the fields this tool reported before a business could have two,
	// and they still mean exactly what they meant: a caller reading a
	// single-system customer sees what it always saw. For a business with
	// several there is no honest single answer -- two systems on two addresses,
	// one of them down -- and inventing one would be worse than the absence,
	// which sends a reader to Systems where the answers are.
	Host      string `json:"host,omitempty"`
	Reachable *bool  `json:"reachable,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// CustomersResult is the customer list.
type CustomersResult struct {
	Customers []CustomerRow `json:"customers"`
	// Count is how many businesses, which is what it has always counted.
	Count int `json:"count"`
	// SystemCount is how many phone systems those businesses own between
	// them. Equal to Count until one of them has two.
	SystemCount int `json:"system_count"`
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
				actx, cancel := context.WithTimeout(ctx, p.cfg.Timeout())
				defer cancel()
				_, err := a.client.Probe(actx)
				a.note(err)
			}(a)
		}
		wg.Wait()
	}
	out := CustomersResult{Customers: make([]CustomerRow, 0, len(p.customers))}
	for _, c := range p.customers {
		row := CustomerRow{
			ID: c.id, Name: c.name, Aliases: []string{},
			Systems:     make([]SystemRow, 0, len(c.systems)),
			SystemCount: len(c.systems),
		}
		for _, a := range c.systems {
			sys := SystemRow{ID: a.id, Name: a.name, Aliases: a.aliases, Host: displayHost(a.host)}
			if sys.Aliases == nil {
				sys.Aliases = []string{}
			}
			sys.Reachable, sys.LastError = a.health()
			row.Systems = append(row.Systems, sys)
		}
		if len(c.systems) == 1 {
			only := row.Systems[0]
			row.Aliases, row.Host = only.Aliases, only.Host
			row.Reachable, row.LastError = only.Reachable, only.LastError
		}
		out.Customers = append(out.Customers, row)
	}
	out.Count = len(out.Customers)
	out.SystemCount = len(p.accounts)
	return out, nil
}

// health is what the last call to this phone system found. Nil rather than
// false when nothing has been asked of it: "not reachable" and "not yet
// asked" are different answers, and a customer nobody has called is not a
// customer with a problem.
func (a *account) health() (*bool, string) {
	a.mu.RLock()
	err, checked := a.lastErr, a.checked
	a.mu.RUnlock()
	if checked.IsZero() {
		return nil, ""
	}
	ok := err == nil
	if err != nil {
		return &ok, plugins.Explain(err).Error()
	}
	return &ok, ""
}

// displayHost renders an address the way a person writes one: the bare FQDN
// for the ordinary https case, and the whole thing when it is not.
func displayHost(root string) string {
	trimmed, found := strings.CutPrefix(root, "https://")
	if !found {
		return root
	}
	return strings.TrimRight(trimmed, "/")
}
