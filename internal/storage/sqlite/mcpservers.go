package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/operations"
)

// MCPServerStore holds imported remote MCP servers and the tools each was last
// seen to offer.
type MCPServerStore struct {
	db  *DB
	now func() time.Time
}

// NewMCPServerStore returns a store backed by db.
func NewMCPServerStore(db *DB, now func() time.Time) *MCPServerStore {
	if now == nil {
		now = time.Now
	}
	return &MCPServerStore{db: db, now: now}
}

// auditServer appends one admin action against a remote MCP server to the
// operations audit trail, inside the transaction that performed it.
//
// The trail, not settings_history and not a third table. These are not
// settings -- enabling a tool grants every caller of a plugin a path into a
// third party's code, which is a privilege grant and belongs where privilege
// grants are recorded. The chain is hash-linked and append-only, it already
// carries entries with no operation of their own (a prune writes one), and
// nothing about it is weakened by an entry whose operation_id is null: the
// column is nullable, the link is over the whole row, and the triggers are
// untouched.
//
// The plugin column carries the server's instance name, which is the name it
// is mounted under and the name a grant lists, so an operator reading the
// trail for one integration sees the decisions that shaped it beside the
// operations that used it.
func auditServer(u *UnitOfWork, kind, actor, server, action string, detail map[string]any) error {
	body, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("sqlite: encode audit detail for %s: %w", kind, err)
	}
	return u.appendAudit(operations.AuditEntry{
		EventID: newEventID(),
		Kind:    kind,
		Plugin:  server,
		Action:  action,
		Actor:   actor,
		Detail:  body,
	})
}

// ErrServerExists reports an import onto a name already taken.
var ErrServerExists = errors.New("sqlite: a remote MCP server with that name already exists")

// ErrNoSuchServer reports an operation against a server that is not imported.
var ErrNoSuchServer = errors.New("sqlite: no such remote MCP server")

// Import records a new server. The insert is guarded by the primary key
// rather than by a prior read, so two operators racing the same name produce
// one server and one refusal.
func (s *MCPServerStore) Import(ctx context.Context, actor, name string, doc []byte, schemaVersion, transport, endpoint string) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(u *UnitOfWork) error {
		res, err := u.exec(`
			INSERT INTO mcp_servers
				(name, document, schema_version, transport, url, enabled, created_at, updated_at)
			VALUES (?,?,?,?,?,1,?,?)
			ON CONFLICT (name) DO NOTHING`,
			name, string(doc), schemaVersion, transport, endpoint, now, now)
		if err != nil {
			return fmt.Errorf("sqlite: import mcp server: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrServerExists
		}
		return auditServer(u, "mcpserver.imported", actor, name, "import", map[string]any{
			"transport":      transport,
			"endpoint":       endpoint,
			"schema_version": schemaVersion,
		})
	})
}

