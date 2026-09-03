package app

import (
	"context"
	"fmt"
	"time"

	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// reportAccessNotes names, once, what the move to editable roles could not
// carry over unchanged, and clears the notes.
//
// The migration widened nobody silently: a group that used to take
// capabilities away no longer can, and a subject that used to reach only its
// own grant now also reaches its groups'. Each such case is a row the
// migration wrote, and this turns each into a warning an operator reads at
// startup -- the same place a stale configuration key is named, for the same
// reason. Best effort: a note that cannot be read is logged as that, and the
// host starts either way.
func (a *App) reportAccessNotes(ctx context.Context) {
	rows, err := a.db.Reader().QueryContext(ctx,
		`SELECT kind, subject, detail FROM access_notes ORDER BY id`)
	if err != nil {
		a.log.WarnContext(ctx, "could not read the access migration notes", "error", err)
		return
	}
	type note struct{ kind, subject, detail string }
	var notes []note
	for rows.Next() {
		var n note
		if err := rows.Scan(&n.kind, &n.subject, &n.detail); err != nil {
			rows.Close()
			a.log.WarnContext(ctx, "could not read an access migration note", "error", err)
			return
		}
		notes = append(notes, n)
	}
	rows.Close()
	if len(notes) == 0 {
		return
	}

	for _, n := range notes {
		var text string
		switch n.kind {
		case "ceiling_dropped":
			text = fmt.Sprintf("group %q used to restrict what its members may do (%s). "+
				"Groups no longer take anything away; each member now holds the rights of "+
				"their own role. Give those members a smaller role under Settings, Roles, "+
				"if that restriction was meant.", n.subject, n.detail)
		case "reach_widens":
			text = fmt.Sprintf("%s has grants of its own and belongs to group %q, which also "+
				"grants plugins. It used to reach only its own; it now reaches both. "+
				"Take it out of the group, or narrow the group, if that is not meant.",
				n.subject, n.detail)
		default:
			text = fmt.Sprintf("%s: %s (%s)", n.kind, n.subject, n.detail)
		}
		a.log.WarnContext(ctx, "access model changed", "detail", text)
	}

	if err := a.db.WriteTx(ctx, time.Now().UnixMilli(), func(tx *sqlite.UnitOfWork) error {
		return tx.Exec(`DELETE FROM access_notes`)
	}); err != nil {
		a.log.WarnContext(ctx, "could not clear the access migration notes", "error", err)
	}
}
