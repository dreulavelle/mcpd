package flowroute

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Run against a real Flowroute account. Skipped unless one is supplied, so it
// costs nothing in CI and is there when somebody has credentials:
//
//	FLOWROUTE_TEST_ACCESS_KEY=… FLOWROUTE_TEST_SECRET_KEY=… \
//	  go test ./internal/plugins/flowroute/ -run Integration -v
//
// This is the half of the package a fake cannot reach. The fake in
// tools_test.go answers with what the documentation says the API returns;
// these prove the API agrees. Two of the shapes here were only ever read from
// the documentation -- the port order listing and the export job -- and its
// sample for the first is not valid JSON, so an account that has either is
// worth running this against.
//
// Nothing here prints a number, a name or an address. What it asserts is that
// a read succeeded and came back in the shape this package expects; the
// account's contents are the customer's.
func integrationPlugin(t *testing.T) *Plugin {
	t.Helper()
	access := os.Getenv("FLOWROUTE_TEST_ACCESS_KEY")
	secret := os.Getenv("FLOWROUTE_TEST_SECRET_KEY")
	if access == "" || secret == "" {
		t.Skip("set FLOWROUTE_TEST_ACCESS_KEY and FLOWROUTE_TEST_SECRET_KEY to run against a real account")
	}
	p, err := New(plugins.Deps{
		Instance: "flowroute",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}, Config{Customers: []Customer{{
		Name: "Test account", AccessKey: access, SecretKey: secret,
	}}})
	if err != nil {
		t.Fatalf("building the plugin: %v", err)
	}
	return p
}

func TestIntegrationReadsTheAccount(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := p.Check(ctx); got.State != plugins.HealthState("healthy") {
		t.Fatalf("Check after a successful start: %+v", got)
	}

	numbers, err := p.listNumbers(ctx, numbersArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_numbers: %v", err)
	}
	t.Logf("list_numbers returned %d", numbers.Count)
	for _, n := range numbers.Numbers {
		if n.Number == "" || n.Formatted == "" {
			t.Fatal("a number row came back without a number")
		}
	}

	// The single read, including the included-array walk that resolves the
	// route. Only run when there is a number to ask about.
	if numbers.Count > 0 {
		detail, err := p.getNumber(ctx, numberArgs{Number: numbers.Numbers[0].Number})
		if err != nil {
			t.Fatalf("get_number: %v", err)
		}
		if detail.Number != numbers.Numbers[0].Number {
			t.Fatalf("get_number answered about a different number")
		}
		if detail.PrimaryRoute != nil && detail.PrimaryRoute.ID == "" {
			t.Fatal("a resolved route came back with no id")
		}
	}

	routes, err := p.listRoutes(ctx, routesArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_routes: %v", err)
	}
	t.Logf("list_routes returned %d", routes.Count)

	edges, err := p.listEdgeStrategies(ctx, edgeArgs{})
	if err != nil {
		t.Fatalf("list_edge_strategies: %v", err)
	}
	if edges.Count == 0 {
		t.Fatal("Flowroute serves a fixed set of edge strategies; none came back")
	}

	e911s, err := p.listE911Addresses(ctx, e911ListArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_e911_addresses: %v", err)
	}
	t.Logf("list_e911_addresses returned %d", e911s.Count)
	for _, a := range e911s.Addresses {
		if a.ID == "" || a.Address == "" {
			t.Fatal("an emergency address came back without an id or an address line")
		}
	}

	cnams, err := p.listCNAMRecords(ctx, cnamArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_cnam_records: %v", err)
	}
	t.Logf("list_cnam_records returned %d", cnams.Count)

	// An account with no port orders answers 404. That is an answer, and this
	// is the assertion that it is read as one.
	orders, err := p.listPortOrders(ctx, portOrdersArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_port_orders: %v", err)
	}
	t.Logf("list_port_orders returned %d", orders.Count)
	for _, o := range orders.Orders {
		if len(o.UnmappedFields) > 0 {
			// Not a failure: the point of the field is to make a shape that
			// has moved visible. Names only, never values.
			t.Logf("port order carried fields this package does not map: %s",
				strings.Join(o.UnmappedFields, ", "))
		}
	}

	exports, err := p.listCDRExports(ctx, cdrExportsArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_cdr_exports: %v", err)
	}
	t.Logf("list_cdr_exports returned %d", exports.Count)
}

// The guarantee, against the real API: a write is refused before it is sent.
func TestIntegrationRefusesAWrite(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	numbers, err := p.listNumbers(ctx, numbersArgs{Limit: 1})
	if err != nil {
		t.Fatalf("list_numbers: %v", err)
	}
	if numbers.Count == 0 {
		t.Skip("the account holds no numbers, so there is nothing to try to release")
	}

	// Built by hand rather than through a tool, because no tool can make one.
	// It must not reach the network.
	a := p.accounts[0]
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		a.client.base+"/v2/numbers/"+numbers.Numbers[0].Number, nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	if _, err := a.client.http.Do(req); err == nil {
		t.Fatal("a DELETE reached Flowroute; the guard did not hold")
	} else if !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("want the read-only refusal, got %v", err)
	}
}
