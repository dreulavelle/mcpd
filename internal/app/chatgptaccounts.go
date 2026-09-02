package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/tunnel"
)

// accountLimiters bounds how fast each ChatGPT account may call this host.
//
// One limiter per account, shared across every tunnel the account owns --
// which is the point. A workspace that has been given three connectors should
// not get three times the allowance simply by using all of them.
//
// The traffic runs inward, so this is not a quota owed to OpenAI. It exists so
// that one account's retry loop cannot become every other account's outage,
// and so it is off by default: a rate of zero builds no limiter at all.
type accountLimiters struct {
	mu    sync.Mutex
	byID  map[string]*rate.Limiter
	rates map[string]float64
}

func newAccountLimiters() *accountLimiters {
	return &accountLimiters{
		byID:  map[string]*rate.Limiter{},
		rates: map[string]float64{},
	}
}

// set reconciles the limiters with the accounts as they now are.
//
// A limiter is rebuilt only when its rate actually changed, because rebuilding
// resets the bucket: an operator editing an account's name would otherwise
// hand that account a fresh allowance, which is a way to bypass the limit by
// touching the form.
func (l *accountLimiters) set(accounts []tunnel.Account) {
	l.mu.Lock()
	defer l.mu.Unlock()

	seen := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		seen[a.ID] = true
		if a.RatePerSec <= 0 {
			delete(l.byID, a.ID)
			delete(l.rates, a.ID)
			continue
		}
		if existing, ok := l.rates[a.ID]; ok && existing == a.RatePerSec {
			continue
		}
		// Burst of one, the same choice the tool limiter makes: a burst
		// allowance lets the first few calls ignore the limit entirely, which
		// is exactly the shape a model retrying in a loop produces.
		l.byID[a.ID] = rate.NewLimiter(rate.Limit(a.RatePerSec), 1)
		l.rates[a.ID] = a.RatePerSec
	}
	for id := range l.byID {
		if !seen[id] {
			delete(l.byID, id)
			delete(l.rates, id)
		}
	}
}

// allow reports whether an account may make a call now.
//
// Refused promptly rather than queued, for the reason the tool limiter states:
// the caller is a model with a deadline, so a queued call arrives having spent
// the budget it needed to do the work, and every caller behind it holds a
// goroutine for as long as the queue is.
func (l *accountLimiters) allow(id string) error {
	l.mu.Lock()
	limiter, ok := l.byID[id]
	perSec := l.rates[id]
	l.mu.Unlock()
	if !ok || limiter == nil {
		return nil
	}
	if limiter.Allow() {
		return nil
	}
	return fmt.Errorf(
		"this ChatGPT account is limited to %g calls per second and has no turn "+
			"available; try again shortly. Nothing was called upstream", perSec)
}

// chatgptAccounts reads every account, or none when the store cannot.
//
// A host with no encryption key cannot decrypt a credential, and there is
// nothing useful to do about that here: the tunnels will not start, which is
// already reported on their own status. Returning an empty list keeps every
// caller from having to decide what a broken key means.
func (a *App) chatgptAccounts(ctx context.Context) []tunnel.Account {
	if a.chatgpt == nil {
		return nil
	}
	list, err := a.chatgpt.List(ctx)
	if err != nil {
		a.log.ErrorContext(ctx, "could not read the ChatGPT accounts", "error", err)
		return nil
	}
	a.limiters.set(list)
	return list
}

// accountFor resolves which account a tunnel connects with.
//
// The empty assignment resolves to the only account when there is exactly one.
// That is what a deployment which has never thought about accounts has, and
// making it name one would be asking it to restate a choice with no
// alternatives. With two or more, an unassigned tunnel is ambiguous rather
// than obvious, so it resolves to nothing and says so where it is used.
func accountFor(accounts []tunnel.Account, id string) (tunnel.Account, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		if len(accounts) == 1 {
			return accounts[0], true
		}
		return tunnel.Account{}, false
	}
	for _, a := range accounts {
		if a.ID == id {
			return a, true
		}
	}
	return tunnel.Account{}, false
}

