package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/tunnel"
)

// MakeTunnelRequest is what a person has to say to get a connector: which
// system, and -- on a host with several accounts -- whose. Everything else
// the host works out.
type MakeTunnelRequest struct {
	// Plugin is the system the tunnel serves, or "" for everything the
	// account is granted.
	Plugin string
	// Account is the ChatGPT account it is made under. Empty means the only
	// one, where there is exactly one.
	Account string
	// Name is optional; the host names it after the system otherwise.
	Name string
}

// MakeTunnelResult is the tunnel, made, assigned and starting.
type MakeTunnelResult struct {
	TunnelID string `json:"id"`
	Name     string `json:"name"`
	Account  string `json:"account_id"`
	// Workspaces the tunnel was listed in: every workspace the account knows
	// of, so the connector appears wherever the account's others do.
	Workspaces []string `json:"workspace_ids"`
}

// ErrWhichAccount is a request that has to name an account and did not.
var ErrWhichAccount = errors.New("tunnel: this host has more than one ChatGPT account; say which")

// MakeTunnel is the whole pipeline: create the tunnel in the account's
// organisation, listed in every workspace the account knows, point it at the
// system, switch tunnels on, and start it. One call, so that a person never
// holds a half-made tunnel -- created but unassigned, assigned but off.
//
// The workspaces are not asked for. An account's own list and the workspaces
// its existing tunnels report are the same thing OpenAI would show a person
// looking, and a form asking for a value the host already holds is where a
// wrong organisation's workspace got typed in and refused.
func (a *App) MakeTunnel(ctx context.Context, actor string, req MakeTunnelRequest) (MakeTunnelResult, error) {
	accounts := a.chatgptAccounts(ctx)
	acct, ok := accountFor(accounts, req.Account)
	if !ok {
		if req.Account == "" && len(accounts) > 1 {
			return MakeTunnelResult{}, ErrWhichAccount
		}
		return MakeTunnelResult{}, fmt.Errorf("tunnel: no such ChatGPT account")
	}
	dir := a.chatgptDirectory(ctx, acct.ID)
	if !dir.Available() {
		return MakeTunnelResult{}, fmt.Errorf("tunnel: the ChatGPT account %s needs %s before it can make tunnels", acct.Name, dir.Missing())
	}
	if req.Plugin != "" && !hasName(a.manager.Names(), req.Plugin) {
		return MakeTunnelResult{}, fmt.Errorf("tunnel: there is no system called %q", req.Plugin)
	}

	// Learned before the create rather than after, so the tunnel is listed
	// where the account's others are from the moment it exists.
	workspaces, listed := a.learnWorkspaces(ctx, acct, dir)

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "mcpd"
		if req.Plugin != "" {
			name = "mcpd: " + req.Plugin
		}
	}
	made, err := dir.Create(ctx, name, createdByMCPD, workspaces)
	if err != nil {
		return MakeTunnelResult{}, a.explainCreate(ctx, dir, err, listed)
	}

	// Assigned and switched on in one write: a tunnel that exists at OpenAI
	// and is not pointed at anything here is an object doing nothing.
	stored := req.Plugin
	if stored == "" {
		stored = settings.TunnelEverything
	}
	encodedPlugin, _ := json.Marshal(stored)
	encodedAccount, _ := json.Marshal(acct.ID)
	changes := []settings.Change{
		{Key: settings.TunnelPluginKey(made.ID), Value: string(encodedPlugin)},
		{Key: settings.TunnelAccountKey(made.ID), Value: string(encodedAccount)},
		{Key: settings.KeyTunnelEnabled, Value: "true"},
	}
	if err := a.settings.Apply(ctx, actor, changes); err != nil {
		// The tunnel exists and nothing here points at it. Said plainly,
		// with the id, rather than left as a tunnel somebody finds later.
		return MakeTunnelResult{}, fmt.Errorf("tunnel: %s was made at OpenAI but could not be assigned here: %w", made.ID, err)
	}
	a.reconnectTunnels(ctx, "a tunnel was made")

	return MakeTunnelResult{
		TunnelID:   made.ID,
		Name:       made.Name,
		Account:    acct.ID,
		Workspaces: append([]string{}, made.WorkspaceIDs...),
	}, nil
}

// createdByMCPD marks a tunnel this host made, so a listing can tell its own
// from ones somebody made in OpenAI's dashboard.
const createdByMCPD = "Created by mcpd"

