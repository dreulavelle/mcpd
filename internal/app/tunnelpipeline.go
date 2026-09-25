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

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "mcpd"
		if req.Plugin != "" {
			name = "mcpd: " + req.Plugin
		}
	}
	made, err := a.createForAccount(ctx, dir, acct, name, createdByMCPD, workspaces)
	if err != nil {
		err = a.explainCreate(ctx, dir, acct, err, workspaces)
		// Logged here, with the context, because nothing else records it: the
		// person is shown a correlation id, and until this line there was no
		// log entry for it to match.
		a.log.WarnContext(ctx, "OpenAI refused to make a tunnel",
			"account", acct.Name, "workspaces", strings.Join(workspaces, ","),
			"reason", tunnel.Reason(err), "upstream", tunnel.Upstream(err), "error", err)
		return MakeTunnelResult{}, err
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

// createForAccount makes a tunnel in an account's organisation, listed in its
// workspaces -- and, when OpenAI cannot verify that the two belong together,
// in the workspaces alone.
//
// OpenAI began refusing an organisation and a workspace named together when
// it could not verify the pairing (CodeAssociationUnverified), on an account
// whose earlier tunnels had been made with exactly that pair. The remedy it
// names is a Support review. A tunnel naming the workspace alone has no
// pairing to verify and is the shape OpenAI's own documentation creates, so
// that is tried before anybody is sent to Support. Make and the account's
// Check both come through here, so the Check answers for what Make will do.
func (a *App) createForAccount(ctx context.Context, dir *tunnel.Directory, acct tunnel.Account, name, description string, workspaces []string) (*tunnel.TunnelInfo, error) {
	made, err := dir.Create(ctx, name, description, workspaces)
	if err == nil || len(workspaces) == 0 || tunnel.Code(err) != tunnel.CodeAssociationUnverified {
		return made, err
	}
	alone, werr := dir.CreateInWorkspaces(ctx, name, description, workspaces)
	if werr != nil {
		a.log.WarnContext(ctx, "OpenAI refused the tunnel in the workspace alone as well",
			"account", acct.Name, "workspaces", strings.Join(workspaces, ","),
			"upstream", tunnel.Upstream(werr), "error", werr)
		// The first refusal, because it is the one that names the problem.
		return nil, err
	}
	a.log.InfoContext(ctx, "made the tunnel in the workspace alone: OpenAI could not verify the workspace belongs to the organisation",
		"account", acct.Name, "tunnel", alone.ID, "workspaces", strings.Join(workspaces, ","),
		"upstream", tunnel.Upstream(err))
	return alone, nil
}

// explainCreate turns OpenAI's refusal of a create into what to do.
//
// A 403 says only that the request may not be made, and a create that names a
// workspace can be refused for the workspace as well as for the key. The
// account's Check used to make its probe without one, so it passed on an
// account whose every real create was refused -- and the refusal then said the
// key lacked the write permission the Check had just proved it has. So when a
// create naming workspaces is refused, the same key makes the same tunnel
// without them: if that is allowed, the workspace is what OpenAI refused.
func (a *App) explainCreate(ctx context.Context, dir *tunnel.Directory, acct tunnel.Account, err error, workspaces []string) error {
	// OpenAI said what it refused, so there is nothing to probe for.
	if tunnel.Code(err) == tunnel.CodeAssociationUnverified {
		return tunnel.Refused(tunnel.ReasonWorkspaceRefused,
			"OpenAI could not verify that the workspace "+strings.Join(workspaces, ", ")+
				" belongs to this account's organisation, and wants its Support to review the "+
				"association before a tunnel is made there.", err)
	}
	if tunnel.Reason(err) != tunnel.ReasonTunnelsManageRequired {
		return err
	}
	keyRefused := tunnel.Refused(tunnel.ReasonTunnelsManageRequired,
		"OpenAI refused to make a tunnel with this account's admin key, so the key lacks "+
			"the tunnel write permission (api.organization.tunnel.write). "+
			"Regenerate it with the tunnel permissions and paste it into the account.", err)
	if len(workspaces) == 0 {
		return keyRefused
	}
	probe, perr := dir.Create(ctx, probeName, probeDescription, nil)
	if perr != nil {
		return keyRefused
	}
	a.deleteProbe(ctx, dir, acct, probe.ID)
	which := "the workspace "
	if len(workspaces) > 1 {
		which = "the workspaces "
	}
	return tunnel.Refused(tunnel.ReasonWorkspaceRefused,
		"This account's admin key can make tunnels, but OpenAI will not list one in "+
			which+strings.Join(workspaces, ", ")+" saved on the account.", err)
}

// probeName and probeDescription mark a tunnel made only to learn what a key
// may do, so one left behind by a failed delete is obviously a probe.
const (
	probeName        = "mcpd check"
	probeDescription = "Made by mcpd to prove this key can make tunnels; deleted at once"
)

// deleteProbe removes a probe tunnel. A failure is logged rather than
// returned: the answer the probe was made for is already known, and the
// leftover is named so somebody can remove it by hand.
func (a *App) deleteProbe(ctx context.Context, dir *tunnel.Directory, acct tunnel.Account, id string) {
	if err := dir.Delete(ctx, id); err != nil {
		a.log.WarnContext(ctx, "the probe tunnel could not be deleted; remove it by hand",
			"account", acct.Name, "tunnel", id, "error", err)
	}
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
	// Upstream is what OpenAI itself said, for Technical details.
	Upstream string `json:"upstream,omitempty"`
	At       string `json:"checked_at"`
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
		out.Problem, out.Reason, out.Upstream = err.Error(), tunnel.Reason(err), tunnel.Upstream(err)
		return out, nil
	}
	out.CanList = true
	workspaces := tunnel.NormalizeWorkspaces(acct.Workspaces)
	out.Workspaces = workspaces

	// The write half: made and deleted inside one call, and made exactly as
	// Make would make it -- in the account's own workspaces. The probe used to
	// be organisation-only, so it tested a request nobody sends: an account
	// whose workspace OpenAI refused passed its Check and then failed every
	// create.
	made, err := a.createForAccount(ctx, dir, acct, probeName, probeDescription, workspaces)
	if err != nil {
		e := a.explainCreate(ctx, dir, acct, err, workspaces)
		out.Problem, out.Reason, out.Upstream = e.Error(), tunnel.Reason(e), tunnel.Upstream(e)
		a.log.WarnContext(ctx, "an account's check could not make a tunnel",
			"account", acct.Name, "workspaces", strings.Join(workspaces, ","),
			"reason", out.Reason, "upstream", out.Upstream, "error", e)
		return out, nil
	}
	a.deleteProbe(ctx, dir, acct, made.ID)
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
