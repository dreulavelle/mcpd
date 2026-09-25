package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/tunnel"
)

// OpenAI verifies that the organisation and the workspace named on a tunnel
// belong together, and refuses a create it cannot verify. There is no endpoint
// that lists which pairings it accepts, and the only remedy for one it refuses
// is a review by its Support. So the one way to know is to make a tunnel and
// see -- which saving an account, its Check and a Make all do -- and what they
// find is kept, so it is known before somebody is in the middle of making a
// connector rather than discovered there.

// probePairing asks OpenAI about one pairing by making a tunnel with it and
// deleting it at once. ws is "" for the organisation on its own.
func (a *App) probePairing(ctx context.Context, dir *tunnel.Directory, acct tunnel.Account, ws string) tunnel.Pairing {
	var named []string
	if ws != "" {
		named = []string{ws}
	}
	made, err := dir.Create(ctx, probeName, probeDescription, named)
	if err == nil {
		a.deleteProbe(ctx, dir, acct, made.ID)
		return tunnel.Pairing{Workspace: ws, Status: tunnel.PairingVerified, CheckedAt: time.Now().UTC()}
	}
	return pairingFrom(ws, err)
}

// pairingFrom reads OpenAI's refusal of a create naming one workspace, or none.
func pairingFrom(ws string, err error) tunnel.Pairing {
	p := tunnel.Pairing{Workspace: ws, CheckedAt: time.Now().UTC(), Upstream: tunnel.Upstream(err)}
	switch {
	case tunnel.Code(err) == tunnel.CodeAssociationUnverified:
		p.Status = tunnel.PairingUnverified
		e := unverifiedRefusal([]string{ws}, err)
		p.Problem, p.Reason = e.Error(), tunnel.Reason(e)
	case tunnel.Reason(err) == "":
		// Not an answer: OpenAI was not reached, or said something this does
		// not read as a refusal. Left without a status, so it is not kept.
		p.Problem = "OpenAI could not be asked about this just now. Try the Check again shortly."
		p.Upstream = err.Error()
	case ws == "":
		p.Status = tunnel.PairingRefused
		e := keyRefusal(err)
		p.Problem, p.Reason = e.Error(), tunnel.Reason(e)
	default:
		p.Status = tunnel.PairingRefused
		p.Reason = tunnel.ReasonWorkspaceRefused
		p.Problem = "This account's admin key can make tunnels, but OpenAI refused to make one in the workspace " + ws + "."
	}
	return p
}

// verifiedPairings is what a create that succeeded proved: the organisation,
// and every workspace it named.
func verifiedPairings(workspaces []string) []tunnel.Pairing {
	now := time.Now().UTC()
	out := []tunnel.Pairing{{Status: tunnel.PairingVerified, CheckedAt: now}}
	for _, ws := range workspaces {
		out = append(out, tunnel.Pairing{Workspace: ws, Status: tunnel.PairingVerified, CheckedAt: now})
	}
	return out
}

// keyRefusal says a create naming no workspace was refused, which is the key.
func keyRefusal(err error) error {
	return tunnel.Refused(tunnel.ReasonTunnelsManageRequired,
		"OpenAI refused to make a tunnel with this account's admin key, so the key lacks "+
			"the tunnel write permission (api.organization.tunnel.write). "+
			"Regenerate it with the tunnel permissions and paste it into the account.", err)
}

// unverifiedRefusal says OpenAI could not verify a workspace belongs to the
// organisation, and what can be done about it -- which is not a new key.
func unverifiedRefusal(workspaces []string, err error) error {
	return tunnel.Refused(tunnel.ReasonWorkspaceRefused,
		"OpenAI could not verify that the workspace "+strings.Join(workspaces, ", ")+
			" belongs to this account's organisation, and only OpenAI Support can review that. "+
			"Until they have, remove the workspace from the account to make tunnels without it.", err)
}

// recordPairings keeps what was found. Answers that are not answers -- OpenAI
// not reached -- are dropped rather than stored as a status nobody gave.
func (a *App) recordPairings(ctx context.Context, acct tunnel.Account, found []tunnel.Pairing) {
	if a.chatgpt == nil {
		return
	}
	var keep []tunnel.Pairing
	for _, p := range found {
		if p.Status != "" {
			keep = append(keep, p)
		}
	}
	if err := a.chatgpt.RecordPairings(ctx, acct.ID, acct.OrgID, keep); err != nil {
		a.log.WarnContext(ctx, "could not keep what OpenAI said about an account's workspaces",
			"account", acct.Name, "error", err)
	}
}

