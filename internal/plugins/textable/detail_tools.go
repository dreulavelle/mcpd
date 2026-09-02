package textable

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// registerDetailTools adds the two reads that take an id.
//
// There is deliberately no get_user, and its absence is a finding rather than
// an omission. The specification lists SystemToken among the credentials
// GET /api/v2/users/{id} accepts; against a live instance it answers 401 to a
// service account token, with a real user id and every read scope granted. So
// the per-user read is not available to this credential at all, and what can be
// known about a user is what list_users reports.
func (p *Plugin) registerDetailTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_organization",
		Title: "Get one organization",
		Description: "One organization in full: its user-record count, billing " +
			"plan, organization admins, whether it is disabled or deleted, and " +
			"whether it requires contacts to consent before anyone may message " +
			"them. A managing user is reported where one is set.\n\n" +
			"A disabled organization stops every user in it from logging in, " +
			"sending, or running a blast, so this is where \"why can none of " +
			"this customer's staff send\" is answered. Takes the id " +
			"list_organizations reports.\n\n" +
			"`user_records` is this organization's own count of attached user " +
			"records. It is deliberately not the same number as the " +
			"`billable_users` get_tenant reports, and not the number of people " +
			"list_users returns. Observed on a live instance: an organization " +
			"marked disabled and deleted reports 41 records and 0 billable, " +
			"because records outlive the licence; two live organizations differ " +
			"by one and by five.\n\n" +
			"So if somebody asks how many people an organization has, answer " +
			"from list_users or from get_tenant's billable_users, and treat " +
			"user_records as an upper bound that includes records no longer in " +
			"use. Do not report the difference between them as an " +
			"inconsistency, and do not state a reason for it beyond this -- the " +
			"API does not say what the extra records are.",
		Idempotent: true,
	}, p.getOrganization)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_contact",
		Title: "Get one contact",
		Description: "One contact by id: name, phone number, email, whether " +
			"they have opted out of messages, and whether they are archived.\n\n" +
			"Note what is not here: there is no way to *list* contacts with " +
			"this credential, and no way to search them, so this tool is usable " +
			"only when a contact id is already known -- from an export, another " +
			"system, or a link somebody pasted. Do not attempt to enumerate " +
			"contacts by guessing ids.\n\n" +
			"If this returns a credential error, that is worth reporting rather " +
			"than retrying: the per-user read on this API is documented as " +
			"accepting a service account and does not, and this endpoint may " +
			"turn out to be the same.",
		Idempotent: true,
	}, p.getContact)
}

// --- one organization ------------------------------------------------------

type orgArgs struct {
	OrganizationID string `json:"organization_id" jsonschema:"the organization's internal id, as list_organizations reports it"`
}

type orgDocument struct {
	ID                           string   `json:"id"`
	OrganizationName             string   `json:"organizationName"`
	Description                  string   `json:"description"`
	UserCount                    float64  `json:"userCount"`
	ManagedBy                    string   `json:"managedBy"`
	OrganizationAdmins           []string `json:"organizationAdmins"`
	RequireContactConsent        bool     `json:"requireContactConsent"`
	RequireContactConsentMessage string   `json:"requireContactConsentMessage"`
	IsDisabled                   bool     `json:"is_disabled"`
	ExternalID                   string   `json:"external_id"`
	TenantID                     string   `json:"tenantId"`
	OrganizationType             string   `json:"organizationType"`
	Deleted                      float64  `json:"deleted"`
	Billing                      struct {
		Plan string `json:"plan"`
	} `json:"billing"`
}

