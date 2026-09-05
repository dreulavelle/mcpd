package flowroute

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/plugins"
)

// Fixtures. Every number is in the 206 555 range Flowroute's own examples use,
// and every name and address is invented.

const numbersPage = `{
  "data": [
    {"attributes":{"alias":"Acme Dental front desk","cnam_lookups_enabled":true,
      "iso_country":"US","number_type":"longcode","rate_center":"seattle",
      "state":"wa","status":"PURCHASED","tier":"Local Inbound","value":"12065550100"},
     "id":"12065550100","links":{"self":"x"},"type":"number"},
    {"attributes":{"alias":null,"cnam_lookups_enabled":false,
      "iso_country":"US","number_type":"tollfree","rate_center":"","state":"",
      "status":"PURCHASED","tier":"Toll Free","value":"18885550101"},
     "id":"18885550101","links":{"self":"x"},"type":"number"}
  ],
  "links":{"self":"x"}
}`

const numberDetail = `{
  "data":{
    "attributes":{"alias":"Acme Dental front desk","cnam_lookups_enabled":true,
      "inbound_rate":0.0025,"iso_country":"US","messaging_enabled":true,
      "monthly_cost":1.0,"note":"Acme Dental Group - main line","number_type":"longcode",
      "rate_center":"seattle","rate_type":"local","setup_cost":0.0,"state":"wa",
      "status":"PURCHASED","tier":"Local Inbound","value":"12065550100"},
    "id":"12065550100",
    "relationships":{
      "cnam_preset":{"data":null},
      "e911_address":{"data":{"id":"20155","type":"e911"}},
      "failover_route":{"data":null},
      "primary_route":{"data":{"id":"83861","type":"route"}}},
    "type":"number"},
  "included":[
    {"attributes":{"alias":"acme-pbx","edge_strategy_id":null,"route_type":"host",
      "value":"pbx.acme.example"},"id":"83861","links":{"self":"x"},"type":"route"}],
  "links":{"self":"x"}
}`

// A number with nothing pointed at it: the silent outage this integration
// exists to make visible.
const numberNoRoute = `{
  "data":{
    "attributes":{"alias":null,"cnam_lookups_enabled":false,"inbound_rate":0.0025,
      "iso_country":"US","messaging_enabled":false,"monthly_cost":1.0,"note":null,
      "number_type":"longcode","rate_center":"seattle","rate_type":"local",
      "setup_cost":0.0,"state":"wa","status":"PURCHASED","tier":"Local Inbound",
      "value":"12065550102"},
    "id":"12065550102",
    "relationships":{"cnam_preset":{"data":null},"e911_address":{"data":null},
      "failover_route":{"data":null},"primary_route":{"data":null}},
    "type":"number"},
  "links":{"self":"x"}
}`

func TestListNumbersCarriesTheAliasAndFormatsTheNumber(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/numbers"] = numbersPage
	p, _ := newPlugin(t, f)

	got, err := p.listNumbers(context.Background(), numbersArgs{})
	if err != nil {
		t.Fatalf("listNumbers: %v", err)
	}
	if got.Count != 2 {
		t.Fatalf("want 2 numbers, got %d", got.Count)
	}
	if got.Numbers[0].Alias != "Acme Dental front desk" {
		t.Errorf("alias not carried through: %q", got.Numbers[0].Alias)
	}
	if got.Numbers[0].Formatted != "+1 206 555 0100" {
		t.Errorf("formatted number is %q", got.Numbers[0].Formatted)
	}
	// A null alias is the common case and must not become the string "null".
	if got.Numbers[1].Alias != "" {
		t.Errorf("a null alias should be empty, got %q", got.Numbers[1].Alias)
	}
	if got.Truncated {
		t.Error("a listing with no next link is not truncated")
	}
}

