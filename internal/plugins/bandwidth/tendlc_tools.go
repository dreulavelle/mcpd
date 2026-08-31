package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/spoked/mcpd/internal/plugins"
)

// 10DLC: who is registered to send messages, and whether they still are.
//
// This half of the Dashboard answers JSON rather than XML, in a {data, page}
// envelope. Bandwidth's arrangement, not a choice made here.

func (p *Plugin) registerTenDLCTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_campaigns",
		Title: "List 10DLC campaigns",
		Description: "10DLC campaigns registered on this account — the " +
			"registrations that allow a number to send A2P messages. Give an id " +
			"to read one, with its status history and the numbers assigned to " +
			"it. A campaign that has been suspended or expired is the usual " +
			"reason messages from a working number start failing.",
		Idempotent: true,
	}, p.listCampaigns)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_brands",
		Title: "List 10DLC brands",
		Description: "10DLC brands on this account — the registered business " +
			"identity that campaigns hang from. Give an id to read one, with " +
			"its vetting record and status history. A brand that failed vetting " +
			"blocks every campaign under it.",
		Idempotent: true,
	}, p.listBrands)
}

// tenDLCEnvelope is the {data, page} wrapper the 10DLC endpoints return.
//
// Data is decoded late because its shape varies by endpoint: an object for a
// single brand, an array for a listing or a history. Decoding it as `any` and
// sorting it out here beats two response types that differ by one field.
type tenDLCEnvelope struct {
	Data any `json:"data"`
	Page struct {
		Number        int `json:"pageNumber"`
		Size          int `json:"pageSize"`
		TotalElements int `json:"totalElements"`
		TotalPages    int `json:"totalPages"`
	} `json:"page"`
}

// records normalises the envelope's data into a slice, whichever shape it
// arrived in.
func (e tenDLCEnvelope) records() []Record {
	switch v := e.Data.(type) {
	case []any:
		out := make([]Record, 0, len(v))
		for _, item := range v {
			if rec, ok := item.(map[string]any); ok {
				out = append(out, rec)
			}
		}
		return out
	case map[string]any:
		return []Record{v}
	}
	return nil
}

// CampaignsInput names one campaign, or none for a listing.
type CampaignsInput struct {
	CampaignID       string `json:"campaign_id,omitempty" jsonschema:"one campaign by id, such as CEXMPL1"`
	WithHistory      bool   `json:"with_history,omitempty" jsonschema:"also fetch the campaign's status history; requires campaign_id"`
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
	if (in.WithHistory || in.WithPhoneNumbers) && in.CampaignID == "" {
		return TenDLCOutput{}, fmt.Errorf("bandwidth: with_history and " +
			"with_phone_numbers need a campaign_id")
	}
	base := fmt.Sprintf("%s/accounts/%s/tendlc/campaigns",
		dashboardPrefix, p.client.AccountID())

	if in.CampaignID == "" {
		return p.tenDLCList(ctx, base, in.Page, p.client.limit(in.Limit))
	}

	one := base + "/" + url.PathEscape(in.CampaignID)
	var env tenDLCEnvelope
	err := p.client.get(ctx, hostAPI, one, nil, &env)
	p.note(err, nil)
	if err != nil {
		return TenDLCOutput{}, err
	}
	out := TenDLCOutput{Items: env.records()}
	out.Returned = len(out.Items)

	var missing []string
	if in.WithHistory {
		if recs, err := p.tenDLCSub(ctx, one+"/history"); err != nil {
			missing = append(missing, "history ("+err.Error()+")")
		} else {
			out.History = recs
		}
	}
	if in.WithPhoneNumbers {
		if recs, err := p.tenDLCSub(ctx, one+"/phoneNumbers"); err != nil {
			missing = append(missing, "phone numbers ("+err.Error()+")")
		} else {
			out.PhoneNumbers = recs
		}
	}
	if len(missing) > 0 {
		out.Note = "the campaign was read; these were not: " + joinAnd(missing)
	}
	return out, nil
}

// BrandsInput names one brand, or none for a listing.
type BrandsInput struct {
	BrandID      string `json:"brand_id,omitempty" jsonschema:"one brand by id, such as BEXMPL6"`
	WithHistory  bool   `json:"with_history,omitempty" jsonschema:"also fetch the brand's status history; requires brand_id"`
	WithVettings bool   `json:"with_vettings,omitempty" jsonschema:"also fetch the brand's vetting record, which says why it passed or failed; requires brand_id"`
	Page         int    `json:"page,omitempty" jsonschema:"1-based page number"`
	Limit        int    `json:"limit,omitempty" jsonschema:"most brands to return; the configured ceiling applies whatever this says"`
}

func (p *Plugin) listBrands(ctx context.Context, in BrandsInput) (TenDLCOutput, error) {
	if err := p.ready(); err != nil {
		return TenDLCOutput{}, err
	}
	if (in.WithHistory || in.WithVettings) && in.BrandID == "" {
		return TenDLCOutput{}, fmt.Errorf("bandwidth: with_history and " +
			"with_vettings need a brand_id")
	}
	base := fmt.Sprintf("%s/accounts/%s/tendlc/brands",
		dashboardPrefix, p.client.AccountID())

	if in.BrandID == "" {
		return p.tenDLCList(ctx, base, in.Page, p.client.limit(in.Limit))
	}

	one := base + "/" + url.PathEscape(in.BrandID)
	var env tenDLCEnvelope
	err := p.client.get(ctx, hostAPI, one, nil, &env)
	p.note(err, nil)
	if err != nil {
		return TenDLCOutput{}, err
	}
	out := TenDLCOutput{Items: env.records()}
	out.Returned = len(out.Items)

	var missing []string
	if in.WithHistory {
		if recs, err := p.tenDLCSub(ctx, one+"/history"); err != nil {
			missing = append(missing, "history ("+err.Error()+")")
		} else {
			out.History = recs
		}
	}
	if in.WithVettings {
		if recs, err := p.tenDLCSub(ctx, one+"/vettings"); err != nil {
			missing = append(missing, "vettings ("+err.Error()+")")
		} else {
			out.Vettings = recs
		}
	}
	if len(missing) > 0 {
		out.Note = "the brand was read; these were not: " + joinAnd(missing)
	}
	return out, nil
}

// tenDLCList reads one page of a 10DLC collection.
func (p *Plugin) tenDLCList(ctx context.Context, path string, page, limit int) (TenDLCOutput, error) {
	q := url.Values{}
	q.Set("size", strconv.Itoa(limit))
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}

	var env tenDLCEnvelope
	err := p.client.get(ctx, hostAPI, path, q, &env)
	p.note(err, nil)
	if err != nil {
		return TenDLCOutput{}, err
	}
	items := env.records()
	out := TenDLCOutput{Items: items, Returned: len(items)}
	// totalElements rather than a short page, because a full page is not
	// evidence of more and a short one is not evidence of none.
	if env.Page.TotalElements > len(items) {
		out.Note = fmt.Sprintf("%d exist; %d returned. Ask for the next page.",
			env.Page.TotalElements, len(items))
	}
	return out, nil
}

// tenDLCSub reads a sub-resource that shares the envelope.
func (p *Plugin) tenDLCSub(ctx context.Context, path string) ([]Record, error) {
	var env tenDLCEnvelope
	if err := p.client.get(ctx, hostAPI, path, nil, &env); err != nil {
		return nil, err
	}
	return env.records(), nil
}
