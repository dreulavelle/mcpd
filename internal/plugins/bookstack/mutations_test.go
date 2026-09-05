package bookstack

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Fixtures. Invented throughout.

const bookOne = `{"id":7,"name":"Runbooks","slug":"runbooks","description":"How we do things",
  "created_at":"2026-01-02T10:00:00.000000Z","updated_at":"2026-08-01T09:00:00.000000Z",
  "contents":[]}`

const pageOne = `{"id":42,"name":"Firewall replacement","slug":"firewall-replacement",
  "book_id":7,"chapter_id":0,"draft":false,"template":false,
  "created_at":"2026-02-02T10:00:00.000000Z","updated_at":"2026-08-01T09:00:00.000000Z",
  "revision_count":4,"editor":"wysiwyg","html":"<p>Old text</p>","markdown":"",
  "tags":[{"name":"customer","value":"Acme"}]}`

// Plan must not mutate. It runs twice for every change -- once at proposal and
// again immediately before execution -- so a Plan with a side effect applies
// that side effect to anybody who merely asks what a change would do.
func TestPlanningNeverWrites(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/pages/42"] = pageOne
	f.bodies["GET /api/books/7"] = bookOne
	f.bodies["GET /api/pages"] = `{"data":[],"total":0}`
	p := newPlugin(t, f)
	ctx := context.Background()

	if _, err := (&pageUpdate{p: p}).Plan(ctx, PageUpdateParams{
		ID: 42, HTML: "<p>New text</p>",
	}); err != nil {
		t.Fatalf("plan update: %v", err)
	}
	if _, err := (&pageDelete{p: p}).Plan(ctx, PageDeleteParams{ID: 42}); err != nil {
		t.Fatalf("plan delete: %v", err)
	}
	if _, err := (&pageCreate{p: p}).Plan(ctx, PageCreateParams{
		BookID: 7, Name: "New page", HTML: "<p>Hello</p>",
	}); err != nil {
		t.Fatalf("plan create: %v", err)
	}
	if _, err := (&containerDelete{p: p, kind: "books"}).Plan(ctx,
		ContainerDeleteParams{ID: 7}); err != nil {
		t.Fatalf("plan book delete: %v", err)
	}

	if wrote := f.wrote(); len(wrote) > 0 {
		t.Fatalf("planning wrote to BookStack: %v", wrote)
	}
}

