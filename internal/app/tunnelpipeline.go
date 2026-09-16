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

	// The account's own saved workspaces. This used to union in the workspaces
	// of every tunnel in the organisation, which meant a workspace id was
	// learned from -- and written to this host's account row from -- tunnels
	// somebody else had made. What a create returns is enough to keep the list
	// filling itself in without reading anybody else's tunnel.
	workspaces := tunnel.NormalizeWorkspaces(acct.Workspaces)
	listed := len(workspaces) > 0

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
	//
	// The same write records that this host made it. That record is what makes
	// it this host's to manage: an organisation's other tunnels are never
	// listed, and one made by another mcpd instance is not this one's to
	// re-point or delete however similar its description looks.
	stored := req.Plugin
	if stored == "" {
		stored = settings.TunnelEverything
	}
	encodedPlugin, _ := json.Marshal(stored)
	encodedAccount, _ := json.Marshal(acct.ID)
	encodedName, _ := json.Marshal(made.Name)
	changes := []settings.Change{
		{Key: settings.TunnelPluginKey(made.ID), Value: string(encodedPlugin)},
		{Key: settings.TunnelAccountKey(made.ID), Value: string(encodedAccount)},
		{Key: settings.TunnelMadeHereKey(made.ID), Value: "true"},
		{Key: settings.TunnelNameKey(made.ID), Value: string(encodedName)},
		{Key: settings.KeyTunnelEnabled, Value: "true"},
	}
	if err := a.settings.Apply(ctx, actor, changes); err != nil {
		// The tunnel exists and nothing here points at it. Said plainly,
		// with the id, rather than left as a tunnel somebody finds later.
		return MakeTunnelResult{}, fmt.Errorf("tunnel: %s was made at OpenAI but could not be assigned here: %w", made.ID, err)
	}
	// After the create, from what it returned. OpenAI may list a tunnel in
	// workspaces beyond the ones asked for, and those are this account's own
	// by definition -- it just made a tunnel in them.
	a.recordWorkspaces(ctx, acct, made.WorkspaceIDs)
	a.reconnectTunnels(ctx, "a tunnel was made")

	return MakeTunnelResult{
		TunnelID:   made.ID,
		Name:       made.Name,
		Account:    acct.ID,
		Workspaces: append([]string{}, made.WorkspaceIDs...),
	}, nil
}

// createdByMCPD is the description this host puts on tunnels it makes.
//
// It is for a person reading OpenAI's console, and nothing more. It used to be
// the signal for "this host made it", which it cannot be: every mcpd build
// stamps the same string, so one instance treated another's tunnels as its
// own. What a tunnel here was created by is recorded locally, under
// settings.TunnelMadeHereKey.
const createdByMCPD = "Created by mcpd"

// recordWorkspaces adds the workspaces a tunnel was created in to its
// account, so the list fills itself in and nobody types a workspace id.
//
// Fed by what Create returned rather than by a listing. Reading the
// organisation's tunnels to harvest workspace ids meant this host learned --
// and stored -- workspaces from connectors other people and other mcpd
// instances had made, which is exactly the boundary this is not supposed to
// cross.
func (a *App) recordWorkspaces(ctx context.Context, acct tunnel.Account, found []string) {
	if a.chatgpt == nil {
		return
	}
	known := tunnel.NormalizeWorkspaces(acct.Workspaces)
	all := tunnel.NormalizeWorkspaces(append(append([]string{}, known...), found...))
	if len(all) == len(known) {
		return
	}
	if _, err := a.chatgpt.Update(ctx, "system:tunnel-reconcile", acct.ID,
		tunnel.AccountUpdate{Workspaces: &all}); err != nil {
		a.log.WarnContext(ctx, "could not record an account's workspaces", "account", acct.Name, "error", err)
		return
	}
	a.log.InfoContext(ctx, "recorded the workspaces a new tunnel was listed in",
		"account", acct.Name, "workspaces", strings.Join(all, ","))
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
	CanList bool `json:"can_list"`
	CanMake bool `json:"can_make"`
	// Workspaces are this account's own, not every workspace its
	// organisation's tunnels are listed in. The count of tunnels in the
	// organisation used to be reported here and is not this host's to say:
	// most of them are nothing to do with it.
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
	// The listing proves the key can read, and that is all it is used for: the
	// tunnels it returns are the organisation's, most of them nothing to do
	// with this host, and neither their count nor their workspaces are this
	// host's business.
	if _, err := dir.List(ctx); err != nil {
		out.Problem, out.Reason = err.Error(), tunnel.Reason(err)
		return out, nil
	}
	out.CanList = true
	out.Workspaces = tunnel.NormalizeWorkspaces(acct.Workspaces)

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
