package bookstack

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// Writing pages: the mutations this plugin exists for.

func (p *Plugin) pageMutations() []mutationEntry {
	return []mutationEntry{
		entry(plugins.MutationSpec{
			Action: "page.create",
			Title:  "Create a page",
			Description: "Proposes a new page in a book or chapter. Nothing is " +
				"written until this is approved; the whole text is shown first.",
			Risk: operations.RiskLow,
			// A page that should not have been created is deleted, and that delete
			// is itself recoverable from the recycle bin.
			Reversible: true,
			Verifiable: true,
		}, &pageCreate{p: p}),

		entry(plugins.MutationSpec{
			Action: "page.update",
			Title:  "Update a page",
			Description: "Proposes a change to a page's title, text, tags or " +
				"location. The old and new text are shown side by side. Refused if " +
				"somebody has edited the page since this was proposed.",
			// Higher than a create because it replaces somebody's writing rather
			// than adding to it, and the thing it replaces is not obviously gone.
			Risk: operations.RiskMedium,
			// BookStack keeps a revision per save, so the previous text can be put
			// back -- and the rollback parameters carry it.
			Reversible: true,
			Verifiable: true,
		}, &pageUpdate{p: p}),

		entry(plugins.MutationSpec{
			Action: "page.delete",
			Title:  "Delete a page",
			Description: "Proposes sending a page to the recycle bin, from where it " +
				"can be restored. Shows what the page says before it goes.",
			Risk: operations.RiskMedium,
			// The recycle bin is the way back, and restore_from_recycle_bin is it.
			Reversible: true,
			Verifiable: true,
		}, &pageDelete{p: p}),
	}
}

// --- create -----------------------------------------------------------------

// PageCreateParams is a new page.
type PageCreateParams struct {
	BookID    int    `json:"book_id,omitempty" jsonschema:"the book to put it in; one of book_id or chapter_id is required"`
	ChapterID int    `json:"chapter_id,omitempty" jsonschema:"the chapter to put it in"`
	Name      string `json:"name" jsonschema:"the page's title"`
	HTML      string `json:"html,omitempty" jsonschema:"the page's text as HTML; use this for an instance whose pages are written in the WYSIWYG editor"`
	Markdown  string `json:"markdown,omitempty" jsonschema:"the page's text as markdown; makes this a markdown page"`
	// Tags are how a knowledge base stays findable. list_tag_names shows what
	// is already in use, and reusing a name beats inventing a synonym.
	Tags []tagPair `json:"tags,omitempty" jsonschema:"tags to set; list_tag_names shows the vocabulary already in use"`
}

type pageCreate struct{ p *Plugin }

func (h *pageCreate) Plan(ctx context.Context, params PageCreateParams) (plugins.Plan[entityState], error) {
	var plan plugins.Plan[entityState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return plan, fmt.Errorf("bookstack: a page needs a title")
	}
	if params.BookID <= 0 && params.ChapterID <= 0 {
		return plan, fmt.Errorf("bookstack: say where the page goes, with book_id " +
			"or chapter_id. list_books and list_chapters report them")
	}
	body, editor, err := pageBody(params.HTML, params.Markdown)
	if err != nil {
		return plan, err
	}

	// Where it is going, named rather than left as a number: an approver
	// reading "book 12" cannot tell whether that is the right book.
	where, err := h.p.describeParent(ctx, params.BookID, params.ChapterID)
	if err != nil {
		return plan, err
	}

	desired := entityState{
		Exists: true, Name: name, BookID: params.BookID, ChapterID: params.ChapterID,
		Content: body, Editor: editor, Tags: wantTagStrings(params.Tags),
	}
	changes := []operations.Change{
		{Field: "page", From: nil, To: name},
		{Field: "location", From: nil, To: where},
		{Field: "content", From: "", To: forDiff(body)},
	}
	if len(desired.Tags) > 0 {
		changes = append(changes, operations.Change{Field: "tags", From: nil, To: desired.Tags})
	}
	return plugins.Plan[entityState]{
		Before:  entityState{Exists: false},
		Desired: desired,
		// Nothing exists yet, so there is nothing to have drifted. Saying so
		// explicitly is better than an empty map, which would be
		// indistinguishable from a check that was never written.
		Preconditions: map[string]any{"exists": false},
		Changes:       changes,
		Impact: fmt.Sprintf("Adds a new page %q to %s. It becomes visible to "+
			"everybody who can see that book.", name, where),
	}, nil
}