// The precondition is what makes a stale proposal fail instead of silently
// discarding somebody's edit. Both fields, because a page saved twice in one
// second moves its revision count without moving its timestamp.
func TestUpdateCapturesBothDriftFields(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/pages/42"] = pageOne
	p := newPlugin(t, f)

	plan, err := (&pageUpdate{p: p}).Plan(context.Background(), PageUpdateParams{
		ID: 42, HTML: "<p>New text</p>",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plan.Preconditions["updated_at"]; got != "2026-08-01T09:00:00.000000Z" {
		t.Errorf("updated_at precondition is %v", got)
	}
	if got := plan.Preconditions["revision_count"]; got != 4 {
		t.Errorf("revision_count precondition is %v", got)
	}
}

// An approver shown one field while five change has not approved anything, so
// the diff has to carry the text itself rather than a summary of it.
func TestUpdateDiffCarriesTheText(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/pages/42"] = pageOne
	p := newPlugin(t, f)

	plan, err := (&pageUpdate{p: p}).Plan(context.Background(), PageUpdateParams{
		ID: 42, Name: "Firewall replacement (2026)", HTML: "<p>New text</p>",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	fields := map[string]bool{}
	for _, c := range plan.Changes {
		fields[c.Field] = true
		if c.Field == "content" {
			if !strings.Contains(c.From.(string), "Old text") {
				t.Error("the diff should show the text being replaced")
			}
			if !strings.Contains(c.To.(string), "New text") {
				t.Error("the diff should show the text replacing it")
			}
		}
	}
	for _, want := range []string{"name", "content"} {
		if !fields[want] {
			t.Errorf("the diff does not mention %q: %+v", want, plan.Changes)
		}
	}
}

// Writing markdown to a page BookStack holds as WYSIWYG converts it and
// discards the formatting. That is a change in its own right, so it is diffed
// as one rather than happening quietly.
func TestChangingTheEditorIsItselfAChange(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/pages/42"] = pageOne
	p := newPlugin(t, f)

	plan, err := (&pageUpdate{p: p}).Plan(context.Background(), PageUpdateParams{
		ID: 42, Markdown: "# New text",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	found := false
	for _, c := range plan.Changes {
		if c.Field == "editor" {
			found = true
			if c.From != "wysiwyg" || c.To != "markdown" {
				t.Errorf("editor change is %v -> %v", c.From, c.To)
			}
		}
	}
	if !found {
		t.Errorf("converting the editor should be named in the diff: %+v", plan.Changes)
	}
}

// A proposal that changes nothing is refused rather than approved and applied
// as a no-op, because an approval record for a change that was not a change is
// noise in the audit trail.
func TestAnEmptyUpdateIsRefused(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/pages/42"] = pageOne
	p := newPlugin(t, f)

	_, err := (&pageUpdate{p: p}).Plan(context.Background(), PageUpdateParams{
		ID: 42, Name: "Firewall replacement",
	})
	if err == nil || !strings.Contains(err.Error(), "nothing to change") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// Sending both forms of a page's text is a caller who has not decided which
// one BookStack should keep.
func TestSendingBothFormsIsRefused(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/pages/42"] = pageOne
	p := newPlugin(t, f)

	_, err := (&pageUpdate{p: p}).Plan(context.Background(), PageUpdateParams{
		ID: 42, HTML: "<p>a</p>", Markdown: "# a",
	})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// Deleting a book takes everything in it. The count is the fact that changes
// somebody's mind and it is not visible from the name.
func TestDeletingABookCountsWhatGoesWithIt(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/books/7"] = bookOne
	f.bodies["GET /api/pages"] = `{"data":[{"id":42,"name":"One","book_id":7}],"total":31}`
	p := newPlugin(t, f)

	plan, err := (&containerDelete{p: p, kind: "books"}).Plan(context.Background(),
		ContainerDeleteParams{ID: 7})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	found := false
	for _, c := range plan.Changes {
		if c.Field == "pages that go with it" {
			found = true
			if c.From != 31 {
				t.Errorf("page count is %v, want 31", c.From)
			}
		}
	}
	if !found {
		t.Errorf("the diff should count the pages: %+v", plan.Changes)
	}
	if !strings.Contains(plan.Impact, "31 page") {
		t.Errorf("the impact should say how many pages: %q", plan.Impact)
	}
	if !strings.Contains(plan.Impact, "recycle bin") {
		t.Errorf("the impact should say it can be restored: %q", plan.Impact)
	}
}

// A delete desires absence, and observing absence is a real confirmation
// rather than an error.
func TestDeleteObservesAbsence(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	p := newPlugin(t, f)

	// The fake answers 404 for anything it does not know, which is what a
	// deleted page looks like.
	got, err := (&pageDelete{p: p}).Observe(context.Background(), PageDeleteParams{ID: 42})
	if err != nil {
		t.Fatalf("Observe after a delete should not error: %v", err)
	}
	if got.Exists {
		t.Fatal("a deleted page should observe as absent")
	}
}

// Replacing an uploaded file with a link would destroy the file, and no diff
// makes that acceptable -- so it is refused at planning rather than shown.
func TestReplacingAnUploadedFileWithALinkIsRefused(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/attachments/3"] = `{"id":3,"name":"Diagram","external":false,
	  "uploaded_to":42,"updated_at":"2026-08-01T09:00:00.000000Z"}`
	p := newPlugin(t, f)

	_, err := (&attachmentUpdate{p: p}).Plan(context.Background(), AttachmentUpdateParams{
		ID: 3, Link: "https://example.invalid/diagram",
	})
	if err == nil || !strings.Contains(err.Error(), "destroy the file") {
		t.Fatalf("want a refusal naming the consequence, got %v", err)
	}
}

// A role's permission diff names what is granted and what is taken away.
// "62 becomes 48" tells an approver nothing about which forty-eight.
func TestRoleDiffNamesThePermissions(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/roles/3"] = `{"id":3,"display_name":"Editor","description":"",
	  "updated_at":"2026-08-01T09:00:00.000000Z","users":[{"id":1,"name":"A"}],
	  "permissions":["page-create-all","page-update-all","page-delete-all"]}`
	p := newPlugin(t, f)

	plan, err := (&roleUpdate{p: p}).Plan(context.Background(), RoleUpdateParams{
		ID: 3, Permissions: []string{"page-create-all", "page-update-all", "book-create-all"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var granted, taken string
	for _, c := range plan.Changes {
		switch c.Field {
		case "permissions granted":
			granted = strings.Join(c.To.([]string), ",")
		case "permissions taken away":
			taken = strings.Join(c.From.([]string), ",")
		}
	}
	if granted != "book-create-all" {
		t.Errorf("granted is %q, want book-create-all", granted)
	}
	if taken != "page-delete-all" {
		t.Errorf("taken away is %q, want page-delete-all", taken)
	}
	if !strings.Contains(plan.Impact, "hold it") {
		t.Errorf("the impact should say how many people are affected: %q", plan.Impact)
	}
}

// Destroying from the recycle bin has no way back, and the impact should say
// so plainly rather than leaving somebody to infer it from the risk level.
func TestDestroyingSaysThereIsNoWayBack(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/recycle-bin"] = `{"data":[{"id":9,"deleted_by":1,
	  "created_at":"2026-08-01T09:00:00.000000Z",
	  "deletable_type":"BookStack\\Entities\\Models\\Page",
	  "deletable":{"id":42,"name":"Firewall replacement","book_id":7}}],"total":1}`
	p := newPlugin(t, f)

	plan, err := (&recycleDestroy{p: p}).Plan(context.Background(), RecycleParams{DeletionID: 9})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !strings.Contains(plan.Impact, "no way back") {
		t.Errorf("the impact should say there is no way back: %q", plan.Impact)
	}
	if !strings.Contains(plan.Impact, "Firewall replacement") {
		t.Errorf("the impact should name what is being destroyed: %q", plan.Impact)
	}
}

// Using an item's own id where a deletion id belongs acts on something else
// entirely, so an id that is not in the bin is refused by name.
func TestRecycleBinRefusesAnIDThatIsNotADeletion(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/recycle-bin"] = `{"data":[],"total":0}`
	p := newPlugin(t, f)

	_, err := (&recycleRestore{p: p}).Plan(context.Background(), RecycleParams{DeletionID: 42})
	if err == nil || !strings.Contains(err.Error(), "no deletion 42") {
		t.Fatalf("want a refusal naming the id, got %v", err)
	}
}

// A long page still has to be readable in the diff, and where it was cut has
// to be visible rather than implied.
func TestALongDiffSaysWhereItWasCut(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", maxDiffBytes+500)
	got := forDiff(long)
	if len(got) > maxDiffBytes+200 {
		t.Fatalf("the diff was not cut: %d bytes", len(got))
	}
	if !strings.Contains(got, "cut here") {
		t.Error("a cut diff should say it was cut")
	}
	if !strings.Contains(got, "characters in total") {
		t.Error("a cut diff should say how much there was")
	}
}

// The plan the host carries is JSON. A change whose fields cannot survive that
// round trip would be shown to an approver differently from how it is applied.
func TestPlansSurviveJSON(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.bodies["GET /api/pages/42"] = pageOne
	p := newPlugin(t, f)

	plan, err := (&pageUpdate{p: p}).Plan(context.Background(), PageUpdateParams{
		ID: 42, HTML: "<p>New text</p>",
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, v := range []any{plan.Before, plan.Desired, plan.Preconditions, plan.Changes, plan.Rollback} {
		if _, err := json.Marshal(v); err != nil {
			t.Fatalf("a plan field does not survive JSON: %v", err)
		}
	}
}