// ChatGPTPairings returns what OpenAI last said about every account's
// pairings, by account id.
func (a *App) ChatGPTPairings(ctx context.Context) map[string][]tunnel.Pairing {
	if a.chatgpt == nil {
		return nil
	}
	all, err := a.chatgpt.Pairings(ctx)
	if err != nil {
		a.log.WarnContext(ctx, "could not read what OpenAI said about the accounts' workspaces", "error", err)
		return nil
	}
	return all
}

// knownUnverified refuses a Make naming a workspace OpenAI has already said
// it cannot verify, with what it said then.
//
// Asking again would only get the same answer: the remedy is a review by
// OpenAI Support, and a Check is how somebody says it has happened -- it asks
// afresh and replaces what is kept.
func (a *App) knownUnverified(ctx context.Context, acct tunnel.Account, workspaces []string) error {
	for _, p := range a.ChatGPTPairings(ctx)[acct.ID] {
		if p.Status != tunnel.PairingUnverified || !slices.Contains(workspaces, p.Workspace) {
			continue
		}
		return tunnel.Recalled(tunnel.ReasonWorkspaceRefused,
			"OpenAI said on "+p.CheckedAt.Format("2 January")+" that it could not verify that the workspace "+
				p.Workspace+" belongs to this account's organisation. Once OpenAI Support has reviewed it, "+
				"press Check on the account; until then, remove the workspace from the account to make "+
				"tunnels without it.", p.Upstream)
	}
	return nil
}

// provePairings asks OpenAI about the workspaces an account is being saved
// with, before it is saved, so a workspace OpenAI will not accept is refused
// at the form rather than at the first Make.
//
// Only a definitive answer about a workspace fails the save, the rule
// proveAdminKey keeps: OpenAI unreachable, or a key that cannot make tunnels
// at all, is not a reason to refuse what the operator typed -- the second is
// the key's problem, and the account's Check says so.
func (a *App) provePairings(ctx context.Context, acct tunnel.Account, workspaces []string) ([]tunnel.Pairing, error) {
	dir := tunnel.NewDirectory(acct.AdminKey, acct.OrgID,
		a.settings.String(ctx, settings.KeyTunnelControlPlane, ""))
	if !dir.Available() || len(workspaces) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	org := a.probePairing(ctx, dir, acct, "")
	if org.Status != tunnel.PairingVerified {
		a.log.WarnContext(ctx, "could not prove a ChatGPT account's workspaces: the organisation "+
			"alone was not accepted; saving it anyway", "account", acct.Name,
			"problem", org.Problem, "upstream", org.Upstream)
		return []tunnel.Pairing{org}, nil
	}
	found := []tunnel.Pairing{org}
	var refused []tunnel.Pairing
	for _, ws := range workspaces {
		p := a.probePairing(ctx, dir, acct, ws)
		found = append(found, p)
		if p.Status == tunnel.PairingUnverified || p.Status == tunnel.PairingRefused {
			refused = append(refused, p)
		}
	}
	if len(refused) == 0 {
		return found, nil
	}
	a.log.WarnContext(ctx, "OpenAI refused a workspace a ChatGPT account was being saved with",
		"account", acct.Name, "workspace", refused[0].Workspace, "upstream", refused[0].Upstream)
	var ids []string
	for _, p := range refused {
		ids = append(ids, p.Workspace)
	}
	which := "the workspace "
	if len(ids) > 1 {
		which = "the workspaces "
	}
	msg := "OpenAI would not make a tunnel in " + which + strings.Join(ids, ", ") +
		" with this organisation and admin key, so the account was not saved. "
	if refused[0].Status == tunnel.PairingUnverified {
		msg += "It could not verify the workspace belongs to the organisation, and only OpenAI " +
			"Support can review that. Save the account without it for now, or with an admin key " +
			"from the organisation the workspace belongs to."
	} else {
		msg += "Check the workspace id, or save the account without it."
	}
	return found, tunnel.Recalled(tunnel.ReasonWorkspaceRefused, msg, refused[0].Upstream)
}

