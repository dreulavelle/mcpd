package bandwidth

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// Which accounts this credential can answer for.

func (p *Plugin) registerAccountTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_accounts",
		Title: "List the accounts this credential reaches",
		Description: "The Bandwidth accounts this instance can read, and which " +
			"one an unqualified question means. Start here when a question is " +
			"about the whole estate rather than one account: every other tool " +
			"takes an account, and this is where the numbers come from.",
		Idempotent: true,
	}, p.listAccounts)
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_products",
		Title: "List what is enabled on an account",
		Description: "The Bandwidth products and features enabled on an " +
			"account — messaging, voice termination and origination, toll-free, " +
			"E911, campaign management and the rest, each with the individual " +
			"features switched on under it.\n\n" +
			"Read this first when a call fails with a permissions or " +
			"entitlement error rather than a bad argument. Bandwidth refuses a " +
			"product an account does not hold in the same shape it refuses a " +
			"credential that lacks a role, and from the error alone the two look " +
			"alike; this says which it is. It is also the fastest way to answer " +
			"\"can this account do X at all\" without trying X and reading the " +
			"refusal.",
		Idempotent: true,
	}, p.listProducts)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_subscriptions",
		Title: "List order notification subscriptions",
		Description: "The callback subscriptions on this account — where " +
			"Bandwidth sends notifications when an order, a port or a " +
			"disconnect changes state, and which events each one covers.\n\n" +
			"Read this when something downstream did not react to an order " +
			"completing. A missing or wrong subscription looks exactly like a " +
			"broken integration from the other end, and this is the only place " +
			"that distinguishes them.",
		Idempotent: true,
	}, p.listSubscriptions)

}

// AccountsInput takes nothing. It exists so the tool has a schema.
type AccountsInput struct{}

// AccountsOutput names what can be read and what happens if nobody says.
type AccountsOutput struct {
	// Accounts is what the credential itself says it covers, from the claims
	// in the token Bandwidth issued for it.
	Accounts []string `json:"accounts"`
	// Default is the account an unqualified call reads. Empty means there is
	// none configured, in which case a call that names no account is refused
	// rather than guessed at -- unless there is only one account, which
	// settles it on its own.
	Default string `json:"default_account,omitempty"`
	Note    string `json:"note,omitempty"`
}

func (p *Plugin) listAccounts(ctx context.Context, _ AccountsInput) (AccountsOutput, error) {
	if err := p.ready(); err != nil {
		return AccountsOutput{}, err
	}
	accounts, err := p.client.Accounts(ctx)
	p.note(err, accounts)
	if err != nil {
		return AccountsOutput{}, err
	}

	out := AccountsOutput{Accounts: accounts, Default: p.client.DefaultAccount()}
	switch {
	case len(accounts) == 0:
		out.Note = "this credential does not say which accounts it covers, so " +
			"an account has to be named on each call"
	case out.Default == "" && len(accounts) > 1:
		out.Note = "no default is set, so a call that names no account is " +
			"refused rather than answered about an arbitrary one"
	}
	return out, nil
}

// ProductsInput names the account to describe.
type ProductsInput struct {
	Account string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
}

// listProducts reports what the account is entitled to.
//
// The endpoint's own description is "Discover what is currently enabled on the
// account", and it is the answer to a question this integration otherwise makes
// people guess at. A Bandwidth refusal for a product the account does not hold
// and one for a credential missing a role are the same status with a similar
// body, and the difference decides whether somebody edits an API credential or
// asks Bandwidth to sell them something.
//
// It came out of exactly that: every 10DLC read here failed with an entitlement
// message for months, and it was the path that was wrong. Had this existed, the
// first call would have shown Campaign Management enabled and moved the
// investigation somewhere useful on day one.
func (p *Plugin) listProducts(ctx context.Context, in ProductsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	rec, err := p.client.getXML(ctx, fmt.Sprintf("/accounts/%s/products", account), nil)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	items, note := collect(rec, "Products", "Product")
	out := Listing{Items: items, Returned: len(items), Note: note}
	if len(items) == 0 && note == "" {
		// An account with no products is not a thing that exists; an empty
		// answer here means the shape changed, and reporting "nothing is
		// enabled" would be worse than saying so.
		out.Note = "no products came back, which no live account should report — " +
			"treat this as unknown rather than as nothing being enabled"
	}
	return out, nil
}

// SubscriptionsInput narrows a listing of callback subscriptions, or names one.
type SubscriptionsInput struct {
	Account        string `json:"account,omitempty" jsonschema:"account number to read; omit for the default account"`
	SubscriptionID string `json:"subscription_id,omitempty" jsonschema:"one subscription by id; omit to list"`
}

func (p *Plugin) listSubscriptions(ctx context.Context, in SubscriptionsInput) (Listing, error) {
	if err := p.ready(); err != nil {
		return Listing{}, err
	}
	account, err := p.client.resolveAccount(ctx, in.Account)
	if err != nil {
		return Listing{}, err
	}
	base := fmt.Sprintf("/accounts/%s/subscriptions", account)
	if id := strings.TrimSpace(in.SubscriptionID); id != "" {
		return p.readOrder(ctx, base+"/"+url.PathEscape(id))
	}
	rec, err := p.client.getXML(ctx, base, nil)
	p.note(err, nil)
	if err != nil {
		return Listing{}, err
	}
	items, note := collect(rec, "Subscriptions", "Subscription")
	return Listing{Items: items, Returned: len(items), Note: note}, nil
}
