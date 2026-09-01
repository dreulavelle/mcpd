package textable

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The two paths the directory is built from.
//
// tenantsPath is undocumented as a GET -- the specification describes only
// POST /api/v2/tenants -- and it is the endpoint that makes this integration
// work, because it is the only one that returns Textable's *internal* tenant
// id. Everything else that takes a tenant wants that id, and the billing report
// carries only an external one. Verified by hand against a live instance.
//
// tenantReportPath is spelled the way the API serves it. The published
// specification writes it "biling", and that misspelling is in the document
// rather than in the deployment: /api/v2/biling/tenantReport answers 404 and
// /api/v2/billing/tenantReport answers 200. Copying the spec verbatim, which is
// usually the safe move with a typo like this, produced an integration where
// every directory call failed.
const (
	tenantsPath      = "/api/v2/tenants"
	tenantReportPath = "/api/v2/billing/tenantReport"
)

// registerDirectoryTools adds the four tools that answer "what is on this
// instance": the tenants, one tenant's licensing and organizations, the users
// inside them, and a tenant's organizations with the ids a detail read needs.
func (p *Plugin) registerDirectoryTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_tenants",
		Title: "List tenants",
		Description: "Every tenant on this Textable instance: its id, name, " +
			"messaging provider, and where present its support contact and " +
			"administrator billing plan -- both are optional in Textable and are " +
			"omitted from the result when unset, rather than returned empty.\n\n" +
			"Start here. This is the top of the hierarchy -- tenant, then " +
			"organization, then user, then contact -- and the tenant id every " +
			"other tool takes comes from here and nowhere else.",
		Idempotent: true,
	}, p.listTenants)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_tenant",
		Title: "Get one tenant's licensing and organizations",
		Description: "One tenant in detail: how many licences of each type it " +
			"holds, and every organization inside it with that organization's " +
			"billable user count and licence type.\n\n" +
			"This is the tool that answers \"what is this customer paying for\" " +
			"and \"which organizations does this tenant have\". Takes the tenant " +
			"id from list_tenants. It reports organizations by name; " +
			"list_organizations reaches the same ones by the id " +
			"get_organization needs.",
		Idempotent: true,
	}, p.getTenant)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_users",
		Title: "List users",
		Description: "Users on this instance, with their name, email, phone " +
			"number, licence type, account type, soft-delete state, and which " +
			"tenant and organization they are in. A user with no email address " +
			"is normal here and is still one record.\n\n" +
			"This is the only user listing available: there is no per-user read " +
			"for this credential, so what is here is what can be known about a " +
			"user. Narrow with `query` against name, email or phone number, or " +
			"with `tenant_id` to stay inside one customer.",
		Idempotent: true,
	}, p.listUsers)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_organizations",
		Title: "List a tenant's organizations",
		Description: "The organizations inside one tenant, with the id " +
			"get_organization takes. Takes the tenant id from list_tenants. " +
			"Returns ids and names only; get_organization has the rest.",
		Idempotent: true,
	}, p.listOrganizations)
}

// flexID is an identifier Textable sends inconsistently.
//
// TenantExternalId is documented as a number, is a number on some tenants, and
// is the empty *string* on others -- a tenant that was never given one. Decoding
// it as either a number or a string fails on the other, and the failure takes
// the whole report with it rather than the one field.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch s {
	case "null", "":
		*f = ""
		return nil
	}
	// A quoted string arrives with its quotes; a number does not. Unquoting by
	// hand rather than trying json.Unmarshal twice, because the only two shapes
	// are these and both are trivially recognisable.
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var unquoted string
		if err := json.Unmarshal(b, &unquoted); err != nil {
			return err
		}
		*f = flexID(unquoted)
		return nil
	}
	*f = flexID(s)
	return nil
}

func (f flexID) String() string { return string(f) }

// --- tenants ---------------------------------------------------------------