// workspacesToProve is which workspaces a save has to ask OpenAI about: all
// of them when the organisation or the admin key changed, since every answer
// was about the old pair, and otherwise only the ones being added.
func workspacesToProve(current tunnel.Account, up tunnel.AccountUpdate, next []string) []string {
	if up.AdminKey != nil && *up.AdminKey != current.AdminKey ||
		up.OrgID != nil && *up.OrgID != current.OrgID {
		return next
	}
	if up.Workspaces == nil {
		return nil
	}
	had := tunnel.NormalizeWorkspaces(current.Workspaces)
	var added []string
	for _, ws := range next {
		if !slices.Contains(had, ws) {
			added = append(added, ws)
		}
	}
	return added
}

// tunnelWorkspaces reports the workspaces each tunnel this host made was
// listed in, by tunnel id. A tunnel made before this was recorded is absent,
// which is "not known" rather than "none".
func (a *App) tunnelWorkspaces(ctx context.Context) map[string][]string {
	out := map[string][]string{}
	for id := range a.tunnelsMadeHere(ctx) {
		var ws []string
		if ok, err := a.settings.GetJSON(ctx, settings.TunnelWorkspacesKey(id), &ws); err != nil || !ok {
			continue
		}
		out[id] = tunnel.NormalizeWorkspaces(ws)
	}
	return out
}

// WorkspaceCandidate is a workspace an account could use, for the picker on
// the account form.
type WorkspaceCandidate struct {
	ID string `json:"workspace_id"`
	// Tunnels is how many of the organisation's tunnels are listed in it.
	Tunnels int  `json:"tunnels"`
	Saved   bool `json:"saved"`
	Default bool `json:"default"`
	// Pairing is what OpenAI last said about it with this organisation, when
	// anything has asked.
	Pairing *tunnel.Pairing `json:"pairing,omitempty"`
}

// WorkspaceCandidates lists the workspaces an account's organisation already
// uses -- the workspaces its tunnels are listed in -- beside the account's own,
// so a person picks one rather than finding and typing an id.
//
// The organisation's listing is read for this and nothing else, and nothing
// from it is stored: it includes tunnels other people and other mcpd hosts
// made, which are not this host's to manage, but the workspaces they sit in
// are exactly where somebody would look for the id. A workspace becomes the
// account's only when a person picks it and the save verifies it with OpenAI.
// OpenAI publishes no endpoint that lists workspaces, so this is the only
// place one can be read rather than typed.
func (a *App) WorkspaceCandidates(ctx context.Context, accountID string) ([]WorkspaceCandidate, error) {
	acct, ok := accountFor(a.chatgptAccounts(ctx), accountID)
	if !ok || accountID == "" {
		return nil, fmt.Errorf("no such ChatGPT account")
	}
	byID := map[string]*WorkspaceCandidate{}
	get := func(id string) *WorkspaceCandidate {
		if c, ok := byID[id]; ok {
			return c
		}
		c := &WorkspaceCandidate{ID: id}
		byID[id] = c
		return c
	}
	for _, ws := range tunnel.NormalizeWorkspaces(acct.Workspaces) {
		c := get(ws)
		c.Saved = true
		c.Default = ws == acct.DefaultWorkspace
	}
	dir := a.chatgptDirectory(ctx, acct.ID)
	if dir.Available() {
		listed, err := dir.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range listed {
			for _, ws := range tunnel.NormalizeWorkspaces(t.WorkspaceIDs) {
				get(ws).Tunnels++
			}
		}
	}
	for _, p := range a.ChatGPTPairings(ctx)[acct.ID] {
		if c, ok := byID[p.Workspace]; ok && p.Workspace != "" {
			pairing := p
			c.Pairing = &pairing
		}
	}
	out := make([]WorkspaceCandidate, 0, len(byID))
	for _, c := range byID {
		out = append(out, *c)
	}
	// Saved first, then the most used: the one somebody wants is almost always
	// the workspace the organisation's other connectors already sit in.
	slices.SortFunc(out, func(x, y WorkspaceCandidate) int {
		switch {
		case x.Saved != y.Saved:
			if x.Saved {
				return -1
			}
			return 1
		case x.Tunnels != y.Tunnels:
			return y.Tunnels - x.Tunnels
		}
		return strings.Compare(x.ID, y.ID)
	})
	return out, nil
}
