package bookstack

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// The write half, against a real BookStack.
//
// Guarded by its own variable rather than sharing the read tests', because
// these genuinely change the instance and somebody running the read tests has
// not agreed to that:
//
//	BOOKSTACK_TEST_HOST=… BOOKSTACK_TEST_TOKEN_ID=… \
//	  BOOKSTACK_TEST_TOKEN_SECRET=… BOOKSTACK_TEST_WRITES=1 \
//	  go test ./internal/plugins/bookstack/ -run IntegrationWrites -v
//
// Everything happens inside one book this test creates, and the book is
// destroyed at the end -- including out of the recycle bin, so the instance is
// left exactly as it was found. Nothing here touches anything that existed
// beforehand.
//
// It exercises the mutations directly rather than through the approval path.
// That is the point: the approval machinery is the host's and is tested there,
// while what is unproven here is whether Plan, Apply and Observe agree with a
// real BookStack.
func writeIntegrationPlugin(t *testing.T) *Plugin {
	t.Helper()
	if os.Getenv("BOOKSTACK_TEST_WRITES") == "" {
		t.Skip("set BOOKSTACK_TEST_WRITES=1 to run the tests that change a real instance")
	}
	return integrationPlugin(t)
}

func TestIntegrationWritesTheWholeContentLifecycle(t *testing.T) {
	p := writeIntegrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	stamp := time.Now().UTC().Format("20060102-150405")
	bookName := "mcpd integration test " + stamp

	// --- create the book everything else happens inside --------------------
	book := &containerCreate{p: p, kind: "books"}
	bookParams := ContainerCreateParams{
		Name:        bookName,
		Description: "Created by mcpd's integration test. Safe to delete.",
	}
	plan, err := book.Plan(ctx, bookParams)
	if err != nil {
		t.Fatalf("plan book.create: %v", err)
	}
	if plan.Before.Exists {
		t.Fatal("a book that does not exist yet should plan from absence")
	}
	if _, err := book.Apply(ctx, bookParams, plan); err != nil {
		t.Fatalf("apply book.create: %v", err)
	}
	created, err := book.Observe(ctx, bookParams)
	if err != nil {
		t.Fatalf("observe book.create: %v", err)
	}
	if !created.Exists || created.ID == 0 {
		t.Fatalf("the book was not created: %+v", created)
	}
	bookID := created.ID
	t.Logf("created book %d", bookID)

	// Whatever happens below, the book and everything in it goes, and then
	// goes from the recycle bin too.
	defer cleanUp(t, p, bookID, bookName)

	// --- a chapter in it ---------------------------------------------------
	chapter := &containerCreate{p: p, kind: "chapters"}
	chapterParams := ContainerCreateParams{Name: "Procedures", BookID: bookID}
	chPlan, err := chapter.Plan(ctx, chapterParams)
	if err != nil {
		t.Fatalf("plan chapter.create: %v", err)
	}
	if _, err := chapter.Apply(ctx, chapterParams, chPlan); err != nil {
		t.Fatalf("apply chapter.create: %v", err)
	}
	chState, err := chapter.Observe(ctx, chapterParams)
	if err != nil || !chState.Exists {
		t.Fatalf("the chapter was not created: %+v (%v)", chState, err)
	}
	t.Logf("created chapter %d", chState.ID)

	// --- a page in the chapter --------------------------------------------
	page := &pageCreate{p: p}
	pageParams := PageCreateParams{
		ChapterID: chState.ID,
		Name:      "Replacing a firewall",
		HTML:      "<p>First, tell the customer.</p>",
		Tags:      []tagPair{{Name: "mcpd-test", Value: stamp}},
	}
	pgPlan, err := page.Plan(ctx, pageParams)
	if err != nil {
		t.Fatalf("plan page.create: %v", err)
	}
	// The plan has to name where it is going, not just the id, or an approver
	// cannot tell whether it is the right place.
	if !strings.Contains(pgPlan.Impact, "Procedures") {
		t.Errorf("the impact should name the chapter: %q", pgPlan.Impact)
	}
	if _, err := page.Apply(ctx, pageParams, pgPlan); err != nil {
		t.Fatalf("apply page.create: %v", err)
	}
	pgState, err := page.Observe(ctx, pageParams)
	if err != nil || !pgState.Exists {
		t.Fatalf("the page was not created: %+v (%v)", pgState, err)
	}
	pageID := pgState.ID
	t.Logf("created page %d, revision %d", pageID, pgState.RevisionCount)

	// --- update it, and check the drift fields actually move ---------------
	upd := &pageUpdate{p: p}
	updParams := PageUpdateParams{
		ID: pageID, HTML: "<p>First, tell the customer. Then take a backup.</p>",
	}
	uPlan, err := upd.Plan(ctx, updParams)
	if err != nil {
		t.Fatalf("plan page.update: %v", err)
	}
	beforeRevision := uPlan.Preconditions["revision_count"]
	if beforeRevision == nil {
		t.Fatal("the update did not capture a revision count to compare")
	}
	if _, err := upd.Apply(ctx, updParams, uPlan); err != nil {
		t.Fatalf("apply page.update: %v", err)
	}
	after, err := upd.Observe(ctx, updParams)
	if err != nil {
		t.Fatalf("observe page.update: %v", err)
	}
	if !strings.Contains(after.Content, "take a backup") {
		t.Errorf("the page was not updated: %q", after.Content)
	}
	// This is the whole basis of drift detection: if BookStack did not move
	// these, a stale proposal would apply silently over somebody's edit.
	if after.RevisionCount <= beforeRevision.(int) {
		t.Errorf("revision_count did not advance: %v then %d",
			beforeRevision, after.RevisionCount)
	}
	t.Logf("updated page, revision %v -> %d", beforeRevision, after.RevisionCount)

	// --- a comment on it ---------------------------------------------------
	comment := &commentCreate{p: p}
	cParams := CommentCreateParams{PageID: pageID, HTML: "<p>Checked against the runbook.</p>"}
	cPlan, err := comment.Plan(ctx, cParams)
	if err != nil {
		t.Fatalf("plan comment.create: %v", err)
	}
	if _, err := comment.Apply(ctx, cParams, cPlan); err != nil {
		t.Fatalf("apply comment.create: %v", err)
	}
	cState, err := comment.Observe(ctx, cParams)
	if err != nil || !cState.Exists {
		t.Fatalf("the comment was not created: %+v (%v)", cState, err)
	}
	t.Logf("created comment %d", cState.ID)

	// --- a link attached to it --------------------------------------------
	att := &attachmentCreate{p: p}
	aParams := AttachmentCreateParams{
		PageID: pageID, Name: "Vendor documentation",
		Link: "https://example.invalid/firewall",
	}
	aPlan, err := att.Plan(ctx, aParams)
	if err != nil {
		t.Fatalf("plan attachment.create: %v", err)
	}
	if _, err := att.Apply(ctx, aParams, aPlan); err != nil {
		t.Fatalf("apply attachment.create: %v", err)
	}
	aState, err := att.Observe(ctx, aParams)
	if err != nil || !aState.Exists {
		t.Fatalf("the attachment was not created: %+v (%v)", aState, err)
	}
	t.Logf("attached link %d", aState.ID)

	// --- delete the page, find it in the bin, put it back ------------------
	del := &pageDelete{p: p}
	dParams := PageDeleteParams{ID: pageID}
	dPlan, err := del.Plan(ctx, dParams)
	if err != nil {
		t.Fatalf("plan page.delete: %v", err)
	}
	if _, err := del.Apply(ctx, dParams, dPlan); err != nil {
		t.Fatalf("apply page.delete: %v", err)
	}
	gone, err := del.Observe(ctx, dParams)
	if err != nil {
		t.Fatalf("observe page.delete: %v", err)
	}
	if gone.Exists {
		t.Fatal("the page should be gone after a delete")
	}

	// The claim that made page.delete Reversible: it is in the bin, and it
	// comes back.
	deletionID := findOurDeletion(t, p, ctx, pageID)
	restore := &recycleRestore{p: p}
	rParams := RecycleParams{DeletionID: deletionID}
	rPlan, err := restore.Plan(ctx, rParams)
	if err != nil {
		t.Fatalf("plan recycle_bin.restore: %v", err)
	}
	if _, err := restore.Apply(ctx, rParams, rPlan); err != nil {
		t.Fatalf("apply recycle_bin.restore: %v", err)
	}
	back, err := p.readEntity(ctx, "pages", pageID)
	if err != nil {
		t.Fatalf("reading the restored page: %v", err)
	}
	if !back.Exists {
		t.Fatal("the page did not come back from the recycle bin; page.delete " +
			"claims Reversible on the strength of this")
	}
	t.Logf("deleted page %d and restored it from deletion %d", pageID, deletionID)
}

