package textable

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A realistic tenant report. Two tenants, and deliberately awkward in three
// ways that the join has to survive:
//
//   - carol appears in UserBreakdown but in no organization, so she has no id
//   - bob's email is capitalised differently in the two breakdowns
//   - the second tenant reuses an organization name
const report = `[
 {"TenantName":"Acme","TenantExternalId":20681,"TenantBillingGroup":"grp-1",
  "UserQuantity":3,"LicenseAllocation":{"cloudmessage-pro":10},
  "OrganizationBreakdown":[
    {"organizationName":"Acme Sales","externalId":"acme-sales",
     "LicenseType":"cloudmessage-pro","UserQuantity":2,
     "users":[{"id":"u-alice","email":"alice@acme.example"},
              {"id":"u-bob","email":"BOB@acme.example"}]}],
  "UserBreakdown":[
    {"fullName":"Alice Adams","email":"alice@acme.example","phoneNumber":"+15550001",
     "accountType":"User","licenseType":"cloudmessage-pro","organizationName":"Acme Sales"},
    {"fullName":"Bob Brown","email":"bob@acme.example","phoneNumber":"+15550002",
     "accountType":"User","licenseType":"cloudmessage-pro","organizationName":"Acme Sales"},
    {"fullName":"Carol Clark","email":"carol@acme.example","phoneNumber":"+15550003",
     "accountType":"Admin","licenseType":"cloudmessage-dashmanger",
     "isSoftDeleted":true,"organizationName":"Acme Admins"}]},
 {"TenantName":"Globex","TenantExternalId":"","UserQuantity":1,
  "OrganizationBreakdown":[
    {"organizationName":"Acme Sales","externalId":"globex-sales","UserQuantity":1,
     "users":[{"id":"u-dave","email":"dave@globex.example"}]}],
  "UserBreakdown":[
    {"fullName":"Dave Davis","email":"dave@globex.example","phoneNumber":"+15550004"}]}
]`

// tenants is what GET /api/v2/tenants returns: the only source of Textable's
// internal tenant id. billingGroup is null on a real instance, and externalId
// is the empty string on a tenant that was never given one.
const tenants = `{"tenants":[
 {"id":"t-acme","tenantName":"Acme","externalId":20681,"tenantAdmins":["u-alice"],
  "adminBillingPlan":"mcpl-pro","billingGroup":null,
  "supportDetails":{"primaryContactEmail":"support@acme.example"},
  "provider":{"name":"bandwidth"}},
 {"id":"t-globex","tenantName":"Globex","externalId":"","tenantAdmins":[],
  "billingGroup":null,"supportDetails":{},"provider":{"name":"bandwidth"}}]}`

func directoryPlugin(t *testing.T) *Plugin {
	t.Helper()
	return toolPlugin(t, routes(t, map[string]string{
		tenantsPath:                  tenants,
		tenantReportPath:             report,
		tenantReportPath + "/t-acme": `[]`,
	}))
}

// The user list is assembled from two arrays that describe the same people
// differently: OrganizationBreakdown carries the ids and nothing else useful,
// UserBreakdown carries the names and phone numbers and no id at all. Email is
// the only field both have, so it is the join key -- and it is not written
// consistently, which is why the key is case-folded.
func TestListUsers_JoinsTheTwoBreakdownsOnEmail(t *testing.T) {
	res, err := directoryPlugin(t).listUsers(context.Background(), usersArgs{})
	if err != nil {
		t.Fatal(err)
	}
	byEmail := map[string]userRow{}
	for _, u := range res.Users {
		byEmail[emailKey(u.Email)] = u
	}

	alice := byEmail["alice@acme.example"]
	if alice.ID != "u-alice" || alice.Name != "Alice Adams" || alice.PhoneNumber != "+15550001" {
		t.Errorf("alice should carry the id from one breakdown and the name and "+
			"number from the other, got %+v", alice)
	}
	if alice.Tenant != "Acme" || alice.Organization != "Acme Sales" {
		t.Errorf("a user should say which tenant and organization they are in, got %+v", alice)
	}

	// The join must survive the same address written two ways.
	bob := byEmail["bob@acme.example"]
	if bob.ID != "u-bob" || bob.Name != "Bob Brown" {
		t.Errorf("bob's two records differ only in the case of his email and "+
			"must still join, got %+v", bob)
	}
}

