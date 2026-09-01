package bandwidth

import (
	"context"
	"fmt"
	"net/url"

	"github.com/spoked/mcpd/internal/plugins"
)

// tenDLCBase is where Bandwidth actually serves campaign management.
//
// This was wrong, and wrong in a way that looked like somebody else's problem.
// It used to build /api/v2/accounts/{id}/tendlc/campaigns, which is a real
// route -- it answers rather than 404s -- but it belongs to the Registration
// Center, a product these accounts do not have. Bandwidth's refusal says
// "Account X is not enabled for the Registration Center", which reads as an
// entitlement to go and buy, and every account failed identically because none
// of them has it.
//
// The documented campaign API is a different path on the same host, under /api
// rather than /api/v2. Reading a campaign that plainly exists was impossible
// until this moved.
//
// https://dev.bandwidth.com/docs/messaging/campaign-management/csp/campaign-api/
func tenDLCBase(account string) string {
	return "/api/accounts/" + url.PathEscape(account) + "/campaignManagement/10dlc"
}

// 10DLC: who is registered to send messages, and whether they still are.
//
// Campaign management answers XML, like the rest of the Dashboard. It was read
// as JSON here, which produced a 406 with an empty body once the paths were
// corrected -- the endpoint declining to produce a media type it does not
// serve. Two wrongs that hid each other: while the path was wrong the request
// never got far enough to be refused for its Accept header.

func (p *Plugin) registerTenDLCTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_campaigns",
		Title: "List 10DLC campaigns",
		Description: "10DLC campaigns registered on this account — the " +
			"registrations that allow a number to send A2P messages. Give an id " +
			"to read one, with the numbers assigned to it. A campaign that has " +
			"been suspended or expired is the usual reason messages from a " +
			"working number start failing.\n\n" +
			"For a rejected or partly-working campaign, the fields that matter " +
			"are Status (The Campaign Registry's view), MnoStatusList (each " +
			"carrier's own view, which is independent — a campaign can be " +
			"ACTIVE at TCR and rejected by one carrier) and " +
			"SecondaryDcaDeclineReason. Compare those against MessageFlow, " +
			"Sample1 to Sample5, the Optin/Optout/Help keywords and messages, " +
			"and the TermsAndConditionsLink and PrivacyPolicyLink, which are " +
			"what a carrier rejects a campaign over.",
		Idempotent: true,
	}, p.listCampaigns)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_brands",
		Title: "List 10DLC brands",
		Description: "10DLC brands on this account — the registered business " +
			"identity that campaigns hang from. Give an id to read one, with " +
			"its external vetting record. A brand that failed vetting blocks " +
			"every campaign under it, so read the brand before concluding a " +
			"campaign's own fields are at fault. IdentityStatus is the brand's " +
			"own verification state; a separate vetting record exists only " +
			"where one was purchased.",
		Idempotent: true,
	}, p.listBrands)
}

// CampaignsInput names one campaign, or none for a listing.
type CampaignsInput struct {
	Account          string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	CampaignID       string `json:"campaign_id,omitempty" jsonschema:"one campaign by id, such as CEXMPL1"`
	WithPhoneNumbers bool   `json:"with_phone_numbers,omitempty" jsonschema:"also fetch the numbers assigned to the campaign; requires campaign_id"`
	Page             int    `json:"page,omitempty" jsonschema:"1-based page number"`
	Limit            int    `json:"limit,omitempty" jsonschema:"most campaigns to return; the configured ceiling applies whatever this says"`
}

// TenDLCOutput is a campaign or brand, with whatever else was asked for.
type TenDLCOutput struct {
	Items    []Record `json:"items"`
	Returned int      `json:"returned"`
	History  []Record `json:"history,omitempty"`
	// PhoneNumbers is set for a campaign; Vettings for a brand. Both are on
	// one type because they are the same shape of answer and one output
	// schema costs less than two.
	PhoneNumbers []Record `json:"phone_numbers,omitempty"`
	Vettings     []Record `json:"vettings,omitempty"`
	Note         string   `json:"note,omitempty"`
}