// A listing that stops short says so, because a model shown part of an account
// and not told will answer as though it saw all of it.
func TestListNumbersSaysWhenItStoppedShort(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/numbers?limit=2&offset=0"] = `{"data":[
      {"attributes":{"value":"12065550100","status":"PURCHASED"},"id":"12065550100","type":"number"},
      {"attributes":{"value":"12065550101","status":"PURCHASED"},"id":"12065550101","type":"number"}],
      "links":{"self":"x","next":"https://api.flowroute.com/v2/numbers?limit=2&offset=2"}}`
	p, _ := newPlugin(t, f)

	got, err := p.listNumbers(context.Background(), numbersArgs{Limit: 2})
	if err != nil {
		t.Fatalf("listNumbers: %v", err)
	}
	if !got.Truncated || !strings.Contains(got.Reason, "narrow") {
		t.Fatalf("want a truncation notice, got %+v", got.truncation)
	}
}

// The listing walks pages until Flowroute stops offering a next one.
func TestListNumbersWalksPages(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/numbers?limit=3&offset=0"] = `{"data":[
      {"attributes":{"value":"12065550100"},"id":"12065550100","type":"number"},
      {"attributes":{"value":"12065550101"},"id":"12065550101","type":"number"}],
      "links":{"self":"x","next":"https://api.flowroute.com/v2/numbers?limit=3&offset=2"}}`
	f.bodies["/v2/numbers?limit=1&offset=2"] = `{"data":[
      {"attributes":{"value":"12065550102"},"id":"12065550102","type":"number"}],
      "links":{"self":"x"}}`

	p, _ := newPlugin(t, f)
	p.accounts[0].client.maxItems = 3

	got, err := p.listNumbers(context.Background(), numbersArgs{})
	if err != nil {
		t.Fatalf("listNumbers: %v", err)
	}
	if got.Count != 3 {
		t.Fatalf("want 3 numbers across two pages, got %d", got.Count)
	}
	if f.reads.Load() != 2 {
		t.Fatalf("want two upstream reads, made %d", f.reads.Load())
	}
}

func TestGetNumberResolvesItsRouteFromTheIncludedArray(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/numbers/12065550100"] = numberDetail
	p, _ := newPlugin(t, f)

	got, err := p.getNumber(context.Background(), numberArgs{Number: "+1 (206) 555-0100"})
	if err != nil {
		t.Fatalf("getNumber: %v", err)
	}
	if got.PrimaryRoute == nil {
		t.Fatal("the primary route should have been resolved")
	}
	// The route arrives beside the number rather than inside it, so this is
	// the assertion that the included array was actually read.
	if got.PrimaryRoute.Value != "pbx.acme.example" || got.PrimaryRoute.Type != "host" {
		t.Fatalf("route not resolved from included: %+v", got.PrimaryRoute)
	}
	if got.Note != "Acme Dental Group - main line" {
		t.Errorf("the note is where the customer's name is usually written; got %q", got.Note)
	}
	if got.E911AddressID != "20155" {
		t.Errorf("e911 address id is %q", got.E911AddressID)
	}
	if len(got.Notes) != 0 {
		t.Errorf("a fully configured number needs no notes, got %v", got.Notes)
	}
}

// A number with no route rings nowhere, and nothing else in the record says
// so. The note is the whole point of the tool.
func TestGetNumberSaysWhenNothingIsPointedAtIt(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/numbers/12065550102"] = numberNoRoute
	p, _ := newPlugin(t, f)

	got, err := p.getNumber(context.Background(), numberArgs{Number: "2065550102"})
	if err != nil {
		t.Fatalf("getNumber: %v", err)
	}
	joined := strings.Join(got.Notes, " ")
	if !strings.Contains(joined, "no primary route") {
		t.Errorf("want a note about the missing route, got %v", got.Notes)
	}
	if !strings.Contains(joined, "no location") {
		t.Errorf("want a note about the missing emergency address, got %v", got.Notes)
	}
}

// A number that is not on the account is an answer. A path Flowroute does not
// serve is a bug here, and the two must not be reported the same way -- one
// sends somebody looking for a number, the other sends them to this package.
func TestGetNumberTellsAbsentFromMisrouted(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.absent["/v2/numbers/12065559999"] = true
	p, _ := newPlugin(t, f)

	_, err := p.getNumber(context.Background(), numberArgs{Number: "12065559999"})
	if err == nil || !strings.Contains(err.Error(), "is not on Acme Dental Group's account") {
		t.Fatalf("an absent number should say so plainly, and name the customer: %v", err)
	}

	// The fake answers an unknown path the way Flowroute does: a 404 whose
	// status is the HTTP line rather than a resource error.
	_, err = p.getNumber(context.Background(), numberArgs{Number: "12065558888"})
	if err == nil {
		t.Fatal("a path the API does not serve should be an error")
	}
	if strings.Contains(err.Error(), "is not on Acme Dental Group's account") {
		t.Fatalf("a routing failure must not be reported as an absent number: %v", err)
	}
	if !strings.Contains(err.Error(), "does not serve that path") {
		t.Fatalf("want the routing failure named, got %v", err)
	}
}

func TestGetNumberRejectsSomethingThatIsNotANumber(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	p, _ := newPlugin(t, f)

	for _, in := range []string{"", "acme", "555-0100"} {
		if _, err := p.getNumber(context.Background(), numberArgs{Number: in}); err == nil {
			t.Errorf("getNumber(%q) should have been refused", in)
		}
	}
	if f.reads.Load() != 0 {
		t.Errorf("a malformed number should not reach the API; made %d reads", f.reads.Load())
	}
}

func TestListE911AddressesRendersOneAddressLine(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/e911s"] = `{"data":[
      {"attributes":{"address_type":"Suite","address_type_number":"400","alias":null,
        "city":"Seattle","country":"US","first_name":"Dana","label":"Acme Dental HQ",
        "last_name":"Reyes","state":"WA","street_name":"Pine Street",
        "street_number":"1100","zip":"98101"},
       "id":"20155","links":{"self":"x"},"type":"e911"}],
      "links":{"self":"x"}}`
	p, _ := newPlugin(t, f)

	got, err := p.listE911Addresses(context.Background(), e911ListArgs{})
	if err != nil {
		t.Fatalf("listE911Addresses: %v", err)
	}
	if got.Count != 1 {
		t.Fatalf("want one address, got %d", got.Count)
	}
	want := "1100 Pine Street, Suite 400, Seattle, WA 98101, US"
	if got.Addresses[0].Address != want {
		t.Errorf("address line is %q, want %q", got.Addresses[0].Address, want)
	}
	if got.Addresses[0].Name != "Dana Reyes" {
		t.Errorf("name is %q", got.Addresses[0].Name)
	}
}

func TestListCNAMRecordsCarriesWhyOneWasRejected(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/cnams"] = `{"data":[
      {"attributes":{"approval_datetime":null,"creation_datetime":"2026-08-01 10:00:00+00:00",
        "is_approved":false,"rejection_reason":"Name does not match the account",
        "value":"ACME DENTAL"},"id":"22671","links":{"self":"x"},"type":"cnam"}],
      "links":{"self":"x"}}`
	p, _ := newPlugin(t, f)

	got, err := p.listCNAMRecords(context.Background(), cnamArgs{})
	if err != nil {
		t.Fatalf("listCNAMRecords: %v", err)
	}
	if got.Records[0].Approved {
		t.Error("the record is not approved")
	}
	if !strings.Contains(got.Records[0].RejectionReason, "does not match") {
		t.Errorf("the rejection reason is the answer to the question; got %q",
			got.Records[0].RejectionReason)
	}
}

// The filter is a pointer so that "not mentioned" and "false" differ. Asking
// for unapproved records must not be indistinguishable from not asking.
func TestListCNAMRecordsOnlyFiltersWhenAsked(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/cnams"] = `{"data":[],"links":{"self":"x"}}`
	p, _ := newPlugin(t, f)

	if _, err := p.listCNAMRecords(context.Background(), cnamArgs{}); err != nil {
		t.Fatalf("unfiltered: %v", err)
	}
	no := false
	if _, err := p.listCNAMRecords(context.Background(), cnamArgs{Approved: &no}); err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if len(f.seen) != 2 || f.seen[0] == f.seen[1] {
		t.Fatalf("the two calls should differ in their query: %v", f.seen)
	}
}

// An account with no port orders answers 404 rather than an empty array. That
// is an answer, and reporting it as a failure would have somebody chasing a
// broken integration instead of reading "there are none".
func TestListPortOrdersTreatsNoneAsEmpty(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.absent["/v2/portorders"] = true
	p, _ := newPlugin(t, f)

	got, err := p.listPortOrders(context.Background(), portOrdersArgs{})
	if err != nil {
		t.Fatalf("an account with no port orders is not an error: %v", err)
	}
	if got.Count != 0 {
		t.Fatalf("want no orders, got %d", got.Count)
	}
}

// The documentation shows the listing nested inside a portorder_list entity
// and the flat shape everything else uses is the obvious alternative. Neither
// could be checked against a live account, so both are read.
func TestListPortOrdersReadsBothDocumentedShapes(t *testing.T) {
	t.Parallel()

	flat := `{"data":[
      {"attributes":{"alias":"Acme port","status":"pending","numbers":["12065550100"]},
       "id":"42323","type":"portorder"}],"links":{"self":"x"}}`
	nested := `{"data":[{"attributes":{"orders":{"data":[
      {"attributes":{"alias":"Acme port","status":"pending","numbers":["12065550100"]},
       "id":"42323","type":"portorder"}]}},"id":"0","type":"portorder_list"}],
      "links":{"self":"x"}}`

	for name, body := range map[string]string{"flat": flat, "nested": nested} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.bodies["/v2/portorders"] = body
			p, _ := newPlugin(t, f)

			got, err := p.listPortOrders(context.Background(), portOrdersArgs{})
			if err != nil {
				t.Fatalf("listPortOrders: %v", err)
			}
			if got.Count != 1 {
				t.Fatalf("want one order, got %d", got.Count)
			}
			o := got.Orders[0]
			if o.ID != "42323" || o.Alias != "Acme port" || o.Status != "pending" {
				t.Fatalf("order not read: %+v", o)
			}
			if len(o.Numbers) != 1 || o.Numbers[0] != "12065550100" {
				t.Fatalf("numbers not read: %v", o.Numbers)
			}
		})
	}
}

// A field this package does not know is dropped and its name reported, so a
// shape that has moved is visible rather than silently empty -- and the value
// is never carried through on a guess.
func TestPortOrderNamesFieldsItDoesNotKnow(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/portorders"] = `{"data":[
      {"attributes":{"alias":"Acme port","status":"pending",
        "losing_carrier":"Some Telco","btn":"12065550199"},
       "id":"42323","type":"portorder"}],"links":{"self":"x"}}`
	p, _ := newPlugin(t, f)

	got, err := p.listPortOrders(context.Background(), portOrdersArgs{})
	if err != nil {
		t.Fatalf("listPortOrders: %v", err)
	}
	names := strings.Join(got.Orders[0].UnmappedFields, ",")
	if names != "btn,losing_carrier" {
		t.Fatalf("want the unknown field names, got %q", names)
	}
	// Names, never values: the point is to make the gap visible without
	// passing through content nobody has looked at.
	if strings.Contains(mustString(t, got), "Some Telco") {
		t.Fatal("an unmapped field's value must not be returned")
	}
}

func TestGetPortOrderPrefersTheStatusEndpointsTimestamp(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/portorders/41351"] = `{"data":{"attributes":{"alias":"Acme port",
      "status":"pending"},"id":"41351","type":"portorder"},"links":{"self":"x"}}`
	f.bodies["/v2/portorders/41351/status"] = `{"data":{"attributes":{"status":"foc",
      "status_updated_at":"2026-08-31T21:34:17Z"},"id":"41351","type":"portorder"}}`
	p, _ := newPlugin(t, f)

	got, err := p.getPortOrder(context.Background(), portOrderArgs{ID: "41351"})
	if err != nil {
		t.Fatalf("getPortOrder: %v", err)
	}
	if got.Status != "foc" {
		t.Errorf("the status endpoint is the fresher answer; got %q", got.Status)
	}
	if got.StatusUpdatedAt != "2026-08-31T21:34:17Z" {
		t.Errorf("status timestamp is %q", got.StatusUpdatedAt)
	}
}

// The order was read; only the timestamp was not. An answer without it beats
// no answer at all.
func TestGetPortOrderSurvivesAStatusEndpointThatFails(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/portorders/41351"] = `{"data":{"attributes":{"alias":"Acme port",
      "status":"pending"},"id":"41351","type":"portorder"},"links":{"self":"x"}}`
	f.absent["/v2/portorders/41351/status"] = true
	p, _ := newPlugin(t, f)

	got, err := p.getPortOrder(context.Background(), portOrderArgs{ID: "41351"})
	if err != nil {
		t.Fatalf("getPortOrder: %v", err)
	}
	if got.Status != "pending" {
		t.Errorf("want the order's own status, got %q", got.Status)
	}
}

func TestListEdgeStrategiesCarriesTheFirewallRules(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/routes/edge_strategies"] = `{"data":[
      {"attributes":{"description":"Sent preferentially from North Virginia.",
        "firewall_rules":"34.226.0.0/16","name":"US-East-VA","naptr":"us-east-va.example"},
       "id":"1","type":"edge_strategy"}],"links":{"self":"x"}}`
	p, _ := newPlugin(t, f)

	got, err := p.listEdgeStrategies(context.Background(), edgeArgs{})
	if err != nil {
		t.Fatalf("listEdgeStrategies: %v", err)
	}
	// The firewall rule is the only reason anybody reads this tool.
	if got.EdgeStrategies[0].FirewallRules != "34.226.0.0/16" {
		t.Errorf("firewall rules are %q", got.EdgeStrategies[0].FirewallRules)
	}
}

// An unconfigured instance still mounts, so every tool has to refuse in a way
// that tells somebody what to do rather than failing at the network.
func TestToolsRefuseBeforeTheCredentialIsSet(t *testing.T) {
	t.Parallel()
	p, err := New(testDeps(), Config{BaseURL: "https://api.flowroute.com"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.listNumbers(context.Background(), numbersArgs{}); err == nil ||
		!strings.Contains(err.Error(), "not configured yet") {
		t.Fatalf("want a not-configured refusal, got %v", err)
	}
	if got := p.Check(context.Background()); got.State == "healthy" {
		t.Errorf("an unconfigured instance is not healthy: %+v", got)
	}
}

func mustString(t *testing.T, v any) string {
	t.Helper()
	m := mustJSON(t, v)
	var b strings.Builder
	writeAll(&b, m)
	return b.String()
}

func writeAll(b *strings.Builder, v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			b.WriteString(k)
			writeAll(b, val)
		}
	case []any:
		for _, val := range x {
			writeAll(b, val)
		}
	case string:
		b.WriteString(x)
	}
}

// "There is no such number" is a successful round trip: it proves the address,
// the TLS and the credential. Recording it as the integration's last error
// would show the whole plugin as degraded because somebody asked about a
// number that had been released.
func TestAnAbsentResourceDoesNotDegradeHealth(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["/v2/numbers"] = numbersPage
	f.absent["/v2/numbers/12065559999"] = true
	p, _ := newPlugin(t, f)

	if _, err := p.listNumbers(context.Background(), numbersArgs{}); err != nil {
		t.Fatalf("listNumbers: %v", err)
	}
	if got := p.Check(context.Background()); got.State != "healthy" {
		t.Fatalf("healthy after a good read, got %+v", got)
	}

	if _, err := p.getNumber(context.Background(), numberArgs{Number: "12065559999"}); err == nil {
		t.Fatal("an absent number should still be an error to the caller")
	}
	if got := p.Check(context.Background()); got.State != "healthy" {
		t.Fatalf("an absent resource must not degrade the plugin, got %+v", got)
	}

	// A credential that stopped working is a different matter entirely.
	f.status["/v2/numbers"] = 401
	f.bodies["/v2/numbers"] = `{"errors":[{"status":401,"title":"Unauthorized"}]}`
	if _, err := p.listNumbers(context.Background(), numbersArgs{}); err == nil {
		t.Fatal("a 401 should be an error")
	}
	if got := p.Check(context.Background()); got.State == "healthy" {
		t.Fatalf("a refused credential should degrade the plugin, got %+v", got)
	}
}

// Registration is where the house rules are enforced -- the verb_resource tool
// name, the derived schemas -- so this mounts the plugin the way the host does
// rather than calling the handlers directly. A tool named badly or carrying a
// type that cannot be reflected fails here rather than at somebody's first
// question.
func TestEveryToolRegisters(t *testing.T) {
	t.Parallel()
	p, err := New(testDeps(), Config{
		Customers: []Customer{{
			Name: "Acme", AccessKey: testAccessKey, SecretKey: testSecretKey,
		}},
		BaseURL: "https://api.flowroute.com",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	m := plugins.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), "test", nil, nil, nil, nil)
	if err := m.Register(context.Background(), p, "flowroute", false); err != nil {
		t.Fatalf("registering the plugin: %v", err)
	}

	mounted := m.Lookup("flowroute")
	if mounted == nil {
		t.Fatal("the plugin did not mount")
	}
	names := mounted.Registry.ToolNames()
	if len(names) != 11 {
		t.Fatalf("want eleven read tools, registered %d: %v", len(names), names)
	}
	// The host prefixes the instance name, so what a model reads is
	// flowroute_list_numbers. The bare name is what the verb rule applies to.
	for _, name := range names {
		bare := strings.TrimPrefix(name, "flowroute_")
		if bare == name {
			t.Errorf("%q is not prefixed with the instance name", name)
		}
		if !strings.HasPrefix(bare, "list_") && !strings.HasPrefix(bare, "get_") {
			t.Errorf("%q is not a read tool name; this integration registers only reads",
				name)
		}
	}
}