// tenantEntry is one tenant as GET /api/v2/tenants returns it.
type tenantEntry struct {
	ID               string   `json:"id"`
	TenantName       string   `json:"tenantName"`
	ExternalID       flexID   `json:"externalId"`
	TenantAdmins     []string `json:"tenantAdmins"`
	AdminBillingPlan string   `json:"adminBillingPlan"`
	BillingGroup     *string  `json:"billingGroup"`
	SupportDetails   struct {
		PrimaryContactEmail string `json:"primaryContactEmail"`
	} `json:"supportDetails"`
	Provider struct {
		Name string `json:"name"`
	} `json:"provider"`
}

type tenantsArgs struct {
	Query string `json:"query,omitempty" jsonschema:"narrows to tenants whose name or external id contains this"`
	Limit int    `json:"limit,omitempty" jsonschema:"most tenants to return"`
}

type tenantRow struct {
	// ID is Textable's internal tenant id, and it is what every other tool
	// taking a tenant wants. The external id is a different identifier and is
	// not accepted anywhere.
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	ExternalID   string `json:"external_id,omitempty"`
	BillingPlan  string `json:"admin_billing_plan,omitempty"`
	BillingGroup string `json:"billing_group,omitempty"`
	SupportEmail string `json:"support_email,omitempty"`
	// Provider is who actually carries the messages -- Bandwidth, Skyswitch and
	// so on. Worth surfacing: a delivery problem that is really the carrier's
	// looks exactly like one that is Textable's until somebody knows which.
	Provider string `json:"provider,omitempty"`
	Admins   int    `json:"tenant_admins,omitempty"`
}

type tenantsResult struct {
	Tenants  []tenantRow `json:"tenants"`
	Returned int         `json:"returned"`
	Matching int         `json:"total_matching"`
	truncation
	Note string `json:"note,omitempty"`
}

func (p *Plugin) listTenants(ctx context.Context, in tenantsArgs) (tenantsResult, error) {
	if err := p.ready(); err != nil {
		return tenantsResult{}, err
	}
	raw, err := p.client.Get(ctx, tenantsPath, nil)
	p.note(err)
	if err != nil {
		return tenantsResult{}, err
	}

	var env struct {
		Tenants []tenantEntry `json:"tenants"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return tenantsResult{}, fmt.Errorf("textable: the tenant listing "+
			"answered with something other than the {\"tenants\":[…]} envelope "+
			"it serves: %w", err)
	}

	rows := make([]tenantRow, 0, len(env.Tenants))
	for _, t := range env.Tenants {
		if !matches(in.Query, t.TenantName, t.ExternalID.String()) {
			continue
		}
		row := tenantRow{
			ID:           t.ID,
			Name:         t.TenantName,
			ExternalID:   t.ExternalID.String(),
			BillingPlan:  t.AdminBillingPlan,
			SupportEmail: t.SupportDetails.PrimaryContactEmail,
			Provider:     t.Provider.Name,
			Admins:       len(t.TenantAdmins),
		}
		if t.BillingGroup != nil {
			row.BillingGroup = *t.BillingGroup
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].ID < rows[j].ID
	})

	matching := len(rows)
	kept, cut := bound(rows, p.limitOf(in.Limit))

	res := tenantsResult{
		Tenants:    kept,
		Returned:   len(kept),
		Matching:   matching,
		truncation: cut,
	}
	if matching == 0 && strings.TrimSpace(in.Query) != "" {
		res.Note = "No tenant's name or external id contains that. The filter " +
			"is applied here rather than by Textable, so it searched every " +
			"tenant this token can see."
	}
	return res, nil
}

// --- the billing report ----------------------------------------------------

// tenantReport is one tenant as the billing endpoint describes it.
//
// The two breakdowns overlap and neither is redundant. OrganizationBreakdown
// carries a user's id; UserBreakdown carries their name, phone number, licence
// type and whether they are soft-deleted, and no id at all. list_users joins
// them on email, because that is the only field both sides have.
type tenantReport struct {
	TenantName         string         `json:"TenantName"`
	TenantExternalID   flexID         `json:"TenantExternalId"`
	TenantBillingGroup string         `json:"TenantBillingGroup"`
	LicenseAllocation  map[string]any `json:"LicenseAllocation"`
	UserQuantity       float64        `json:"UserQuantity"`

	OrganizationBreakdown []struct {
		OrganizationName string  `json:"organizationName"`
		ExternalID       flexID  `json:"externalId"`
		LicenseType      string  `json:"LicenseType"`
		UserQuantity     float64 `json:"UserQuantity"`
		Users            []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"users"`
	} `json:"OrganizationBreakdown"`

	UserBreakdown []struct {
		FullName               string `json:"fullName"`
		Email                  string `json:"email"`
		PhoneNumber            string `json:"phoneNumber"`
		AccountType            string `json:"accountType"`
		LicenseType            string `json:"licenseType"`
		IsSoftDeleted          bool   `json:"isSoftDeleted"`
		OrganizationName       string `json:"organizationName"`
		OrganizationExternalID string `json:"organizationExternalId"`
	} `json:"UserBreakdown"`
}