// learnWorkspaces returns every workspace an account is known to use: its
// own list, plus what its tunnels report. Anything new is written back to
// the account, so the list fills itself in and nobody types a workspace id.
// listed says whether the listing succeeded, which explainCreate uses.
func (a *App) learnWorkspaces(ctx context.Context, acct tunnel.Account, dir *tunnel.Directory) (workspaces []string, listed bool) {
	known := tunnel.NormalizeWorkspaces(acct.Workspaces)
	list, err := dir.List(ctx)
	if err != nil {
		return known, false
	}
	seen := append([]string{}, known...)
	for _, t := range list {
		seen = append(seen, t.WorkspaceIDs...)
	}
	all := tunnel.NormalizeWorkspaces(seen)
	if len(all) != len(known) && a.chatgpt != nil {
		if _, err := a.chatgpt.Update(ctx, "system:tunnel-reconcile", acct.ID,
			tunnel.AccountUpdate{Workspaces: &all}); err != nil {
			a.log.WarnContext(ctx, "could not record an account's workspaces", "account", acct.Name, "error", err)
		} else {
			a.log.InfoContext(ctx, "learned an account's workspaces from its tunnels",
				"account", acct.Name, "workspaces", strings.Join(all, ","))
		}
	}
	return all, true
}

// explainCreate turns OpenAI's refusal of a create into what to do. A 403
// says only that the key may not; the same key having just listed tunnels
// says it lacks the write scope specifically, and a key that could not list
// lacks them all.
func (a *App) explainCreate(ctx context.Context, dir *tunnel.Directory, err error, listed bool) error {
	if tunnel.Reason(err) != tunnel.ReasonTunnelsManageRequired {
		return err
	}
	if listed {
		return tunnel.Refused(tunnel.ReasonTunnelsManageRequired,
			"This account's admin key can list tunnels but OpenAI refused to make one, "+
				"so the key lacks the tunnel write scope (api.organization.tunnel.write). "+
				"Regenerate it with the tunnel scopes and paste it into the account.")
	}
	_ = ctx
	_ = dir
	return tunnel.Refused(tunnel.ReasonTunnelsManageRequired,
		"This account's admin key cannot list tunnels either, so it has no tunnel scopes at all.")
}

// AccountCheck is what a "Check" on an account found out, by doing: a
// listing proves the key can read, and a tunnel made and deleted at once
// proves it can write. Reported rather than inferred, because "has an admin
// key" was being shown as "can make tunnels" and the difference is exactly
// what somebody pressing Make needs to know first.
type AccountCheck struct {
	CanList    bool     `json:"can_list"`
	CanMake    bool     `json:"can_make"`
	Tunnels    int      `json:"tunnels"`
	Workspaces []string `json:"workspaces"`
	// Problem is OpenAI's refusal, in the words the dialog shows.
	Problem string `json:"problem,omitempty"`
	Reason  string `json:"reason,omitempty"`
	At      string `json:"checked_at"`
}

// CheckChatGPTAccount proves what an account's admin key can do.
func (a *App) CheckChatGPTAccount(ctx context.Context, id string) (AccountCheck, error) {
	acct, ok := accountFor(a.chatgptAccounts(ctx), id)
	if !ok {
		return AccountCheck{}, fmt.Errorf("no such ChatGPT account")
	}
	out := AccountCheck{At: time.Now().UTC().Format(time.RFC3339)}
	dir := a.chatgptDirectory(ctx, acct.ID)
	if !dir.Available() {
		out.Problem = "This account has " + dir.Missing() + " missing, so it can run tunnels pasted in but not list or make them."
		return out, nil
	}
	list, err := dir.List(ctx)
	if err != nil {
		out.Problem, out.Reason = err.Error(), tunnel.Reason(err)
		return out, nil
	}
	out.CanList = true
	out.Tunnels = len(list)
	out.Workspaces, _ = a.learnWorkspaces(ctx, acct, dir)

	// The write half: made and deleted inside one call, organisation-only so
	// it appears nowhere, named so that a leftover -- if the delete failed --
	// is obviously a probe.
	made, err := dir.Create(ctx, "mcpd check", "Made by mcpd to prove this key can make tunnels; deleted at once", nil)
	if err != nil {
		e := a.explainCreate(ctx, dir, err, true)
		out.Problem, out.Reason = e.Error(), tunnel.Reason(e)
		return out, nil
	}
	if err := dir.Delete(ctx, made.ID); err != nil {
		a.log.WarnContext(ctx, "the probe tunnel could not be deleted; remove it by hand",
			"account", acct.Name, "tunnel", made.ID, "error", err)
	}
	out.CanMake = true
	return out, nil
}

func hasName(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