// cleanUp removes the test book and empties it from the recycle bin, so the
// instance is left as it was found.
func cleanUp(t *testing.T, p *Plugin, bookID int, bookName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	del := &containerDelete{p: p, kind: "books"}
	params := ContainerDeleteParams{ID: bookID}
	plan, err := del.Plan(ctx, params)
	if err != nil {
		t.Errorf("CLEAN UP FAILED: planning the delete of book %d (%s): %v. "+
			"Remove it by hand", bookID, bookName, err)
		return
	}
	if _, err := del.Apply(ctx, params, plan); err != nil {
		t.Errorf("CLEAN UP FAILED: deleting book %d (%s): %v. Remove it by hand",
			bookID, bookName, err)
		return
	}

	// And out of the recycle bin, which is the one irreversible mutation in
	// this package -- used here deliberately, on a book this test made.
	deletionID := findOurDeletion(t, p, ctx, bookID)
	if deletionID == 0 {
		t.Logf("book %d is in the recycle bin; empty it by hand if you want it gone", bookID)
		return
	}
	destroy := &recycleDestroy{p: p}
	rParams := RecycleParams{DeletionID: deletionID}
	rPlan, err := destroy.Plan(ctx, rParams)
	if err != nil {
		t.Logf("book %d is in the recycle bin at deletion %d: %v", bookID, deletionID, err)
		return
	}
	if _, err := destroy.Apply(ctx, rParams, rPlan); err != nil {
		t.Logf("could not empty deletion %d from the recycle bin: %v", deletionID, err)
		return
	}
	t.Logf("cleaned up: book %d deleted and emptied from the recycle bin", bookID)
}