func (h *pageCreate) Apply(ctx context.Context, params PageCreateParams, _ plugins.Plan[entityState]) (plugins.ApplyResult, error) {
	body, _, err := pageBody(params.HTML, params.Markdown)
	if err != nil {
		return plugins.ApplyResult{}, err
	}
	payload := map[string]any{"name": strings.TrimSpace(params.Name)}
	if params.BookID > 0 {
		payload["book_id"] = params.BookID
	}
	if params.ChapterID > 0 {
		payload["chapter_id"] = params.ChapterID
	}
	if strings.TrimSpace(params.Markdown) != "" {
		payload["markdown"] = params.Markdown
	} else {
		payload["html"] = body
	}
	if len(params.Tags) > 0 {
		payload["tags"] = apiTags(params.Tags)
	}
	raw, err := h.p.client.send(ctx, "POST", "/api/pages", payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

// Observe finds the page the create made.
//
// A create has no id to re-read until it has run, so this looks the page up by
// name within the book it was put in. That is why the name is required and why
// two pages with one name in one book is a case worth knowing about: the
// observation names what it found so a mismatch is visible rather than
// assumed.
func (h *pageCreate) Observe(ctx context.Context, params PageCreateParams) (entityState, error) {
	bookID := params.BookID
	if bookID == 0 && params.ChapterID > 0 {
		ch, err := h.p.readEntity(ctx, "chapters", params.ChapterID)
		if err != nil {
			return entityState{}, err
		}
		bookID = ch.BookID
	}
	id, err := h.p.findPageByName(ctx, bookID, params.ChapterID, strings.TrimSpace(params.Name))
	if err != nil || id == 0 {
		return entityState{Exists: false}, err
	}
	return h.p.readEntity(ctx, "pages", id)
}

// --- update -----------------------------------------------------------------

// PageUpdateParams changes an existing page.
//
// Every field is optional except the page itself: what is left out is left
// alone, which is what makes a small correction a small proposal rather than a
// whole-page rewrite an approver has to read in full.
type PageUpdateParams struct {
	ID        int       `json:"id,omitempty" jsonschema:"the page's numeric id"`
	Slug      string    `json:"slug,omitempty" jsonschema:"the page's slug, if you have that instead"`
	URL       string    `json:"url,omitempty" jsonschema:"a BookStack link to the page"`
	Name      string    `json:"name,omitempty" jsonschema:"a new title; leave out to keep the current one"`
	HTML      string    `json:"html,omitempty" jsonschema:"replacement text as HTML"`
	Markdown  string    `json:"markdown,omitempty" jsonschema:"replacement text as markdown; on a page BookStack holds as WYSIWYG this converts it, which is reported as a change"`
	BookID    int       `json:"book_id,omitempty" jsonschema:"move the page to this book"`
	ChapterID int       `json:"chapter_id,omitempty" jsonschema:"move the page to this chapter"`
	Tags      []tagPair `json:"tags,omitempty" jsonschema:"replace the page's tags with these; leave out to keep them"`
}

type pageUpdate struct{ p *Plugin }

func (h *pageUpdate) Plan(ctx context.Context, params PageUpdateParams) (plugins.Plan[entityState], error) {
	var plan plugins.Plan[entityState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	id, err := h.p.locate(ctx, "pages", params.ID, params.Slug, params.URL)
	if err != nil {
		return plan, err
	}
	before, err := h.p.readEntity(ctx, "pages", id)
	if err != nil {
		return plan, err
	}
	if !before.Exists {
		return plan, fmt.Errorf("bookstack: there is no page %d to update", id)
	}
	if strings.TrimSpace(params.HTML) != "" && strings.TrimSpace(params.Markdown) != "" {
		return plan, fmt.Errorf("bookstack: send html or markdown, not both — they " +
			"are two ways of writing the same page and BookStack keeps one")
	}

	desired := before
	desired.ID = id
	changes := []operations.Change{}

	if n := strings.TrimSpace(params.Name); n != "" && n != before.Name {
		desired.Name = n
		changes = diffField(changes, "name", before.Name, n)
	}
	if body := strings.TrimSpace(params.HTML); body != "" {
		desired.Content = params.HTML
		changes = diffText(changes, "content", before.Content, params.HTML)
		// Writing html to a page BookStack holds as markdown makes html the
		// source and the markdown is dropped. That is a change in its own
		// right and an approver should see it named.
		if before.Editor == "markdown" {
			desired.Editor = "wysiwyg"
			changes = diffField(changes, "editor", before.Editor, "wysiwyg")
		}
	}
	if body := strings.TrimSpace(params.Markdown); body != "" {
		desired.Content = params.Markdown
		changes = diffText(changes, "content", before.Content, params.Markdown)
		if before.Editor != "markdown" {
			desired.Editor = "markdown"
			changes = diffField(changes, "editor", before.Editor, "markdown")
		}
	}
	if params.BookID > 0 && params.BookID != before.BookID {
		desired.BookID = params.BookID
		where, err := h.p.describeParent(ctx, params.BookID, 0)
		if err != nil {
			return plan, err
		}
		from, _ := h.p.describeParent(ctx, before.BookID, 0)
		changes = diffField(changes, "book", from, where)
	}
	if params.ChapterID > 0 && params.ChapterID != before.ChapterID {
		desired.ChapterID = params.ChapterID
		changes = diffField(changes, "chapter_id", before.ChapterID, params.ChapterID)
	}
	if params.Tags != nil {
		want := wantTagStrings(params.Tags)
		desired.Tags = want
		changes = diffField(changes, "tags",
			strings.Join(before.Tags, ", "), strings.Join(want, ", "))
	}

	if len(changes) == 0 {
		return plan, fmt.Errorf("bookstack: nothing to change on page %d — the "+
			"values given match what is already there", id)
	}

	impact := fmt.Sprintf("Replaces what page %q says. BookStack keeps the "+
		"current text as a revision, so it can be put back.", before.Name)
	if before.Editor == "markdown" && strings.TrimSpace(params.HTML) != "" {
		impact += " This page is written in markdown and this proposal writes " +
			"HTML, which discards the markdown source."
	}

	return plugins.Plan[entityState]{
		Before:  before,
		Desired: desired,
		// Both, because a page saved twice in the same second moves its
		// revision count without moving its timestamp.
		Preconditions: preconditionsFor(before),
		Changes:       changes,
		Impact:        impact,
		// The way back: the text as it stands now, written to the same page.
		Rollback: PageUpdateParams{ID: id, Name: before.Name, HTML: before.Content},
	}, nil
}

func (h *pageUpdate) Apply(ctx context.Context, params PageUpdateParams, plan plugins.Plan[entityState]) (plugins.ApplyResult, error) {
	id := plan.Before.ID
	if id == 0 {
		id = params.ID
	}
	payload := map[string]any{}
	if n := strings.TrimSpace(params.Name); n != "" {
		payload["name"] = n
	}
	if body := strings.TrimSpace(params.HTML); body != "" {
		payload["html"] = params.HTML
	}
	if body := strings.TrimSpace(params.Markdown); body != "" {
		payload["markdown"] = params.Markdown
	}
	if params.BookID > 0 {
		payload["book_id"] = params.BookID
	}
	if params.ChapterID > 0 {
		payload["chapter_id"] = params.ChapterID
	}
	if params.Tags != nil {
		payload["tags"] = apiTags(params.Tags)
	}
	raw, err := h.p.client.send(ctx, "PUT", "/api/pages/"+strconv.Itoa(id), payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *pageUpdate) Observe(ctx context.Context, params PageUpdateParams) (entityState, error) {
	id, err := h.p.locate(ctx, "pages", params.ID, params.Slug, params.URL)
	if err != nil {
		return entityState{}, err
	}
	return h.p.readEntity(ctx, "pages", id)
}

// --- delete -----------------------------------------------------------------

// PageDeleteParams names a page to remove.
type PageDeleteParams struct {
	ID   int    `json:"id,omitempty" jsonschema:"the page's numeric id"`
	Slug string `json:"slug,omitempty" jsonschema:"the page's slug, if you have that instead"`
	URL  string `json:"url,omitempty" jsonschema:"a BookStack link to the page"`
}

type pageDelete struct{ p *Plugin }

func (h *pageDelete) Plan(ctx context.Context, params PageDeleteParams) (plugins.Plan[entityState], error) {
	var plan plugins.Plan[entityState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	id, err := h.p.locate(ctx, "pages", params.ID, params.Slug, params.URL)
	if err != nil {
		return plan, err
	}
	before, err := h.p.readEntity(ctx, "pages", id)
	if err != nil {
		return plan, err
	}
	if !before.Exists {
		return plan, fmt.Errorf("bookstack: there is no page %d to delete", id)
	}
	where, _ := h.p.describeParent(ctx, before.BookID, before.ChapterID)
	return plugins.Plan[entityState]{
		Before:        before,
		Desired:       entityState{Exists: false, ID: id},
		Preconditions: preconditionsFor(before),
		Changes: []operations.Change{
			{Field: "page", From: before.Name, To: nil},
			{Field: "location", From: where, To: nil},
			// The text goes into the diff so that whoever approves the delete
			// has read what is being removed rather than only its title.
			{Field: "content", From: forDiff(before.Content), To: ""},
		},
		Impact: fmt.Sprintf("Removes %q from %s. It goes to the recycle bin, so "+
			"list_recycle_bin will show it and restore_from_recycle_bin can put "+
			"it back.", before.Name, where),
	}, nil
}

func (h *pageDelete) Apply(ctx context.Context, params PageDeleteParams, plan plugins.Plan[entityState]) (plugins.ApplyResult, error) {
	id := plan.Before.ID
	if id == 0 {
		id = params.ID
	}
	_, err := h.p.client.send(ctx, "DELETE", "/api/pages/"+strconv.Itoa(id), nil)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return plugins.ApplyResult{UpstreamRef: strconv.Itoa(id)}, nil
}

// Observe confirms absence, which is what this delete desired.
func (h *pageDelete) Observe(ctx context.Context, params PageDeleteParams) (entityState, error) {
	id := params.ID
	if id == 0 {
		var err error
		id, err = h.p.locate(ctx, "pages", params.ID, params.Slug, params.URL)
		if err != nil {
			// A page that can no longer be found by its slug is a page that
			// was deleted, which is the outcome this wanted.
			return entityState{Exists: false}, nil
		}
	}
	return h.p.readEntity(ctx, "pages", id)
}

// --- shared -----------------------------------------------------------------

// pageBody settles which form a page's text is in.
func pageBody(html, markdown string) (body, editor string, err error) {
	h, m := strings.TrimSpace(html), strings.TrimSpace(markdown)
	switch {
	case h != "" && m != "":
		return "", "", fmt.Errorf("bookstack: send html or markdown, not both — they " +
			"are two ways of writing the same page and BookStack keeps one")
	case m != "":
		return markdown, "markdown", nil
	case h != "":
		return html, "wysiwyg", nil
	}
	return "", "", fmt.Errorf("bookstack: a page needs text: send html or markdown")
}

// describeParent names where a page lives, for a diff somebody has to read.
func (p *Plugin) describeParent(ctx context.Context, bookID, chapterID int) (string, error) {
	if chapterID > 0 {
		ch, err := p.readEntity(ctx, "chapters", chapterID)
		if err != nil {
			return "", err
		}
		if !ch.Exists {
			return "", fmt.Errorf("bookstack: there is no chapter %d", chapterID)
		}
		book, err := p.readEntity(ctx, "books", ch.BookID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("chapter %q in book %q", ch.Name, book.Name), nil
	}
	if bookID > 0 {
		book, err := p.readEntity(ctx, "books", bookID)
		if err != nil {
			return "", err
		}
		if !book.Exists {
			return "", fmt.Errorf("bookstack: there is no book %d", bookID)
		}
		return fmt.Sprintf("book %q", book.Name), nil
	}
	return "", nil
}

// findPageByName looks for a page a create has just made.
func (p *Plugin) findPageByName(ctx context.Context, bookID, chapterID int, name string) (int, error) {
	q := filterByName(name)
	if bookID > 0 {
		q.Set("filter[book_id]", strconv.Itoa(bookID))
	}
	if chapterID > 0 {
		q.Set("filter[chapter_id]", strconv.Itoa(chapterID))
	}
	q.Set("sort", "-created_at")
	pg, err := p.client.list(ctx, "/api/pages", q, 5)
	p.noted(err)
	if err != nil {
		return 0, err
	}
	rows, err := decodeRows[pageRow](pg)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if strings.EqualFold(strings.TrimSpace(r.Name), name) {
			return r.ID, nil
		}
	}
	return 0, nil
}
