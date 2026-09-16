package app

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/spoked/mcpd/internal/settings"
)

// tunnelAssignment is what one tunnel is for: the plugin it serves and the
// ChatGPT account whose credential it connects with.
//
// An empty Plugin is the aggregate tunnel, which serves everything its
// account's grant allows.
type tunnelAssignment struct {
	TunnelID string
	Plugin   string
	Account  string
}

// assignedTunnels reads every tunnel's assignment, in a stable order.
//
// Sorted by tunnel id rather than left in map order, because two tunnels
// serving one plugin is now ordinary and the order decides which is reported
// first in a log line an operator is comparing between restarts.
func (a *App) assignedTunnels(ctx context.Context) []tunnelAssignment {
	if a.settings == nil {
		return nil
	}
	rows := a.settings.WithPrefix(ctx, "tunnel.")

	out := make([]tunnelAssignment, 0, len(rows)/2)
	for key := range rows {
		id := settings.TunnelIDFromKey(key)
		if id == "" {
			continue
		}
		out = append(out, tunnelAssignment{
			TunnelID: id,
			Plugin:   a.settings.String(ctx, settings.TunnelPluginKey(id), ""),
			Account:  a.settings.String(ctx, settings.TunnelAccountKey(id), ""),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TunnelID < out[j].TunnelID })
	return out
}

// tunnelsMadeHere reports the tunnels this host created, by id, with the name
// each was created with.
//
// This is the whole of what mcpd will show, re-point or delete. An
// organisation's other tunnels are not read, so they cannot be offered by
// mistake: a tunnel another mcpd instance made in the same organisation
// carries the same description as one this host made, and treating that
// description as provenance let each instance manage the other's connectors.
func (a *App) tunnelsMadeHere(ctx context.Context) map[string]string {
	out := map[string]string{}
	if a.settings == nil {
		return out
	}
	for _, at := range a.assignedTunnels(ctx) {
		if !a.settings.Bool(ctx, settings.TunnelMadeHereKey(at.TunnelID), false) {
			continue
		}
		out[at.TunnelID] = a.settings.String(ctx, settings.TunnelNameKey(at.TunnelID), "")
	}
	return out
}

// adoptExistingTunnels records the tunnels this host already had assignments
// for as ones it made, once, on the first start after the upgrade.
//
// Before this, "made by mcpd" was a description string every build stamps, so
// there was nothing to carry forward but the assignments themselves -- and an
// assignment is this host's own record of a tunnel it was running, written by
// its own create. Read from settings rather than from OpenAI: asking the
// organisation which tunnels look like mcpd's is the question that produced
// the problem.
//
// Not doing this would drop every existing connector off the page and stop it
// on the first start after an update, which is a worse failure than the one
// being fixed.
func (a *App) adoptExistingTunnels(ctx context.Context) {
	if a.settings == nil {
		return
	}
	var changes []settings.Change
	var adopted []string
	for _, at := range a.assignedTunnels(ctx) {
		if at.Plugin == "" {
			// Nothing is using it, so there is nothing to carry forward.
			continue
		}
		key := settings.TunnelMadeHereKey(at.TunnelID)
		if a.settings.String(ctx, key, "\x00") != "\x00" {
			// Already recorded, either by a create or by a previous start.
			continue
		}
		changes = append(changes, settings.Change{Key: key, Value: "true"})
		adopted = append(adopted, at.TunnelID)
	}
	if len(changes) == 0 {
		return
	}
	sort.Strings(adopted)
	if err := a.settings.Apply(ctx, "system:config-import", changes); err != nil {
		a.log.ErrorContext(ctx, "could not record which tunnels this host made; "+
			"they will not be shown on the Tunnels page until they are made again",
			"error", err)
		return
	}
	a.log.InfoContext(ctx, "recorded the tunnels this host was already running as its own",
		"tunnels", len(adopted), "which", strings.Join(adopted, ","))
}

// migrateTunnelAssignments moves the plugin-keyed assignments to the
// tunnel-keyed ones, once, on the first start after the upgrade.
//
// The old keys are left where they are. They are ignored from then on, and
// leaving them means a rollback to the previous build still finds its
// assignments -- which matters because the alternative is every tunnel coming
// up unassigned on a host somebody has just downgraded to get out of trouble.
//
// Writing is skipped where a tunnel-keyed value already exists, so this is
// safe to run on every start and does not overwrite an operator's later edit.
func (a *App) migrateTunnelAssignments(ctx context.Context) {
	if a.settings == nil {
		return
	}
	rows := a.settings.WithPrefix(ctx, "tunnel.plugin.")

	var changes []settings.Change
	migrated := make([]string, 0, len(rows))
	for key := range rows {
		plugin := settings.PluginFromTunnelKey(key)
		if plugin == "" {
			continue
		}
		// The old key goes whether or not it is carried over: a value under
		// it is a second authority for the same tunnel, and one that nothing
		// reads any more. Leaving it was how a host came to hold two answers
		// for one plugin.
		changes = append(changes,
			settings.Change{Key: key, Delete: true},
			settings.Change{Key: settings.PluginTunnelAccountKey(plugin), Delete: true},
		)
		id := a.settings.String(ctx, key, "")
		if id == "" {
			continue
		}
		// Already moved, or assigned directly since. Either way this is not
		// ours to change.
		if a.settings.String(ctx, settings.TunnelPluginKey(id), "\x00") != "\x00" {
			continue
		}
		account := a.settings.String(ctx, settings.PluginTunnelAccountKey(plugin), "")

		encodedPlugin, err := json.Marshal(plugin)
		if err != nil {
			continue
		}
		encodedAccount, err := json.Marshal(account)
		if err != nil {
			continue
		}
		changes = append(changes,
			settings.Change{Key: settings.TunnelPluginKey(id), Value: string(encodedPlugin)},
			settings.Change{Key: settings.TunnelAccountKey(id), Value: string(encodedAccount)},
		)
		migrated = append(migrated, plugin)
	}

	// The aggregate tunnel had a pair of keys of its own, because "" as a
	// tunnel's plugin already meant "not used". It is a tunnel like the
	// others now, serving TunnelEverything under its own id.
	if id := a.settings.String(ctx, settings.KeyTunnelID, ""); id != "" {
		if a.settings.String(ctx, settings.TunnelPluginKey(id), "\x00") == "\x00" {
			account := a.settings.String(ctx, settings.KeyTunnelAccount, "")
			encodedPlugin, _ := json.Marshal(settings.TunnelEverything)
			encodedAccount, _ := json.Marshal(account)
			changes = append(changes,
				settings.Change{Key: settings.TunnelPluginKey(id), Value: string(encodedPlugin)},
				settings.Change{Key: settings.TunnelAccountKey(id), Value: string(encodedAccount)},
			)
			migrated = append(migrated, "everything")
		}
		changes = append(changes,
			settings.Change{Key: settings.KeyTunnelID, Delete: true},
			settings.Change{Key: settings.KeyTunnelAccount, Delete: true},
		)
	}
	if len(changes) == 0 {
		return
	}
	sort.Strings(migrated)

	// The same actor the configuration import uses, because this is the same
	// kind of event: a one-off move performed by the host rather than a change
	// somebody made, and settings_history should say so.
	if err := a.settings.Apply(ctx, "system:config-import", changes); err != nil {
		a.log.ErrorContext(ctx, "could not move the tunnel assignments to their new keys; "+
			"tunnels will come up unassigned until they are set again on the Tunnels page",
			"error", err)
		return
	}
	a.log.InfoContext(ctx, "moved tunnel assignments onto each tunnel's own key and "+
		"removed the old ones, so a tunnel has one authority for what it serves",
		"carried", len(migrated), "which", strings.Join(migrated, ","))
}