// findOurDeletion looks for the bin entry covering one item.
func findOurDeletion(t *testing.T, p *Plugin, ctx context.Context, itemID int) int {
	t.Helper()
	bin, err := p.listRecycleBin(ctx, limitArgs{Limit: 100})
	if err != nil {
		t.Errorf("reading the recycle bin: %v", err)
		return 0
	}
	for _, row := range bin.Items {
		if row.ItemID == itemID {
			return row.DeletionID
		}
	}
	return 0
}

// A proposal built against a page somebody has since edited must fail rather
// than silently discard their work. This is that race, made deliberate.
func TestIntegrationDriftIsDetected(t *testing.T) {
	p := writeIntegrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	stamp := time.Now().UTC().Format("20060102-150405")
	book := &containerCreate{p: p, kind: "books"}
	bookParams := ContainerCreateParams{Name: "mcpd drift test " + stamp}
	bPlan, err := book.Plan(ctx, bookParams)
	if err != nil {
		t.Fatalf("plan book.create: %v", err)
	}
	if _, err := book.Apply(ctx, bookParams, bPlan); err != nil {
		t.Fatalf("apply book.create: %v", err)
	}
	created, err := book.Observe(ctx, bookParams)
	if err != nil || !created.Exists {
		t.Fatalf("the book was not created: %v", err)
	}
	defer cleanUp(t, p, created.ID, bookParams.Name)

	page := &pageCreate{p: p}
	pageParams := PageCreateParams{
		BookID: created.ID, Name: "Drift", HTML: "<p>Original</p>",
	}
	pPlan, err := page.Plan(ctx, pageParams)
	if err != nil {
		t.Fatalf("plan page.create: %v", err)
	}
	if _, err := page.Apply(ctx, pageParams, pPlan); err != nil {
		t.Fatalf("apply page.create: %v", err)
	}
	pState, err := page.Observe(ctx, pageParams)
	if err != nil || !pState.Exists {
		t.Fatalf("the page was not created: %v", err)
	}

	upd := &pageUpdate{p: p}
	mine := PageUpdateParams{ID: pState.ID, HTML: "<p>My change</p>"}

	// The proposal, planned now.
	proposed, err := upd.Plan(ctx, mine)
	if err != nil {
		t.Fatalf("plan page.update: %v", err)
	}

	// Somebody else edits the page in the meantime.
	theirs := PageUpdateParams{ID: pState.ID, HTML: "<p>Their change</p>"}
	theirPlan, err := upd.Plan(ctx, theirs)
	if err != nil {
		t.Fatalf("plan their update: %v", err)
	}
	if _, err := upd.Apply(ctx, theirs, theirPlan); err != nil {
		t.Fatalf("apply their update: %v", err)
	}

	// The host re-plans immediately before executing and compares. This is
	// that comparison, made explicit: the two snapshots must differ, or drift
	// would go unnoticed.
	replanned, err := upd.Plan(ctx, mine)
	if err != nil {
		t.Fatalf("re-plan page.update: %v", err)
	}
	same := fmt.Sprint(proposed.Preconditions) == fmt.Sprint(replanned.Preconditions)
	if same {
		t.Fatalf("the preconditions did not move after somebody else's edit: %v — "+
			"drift would not be detected, and this proposal would overwrite them",
			replanned.Preconditions)
	}
	t.Logf("drift detected: %v became %v", proposed.Preconditions, replanned.Preconditions)
}

// Nothing in this file should be reachable without the plugin being mounted
// with an approval service, which is the host's job. This is the reminder in
// code that these handlers are not tools.
var _ = plugins.MutationSpec{}