// report fetches every tenant's breakdown, through the cache.
func (p *Plugin) report(ctx context.Context) ([]tenantReport, error) {
	raw, err := p.client.Get(ctx, tenantReportPath, nil)
	p.note(err)
	if err != nil {
		return nil, err
	}
	var out []tenantReport
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("textable: the tenant report answered with "+
			"something other than the array of tenants it documents: %w", err)
	}
	return out, nil
}

// --- one tenant ------------------------------------------------------------

type tenantArgs struct {
	TenantID string `json:"tenant_id" jsonschema:"the tenant's id, as list_tenants reports it"`
}

type tenantOrgRow struct {
	Name       string `json:"name"`
	ExternalID string `json:"external_id,omitempty"`
	License    string `json:"license_type,omitempty"`
	// Users is the billable count the report gives; Listed is how many user
	// records came with it. They can differ -- the first is a billing figure and
	// the second is a list -- and reporting one as the other would be inventing
	// a reconciliation nobody asked for.
	//
	// Both differ again from get_organization's user_records, which is the
	// organization document's own count. On a live instance a disabled
	// organization reports 0 billable, 0 listed and 41 records.
	Users  int `json:"billable_users"`
	Listed int `json:"users_listed"`
}

type tenantResult struct {
	ID            string         `json:"id"`
	Name          string         `json:"name,omitempty"`
	ExternalID    string         `json:"external_id,omitempty"`
	BillingGroup  string         `json:"billing_group,omitempty"`
	Users         int            `json:"total_users"`
	Licenses      map[string]any `json:"licenses,omitempty"`
	Organizations []tenantOrgRow `json:"organizations,omitempty"`
	truncation
}

func (p *Plugin) getTenant(ctx context.Context, in tenantArgs) (tenantResult, error) {
	if err := p.ready(); err != nil {
		return tenantResult{}, err
	}
	id := strings.TrimSpace(in.TenantID)
	if id == "" {
		return tenantResult{}, fmt.Errorf("textable: get_tenant needs a " +
			"tenant_id; list_tenants reports one for every tenant")
	}

	raw, err := p.client.Get(ctx, tenantReportPath+"/"+url.PathEscape(id), nil)
	p.note(err)
	if err != nil {
		return tenantResult{}, err
	}
	var t tenantReport
	if err := json.Unmarshal(raw, &t); err != nil {
		return tenantResult{}, fmt.Errorf("textable: the report for tenant %s "+
			"did not decode as the tenant document it documents: %w", id, err)
	}

	orgs := make([]tenantOrgRow, 0, len(t.OrganizationBreakdown))
	for _, o := range t.OrganizationBreakdown {
		orgs = append(orgs, tenantOrgRow{
			Name:       o.OrganizationName,
			ExternalID: o.ExternalID.String(),
			License:    o.LicenseType,
			Users:      int(o.UserQuantity),
			Listed:     len(o.Users),
		})
	}
	sort.Slice(orgs, func(i, j int) bool { return orgs[i].Name < orgs[j].Name })
	kept, cut := bound(orgs, p.limitOf(0))

	return tenantResult{
		ID:            id,
		Name:          t.TenantName,
		ExternalID:    t.TenantExternalID.String(),
		BillingGroup:  t.TenantBillingGroup,
		Users:         int(t.UserQuantity),
		Licenses:      t.LicenseAllocation,
		Organizations: kept,
		truncation:    cut,
	}, nil
}

