package bookstack

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// Undoing a delete, and the one change in this package that cannot be undone.
//
// These two are the same endpoint with different verbs and they could not be
// more different in consequence, which is why they are declared as far apart
// as the vocabulary allows: restore is low risk and reversible, destroy is
// critical and irreversible. The second declaration is the one that matters
// most -- an irreversible mutation can never be approved by a standing rule,
// so emptying the recycle bin always waits for a person.

func (p *Plugin) recycleBinMutations() []mutationEntry {
	return []mutationEntry{
		entry(plugins.MutationSpec{
			Action: "recycle_bin.restore",
			Title:  "Restore something deleted",
			Description: "Proposes putting a deleted shelf, book, chapter or page " +
				"back where it was. list_recycle_bin reports the deletion ids.",
			Risk: operations.RiskLow,
			// The way back from a restore is a delete, which is itself recoverable.
			Reversible: true,
			Verifiable: true,
		}, &recycleRestore{p: p}),

		entry(plugins.MutationSpec{
			Action: "recycle_bin.destroy",
			Title:  "Permanently destroy something",
			Description: "Proposes permanently destroying one item in the recycle " +
				"bin. This cannot be undone and BookStack keeps no copy.",
			Risk: operations.RiskCritical,
			// There is no way back. Declaring this honestly is also what stops a
			// standing rule from ever approving one on its own.
			Reversible: false,
			Verifiable: true,
		}, &recycleDestroy{p: p}),
	}
}

// RecycleParams names one deletion.
type RecycleParams struct {
	// DeletionID is the recycle bin's own id, not the item's. list_recycle_bin
	// reports both, and using the wrong one acts on a different item.
	DeletionID int `json:"deletion_id" jsonschema:"the deletion id from list_recycle_bin — not the item's own id"`
}

// recycleState is what these observe: whether the deletion is still in the bin.
type recycleState struct {
	InBin bool   `json:"in_bin"`
	Type  string `json:"type,omitempty"`
	Name  string `json:"name,omitempty"`
}

type recycleRestore struct{ p *Plugin }

