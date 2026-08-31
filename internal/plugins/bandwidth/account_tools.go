package bandwidth

import (
	"context"

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