// --- users -----------------------------------------------------------------

type usersArgs struct {
	Query    string `json:"query,omitempty" jsonschema:"narrows to users whose name, email or phone number contains this"`
	TenantID string `json:"tenant_id,omitempty" jsonschema:"restricts to one tenant, by the id list_tenants reports"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most users to return"`
}

type userRow struct {
	// ID is present for a user an organization lists and absent for one only
	// the billing breakdown mentions. Nothing takes it today -- there is no
	// per-user read for this credential -- but it is what distinguishes two
	// people with the same name, and it is what a future tool would need.
	ID          string `json:"id,omitempty"`
	Name        string `json:"full_name,omitempty"`
	Email       string `json:"email,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	AccountType string `json:"account_type,omitempty"`
	License     string `json:"license_type,omitempty"`
	// Always present, never omitempty. It is a fact a reader acts on, and a
	// field that vanishes when false cannot be told from one the tool does not
	// report at all -- which is how it read to a model that was told the listing
	// carries it and then never saw it.
	SoftDeleted  bool   `json:"soft_deleted"`
	Tenant       string `json:"tenant,omitempty"`
	Organization string `json:"organization,omitempty"`
}

type usersResult struct {
	Users    []userRow `json:"users"`
	Returned int       `json:"returned"`
	Matching int       `json:"total_matching"`
	Total    int       `json:"total_users"`
	truncation
	Note string `json:"note,omitempty"`
}

func (p *Plugin) listUsers(ctx context.Context, in usersArgs) (usersResult, error) {
	if err := p.ready(); err != nil {
		return usersResult{}, err
	}

	// One tenant is fetched on its own rather than filtered out of the whole
	// report: the per-tenant path takes the same id list_tenants reports, and
	// asking for one customer should not walk every other one.
	var report []tenantReport
	tenantID := strings.TrimSpace(in.TenantID)
	if tenantID != "" {
		raw, err := p.client.Get(ctx, tenantReportPath+"/"+url.PathEscape(tenantID), nil)
		p.note(err)
		if err != nil {
			return usersResult{}, err
		}
		var one tenantReport
		if err := json.Unmarshal(raw, &one); err != nil {
			return usersResult{}, fmt.Errorf("textable: the report for tenant %s "+
				"did not decode as the tenant document it documents: %w", tenantID, err)
		}
		report = []tenantReport{one}
	} else {
		var err error
		if report, err = p.report(ctx); err != nil {
			return usersResult{}, err
		}
	}

	var rows []userRow
	var total int
	for _, t := range report {
		// The two breakdowns are joined on email, the only field both carry.
		// OrganizationBreakdown has the ids and UserBreakdown has everything
		// else, so neither alone answers "who is this".
		//
		// A blank email cannot be a join key -- two nameless users would match
		// each other -- so those are paired separately, by organization, below.
		// This is not hypothetical: a real instance has a user called "SPARK
		// Emergency SMS" with no address, and joining on email alone listed it
		// twice, once with an id and no name and once with a name and no id.
		// One person, two rows, and a user count one higher than the tenant's
		// own.
		detail := make(map[string]int, len(t.UserBreakdown))
		blankByOrg := make(map[string][]int)
		for i, u := range t.UserBreakdown {
			if key := emailKey(u.Email); key != "" {
				detail[key] = i
				continue
			}
			org := strings.TrimSpace(u.OrganizationName)
			blankByOrg[org] = append(blankByOrg[org], i)
		}

		// Indexed rather than keyed by email, because the blank-email pairing
		// has no key to record itself under.
		consumed := make([]bool, len(t.UserBreakdown))

		for _, o := range t.OrganizationBreakdown {
			for _, u := range o.Users {
				row := userRow{
					ID:           u.ID,
					Email:        u.Email,
					Tenant:       t.TenantName,
					Organization: o.OrganizationName,
					License:      o.LicenseType,
				}

				idx := -1
				if key := emailKey(u.Email); key != "" {
					if i, ok := detail[key]; ok {
						idx = i
					}
				} else if org := strings.TrimSpace(o.OrganizationName); len(blankByOrg[org]) > 0 {
					// Taken from the front and removed, so two nameless users in
					// one organization pair with two records rather than both
					// pairing with the first. Nothing distinguishes them -- no
					// email, and the id is on one side only -- so pairing them
					// in the order both arrays give is the best available, and
					// it still yields one row per person, which is the property
					// that matters.
					idx = blankByOrg[org][0]
					blankByOrg[org] = blankByOrg[org][1:]
				}

				if idx >= 0 {
					d := t.UserBreakdown[idx]
					row.Name = d.FullName
					row.PhoneNumber = d.PhoneNumber
					row.AccountType = d.AccountType
					row.SoftDeleted = d.IsSoftDeleted
					if d.LicenseType != "" {
						row.License = d.LicenseType
					}
					consumed[idx] = true
				}
				total++
				if matches(in.Query, row.Name, row.Email, row.PhoneNumber) {
					rows = append(rows, row)
				}
			}
		}

		// A user the billing breakdown describes but no organization listed.
		// Carried through without an id rather than dropped: "who is on this
		// tenant" is answered by the name, and silently omitting somebody is
		// worse than listing them with less detail.
		for i, d := range t.UserBreakdown {
			if consumed[i] {
				continue
			}
			row := userRow{
				Name:         d.FullName,
				Email:        d.Email,
				PhoneNumber:  d.PhoneNumber,
				AccountType:  d.AccountType,
				License:      d.LicenseType,
				SoftDeleted:  d.IsSoftDeleted,
				Tenant:       t.TenantName,
				Organization: d.OrganizationName,
			}
			total++
			if matches(in.Query, row.Name, row.Email, row.PhoneNumber) {
				rows = append(rows, row)
			}
		}
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Tenant != rows[j].Tenant {
			return rows[i].Tenant < rows[j].Tenant
		}
		if rows[i].Email != rows[j].Email {
			return rows[i].Email < rows[j].Email
		}
		return rows[i].ID < rows[j].ID
	})

	matching := len(rows)
	kept, cut := bound(rows, p.limitOf(in.Limit))

	res := usersResult{
		Users:      kept,
		Returned:   len(kept),
		Matching:   matching,
		Total:      total,
		truncation: cut,
	}
	if matching == 0 && strings.TrimSpace(in.Query) != "" {
		res.Note = "No user's name, email or phone number contains that. The " +
			"filter is applied here rather than by Textable, so it searched " +
			"every user in scope."
	}
	return res, nil
}

// emailKey normalises an address for joining the two breakdowns.
//
// Case-folded and trimmed, because the same person is written both ways in a
// report assembled from two sources. Not a general-purpose address
// normalisation -- it exists to match a record against itself.
func emailKey(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// --- organizations ---------------------------------------------------------

type orgsArgs struct {
	TenantID string `json:"tenant_id" jsonschema:"the tenant's id, as list_tenants reports it"`
	Query    string `json:"query,omitempty" jsonschema:"narrows to organizations whose name contains this"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most organizations to return"`
}