// A user the billing breakdown describes but no organization lists has no id,
// and is still reported. "Who is on this tenant" is answered by the name;
// silently omitting somebody is worse than saying they cannot be looked up.
func TestListUsers_KeepsAUserWithNoId(t *testing.T) {
	res, err := directoryPlugin(t).listUsers(context.Background(), usersArgs{})
	if err != nil {
		t.Fatal(err)
	}
	var carol *userRow
	for i := range res.Users {
		if res.Users[i].Name == "Carol Clark" {
			carol = &res.Users[i]
		}
	}
	if carol == nil {
		t.Fatal("a user present only in the billing breakdown must still be listed")
	}
	if carol.ID != "" {
		t.Errorf("carol is in no organization, so there is no id to report, got %q", carol.ID)
	}
	if carol.Organization != "Acme Admins" {
		t.Errorf("her organization comes from the billing breakdown, got %q", carol.Organization)
	}
	// The soft-delete flag is the reason to keep her: a departed user still
	// holding a licence is exactly what a billing question is about.
	if !carol.SoftDeleted {
		t.Error("soft_deleted should be carried through from the billing breakdown")
	}
}

// Nobody is counted twice. A user present in both breakdowns is one person, and
// a directory that double-counts is one nobody can reconcile against a bill.
func TestListUsers_DoesNotCountAnybodyTwice(t *testing.T) {
	res, err := directoryPlugin(t).listUsers(context.Background(), usersArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 4 {
		t.Errorf("total_users = %d, want 4 distinct people across both tenants", res.Total)
	}
	seen := map[string]bool{}
	for _, u := range res.Users {
		key := emailKey(u.Email)
		if seen[key] {
			t.Errorf("%s appears more than once", u.Email)
		}
		seen[key] = true
	}
}

// Narrowing by tenant is the difference between a usable answer and every user
// on the instance. It accepts the name or the external id, because those are
// the two things list_tenants reports.
// Narrowing to one tenant fetches that tenant's report rather than filtering the
// whole instance out of the full one. Asking about one customer should not walk
// every other customer.
func TestListUsers_FetchesOneTenantRatherThanFilteringThemAll(t *testing.T) {
	var paths []string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		// Only the per-tenant path is stubbed with content; if the tool reaches
		// for the full report instead, it gets an empty array and the counts
		// below fail.
		if r.URL.Path == tenantReportPath+"/t-globex" {
			_, _ = w.Write([]byte(`{"TenantName":"Globex","TenantExternalId":"",
				"UserQuantity":1,
				"OrganizationBreakdown":[{"organizationName":"Globex Sales",
				  "users":[{"id":"u-dave","email":"dave@globex.example"}]}],
				"UserBreakdown":[{"fullName":"Dave Davis",
				  "email":"dave@globex.example","phoneNumber":"+15550004"}]}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	})

	res, err := p.listUsers(context.Background(), usersArgs{TenantID: "t-globex"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Users) != 1 || res.Users[0].Name != "Dave Davis" {
		t.Errorf("expected only Globex's user, got %+v", res.Users)
	}
	for _, path := range paths {
		if path == tenantReportPath {
			t.Error("the full report was fetched; a tenant-scoped listing should " +
				"read only that tenant")
		}
	}
}

// A tenant that does not exist is not an empty tenant, and the difference
// matters: one is "nobody is in this customer" and the other is "you named
// something that is not a customer". Reported as an empty list with a note that
// says which.
// TenantExternalId is documented as a number, is a number on some tenants, and
// is the empty string on others. Either alone fails to decode the other, and the
// failure takes the whole report with it rather than the one field.
func TestExternalId_DecodesWhetherItIsANumberOrAString(t *testing.T) {
	res, err := directoryPlugin(t).listTenants(context.Background(), tenantsArgs{})
	if err != nil {
		t.Fatalf("a report mixing both spellings must still decode: %v", err)
	}
	byName := map[string]tenantRow{}
	for _, tn := range res.Tenants {
		byName[tn.Name] = tn
	}
	if got := byName["Acme"].ExternalID; got != "20681" {
		t.Errorf("a numeric external id should survive as %q, got %q", "20681", got)
	}
	if got := byName["Globex"].ExternalID; got != "" {
		t.Errorf("an empty external id should stay empty, got %q", got)
	}
	// And never as a float, which is what a float64 does to an identifier.
	for _, tn := range res.Tenants {
		if strings.ContainsAny(tn.ExternalID, "e+.") {
			t.Errorf("external id %q was rendered as a float", tn.ExternalID)
		}
	}
}

// The tenant listing is the top of the hierarchy and must not carry the whole
// directory with it. Every user on the instance arriving inside a list of
// tenants is the largest avoidable thing this integration could return.
// The tenant listing must carry the internal id. It is the only place that id
// exists, and every other tool taking a tenant needs it -- a listing without it
// leaves a caller with names they cannot pass to anything.
func TestListTenants_CarriesTheInternalIdEverythingElseNeeds(t *testing.T) {
	res, err := directoryPlugin(t).listTenants(context.Background(), tenantsArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tenants) != 2 {
		t.Fatalf("expected both tenants, got %d", len(res.Tenants))
	}
	var acme tenantRow
	for _, tn := range res.Tenants {
		if tn.Name == "Acme" {
			acme = tn
		}
	}
	if acme.ID != "t-acme" {
		t.Errorf("id = %q, want the internal tenant id", acme.ID)
	}
	if acme.SupportEmail != "support@acme.example" || acme.Provider != "bandwidth" {
		t.Errorf("the support contact and carrier are worth carrying, got %+v", acme)
	}
	if acme.Admins != 1 {
		t.Errorf("tenant_admins = %d, want a count of the one admin", acme.Admins)
	}

	// A null billingGroup must not become the string "null".
	if acme.BillingGroup != "" {
		t.Errorf("a null billing group should be absent, got %q", acme.BillingGroup)
	}
}

// One tenant is selected out of the report rather than fetched by id, because
// the report carries an external id and the per-tenant endpoint wants
// Textable's internal one. It accepts either the name or that external id.
// get_tenant reads the per-tenant report directly, by the id list_tenants
// reports. It used to select out of the whole report because the external id
// could not be turned into an internal one; the tenant listing supplies the
// internal id, so the direct read is available and the whole-instance walk is
// not needed to answer a question about one customer.
func TestGetTenant_ReadsOneTenantByItsId(t *testing.T) {
	var paths []string
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"TenantName":"Acme","TenantExternalId":20681,
			"UserQuantity":3,"LicenseAllocation":{"mcpl-pro":18},
			"OrganizationBreakdown":[{"organizationName":"Acme Sales",
			  "LicenseType":"mcpl-pro","UserQuantity":2,
			  "users":[{"id":"u-alice","email":"alice@acme.example"}]}],
			"UserBreakdown":[]}`))
	})

	res, err := p.getTenant(context.Background(), tenantArgs{TenantID: "t-acme"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "t-acme" || res.Name != "Acme" || res.Users != 3 {
		t.Errorf("unexpected tenant: %+v", res)
	}
	if len(res.Organizations) != 1 || res.Organizations[0].Users != 2 {
		t.Errorf("the organizations and their billable counts should survive, got %+v",
			res.Organizations)
	}
	// Billable count and listed count are two facts, not one.
	if res.Organizations[0].Listed != 1 {
		t.Errorf("users_listed = %d; it is a count of records, not the billing "+
			"figure", res.Organizations[0].Listed)
	}
	if len(paths) != 1 || paths[0] != tenantReportPath+"/t-acme" {
		t.Errorf("expected one read of the per-tenant report, got %v", paths)
	}
}

// A missing id says which tool supplies one, because a model handed "needs a
// tenant_id" and nothing else will guess.
func TestGetTenant_SaysWhereTheIdComesFrom(t *testing.T) {
	_, err := directoryPlugin(t).getTenant(context.Background(), tenantArgs{})
	if err == nil {
		t.Fatal("a missing tenant_id should be refused")
	}
	if !strings.Contains(err.Error(), "list_tenants") {
		t.Errorf("the error should name the tool that reports ids, got: %v", err)
	}
}

// An organization says which tenant it belongs to, which is the one direction
// the hierarchy cannot otherwise be walked.
func TestGetOrganization_ReportsItsTenant(t *testing.T) {
	p := toolPlugin(t, routes(t, map[string]string{
		"/api/v2/organizations/o1": `{"id":"o1","organizationName":"Acme Sales",
			"tenantId":"tenant-internal-1","userCount":12,"is_disabled":true,
			"organizationAdmins":["u-alice"],"billing":{"plan":"starter"}}`,
	}))
	res, err := p.getOrganization(context.Background(), orgArgs{OrganizationID: "o1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.TenantID != "tenant-internal-1" {
		t.Errorf("tenant_id = %q; an organization should say which tenant it is "+
			"in", res.TenantID)
	}
	if !res.Disabled {
		t.Error("a disabled organization stops every user in it from sending")
	}
	// Named user_records rather than users, because it is not the billing
	// report's number and a reader comparing them will otherwise report a
	// contradiction. A live disabled organization has 41 records and 0 billable.
	if res.UserRecords != 12 {
		t.Errorf("user_records = %d, want the organization document's own count",
			res.UserRecords)
	}
	if len(res.Admins) != 1 || res.Admins[0] != "u-alice" {
		t.Errorf("organization admins are ids usable with get_user, got %v", res.Admins)
	}
}

// list_organizations needs Textable's internal tenant id, and the external id
// from the tenant report is a different identifier. Somebody will pass the
// wrong one, so the refusal says so rather than letting the API answer nothing.
func TestListOrganizations_SaysWhereTheTenantIdComesFrom(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{"organizations":[]}`))
	_, err := p.listOrganizations(context.Background(), orgsArgs{})
	if err == nil {
		t.Fatal("a missing tenant_id should be refused")
	}
	if !strings.Contains(err.Error(), "list_tenants") {
		t.Errorf("the error should name the tool that reports ids, got: %v", err)
	}
}

// get_contact is usable only with an id from outside Textable, because no
// listing exists for this credential. The refusal says that rather than naming
// a tool that would provide one, because there is none.
func TestGetContact_SaysThereIsNoListingToGetAnIdFrom(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{}`))
	_, err := p.getContact(context.Background(), contactArgs{})
	if err == nil {
		t.Fatal("an empty contact_id should be refused")
	}
	if !strings.Contains(err.Error(), "no contact listing") {
		t.Errorf("the error should say why no tool can supply the id, got: %v", err)
	}
}

// Opting out is a legal position rather than a preference, so it is reported
// explicitly rather than left to be inferred from a missing field.
func TestGetContact_ReportsOptOutExplicitly(t *testing.T) {
	p := toolPlugin(t, routes(t, map[string]string{
		"/api/v2/contacts/c1": `{"full_name":"Jane Roe","phone_number":"+15551234567",
			"optedOut":true,"isArchived":false,"notes":"do not call"}`,
	}))
	res, err := p.getContact(context.Background(), contactArgs{ContactID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OptedOut {
		t.Error("an opted-out contact must be reported as such")
	}
	// The document carries no id of its own, so the one asked for is what comes
	// back -- a result with an empty id is one a caller cannot chain from.
	if res.ID != "c1" {
		t.Errorf("id = %q, want the id the caller asked for", res.ID)
	}
}

// A listing that stops short has to say so, or a model answers as though it saw
// everything.
func TestListings_SayWhenTheyStopShort(t *testing.T) {
	var users []string
	for i := 0; i < 50; i++ {
		users = append(users, fmt.Sprintf(
			`{"id":"u%02d","email":"user%02d@x.example"}`, i, i))
	}
	body := `[{"TenantName":"Big","TenantExternalId":1,"UserQuantity":50,
		"OrganizationBreakdown":[{"organizationName":"All","users":[` +
		strings.Join(users, ",") + `]}],"UserBreakdown":[]}]`

	p := toolPlugin(t, routes(t, map[string]string{tenantReportPath: body}))
	res, err := p.listUsers(context.Background(), usersArgs{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Returned != 10 {
		t.Errorf("returned = %d, want the requested 10", res.Returned)
	}
	if !res.Truncated || res.Reason == "" {
		t.Error("a listing cut by the limit should say so and say which ceiling did it")
	}
	if res.Matching != 50 {
		t.Errorf("total_matching = %d; the count before the ceiling should survive", res.Matching)
	}
}

// Every listing is fetched from an endpoint that promises no order, so a stable
// one is imposed here. Without it a truncated answer cuts a different set on
// every call and two readings a minute apart cannot be compared.
func TestListings_AreOrderedStably(t *testing.T) {
	p := directoryPlugin(t)
	var first string
	for run := 0; run < 3; run++ {
		res, err := p.listUsers(context.Background(), usersArgs{})
		if err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, u := range res.Users {
			order = append(order, u.Email)
		}
		joined := strings.Join(order, ",")
		if run == 0 {
			first = joined
			continue
		}
		if joined != first {
			t.Errorf("run %d ordered %q, first run ordered %q", run, joined, first)
		}
	}
}

// An unconfigured instance refuses in words a model can act on. One handed a
// connection error tries three more tools first.
func TestTools_RefuseAnUnconfiguredInstanceInWords(t *testing.T) {
	p := toolPlugin(t, jsonOK(`{}`))
	p.configured = false
	ctx := context.Background()
	calls := map[string]func() error{
		"list_tenants":       func() error { _, err := p.listTenants(ctx, tenantsArgs{}); return err },
		"get_tenant":         func() error { _, err := p.getTenant(ctx, tenantArgs{TenantID: "t"}); return err },
		"list_users":         func() error { _, err := p.listUsers(ctx, usersArgs{}); return err },
		"list_organizations": func() error { _, err := p.listOrganizations(ctx, orgsArgs{TenantID: "t"}); return err },
		"get_organization":   func() error { _, err := p.getOrganization(ctx, orgArgs{OrganizationID: "o"}); return err },
		"get_contact":        func() error { _, err := p.getContact(ctx, contactArgs{ContactID: "c"}); return err },
	}
	for name, call := range calls {
		err := call()
		if err == nil {
			t.Errorf("%s should refuse an unconfigured instance", name)
			continue
		}
		if !strings.Contains(err.Error(), "not configured yet") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A response that is not the shape the API documents should say so, naming the
// shape it expected. "unexpected end of JSON input" sends somebody looking in
// the wrong place.
func TestTools_NameTheShapeTheyExpectedWhenDecodingFails(t *testing.T) {
	p := toolPlugin(t, routes(t, map[string]string{tenantReportPath: `{"tenants":[]}`}))
	_, err := p.listUsers(context.Background(), usersArgs{})
	if err == nil {
		t.Fatal("an object should not decode as the documented array of tenants")
	}
	if !strings.Contains(err.Error(), "array of tenants") {
		t.Errorf("the error should name the shape it expected, got: %v", err)
	}
}

// The plugin must reach only the endpoints its tools document. This is the test
// that fails if a tool is quietly repointed at a different API version -- which
// matters here because the v1 equivalents exist, are richer, and do not accept
// this credential.
func TestTools_CallOnlyTheEndpointsTheyDocument(t *testing.T) {
	seen := map[string]bool{}
	p := toolPlugin(t, func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.Path] = true
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == tenantsPath:
			_, _ = w.Write([]byte(tenants))
		case r.URL.Path == tenantReportPath:
			_, _ = w.Write([]byte(report))
		case strings.HasPrefix(r.URL.Path, tenantReportPath+"/"):
			_, _ = w.Write([]byte(`{"TenantName":"Acme","UserBreakdown":[]}`))
		case r.URL.Path == "/api/v2/organizations":
			_, _ = w.Write([]byte(`{"organizations":[{"id":"o1","organizationName":"Ops"}]}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	})

	ctx := context.Background()
	for _, call := range []func() error{
		func() error { _, err := p.listTenants(ctx, tenantsArgs{}); return err },
		func() error { _, err := p.listUsers(ctx, usersArgs{}); return err },
		func() error { _, err := p.getTenant(ctx, tenantArgs{TenantID: "t-acme"}); return err },
		func() error { _, err := p.listOrganizations(ctx, orgsArgs{TenantID: "t-acme"}); return err },
		func() error { _, err := p.getOrganization(ctx, orgArgs{OrganizationID: "o1"}); return err },
		func() error { _, err := p.getContact(ctx, contactArgs{ContactID: "c1"}); return err },
	} {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}

	for _, want := range []string{
		tenantsPath, tenantReportPath, tenantReportPath + "/t-acme",
		"/api/v2/organizations", "/api/v2/organizations/o1", "/api/v2/contacts/c1",
	} {
		if !seen[want] {
			t.Errorf("%s was never called", want)
		}
	}
	// v1 is richer and does not accept a service account. Reaching for it would
	// pass every unit test that stubs it and fail against every real instance.
	// v1 does not accept a service account, /api/v2/users/{id} answers 401 to
	// one whatever the spec says, and the spec's "biling" spelling 404s.
	// Reaching for any of them would pass every test that stubs it and fail
	// against every real instance.
	for _, forbidden := range []string{
		"/api/users", "/api/contacts", "/api/organizations",
		"/api/v2/users/u1", "/api/v2/biling/tenantReport",
	} {
		if seen[forbidden] {
			t.Errorf("%s was called, and it cannot work against a real instance", forbidden)
		}
	}
}

// An empty organization listing is two different answers and the endpoint sends
// the same bytes for both: a tenant with none, and a tenant id that does not
// exist. Textable answers an unknown tenant with {"organizations":[]} rather
// than a 404 -- so a caller shown an empty list cannot tell whether they
// mistyped, and would report "that customer has no organizations".
func TestListOrganizations_TellsAnEmptyTenantFromAnUnknownOne(t *testing.T) {
	handler := func(t *testing.T) http.HandlerFunc {
		return routes(t, map[string]string{
			tenantsPath:             tenants,
			"/api/v2/organizations": `{"organizations":[]}`,
		})
	}

	// A tenant that exists and genuinely has none.
	res, err := toolPlugin(t, handler(t)).listOrganizations(context.Background(),
		orgsArgs{TenantID: "t-acme"})
	if err != nil {
		t.Fatalf("a real tenant with no organizations is not an error: %v", err)
	}
	if !strings.Contains(res.Note, "exists and has no organizations") {
		t.Errorf("the note should say the empty list is real, got: %q", res.Note)
	}

	// A tenant id that is not on the instance.
	_, err = toolPlugin(t, handler(t)).listOrganizations(context.Background(),
		orgsArgs{TenantID: "t-nope"})
	if err == nil {
		t.Fatal("an unknown tenant id should be an error, not an empty list")
	}
	if !strings.Contains(err.Error(), "list_tenants") {
		t.Errorf("the error should name where real ids come from, got: %v", err)
	}
}

// A false soft_deleted has to be visible. A field that vanishes when false
// cannot be told from one the tool never reports -- and the listing's
// description says it carries it.
func TestListUsers_AlwaysReportsSoftDeleteState(t *testing.T) {
	res, err := directoryPlugin(t).listUsers(context.Background(), usersArgs{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(res.Users[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "soft_deleted") {
		t.Errorf("soft_deleted disappeared when false:\n%s", encoded)
	}
}