func (h *recycleRestore) Plan(ctx context.Context, params RecycleParams) (plugins.Plan[recycleState], error) {
	var plan plugins.Plan[recycleState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	item, err := h.p.findDeletion(ctx, params.DeletionID)
	if err != nil {
		return plan, err
	}
	before := recycleState{InBin: true, Type: item.Type, Name: item.Name}
	return plugins.Plan[recycleState]{
		Before:        before,
		Desired:       recycleState{InBin: false, Type: item.Type, Name: item.Name},
		Preconditions: map[string]any{"in_bin": true, "deletion_id": params.DeletionID},
		Changes: []operations.Change{
			{Field: "restore", From: "in the recycle bin", To: "back in place"},
			{Field: item.Type, From: item.Name, To: item.Name},
		},
		Impact: fmt.Sprintf("Puts the %s %q back where it was, along with "+
			"anything that was deleted with it.", item.Type, item.Name),
	}, nil
}

func (h *recycleRestore) Apply(ctx context.Context, params RecycleParams, _ plugins.Plan[recycleState]) (plugins.ApplyResult, error) {
	_, err := h.p.client.send(ctx, "PUT",
		"/api/recycle-bin/"+strconv.Itoa(params.DeletionID), nil)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return plugins.ApplyResult{UpstreamRef: strconv.Itoa(params.DeletionID)}, nil
}

// Observe confirms the deletion has left the bin, which is what a restore
// means from the outside.
func (h *recycleRestore) Observe(ctx context.Context, params RecycleParams) (recycleState, error) {
	item, err := h.p.findDeletion(ctx, params.DeletionID)
	if isNotFound(err) || (err == nil && item.DeletionID == 0) {
		return recycleState{InBin: false}, nil
	}
	if err != nil {
		return recycleState{}, err
	}
	return recycleState{InBin: true, Type: item.Type, Name: item.Name}, nil
}

type recycleDestroy struct{ p *Plugin }

func (h *recycleDestroy) Plan(ctx context.Context, params RecycleParams) (plugins.Plan[recycleState], error) {
	var plan plugins.Plan[recycleState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	item, err := h.p.findDeletion(ctx, params.DeletionID)
	if err != nil {
		return plan, err
	}
	return plugins.Plan[recycleState]{
		Before:        recycleState{InBin: true, Type: item.Type, Name: item.Name},
		Desired:       recycleState{InBin: false},
		Preconditions: map[string]any{"in_bin": true, "deletion_id": params.DeletionID},
		Changes: []operations.Change{
			{Field: item.Type, From: item.Name, To: nil},
			{Field: "recoverable", From: true, To: false},
		},
		Impact: fmt.Sprintf("Permanently destroys the %s %q and anything deleted "+
			"with it. BookStack keeps no copy and there is no way back. If there "+
			"is any doubt, leave it in the recycle bin — it costs nothing to sit "+
			"there.", item.Type, item.Name),
	}, nil
}

func (h *recycleDestroy) Apply(ctx context.Context, params RecycleParams, _ plugins.Plan[recycleState]) (plugins.ApplyResult, error) {
	_, err := h.p.client.send(ctx, "DELETE",
		"/api/recycle-bin/"+strconv.Itoa(params.DeletionID), nil)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return plugins.ApplyResult{UpstreamRef: strconv.Itoa(params.DeletionID)}, nil
}

func (h *recycleDestroy) Observe(ctx context.Context, params RecycleParams) (recycleState, error) {
	item, err := h.p.findDeletion(ctx, params.DeletionID)
	if isNotFound(err) || (err == nil && item.DeletionID == 0) {
		return recycleState{InBin: false}, nil
	}
	if err != nil {
		return recycleState{}, err
	}
	return recycleState{InBin: true, Type: item.Type, Name: item.Name}, nil
}

// findDeletion looks one deletion up in the bin.
//
// BookStack serves no read for a single deletion, so this walks the listing.
// It is bounded by the instance's row ceiling like everything else, which is
// worth saying: a bin with more deletions than that will not find an old one,
// and the error says so rather than reporting it as absent.
func (p *Plugin) findDeletion(ctx context.Context, deletionID int) (RecycleRow, error) {
	if deletionID <= 0 {
		return RecycleRow{}, fmt.Errorf("bookstack: a deletion id is required; " +
			"list_recycle_bin reports them, and it is not the item's own id")
	}
	pg, err := p.client.list(ctx, "/api/recycle-bin", nil, 0)
	p.noted(err)
	if err != nil {
		return RecycleRow{}, explainPeopleFailure(err, "settings")
	}
	for _, raw := range pg.rows {
		var d recycleRow
		if err := json.Unmarshal(raw, &d); err != nil {
			return RecycleRow{}, fmt.Errorf("bookstack: could not read the recycle bin: %w", err)
		}
		if d.ID != deletionID {
			continue
		}
		return RecycleRow{
			DeletionID: d.ID, Type: shortType(d.DeletableType),
			ItemID: d.Deletable.ID, Name: d.Deletable.Name,
			BookID: d.Deletable.BookID, ChapterID: d.Deletable.ChapterID,
			DeletedBy: d.DeletedBy, DeletedAt: d.CreatedAt,
		}, nil
	}
	if pg.total > len(pg.rows) {
		return RecycleRow{}, fmt.Errorf("bookstack: deletion %d was not among the "+
			"%d most recent of %d in the recycle bin. Raise the instance's row "+
			"ceiling, or empty some of the bin", deletionID, len(pg.rows), pg.total)
	}
	return RecycleRow{}, fmt.Errorf("bookstack: there is no deletion %d in the "+
		"recycle bin; list_recycle_bin reports what is there", deletionID)
}