// Remove forgets a server and its tool snapshot. The snapshot goes with it by
// foreign key, so a name reused later cannot inherit someone else's approvals.
func (s *MCPServerStore) Remove(ctx context.Context, actor, name string) error {
	return s.db.WriteTx(ctx, s.now().UnixMilli(), func(u *UnitOfWork) error {
		// Counted before the delete, because the cascade takes the rows with
		// it and an audit entry saying how much access was withdrawn is worth
		// more than one saying a row went away.
		var enabled, stored int
		if err := u.queryRow(`
			SELECT COUNT(*), COALESCE(SUM(state = 'enabled'), 0)
			  FROM mcp_server_tools WHERE server_name = ?`,
			name).Scan(&stored, &enabled); err != nil {
			return fmt.Errorf("sqlite: count tools before removal: %w", err)
		}

		res, err := u.exec(`DELETE FROM mcp_servers WHERE name = ?`, name)
		if err != nil {
			return fmt.Errorf("sqlite: remove mcp server: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoSuchServer
		}
		return auditServer(u, "mcpserver.removed", actor, name, "remove", map[string]any{
			"tools_stored":  stored,
			"tools_enabled": enabled,
		})
	})
}

// SetEnabled turns a server on or off.
func (s *MCPServerStore) SetEnabled(ctx context.Context, actor, name string, enabled bool) error {
	now := s.now().UnixMilli()
	return s.db.WriteTx(ctx, now, func(u *UnitOfWork) error {
		res, err := u.exec(`
			UPDATE mcp_servers SET enabled = ?, updated_at = ?
			 WHERE name = ? AND enabled <> ?`,
			boolToInt(enabled), now, name, boolToInt(enabled))
		if err != nil {
			return fmt.Errorf("sqlite: enable mcp server: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Either it is gone or it was already in that state. The second is
			// not a failure, so distinguish them rather than guessing.
			var exists int
			if err := u.queryRow(`SELECT COUNT(*) FROM mcp_servers WHERE name = ?`,
				name).Scan(&exists); err != nil {
				return err
			}
			if exists == 0 {
				return ErrNoSuchServer
			}
			// Already in that state. Nothing changed, so nothing is recorded:
			// a trail that logs non-events is one nobody reads carefully.
			return nil
		}
		kind := "mcpserver.disabled"
		if enabled {
			kind = "mcpserver.enabled"
		}
		return auditServer(u, kind, actor, name, "set_enabled",
			map[string]any{"enabled": enabled})
	})
}

// List returns every imported server, by name.
func (s *MCPServerStore) List(ctx context.Context) ([]mcpservers.Server, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT name, document, schema_version, transport, url, enabled, created_at, updated_at
		  FROM mcp_servers
		 ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list mcp servers: %w", err)
	}
	defer rows.Close()

	out := []mcpservers.Server{}
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// Get returns one server.
func (s *MCPServerStore) Get(ctx context.Context, name string) (mcpservers.Server, bool, error) {
	row := s.db.Reader().QueryRowContext(ctx, `
		SELECT name, document, schema_version, transport, url, enabled, created_at, updated_at
		  FROM mcp_servers WHERE name = ?`, name)
	srv, err := scanServer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return mcpservers.Server{}, false, nil
	}
	if err != nil {
		return mcpservers.Server{}, false, err
	}
	return srv, true, nil
}

// scanner is what a *sql.Row and a *sql.Rows have in common.
type scanner interface{ Scan(dest ...any) error }

func scanServer(row scanner) (mcpservers.Server, error) {
	var (
		srv                  mcpservers.Server
		doc                  string
		enabled              int
		createdAt, updatedAt int64
	)
	if err := row.Scan(&srv.Name, &doc, &srv.SchemaVersion, &srv.Transport, &srv.URL,
		&enabled, &createdAt, &updatedAt); err != nil {
		return mcpservers.Server{}, err
	}
	srv.Document = json.RawMessage(doc)
	srv.Enabled = enabled == 1
	srv.CreatedAt = time.UnixMilli(createdAt)
	srv.UpdatedAt = time.UnixMilli(updatedAt)

	// A document this build can no longer read is reported as an unparsed
	// server rather than an error: the row still has to be listable and
	// removable, which is the only thing an operator can usefully do with it.
	if parsed, err := mcpservers.Parse(srv.Document); err == nil {
		srv.Parsed = parsed
	}
	return srv, nil
}

// Tools returns a server's whole snapshot, in every state.
func (s *MCPServerStore) Tools(ctx context.Context, server string) ([]mcpservers.Tool, error) {
	return s.queryTools(ctx, `
		SELECT tool_name, descriptor, descriptor_hash, state, COALESCE(problem,''),
		       first_seen_at, last_seen_at
		  FROM mcp_server_tools
		 WHERE server_name = ?
		 ORDER BY tool_name`, server)
}

// EnabledTools returns only what should be mounted.
//
// This is the query Register runs, and the reason the snapshot exists: the
// tools a remote server offers are read from here at boot, never from the
// network, so a server that is down costs an unhealthy indicator rather than a
// host that comes up with no tools at all.
func (s *MCPServerStore) EnabledTools(ctx context.Context, server string) ([]mcpservers.Tool, error) {
	return s.queryTools(ctx, `
		SELECT tool_name, descriptor, descriptor_hash, state, COALESCE(problem,''),
		       first_seen_at, last_seen_at
		  FROM mcp_server_tools
		 WHERE server_name = ? AND state = 'enabled'
		 ORDER BY tool_name`, server)
}

func (s *MCPServerStore) queryTools(ctx context.Context, query string, args ...any) ([]mcpservers.Tool, error) {
	rows, err := s.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read mcp server tools: %w", err)
	}
	defer rows.Close()

	out := []mcpservers.Tool{}
	for rows.Next() {
		var (
			t                   mcpservers.Tool
			descriptor, state   string
			firstSeen, lastSeen int64
		)
		if err := rows.Scan(&t.Name, &descriptor, &t.Hash, &state, &t.Problem,
			&firstSeen, &lastSeen); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(descriptor), &t.Descriptor); err != nil {
			return nil, fmt.Errorf("sqlite: tool %q has an undecodable descriptor: %w", t.Name, err)
		}
		t.State = mcpservers.ToolState(state)
		t.FirstSeenAt = time.UnixMilli(firstSeen)
		t.LastSeenAt = time.UnixMilli(lastSeen)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Snapshot records what a discovery found, and reports what changed.
//
// The whole thing is one transaction: a discovery that half-applied would
// leave a server offering some of its old tools and some of its new ones, with
// nothing saying which.
//
// Three rules, and each is in the WHERE clause rather than in Go:
//
//   - A tool nobody has seen before arrives pending. It is not mounted, so a
//     server that grows a tool overnight cannot put it in front of a model.
//   - A tool whose descriptor changed loses an enabled classification and
//     returns to pending. What was approved was a descriptor; a different one
//     has not been approved. A disabled tool stays disabled -- re-offering it
//     with a new schema is not a way to undo a refusal.
//   - A tool no longer offered goes disabled and stays in the table, so its
//     return is recognised as a return rather than as something new.
func (s *MCPServerStore) Snapshot(ctx context.Context, actor, server string, seen []mcpservers.Tool) (mcpservers.Diff, error) {
	var diff mcpservers.Diff
	now := s.now().UnixMilli()

	err := s.db.WriteTx(ctx, now, func(u *UnitOfWork) error {
		// One discovery, one sequence number, claimed by the same statement
		// that proves the server exists. Every tool this discovery saw is
		// stamped with it, so "the server no longer offers this" becomes a
		// comparison the WHERE clause can make -- rather than a set difference
		// computed in Go and then trusted.
		claimed, err := u.ExecAffected(`
			UPDATE mcp_servers SET discovery_seq = discovery_seq + 1, updated_at = ?
			 WHERE name = ?`, now, server)
		if err != nil {
			return err
		}
		if claimed == 0 {
			return ErrNoSuchServer
		}
		var seq int64
		if err := u.queryRow(`SELECT discovery_seq FROM mcp_servers WHERE name = ?`,
			server).Scan(&seq); err != nil {
			return err
		}

		for _, t := range seen {
			descriptor, err := json.Marshal(t.Descriptor)
			if err != nil {
				return err
			}
			problem := nullIfBlank(t.Problem)

			inserted, err := u.ExecAffected(`
				INSERT INTO mcp_server_tools
					(server_name, tool_name, descriptor, descriptor_hash, state,
					 problem, first_seen_at, last_seen_at, last_seen_seq)
				VALUES (?,?,?,?,'pending',?,?,?,?)
				ON CONFLICT (server_name, tool_name) DO NOTHING`,
				server, t.Name, string(descriptor), t.Hash, problem, now, now, seq)
			if err != nil {
				return err
			}
			if inserted == 1 {
				diff.Added = append(diff.Added, t.Name)
				continue
			}

			changed, err := u.ExecAffected(`
				UPDATE mcp_server_tools
				   SET descriptor = ?, descriptor_hash = ?, problem = ?,
				       last_seen_at = ?, last_seen_seq = ?,
				       state = CASE WHEN state = 'enabled' THEN 'pending' ELSE state END
				 WHERE server_name = ? AND tool_name = ? AND descriptor_hash <> ?`,
				string(descriptor), t.Hash, problem, now, seq, server, t.Name, t.Hash)
			if err != nil {
				return err
			}
			if changed == 1 {
				diff.Changed = append(diff.Changed, t.Name)
			} else {
				if _, err := u.ExecAffected(`
					UPDATE mcp_server_tools
					   SET last_seen_at = ?, last_seen_seq = ?, problem = ?
					 WHERE server_name = ? AND tool_name = ?`,
					now, seq, problem, server, t.Name); err != nil {
					return err
				}
				diff.Unchanged = append(diff.Unchanged, t.Name)
			}
		}

		// A tool that cannot be mounted must not stay enabled, whether the
		// reason arrived with this discovery or with a previous one.
		if _, err := u.ExecAffected(`
			UPDATE mcp_server_tools SET state = 'disabled'
			 WHERE server_name = ? AND problem IS NOT NULL AND state <> 'disabled'`,
			server); err != nil {
			return err
		}

		withdrawn, err := s.disappeared(u, server, seq)
		if err != nil {
			return err
		}
		diff.Removed = withdrawn

		// A tool nobody ever classified, which the server no longer offers, is
		// not a record of anything. Keeping it was the wrong reading of "keep
		// withdrawn tools": what is worth keeping is a decision -- an
		// administrator's yes or no -- and a tool that came and went before
		// anyone looked at it carries neither.
		//
		// Without this, a server rotating unique tool names on every discovery
		// grows this table without limit, until reading it materialises the
		// whole history and removing the server holds the single writer while
		// it cascades through all of it.
		//
		// The condition is in the WHERE rather than in Go: every tool seen in
		// this discovery was just stamped with this transaction's timestamp,
		// so an older one is exactly a tool that was not.
		if _, err := u.ExecAffected(`
			DELETE FROM mcp_server_tools
			 WHERE server_name = ? AND ever_classified = 0 AND last_seen_seq < ?`,
			server, seq); err != nil {
			return err
		}

		// Second line, for a server that rotates names faster than discoveries
		// prune them, or finds some other way to grow the table.
		var stored int
		if err := u.queryRow(`SELECT COUNT(*) FROM mcp_server_tools WHERE server_name = ?`,
			server).Scan(&stored); err != nil {
			return err
		}
		if stored > maxStoredTools {
			return fmt.Errorf("%w: %s now has %d stored tools, past the %d this "+
				"host keeps for one server; remove tools you have refused, or "+
				"remove the server", ErrTooManyTools, server, stored, maxStoredTools)
		}

		// Sorted here rather than only on the way out, so the audit entry and
		// the returned diff say the same thing in the same order.
		for _, list := range [][]string{diff.Added, diff.Changed, diff.Removed, diff.Unchanged} {
			sort.Strings(list)
		}

		// Recorded even when nothing changed. A discovery that found the
		// catalogue unaltered is the evidence that it was unaltered, and it is
		// also the moment an approval can be silently invalidated -- a changed
		// descriptor sends an enabled tool back to pending, which withdraws
		// access nobody asked to withdraw.
		return auditServer(u, "mcpserver.discovered", actor, server, "discover",
			map[string]any{
				"offered":   len(seen),
				"sequence":  seq,
				"added":     diff.Added,
				"changed":   diff.Changed,
				"removed":   diff.Removed,
				"unchanged": len(diff.Unchanged),
			})
	})
	if err != nil {
		return mcpservers.Diff{}, err
	}
	return diff, nil
}

// disappeared disables everything the server no longer offers, and names it.
//
// A tool is withdrawn exactly when this discovery did not stamp its sequence
// onto it, which is a condition the WHERE clause carries rather than a set
// difference assembled in Go and then trusted.
//
// A row that was already disabled is not reported. It was not being served, so
// the server dropping it is not news -- and the operator's own refusal, which
// is the reason most of them are disabled, has not changed.
//
// Rows are kept rather than deleted here. A grant is per plugin, so nothing
// widens or narrows access -- but the fact that a tool was once approved, and
// when it was first seen, is exactly what an operator needs when it comes
// back. What is not kept is a tool nobody ever classified; that is pruned by
// the caller, because there is no decision in it to preserve.
func (s *MCPServerStore) disappeared(u *UnitOfWork, server string, seq int64) ([]string, error) {
	rows, err := u.tx.QueryContext(u.ctx, `
		SELECT tool_name FROM mcp_server_tools
		 WHERE server_name = ? AND state <> 'disabled' AND last_seen_seq < ?
		 ORDER BY tool_name`, server, seq)
	if err != nil {
		return nil, err
	}
	var gone []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		gone = append(gone, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if _, err := u.ExecAffected(`
		UPDATE mcp_server_tools SET state = 'disabled'
		 WHERE server_name = ? AND state <> 'disabled' AND last_seen_seq < ?`,
		server, seq); err != nil {
		return nil, err
	}
	return gone, nil
}

// maxStoredTools bounds how much of one server's history this host keeps.
//
// Reached only by a server that is misbehaving: an ordinary one's rows are
// bounded by its catalogue, and the prune in Snapshot clears the rest.
const maxStoredTools = 5000

// ErrTooManyTools reports a server whose stored tools are past the cap.
var ErrTooManyTools = errors.New("sqlite: too many stored tools for one server")

// ErrToolClassification reports a classification that did not apply.
var ErrToolClassification = errors.New("sqlite: the tool could not be classified")

// ClassifyTool records an administrator's decision about one tool.
//
// The descriptor hash is part of the guard, not a courtesy check before it. An
// administrator reads a description and a schema and says yes to those; if a
// discovery replaced them a moment earlier, the statement matches zero rows
// and the caller is told the decision was about something else.
//
// Enabling additionally requires that the tool has no recorded problem, so a
// tool this host cannot mount cannot be marked mounted.
func (s *MCPServerStore) ClassifyTool(ctx context.Context, actor, server, tool, hash string, state mcpservers.ToolState) error {
	if !state.Valid() {
		return fmt.Errorf("sqlite: %q is not a tool state", state)
	}
	return s.db.WriteTx(ctx, s.now().UnixMilli(), func(u *UnitOfWork) error {
		// Read inside the same transaction as the update, so the recorded
		// "from" is the state the guarded statement was applied to. It is
		// read rather than guarded on: the descriptor hash is the guard, and
		// adding the previous state to the WHERE clause would refuse a
		// harmless re-classification of something already in that state.
		var previous string
		if err := u.queryRow(`
			SELECT state FROM mcp_server_tools WHERE server_name = ? AND tool_name = ?`,
			server, tool).Scan(&previous); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("sqlite: read tool state before classifying: %w", err)
		}

		n, err := u.ExecAffected(`
			UPDATE mcp_server_tools
			   SET state = ?, ever_classified = 1
			 WHERE server_name = ? AND tool_name = ? AND descriptor_hash = ?
			   AND (? <> 'enabled' OR problem IS NULL)`,
			string(state), server, tool, hash, string(state))
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrToolClassification
		}
		// Enabling is a privilege grant: it hands every caller of this plugin
		// a path into a third party's code. The descriptor hash goes into the
		// record because what was approved was a description and a schema, not
		// a name.
		return auditServer(u, "mcpserver.tool_classified", actor, server, "classify",
			map[string]any{
				"tool":            tool,
				"from":            previous,
				"to":              string(state),
				"descriptor_hash": hash,
			})
	})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullIfBlank(s string) any {
	if s == "" {
		return nil
	}
	return s
}