type orgResult struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// TenantID says which tenant this organization belongs to, which is the one
	// direction the hierarchy cannot otherwise be walked: everything else goes
	// tenant to organization.
	TenantID   string `json:"tenant_id,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
	Type       string `json:"organization_type,omitempty"`
	// UserRecords is the organization document's own count, and it is a
	// different number from the billing report's -- not a contradiction, and
	// worth naming so nobody reads it as one.
	//
	// Measured on a live instance: a disabled organization reports 41 records
	// and 0 billable, because a disabled organization bills nothing and still
	// has its people. Two enabled ones also differ, by one and by five. The
	// billing figure is "billable now"; this is "user records attached to this
	// organization", and only the first is what an invoice is built from.
	UserRecords int    `json:"user_records"`
	Plan        string `json:"billing_plan,omitempty"`
	Disabled    bool   `json:"disabled"`
	Deleted     bool   `json:"deleted"`
	DeletedAt   string `json:"deleted_at,omitempty"`
	ManagedBy   string `json:"managed_by,omitempty"`
	// Admins is the list rather than a count, unlike the equivalent on a user.
	// These are the ids list_users reports, and the question "who
	// administers this customer" is one somebody follows up on.
	Admins                []string `json:"organization_admins,omitempty"`
	RequireContactConsent bool     `json:"requires_contact_consent"`
	ConsentMessage        string   `json:"consent_message,omitempty"`
	ValuesShortened       int      `json:"values_shortened,omitempty"`
}

func (p *Plugin) getOrganization(ctx context.Context, in orgArgs) (orgResult, error) {
	if err := p.ready(); err != nil {
		return orgResult{}, err
	}
	id := strings.TrimSpace(in.OrganizationID)
	if id == "" {
		return orgResult{}, fmt.Errorf("textable: get_organization needs an " +
			"organization_id; list_organizations reports one for each")
	}

	raw, err := p.client.Get(ctx, "/api/v2/organizations/"+url.PathEscape(id), nil)
	p.note(err)
	if err != nil {
		return orgResult{}, err
	}

	var o orgDocument
	if err := json.Unmarshal(raw, &o); err != nil {
		return orgResult{}, fmt.Errorf("textable: the organization record for %s "+
			"did not decode as the organization document it documents: %w", id, err)
	}

	res := orgResult{
		ID:                    orElse(o.ID, id),
		Name:                  o.OrganizationName,
		Description:           o.Description,
		TenantID:              o.TenantID,
		ExternalID:            o.ExternalID,
		Type:                  o.OrganizationType,
		UserRecords:           int(o.UserCount),
		Plan:                  o.Billing.Plan,
		Disabled:              o.IsDisabled,
		Deleted:               o.Deleted > 0,
		DeletedAt:             millisToRFC3339(o.Deleted),
		ManagedBy:             o.ManagedBy,
		Admins:                o.OrganizationAdmins,
		RequireContactConsent: o.RequireContactConsent,
	}
	res.ConsentMessage, res.ValuesShortened = countShorten(o.RequireContactConsentMessage, 0)
	return res, nil
}

// --- one contact -----------------------------------------------------------

type contactArgs struct {
	ContactID string `json:"contact_id" jsonschema:"the contact's id"`
}

// contactDocument is Textable's Contact, which is ContactFields and documents
// no id of its own -- so the id a caller asked for is what the result carries.
type contactDocument struct {
	ID          string `json:"id"`
	FullName    string `json:"full_name"`
	PhoneNumber string `json:"phone_number"`
	Email       string `json:"email"`
	Title       string `json:"title"`
	CompanyName string `json:"companyName"`
	Notes       string `json:"notes"`
	OptedOut    bool   `json:"optedOut"`
	IsArchived  bool   `json:"isArchived"`
}

type contactResult struct {
	ID          string `json:"id"`
	Name        string `json:"full_name,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Email       string `json:"email,omitempty"`
	Title       string `json:"title,omitempty"`
	Company     string `json:"company,omitempty"`
	Notes       string `json:"notes,omitempty"`
	// OptedOut is the one field here with consequences: a contact who has opted
	// out must not be messaged, and that is a legal position rather than a
	// preference.
	OptedOut        bool `json:"opted_out"`
	Archived        bool `json:"archived"`
	ValuesShortened int  `json:"values_shortened,omitempty"`
}

func (p *Plugin) getContact(ctx context.Context, in contactArgs) (contactResult, error) {
	if err := p.ready(); err != nil {
		return contactResult{}, err
	}
	id := strings.TrimSpace(in.ContactID)
	if id == "" {
		return contactResult{}, fmt.Errorf("textable: get_contact needs a " +
			"contact_id. There is no contact listing available to this " +
			"credential, so the id has to come from outside Textable")
	}

	raw, err := p.client.Get(ctx, "/api/v2/contacts/"+url.PathEscape(id), nil)
	p.note(err)
	if err != nil {
		return contactResult{}, err
	}

	var c contactDocument
	if err := json.Unmarshal(raw, &c); err != nil {
		return contactResult{}, fmt.Errorf("textable: the contact record for %s "+
			"did not decode as the contact document it documents: %w", id, err)
	}

	res := contactResult{
		ID:          orElse(c.ID, id),
		Name:        c.FullName,
		PhoneNumber: c.PhoneNumber,
		Email:       c.Email,
		Title:       c.Title,
		Company:     c.CompanyName,
		OptedOut:    c.OptedOut,
		Archived:    c.IsArchived,
	}
	res.Notes, res.ValuesShortened = countShorten(c.Notes, 0)
	return res, nil
}
