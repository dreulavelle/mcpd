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
	a.log.InfoContext(ctx, "moved tunnel assignments onto the tunnel's own key, so a "+
		"plugin can now be served to more than one ChatGPT account",
		"tunnels", len(migrated), "plugins", strings.Join(migrated, ","))
}
