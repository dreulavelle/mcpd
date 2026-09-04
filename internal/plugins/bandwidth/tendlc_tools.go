package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

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

// tenDLCMaxPageSize is the largest page this API will serve.
//
// Asking for more is not clamped: `size=200`, which is what the plugin-wide
// default ceiling produced, comes back as a 200 carrying
// `<ErrorCode>1006</ErrorCode> size must be between 1 and 8`, so every
// unqualified list_campaigns and list_brands call failed outright. The ceiling
// in settings governs how much a tool will return; it is not a page size, and
// on this endpoint the two must not be the same number.
const tenDLCMaxPageSize = 8

// setTenDLCPage writes the page and size this API expects, and reports the
// size it settled on.
//
// The page is zero-based here, and that is not a detail: the shared setPage
// helper floors the page at 1, and page 1 on this endpoint is the *second*
// page. On account 9000004 that silently hid the first eight of twenty-one
// campaigns -- the listing reported TotalCount 21 and returned 13, and no
// amount of paging forward ever reached the missing eight. Nothing failed, so
// nothing said so.
//
// Other Bandwidth endpoints do not share the convention -- /inserviceNumbers
// treats page 0 and page 1 alike -- which is why this is a 10DLC-local helper
// rather than a change to setPage.
func setTenDLCPage(q url.Values, page, size int) int {
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > tenDLCMaxPageSize {
		size = tenDLCMaxPageSize
	}
	// The tool's own page stays 1-based, because that is what a caller means
	// by "the first page"; the translation belongs here rather than in every
	// caller's head.
	q.Set("page", strconv.Itoa(page-1))
	q.Set("size", strconv.Itoa(size))
	return size
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
			"what a carrier rejects a campaign over.\n\n" +
			"decline_reasons is this connector's parse of " +
			"SecondaryDcaDeclineReason into separate codes, keyed by campaign " +
			"id; the raw field stays on the campaign and is the authority.\n\n" +
			"There is no campaign history here, and asking for one will not " +
			"produce it. Bandwidth serves campaign provisioning history only " +
			"through the Registration Center API, which these accounts are " +
			"not enabled for, and the campaign API that does serve them has " +
			"no history, revision or audit sub-resource. What a campaign " +
			"carries is its current state and its current decline reason — " +
			"no previous submissions, and no record of what changed between " +
			"attempts. To compare attempts, read the campaigns and compare " +
			"their fields.",
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
	DCAStatus        string `json:"dca_status,omitempty" jsonschema:"keep only campaigns whose SecondaryDcaSharingStatus matches, such as DECLINED, ACCEPTED or PENDING; applied to the page that came back, so page through the whole listing to find every match"`
	Page             int    `json:"page,omitempty" jsonschema:"1-based page number"`
	Limit            int    `json:"limit,omitempty" jsonschema:"most campaigns to return; this endpoint serves at most 8 per page whatever this says"`
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

	// DeclineReasons is keyed by campaign id, and is mcpd's reading of each
	// SecondaryDcaDeclineReason rather than anything Bandwidth sent. It is
	// kept out of the campaign records themselves so that what a carrier
	// actually wrote and what this code made of it stay told apart.
	DeclineReasons map[string][]DeclineReason `json:"decline_reasons,omitempty"`

	// Where this page sits in the listing. Reported rather than left to be
	// inferred: an agent asked to read every campaign has to know whether it
	// has, and "the page was full" is a guess where TotalCount is a fact.
	Page       int  `json:"page,omitempty"`
	PageSize   int  `json:"page_size,omitempty"`
	TotalCount int  `json:"total_count,omitempty"`
	HasMore    bool `json:"has_more,omitempty"`
	NextPage   int  `json:"next_page,omitempty"`

	Note string `json:"note,omitempty"`
}

