package bandwidth

import (
	"context"
	"fmt"
	"net/url"

	"github.com/spoked/mcpd/internal/plugins"
)

// Numbers, and whether they are allowed to do what somebody expects.

func (p *Plugin) registerNumberTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_tollfree_verification",
		Title: "Get a toll-free number's verification status",
		Description: "Whether one toll-free number is verified to send " +
			"messages, and if not, where the submission has got to. This is " +
			"the answer to “why is texting from our 800 number failing”: an " +
			"unverified toll-free number is blocked by carriers rather than by " +
			"Bandwidth, so nothing in the message logs explains it.",
		Idempotent: true,
	}, p.getTollFreeVerification)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_tollfree_usecases",
		Title: "List toll-free verification use cases",
		Description: "The use-case categories Bandwidth accepts on a toll-free " +
			"verification submission. Reference data; it does not change often.",
		Idempotent: true,
	}, p.listTollFreeUseCases)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_endpoints",
		Title: "List endpoints",
		Description: "Endpoints configured on this account. Ask for one by id " +
			"to read it on its own.",
		Idempotent: true,
	}, p.listEndpoints)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_number_lookup",
		Title: "Get a number lookup result",
		Description: "The result of a bulk number lookup that was already " +
			"submitted, by its request id — carrier, line type and portability " +
			"for each number. Lookups are started elsewhere; this reads a " +
			"finished one, and says so when it is not finished yet.",
		Idempotent: true,
	}, p.getNumberLookup)
}

// TollFreeInput names the number to check.
type TollFreeInput struct {
	Account     string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	PhoneNumber string `json:"phone_number" jsonschema:"the toll-free number in E.164, such as +18005551234"`
}

func (p *Plugin) getTollFreeVerification(ctx context.Context, in TollFreeInput) (Record, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return nil, err
	}
	if in.PhoneNumber == "" {
		return nil, fmt.Errorf("bandwidth: a phone number is required, in E.164 " +
			"such as +18005551234")
	}
	var out Record
	err = p.client.get(ctx, hostAPI,
		fmt.Sprintf("/api/v2/accounts/%s/phoneNumbers/%s/tollFreeVerification",
			account, url.PathEscape(in.PhoneNumber)), nil, &out)
	p.note(err, nil)
	return out, err
}

// UseCasesInput takes nothing, and takes no account either: this is
// Bandwidth's own reference list, the same for every customer. It exists so
// the tool has a schema.
type UseCasesInput struct{}

func (p *Plugin) listTollFreeUseCases(ctx context.Context, _ UseCasesInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	var items []Record
	var err error
	// Account-free: this is Bandwidth's own reference list, the same for
	// everybody, which is why the path carries no account id.
	err = p.client.get(ctx, hostAPI, "/api/v2/tollFreeVerification/useCases", nil, &items)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	return capped(items, p.client.limit(0)), nil
}

// EndpointsInput narrows a listing of endpoints.
type EndpointsInput struct {
	Account      string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	EndpointType string `json:"endpoint_type,omitempty" jsonschema:"narrow to one kind of endpoint"`
	EndpointID   string `json:"endpoint_id,omitempty" jsonschema:"one endpoint by id, to read it on its own"`
}

func (p *Plugin) listEndpoints(ctx context.Context, in EndpointsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}

	// Note the prefix: Bandwidth serves endpoints and number lookup under /v2
	// on the same host that serves toll-free verification under /api/v2.
	if in.EndpointID != "" {
		var one Record
		err = p.client.get(ctx, hostAPI,
			fmt.Sprintf("/v2/accounts/%s/endpoints/%s", account,
				url.PathEscape(in.EndpointID)), nil, &one)
		p.note(err, nil)
		if err != nil {
			return Listing{}, err
		}
		return Listing{Items: []Record{one}, Returned: 1}, nil
	}

	q := url.Values{}
	set(q, "endpointType", in.EndpointType)

	var items []Record
	err = p.client.get(ctx, hostAPI,
		fmt.Sprintf("/v2/accounts/%s/endpoints", account), q, &items)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	return capped(items, p.client.limit(0)), nil
}

// LookupInput names the lookup request to read.
type LookupInput struct {
	Account   string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	RequestID string `json:"request_id" jsonschema:"the id returned when the bulk lookup was submitted"`
}

func (p *Plugin) getNumberLookup(ctx context.Context, in LookupInput) (Record, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return nil, err
	}
	if in.RequestID == "" {
		return nil, fmt.Errorf("bandwidth: a lookup request id is required")
	}
	var out Record
	err = p.client.get(ctx, hostAPI,
		fmt.Sprintf("/v2/accounts/%s/phoneNumberLookup/bulk/%s",
			account, url.PathEscape(in.RequestID)), nil, &out)
	p.note(err, nil)
	return out, err
}
