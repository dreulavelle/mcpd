package textable

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Run against a real Textable. Skipped unless one is supplied, so it costs
// nothing in CI and is there when somebody has an instance:
//
//	TEXTABLE_TEST_URL=https://your-instance.textable.app \
//	TEXTABLE_TEST_TOKEN=… \
//	go test ./internal/plugins/textable/ -run Integration -v
//
// This is the half of the package a fake cannot reach, and on this API it is
// worth more than the rest of the suite put together. Every tool here was first
// written from the published OpenAPI document, and running these found three
// things wrong with it that no unit test could have:
//
//   - the billing path is /api/v2/billing/tenantReport; the document spells it
//     "biling", and that path answers 404;
//   - GET /api/v2/users/{id} is documented as accepting a service account and
//     answers 401 to one, with a real id and every read scope granted;
//   - GET /api/v2/tenants is not documented as a GET at all, and is the only
//     source of the internal tenant id every other endpoint requires.
//
// A stub written from the document would have agreed with the document.
func integrationPlugin(t *testing.T) *Plugin {
	t.Helper()
	base := os.Getenv("TEXTABLE_TEST_URL")
	token := os.Getenv("TEXTABLE_TEST_TOKEN")
	if base == "" || token == "" {
		t.Skip("set TEXTABLE_TEST_URL and TEXTABLE_TEST_TOKEN to run against a real Textable")
	}

	p, err := New(plugins.Deps{
		Instance: "textable",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}, Config{BaseURL: base, APIKey: token})
	if err != nil {
		t.Fatalf("building the plugin: %v", err)
	}
	return p
}

// The startup probe, as the host runs it. A failure here is the whole
// integration failing, so it runs first and separately.
func TestIntegration_Starts(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h := p.Check(ctx); h.State != plugins.HealthyState {
		t.Errorf("Check after a successful start: %+v", h)
	}
}

// The directory, walked the way a model walks it: tenants, then one tenant's
// organizations and users, then one organization in full.
//
// Every id used below comes from the call before it, which is the property that
// actually matters. A tool returning an id nothing else accepts is the failure
// mode this API invites -- it has two tenant identifiers and only one of them
// works anywhere.
func TestIntegration_TheDirectoryChains(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	tenants, err := p.listTenants(ctx, tenantsArgs{})
	if err != nil {
		t.Fatalf("list_tenants: %v", err)
	}
	if len(tenants.Tenants) == 0 {
		t.Fatal("no tenants; this token cannot see anything and nothing below can run")
	}
	tenant := tenants.Tenants[0]
	if tenant.ID == "" {
		t.Fatal("a tenant with no id; every other tool takes one")
	}
	t.Logf("tenants: %d, first: %s (provider %s, %d admins)",
		tenants.Returned, tenant.Name, tenant.Provider, tenant.Admins)

	detail, err := p.getTenant(ctx, tenantArgs{TenantID: tenant.ID})
	if err != nil {
		t.Fatalf("get_tenant(%s): %v", tenant.ID, err)
	}
	if detail.Name == "" {
		t.Error("the tenant report named no tenant")
	}
	t.Logf("tenant %s: %d users, %d organizations, licences %v",
		detail.Name, detail.Users, len(detail.Organizations), detail.Licenses)

	orgs, err := p.listOrganizations(ctx, orgsArgs{TenantID: tenant.ID})
	if err != nil {
		t.Fatalf("list_organizations(%s): %v", tenant.ID, err)
	}
	if len(orgs.Organizations) == 0 {
		t.Skip("the tenant has no organizations; nothing further to chain")
	}
	org := orgs.Organizations[0]
	t.Logf("organizations: %d, first: %s", orgs.Returned, org.Name)

	full, err := p.getOrganization(ctx, orgArgs{OrganizationID: org.ID})
	if err != nil {
		t.Fatalf("get_organization(%s): %v", org.ID, err)
	}
	// The one direction the hierarchy cannot otherwise be walked. If this comes
	// back empty the organization cannot be tied to the tenant it came from.
	if full.TenantID != tenant.ID {
		t.Errorf("organization %s reports tenant %q, but it was listed under %q",
			org.ID, full.TenantID, tenant.ID)
	}
	t.Logf("organization %s: %d user records, plan %q, disabled %v",
		full.Name, full.UserRecords, full.Plan, full.Disabled)
}

// The user listing, and the join it is built from.
//
// The report describes the same people in two arrays -- one carrying ids and no
// names, the other names and no ids -- joined on email. A join that silently
// fails produces a listing of people with no names, which reads as a working
// tool returning sparse data.
func TestIntegration_ListUsersJoinsBothBreakdowns(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	users, err := p.listUsers(ctx, usersArgs{})
	if err != nil {
		t.Fatalf("list_users: %v", err)
	}
	if len(users.Users) == 0 {
		t.Skip("no users on this instance")
	}
	t.Logf("users: %d returned of %d total", users.Returned, users.Total)

	var named, withID int
	for _, u := range users.Users {
		if u.Name != "" {
			named++
		}
		if u.ID != "" {
			withID++
		}
	}
	// Both halves of the join have to be landing. Either count at zero means one
	// array was decoded and the other was not.
	if named == 0 {
		t.Error("no user has a name; the UserBreakdown half of the join is not landing")
	}
	if withID == 0 {
		t.Error("no user has an id; the OrganizationBreakdown half is not landing")
	}
	t.Logf("of %d listed: %d have names, %d have ids", len(users.Users), named, withID)

	// And narrowing to one tenant must not return more than the whole instance.
	tenants, err := p.listTenants(ctx, tenantsArgs{})
	if err != nil || len(tenants.Tenants) == 0 {
		return
	}
	scoped, err := p.listUsers(ctx, usersArgs{TenantID: tenants.Tenants[0].ID})
	if err != nil {
		t.Fatalf("list_users(tenant): %v", err)
	}
	if scoped.Total > users.Total {
		t.Errorf("one tenant reported %d users, more than the whole instance's %d",
			scoped.Total, users.Total)
	}
}

// The endpoints this integration deliberately does not call, checked to still be
// refusing it. Each was believed callable from the specification and is not, and
// a future maintainer reading the spec will be tempted by all three.
//
// This is the test that catches Textable fixing one of them, too -- at which
// point it fails, and the fix is to add the endpoint back deliberately.
func TestIntegration_TheEndpointsTheSpecIsWrongAbout(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	users, err := p.listUsers(ctx, usersArgs{})
	if err != nil {
		t.Fatalf("list_users: %v", err)
	}
	var userID string
	for _, u := range users.Users {
		if u.ID != "" {
			userID = u.ID
			break
		}
	}
	if userID == "" {
		t.Skip("no user id available to probe with")
	}

	// The transport refuses these before the network, which is the guarantee
	// under test for the first two. The third is refused by Textable.
	for _, path := range []string{
		"/api/v2/users/" + userID,     // documented as accepting a service account; 401s
		"/api/v2/biling/tenantReport", // the specification's spelling; 404s
		"/api/users",                  // v1, user tokens only
	} {
		if _, err := p.client.Get(ctx, path, nil); err == nil {
			t.Errorf("%s answered; it was believed unreachable, and if that has "+
				"changed the allow-list should be widened deliberately", path)
		} else {
			t.Logf("%s is still refused: %v", path, err)
		}
	}
}