// seedChatGPTAccount carries the single set of credentials into an account,
// once.
//
// The arrangement before accounts was one OpenAI key, one role and one plugin
// grant in `settings`, shared by every tunnel. Those keys are no longer read
// to run anything, so an upgrade that left them there would take a working
// deployment's connectors offline with nothing said. This is the one turn
// they get, and it follows the same rule the config import does: it runs only
// when there is nothing already, it never overwrites, and it is recorded.
//
// Guarded on the table being empty rather than on a marker setting. The
// question being asked is "does this host have any accounts", and the table is
// the authority for that -- a marker would be a second one, and would seed a
// second time the moment they disagreed.
func (a *App) seedChatGPTAccount(ctx context.Context) error {
	if a.chatgpt == nil {
		return nil
	}
	n, err := a.chatgpt.Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	key := a.settings.Secret(ctx, settings.KeyTunnelAPIKey, "")
	if strings.TrimSpace(key) == "" {
		// Nothing to carry. A new deployment adds its first account on the
		// ChatGPT page, which is the ordinary path.
		return nil
	}

	seeded := tunnel.Account{
		Name:     "Default",
		APIKey:   key,
		AdminKey: a.settings.Secret(ctx, settings.KeyTunnelAdminKey, ""),
		OrgID:    a.settings.String(ctx, settings.KeyTunnelOrgID, ""),
		// The identity this host's tunnels already act as, kept exactly. A new
		// account would get svc:chatgpt:default; using that here would rename
		// the principal in the middle of an audit trail that has been
		// recording the old one, and every entry before the upgrade would
		// refer to an identity nothing else mentions.
		Principal:  a.settings.String(ctx, settings.KeyTunnelPrincipal, "svc:chatgpt"),
		Role:       auth.Role(a.settings.FieldString(ctx, settings.KeyTunnelRole)),
		Plugins:    a.settings.Strings(ctx, settings.KeyTunnelPlugins, nil),
		RatePerSec: 0,
		Enabled:    true,
	}
	if !seeded.Role.Valid() {
		seeded.Role = auth.RoleUser
	}
	if len(seeded.Plugins) == 0 {
		seeded.Plugins = []string{auth.Wildcard}
	}

	created, err := a.chatgpt.Create(ctx, "system:account-seed", seeded)
	if err != nil {
		return fmt.Errorf("app: carry the existing ChatGPT credentials into an account: %w", err)
	}

	a.log.InfoContext(ctx,
		"the existing ChatGPT credentials were carried into an account; "+
			"they are edited on the ChatGPT page from now on",
		"account", created.Name, "account_id", created.ID,
		"principal", created.Principal)
	return nil
}

// pinTunnelsToTheOnlyAccount writes down the answer while there is only one.
//
// An unassigned tunnel already resolves to the only account, so nothing about
// what is running changes here. What changes is what happens next: with two
// accounts an unassigned tunnel is ambiguous and does not start, so somebody
// adding an account for a second workspace would take every connector they
// already had offline, having changed nothing about those connectors. The trap
// is that the breakage arrives with an action that had nothing to do with them.
//
// Written at startup rather than only after seeding, so a host that upgraded
// before this existed is repaired rather than left holding the trap. It is
// idempotent by construction: a tunnel that already names an account keeps it,
// and with none or several stored there is no obvious answer to write.
func (a *App) pinTunnelsToTheOnlyAccount(ctx context.Context) error {
	accounts := a.chatgptAccounts(ctx)
	if len(accounts) != 1 {
		return nil
	}
	encoded, err := json.Marshal(accounts[0].ID)
	if err != nil {
		return err
	}

	// The aggregate keeps its own pair; every other tunnel is keyed by its own
	// id, so a plugin served by two of them pins each separately rather than
	// the pair colliding on the plugin name.
	var pairs [][2]string
	for _, at := range a.assignedTunnels(ctx) {
		pairs = append(pairs, [2]string{
			settings.TunnelPluginKey(at.TunnelID),
			settings.TunnelAccountKey(at.TunnelID),
		})
	}

	var changes []settings.Change
	for _, pair := range pairs {
		if _, ok, _ := a.settings.Get(ctx, pair[0]); !ok {
			continue
		}
		if a.settings.String(ctx, pair[1], "") != "" {
			continue
		}
		changes = append(changes, settings.Change{Key: pair[1], Value: string(encoded)})
	}
	if len(changes) == 0 {
		return nil
	}
	return a.settings.Apply(ctx, "system:account-seed", changes)
}

// The dashboard's view of the accounts.
//
// Each of these does two things: the store write, and the reconnect that makes
// it real. Editing an account changes which credential a running tunnel
// authenticates with and what its calls are allowed to reach, so a write that
// only landed in the database would leave the dashboard describing a host that
// is still running the previous grant -- which is the failure the settings
// watcher already exists to prevent for everything else.