func (p *Plugin) listCampaigns(ctx context.Context, in CampaignsInput) (TenDLCOutput, error) {
	if err := p.ready(); err != nil {
		return TenDLCOutput{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return TenDLCOutput{}, err
	}
	if in.WithPhoneNumbers && in.CampaignID == "" {
		return TenDLCOutput{}, fmt.Errorf("bandwidth: with_phone_numbers needs " +
			"a campaign_id")
	}
	base := tenDLCBase(account) + "/campaigns"

	if in.CampaignID == "" {
		return p.tenDLCList(ctx, base, "Campaigns", "Campaign",
			in.Page, p.client.limit(in.Limit))
	}

	one := base + "/" + url.PathEscape(in.CampaignID)
	rec, err := p.client.getXMLAt(ctx, one, nil)
	p.note(err, nil)
	if err != nil {
		return TenDLCOutput{}, err
	}
	out := TenDLCOutput{Items: unwrap(rec, "Campaign")}
	out.Returned = len(out.Items)

	if in.WithPhoneNumbers {
		// /tn, not /phoneNumbers.
		recs, note, err := p.tenDLCSub(ctx, one+"/tn", "TelephoneNumbers", "TelephoneNumber")
		switch {
		case err != nil:
			out.Note = "the campaign was read; its numbers were not: " + err.Error()
		case note != "":
			out.Note = "phone numbers: " + note
		case len(recs) == 0:
			out.Note = "no numbers are assigned to this campaign"
		default:
			out.PhoneNumbers = recs
		}
	}
	return out, nil
}

// unwrap returns a single-record response as a one-element slice.
//
// Bandwidth wraps a single entity in a named element on some endpoints and not
// on others, so the wrapper is unwrapped when it is there and the record is
// used as-is when it is not. Returning the wrapper itself would give a model a
// record whose only field is the entity it wanted.
func unwrap(rec Record, element string) []Record {
	if inner, ok := rec[element].(Record); ok {
		return []Record{inner}
	}
	if len(rec) == 0 {
		return nil
	}
	return []Record{rec}
}

// BrandsInput names one brand, or none for a listing.
type BrandsInput struct {
	Account      string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	BrandID      string `json:"brand_id,omitempty" jsonschema:"one brand by id, such as BEXMPL6"`
	WithVettings bool   `json:"with_vettings,omitempty" jsonschema:"also fetch the brand's external vetting record, which says why it passed or failed; requires brand_id"`
	Page         int    `json:"page,omitempty" jsonschema:"1-based page number"`
	Limit        int    `json:"limit,omitempty" jsonschema:"most brands to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listBrands(ctx context.Context, in BrandsInput) (TenDLCOutput, error) {
	if err := p.ready(); err != nil {
		return TenDLCOutput{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return TenDLCOutput{}, err
	}
	if in.WithVettings && in.BrandID == "" {
		return TenDLCOutput{}, fmt.Errorf("bandwidth: with_vettings needs a brand_id")
	}
	base := tenDLCBase(account) + "/brands"

	if in.BrandID == "" {
		// /brands/details rather than /brands: the bare listing is abbreviated
		// and unpaginated, which is the wrong shape for a tool that bounds what
		// it returns and reports what it left out.
		return p.tenDLCList(ctx, base+"/details", "Brands", "Brand",
			in.Page, p.client.limit(in.Limit))
	}

	one := base + "/" + url.PathEscape(in.BrandID)
	rec, err := p.client.getXMLAt(ctx, one, nil)
	p.note(err, nil)
	if err != nil {
		return TenDLCOutput{}, err
	}
	out := TenDLCOutput{Items: unwrap(rec, "Brand")}
	out.Returned = len(out.Items)

	if in.WithVettings {
		// /vetting, singular.
		recs, note, err := p.tenDLCSub(ctx, one+"/vetting", "Vettings", "Vetting")
		switch {
		case err != nil:
			out.Note = "the brand was read; its vetting record was not: " + err.Error()
		case note != "":
			out.Note = "vetting: " + note
		case len(recs) == 0:
			out.Note = "this brand has no external vetting record; the brand's " +
				"own IdentityStatus is what says whether it passed"
		default:
			out.Vettings = recs
		}
	}
	return out, nil
}

// tenDLCList reads one page of a 10DLC collection.
func (p *Plugin) tenDLCList(ctx context.Context, path, container, element string,
	page, limit int) (TenDLCOutput, error) {

	q := url.Values{}
	setPage(q, page, limit)

	rec, err := p.client.getXMLAt(ctx, path, q)
	p.note(err, nil)
	if err != nil {
		return TenDLCOutput{}, err
	}
	// collect falls back to finding a repeated element when the named wrapper
	// is not the one Bandwidth used. That fallback matters more here than
	// anywhere else in this package: these element names are the one part of
	// this endpoint nobody has confirmed against a live response, and a wrong
	// guess would report a populated account as having no campaigns.
	items, note := collect(rec, container, element)
	out := TenDLCOutput{Items: items, Returned: len(items), Note: note}
	if len(items) == limit {
		if out.Note != "" {
			out.Note += " "
		}
		out.Note += "a full page came back, so there may be more; ask for the next page"
	}
	return out, nil
}

// tenDLCSub reads a sub-resource of one campaign or brand.
//
// The note from collect is returned rather than dropped. Without it, three
// different outcomes reached a caller as the same empty field: the campaign
// genuinely has no numbers, the response carried numbers under an element name
// this package did not recognise, and the sub-resource was not read at all.
// Only the first is good news, and a model shown an absent field will assume it.
func (p *Plugin) tenDLCSub(ctx context.Context, path, container, element string) ([]Record, string, error) {
	rec, err := p.client.getXMLAt(ctx, path, nil)
	if err != nil {
		return nil, "", err
	}
	items, note := collect(rec, container, element)
	return items, note, nil
}