type orgRow struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type orgsResult struct {
	Organizations []orgRow `json:"organizations"`
	Returned      int      `json:"returned"`
	Matching      int      `json:"total_matching"`
	truncation
	Note string `json:"note,omitempty"`
}

func (p *Plugin) listOrganizations(ctx context.Context, in orgsArgs) (orgsResult, error) {
	if err := p.ready(); err != nil {
		return orgsResult{}, err
	}
	tenant := strings.TrimSpace(in.TenantID)
	if tenant == "" {
		return orgsResult{}, fmt.Errorf("textable: list_organizations needs a " +
			"tenant_id; list_tenants reports one for every tenant")
	}

	params := url.Values{}
	params.Set("tenantId", tenant)
	raw, err := p.client.Get(ctx, "/api/v2/organizations", params)
	p.note(err)
	if err != nil {
		return orgsResult{}, err
	}

	var env struct {
		Organizations []struct {
			ID               string `json:"id"`
			OrganizationName string `json:"organizationName"`
		} `json:"organizations"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return orgsResult{}, fmt.Errorf("textable: the organization listing "+
			"answered with something other than the {\"organizations\":[…]} "+
			"envelope it documents: %w", err)
	}

	rows := make([]orgRow, 0, len(env.Organizations))
	for _, o := range env.Organizations {
		if !matches(in.Query, o.OrganizationName) {
			continue
		}
		rows = append(rows, orgRow{ID: o.ID, Name: o.OrganizationName})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].ID < rows[j].ID
	})

	matching := len(rows)
	kept, cut := bound(rows, p.limitOf(in.Limit))

	res := orgsResult{
		Organizations: kept,
		Returned:      len(kept),
		Matching:      matching,
		truncation:    cut,
	}

	// An empty listing is two different answers and the endpoint gives the same
	// bytes for both: a tenant with no organizations, and a tenant id that does
	// not exist. Textable answers the second with {"organizations":[]} rather
	// than a 404 -- and inconsistently, since the same request has also been
	// seen to answer 502 -- so a caller shown an empty list cannot tell whether
	// they mistyped an id.
	//
	// Resolved by checking the id against the tenant listing, which is cached,
	// so this costs nothing in the ordinary case and only runs when the answer
	// was empty.
	if len(env.Organizations) == 0 {
		known, err := p.tenantExists(ctx, tenant)
		switch {
		case err != nil:
			// The check itself failed. Say what is and is not known rather than
			// claiming either answer.
			res.Note = "No organizations came back. Whether that is because the " +
				"tenant has none or because this tenant id does not exist could " +
				"not be checked: " + err.Error()
		case !known:
			return orgsResult{}, fmt.Errorf("textable: no tenant on this instance "+
				"has the id %q. The endpoint answers an unknown tenant with an "+
				"empty list rather than an error, so this was checked against "+
				"list_tenants, which reports every tenant this token can see", tenant)
		default:
			res.Note = "This tenant exists and has no organizations. Checked " +
				"against the tenant listing, because an unknown tenant id " +
				"returns an empty list here rather than an error."
		}
	}
	return res, nil
}

// tenantExists reports whether id names a tenant this token can see.
//
// Reads the tenant listing, which is the cached one, so an ordinary call pays
// nothing for it.
func (p *Plugin) tenantExists(ctx context.Context, id string) (bool, error) {
	raw, err := p.client.Get(ctx, tenantsPath, nil)
	if err != nil {
		return false, err
	}
	var env struct {
		Tenants []struct {
			ID string `json:"id"`
		} `json:"tenants"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return false, fmt.Errorf("the tenant listing did not decode: %w", err)
	}
	for _, t := range env.Tenants {
		if strings.TrimSpace(t.ID) == id {
			return true, nil
		}
	}
	return false, nil
}