// ListChatGPTAccounts returns every account.
func (a *App) ListChatGPTAccounts(ctx context.Context) ([]tunnel.Account, error) {
	if a.chatgpt == nil {
		return nil, fmt.Errorf("ChatGPT accounts are unavailable")
	}
	return a.chatgpt.List(ctx)
}

// AddChatGPTAccount stores a new account and connects anything waiting on it.
func (a *App) AddChatGPTAccount(ctx context.Context, actor string, acct tunnel.Account) (tunnel.Account, error) {
	if a.chatgpt == nil {
		return tunnel.Account{}, fmt.Errorf("ChatGPT accounts are unavailable")
	}
	if err := a.proveAdminKey(ctx, acct.AdminKey, acct.OrgID); err != nil {
		return tunnel.Account{}, err
	}
	created, err := a.chatgpt.Create(ctx, actor, acct)
	if err != nil {
		return tunnel.Account{}, err
	}
	a.reconnectTunnels(ctx, "a ChatGPT account was added")
	return created, nil
}

// UpdateChatGPTAccount edits an account and reconnects with what it now says.
func (a *App) UpdateChatGPTAccount(ctx context.Context, actor, id string, up tunnel.AccountUpdate) (tunnel.Account, error) {
	if a.chatgpt == nil {
		return tunnel.Account{}, fmt.Errorf("ChatGPT accounts are unavailable")
	}
	// Proved against what the account will hold after the edit, since an
	// edit may change either half of the pair.
	if up.AdminKey != nil || up.OrgID != nil {
		if current, ok := accountFor(a.chatgptAccounts(ctx), id); ok {
			adminKey, orgID := current.AdminKey, current.OrgID
			if up.AdminKey != nil {
				adminKey = *up.AdminKey
			}
			if up.OrgID != nil {
				orgID = *up.OrgID
			}
			if err := a.proveAdminKey(ctx, adminKey, orgID); err != nil {
				return tunnel.Account{}, err
			}
		}
	}
	updated, err := a.chatgpt.Update(ctx, actor, id, up)
	if err != nil {
		return tunnel.Account{}, err
	}
	a.reconnectTunnels(ctx, "a ChatGPT account was changed")
	return updated, nil
}

// RemoveChatGPTAccount forgets an account and stops what was using it.
//
// The tunnels assigned to it are unassigned in the same act. Leaving the
// assignments behind would leave rows pointing at an account that no longer
// exists -- which reads, on the Tunnels page, exactly like a tunnel that is
// merely failing to connect.
func (a *App) RemoveChatGPTAccount(ctx context.Context, actor, id string) error {
	if a.chatgpt == nil {
		return fmt.Errorf("ChatGPT accounts are unavailable")
	}
	if err := a.chatgpt.Delete(ctx, actor, id); err != nil {
		return err
	}
	if err := a.unassignAccount(ctx, actor, id); err != nil {
		a.log.ErrorContext(ctx, "a ChatGPT account was removed but its tunnel "+
			"assignments could not be cleared", "account_id", id, "error", err)
	}
	a.reconnectTunnels(ctx, "a ChatGPT account was removed")
	return nil
}

// unassignAccount clears every tunnel assignment naming an account.
func (a *App) unassignAccount(ctx context.Context, actor, id string) error {
	var keys []string
	for _, at := range a.assignedTunnels(ctx) {
		keys = append(keys, settings.TunnelAccountKey(at.TunnelID))
	}

	var changes []settings.Change
	for _, key := range keys {
		if a.settings.String(ctx, key, "") == id {
			changes = append(changes, settings.Change{Key: key, Delete: true})
		}
	}
	if len(changes) == 0 {
		return nil
	}
	return a.settings.Apply(ctx, actor, changes)
}

// reconnectTunnels re-applies the tunnel configuration after an account moved.
//
// Best effort and never fatal, the same as the settings watcher: a tunnel that
// will not come up reports it on its own status, and the write that prompted
// this has already landed.
func (a *App) reconnectTunnels(ctx context.Context, why string) {
	if a.tunnels == nil || a.tunnelFactory == nil {
		return
	}
	if err := a.tunnels.Apply(ctx, a.tunnelConfigs(ctx), a.tunnelFactory); err != nil {
		a.log.WarnContext(ctx, "tunnels did not reconnect", "reason", why, "error", err)
	}
}