// attachDeclineReasons parses every campaign's decline reason into the output.
//
// The campaign records are not touched. Twelve of the twenty-one campaigns on
// the account this was written against carry a reason, in two different
// formats, and the raw field remains the authority for all of them.
func (o *TenDLCOutput) attachDeclineReasons() {
	for _, item := range o.Items {
		raw, _ := item["SecondaryDcaDeclineReason"].(string)
		parsed := parseDeclineReasons(raw)
		if len(parsed) == 0 {
			continue
		}
		id, _ := item["CampaignId"].(string)
		if id == "" {
			continue
		}
		if o.DeclineReasons == nil {
			o.DeclineReasons = make(map[string][]DeclineReason)
		}
		o.DeclineReasons[id] = parsed
	}
}

// filterByDCAStatus keeps only the campaigns matching a SecondaryDcaSharingStatus.
//
// The filter runs over the page that came back rather than the whole listing,
// because the upstream has no such filter and fetching every page to honour
// one would turn a single read into an unbounded number of them against an API
// that rate-limits. The note says so, so that an empty result is not read as
// "there are none".
func (o *TenDLCOutput) filterByDCAStatus(want string) {
	want = strings.TrimSpace(want)
	if want == "" {
		return
	}
	kept := make([]Record, 0, len(o.Items))
	for _, item := range o.Items {
		got, _ := item["SecondaryDcaSharingStatus"].(string)
		if strings.EqualFold(strings.TrimSpace(got), want) {
			kept = append(kept, item)
		}
	}
	dropped := len(o.Items) - len(kept)
	o.Items = kept
	o.Returned = len(kept)
	if dropped > 0 {
		if o.Note != "" {
			o.Note += " "
		}
		o.Note += fmt.Sprintf("%d campaign(s) on this page did not match "+
			"dca_status=%s; the filter applies to this page only, so page "+
			"through the listing to find every match", dropped, want)
	}
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
		out, err := p.tenDLCList(ctx, base, "Campaigns", "Campaign",
			in.Page, p.client.limit(in.Limit))
		if err != nil {
			return out, err
		}
		out.filterByDCAStatus(in.DCAStatus)
		out.attachDeclineReasons()
		return out, nil
	}

	one := base + "/" + url.PathEscape(in.CampaignID)
	rec, err := p.client.getXMLAt(ctx, one, nil)
	p.note(err, nil)
	if err != nil {
		return TenDLCOutput{}, err
	}
	out := TenDLCOutput{Items: unwrap(rec, "Campaign")}
	out.Returned = len(out.Items)
	out.attachDeclineReasons()

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

	if page <= 0 {
		page = 1
	}
	q := url.Values{}
	size := setTenDLCPage(q, page, limit)

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
	out := TenDLCOutput{
		Items:      items,
		Returned:   len(items),
		Note:       note,
		Page:       page,
		PageSize:   size,
		TotalCount: intField(rec, "TotalCount"),
	}

	// TotalCount is the honest answer where the endpoint sends one; a full
	// page is the fallback guess where it does not. Saying which of the two
	// this is matters, because "there may be more" and "there are eight more"
	// lead an agent to different behaviour.
	switch {
	case out.TotalCount > 0:
		out.HasMore = page*size < out.TotalCount
	default:
		out.HasMore = len(items) == size
	}
	if out.HasMore {
		out.NextPage = page + 1
		if out.Note != "" {
			out.Note += " "
		}
		if out.TotalCount > 0 {
			out.Note += fmt.Sprintf("showing %d of %d; ask for page %d",
				len(items), out.TotalCount, out.NextPage)
		} else {
			out.Note += "a full page came back, so there may be more; ask for the next page"
		}
	}
	return out, nil
}

// intField reads a count that Bandwidth may have sent as a number or as text.
//
// The XML decoder keeps element bodies as strings, so TotalCount arrives as
// "21" rather than 21; a plain type assertion to int silently yields zero,
// which reads as "the endpoint sent no total" and turns a known count back
// into a guess.
func intField(rec Record, key string) int {
	switch v := rec[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	}
	return 0
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
