package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/spoked/mcpd/internal/tunnel"
)

// RecordPairings keeps what OpenAI last said about an account's pairings.
//
// Guarded on the account still naming the organisation the answers were
// about: an answer that arrives after somebody changed the organisation is
// about a question nobody is asking any more, and matches no row rather than
// being filed against the new one.
func (s *ChatGPTAccountStore) RecordPairings(ctx context.Context, accountID, orgID string, ps []tunnel.Pairing) error {
	if len(ps) == 0 {
		return nil
	}
	return s.db.WriteTx(ctx, s.now().UnixMilli(), func(u *UnitOfWork) error {
		for _, p := range ps {
			_, err := u.exec(`
				INSERT INTO chatgpt_pairings
				       (account_id, workspace_id, status, org_id, problem, reason, upstream, checked_at)
				SELECT ?, ?, ?, ?, ?, ?, ?, ?
				 WHERE EXISTS (SELECT 1 FROM chatgpt_accounts WHERE id = ? AND org_id = ?)
				ON CONFLICT (account_id, workspace_id) DO UPDATE SET
				       status = excluded.status, org_id = excluded.org_id,
				       problem = excluded.problem, reason = excluded.reason,
				       upstream = excluded.upstream, checked_at = excluded.checked_at`,
				accountID, p.Workspace, string(p.Status), orgID,
				p.Problem, p.Reason, p.Upstream, p.CheckedAt.UnixMilli(),
				accountID, orgID)
			if err != nil {
				return fmt.Errorf("sqlite: record a chatgpt pairing: %w", err)
			}
		}
		// A workspace taken off the account takes its answer with it, so a
		// workspace added back later is asked about afresh.
		if _, err := u.exec(`
			DELETE FROM chatgpt_pairings
			 WHERE account_id = ? AND workspace_id <> ''
			   AND workspace_id NOT IN (
			       SELECT value FROM json_each(
			           (SELECT workspaces FROM chatgpt_accounts WHERE id = ?)))`,
			accountID, accountID); err != nil {
			return fmt.Errorf("sqlite: forget a removed workspace's pairing: %w", err)
		}
		return nil
	})
}

// Pairings returns what OpenAI last said about every account's pairings, by
// account id.
//
// Only answers that are still about the account as it now is: its current
// organisation, and the organisation alone or a workspace it still names. An
// answer about a workspace since removed, or an organisation since changed, is
// not an answer to anything the account now asks.
func (s *ChatGPTAccountStore) Pairings(ctx context.Context) (map[string][]tunnel.Pairing, error) {
	rows, err := s.db.Reader().QueryContext(ctx, `
		SELECT p.account_id, p.workspace_id, p.status, p.org_id, p.problem, p.reason,
		       p.upstream, p.checked_at
		  FROM chatgpt_pairings p JOIN chatgpt_accounts a ON a.id = p.account_id
		 WHERE p.org_id = COALESCE(a.org_id, '')
		   AND (p.workspace_id = ''
		        OR p.workspace_id IN (SELECT value FROM json_each(a.workspaces)))
		 ORDER BY p.account_id, p.workspace_id`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list chatgpt pairings: %w", err)
	}
	defer rows.Close()
	out := map[string][]tunnel.Pairing{}
	for rows.Next() {
		var (
			id, status string
			p          tunnel.Pairing
			at         int64
		)
		if err := rows.Scan(&id, &p.Workspace, &status, &p.OrgID, &p.Problem,
			&p.Reason, &p.Upstream, &at); err != nil {
			return nil, fmt.Errorf("sqlite: read a chatgpt pairing: %w", err)
		}
		p.Status = tunnel.PairingStatus(status)
		p.CheckedAt = time.UnixMilli(at).UTC()
		out[id] = append(out[id], p)
	}
	return out, rows.Err()
}