// chatgptDirectory manages tunnels in one account's organisation.
//
// Per account rather than per host, because a tunnel is created inside an
// organisation and two accounts are two organisations. A directory built from
// whichever admin key happened to be first would list one workspace's tunnels
// and offer to delete them from another's, which is a mistake that cannot be
// undone from here.
//
// An unknown or unset account with exactly one stored resolves to that one,
// matching accountFor: a deployment with a single account should not have to
// name it.
func (a *App) chatgptDirectory(ctx context.Context, accountID string) *tunnel.Directory {
	acct, ok := accountFor(a.chatgptAccounts(ctx), accountID)
	if !ok {
		return tunnel.NewDirectory("", "", "")
	}
	return tunnel.NewDirectory(acct.AdminKey, acct.OrgID,
		a.settings.String(ctx, settings.KeyTunnelControlPlane, ""))
}

// tunnelAccountAssignments reports which account each tunnel connects with, by
// tunnel id.
//
// Read from the settings rather than from the running tunnels, for the reason
// tunnelAssignments is: a tunnel that is configured and failing to start has
// an assignment, and deriving this from what is running cannot tell that apart
// from a tunnel nobody assigned.
func (a *App) tunnelAccountAssignments(ctx context.Context) map[string]string {
	out := map[string]string{}
	for _, at := range a.assignedTunnels(ctx) {
		if at.Account != "" {
			out[at.TunnelID] = at.Account
		}
	}
	return out
}

// proveAdminKey asks OpenAI whether an admin key and organisation go
// together before they are saved, so a key pasted from the wrong
// organisation fails at the form rather than at the first connector.
//
// Only a definitive refusal fails the save: a control plane that cannot be
// reached is not a reason to refuse to store what the operator typed, and is
// logged instead.
func (a *App) proveAdminKey(ctx context.Context, adminKey, orgID string) error {
	dir := tunnel.NewDirectory(adminKey, orgID,
		a.settings.String(ctx, settings.KeyTunnelControlPlane, ""))
	if !dir.Available() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := dir.List(ctx)
	if err == nil {
		return nil
	}
	if tunnel.Reason(err) != "" {
		return fmt.Errorf("chatgpt account: OpenAI refused that admin key for that "+
			"organisation: %w", err)
	}
	a.log.WarnContext(ctx, "could not prove a ChatGPT account's admin key against OpenAI; "+
		"saving it anyway", "error", err)
	return nil
}

// reconcileTunnelOwners writes the account that actually owns each tunnel.
//
// A tunnel is created inside one organisation and can only be run by that
// organisation's key, so which account a tunnel belongs to is a fact rather
// than a choice. The listings say whose it is; an assignment that names a
// different account is corrected here, recorded, and the tunnel reconnected
// under the key that can use it.
func (a *App) reconcileTunnelOwners(ctx context.Context) {
	if a.settings == nil {
		return
	}
	// A tunnel's record carries organisations as a list, so one tunnel may
	// be listed by several accounts and any of their keys may run it. Which
	// accounts may is a set; the assignment is wrong only when it names an
	// account outside it, and it is moved to the first inside it, in a
	// stable order, so two valid accounts never trade the tunnel back and
	// forth between runs.
	owners := map[string][]string{}
	for _, acct := range a.chatgptAccounts(ctx) {
		dir := a.chatgptDirectory(ctx, acct.ID)
		if !dir.Available() {
			continue
		}
		list, err := dir.List(ctx)
		if err != nil {
			a.log.DebugContext(ctx, "could not list an account's tunnels", "account", acct.Name, "error", err)
			continue
		}
		for _, t := range list {
			owners[t.ID] = append(owners[t.ID], acct.ID)
		}
		// The same listing says which workspaces the account uses; written
		// back so the account's list fills itself in.
		a.learnWorkspaces(ctx, acct, dir)
	}
	if len(owners) == 0 {
		return
	}

	var changes []settings.Change
	var moved []string
	for _, at := range a.assignedTunnels(ctx) {
		able := owners[at.TunnelID]
		if len(able) == 0 || slices.Contains(able, at.Account) {
			continue
		}
		sort.Strings(able)
		encoded, err := json.Marshal(able[0])
		if err != nil {
			continue
		}
		changes = append(changes, settings.Change{Key: settings.TunnelAccountKey(at.TunnelID), Value: string(encoded)})
		moved = append(moved, at.TunnelID)
	}
	if len(changes) == 0 {
		return
	}
	if err := a.settings.Apply(ctx, "system:tunnel-reconcile", changes); err != nil {
		a.log.ErrorContext(ctx, "could not move tunnels to the accounts that own them", "error", err)
		return
	}
	a.log.InfoContext(ctx, "moved tunnels to the accounts whose organisations own them",
		"tunnels", strings.Join(moved, ","))
	a.reconnectTunnels(ctx, "tunnels were moved to the accounts that own them")
}
