package bookstack

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/plugins"
)

// Writing the structure a knowledge base is organised into: books, chapters
// and shelves.
//
// These are the containers rather than the writing, so the diffs are small and
// the consequences are mostly about where things are. The exception is
// deleting one: a book goes to the recycle bin with everything in it, and the
// number of pages that go with it is the fact an approver most needs, so every
// delete here counts them first.

func (p *Plugin) bookMutations() []mutationEntry {
	return []mutationEntry{
		entry(plugins.MutationSpec{
			Action:      "book.create",
			Title:       "Create a book",
			Description: "Proposes a new, empty book.",
			Risk:        operations.RiskLow,
			Reversible:  true,
			Verifiable:  true,
		}, &containerCreate{p: p, kind: "books"}),

		entry(plugins.MutationSpec{
			Action:      "book.update",
			Title:       "Update a book",
			Description: "Proposes a change to a book's name, description or tags.",
			Risk:        operations.RiskLow,
			Reversible:  true,
			Verifiable:  true,
		}, &containerUpdate{p: p, kind: "books"}),

		entry(plugins.MutationSpec{
			Action: "book.delete",
			Title:  "Delete a book",
			Description: "Proposes sending a book, and every chapter and page in " +
				"it, to the recycle bin. Says how many pages that is before it goes.",
			// A book is a lot of somebody's writing at once, and the count is
			// often a surprise. High rather than medium for that reason alone.
			Risk:       operations.RiskHigh,
			Reversible: true,
			Verifiable: true,
		}, &containerDelete{p: p, kind: "books"}),
	}
}

func (p *Plugin) chapterMutations() []mutationEntry {
	return []mutationEntry{
		entry(plugins.MutationSpec{
			Action:      "chapter.create",
			Title:       "Create a chapter",
			Description: "Proposes a new, empty chapter in a book.",
			Risk:        operations.RiskLow,
			Reversible:  true,
			Verifiable:  true,
		}, &containerCreate{p: p, kind: "chapters"}),

		entry(plugins.MutationSpec{
			Action: "chapter.update",
			Title:  "Update a chapter",
			Description: "Proposes a change to a chapter's name, description, tags " +
				"or which book it belongs to.",
			Risk:       operations.RiskLow,
			Reversible: true,
			Verifiable: true,
		}, &containerUpdate{p: p, kind: "chapters"}),

		entry(plugins.MutationSpec{
			Action: "chapter.delete",
			Title:  "Delete a chapter",
			Description: "Proposes sending a chapter, and every page in it, to the " +
				"recycle bin. Says how many pages that is.",
			Risk:       operations.RiskHigh,
			Reversible: true,
			Verifiable: true,
		}, &containerDelete{p: p, kind: "chapters"}),
	}
}

func (p *Plugin) shelfMutations() []mutationEntry {
	return []mutationEntry{
		entry(plugins.MutationSpec{
			Action:      "shelf.create",
			Title:       "Create a shelf",
			Description: "Proposes a new shelf, optionally holding a set of books.",
			Risk:        operations.RiskLow,
			Reversible:  true,
			Verifiable:  true,
		}, &containerCreate{p: p, kind: "shelves"}),

		entry(plugins.MutationSpec{
			Action: "shelf.update",
			Title:  "Update a shelf",
			Description: "Proposes a change to a shelf's name, description, tags or " +
				"the books on it. Setting books replaces the whole set.",
			Risk:       operations.RiskLow,
			Reversible: true,
			Verifiable: true,
		}, &containerUpdate{p: p, kind: "shelves"}),

		entry(plugins.MutationSpec{
			Action: "shelf.delete",
			Title:  "Delete a shelf",
			Description: "Proposes sending a shelf to the recycle bin. The books on " +
				"it are not deleted — a shelf is a grouping, not a container.",
			// Lower than a book delete precisely because nothing goes with it.
			Risk:       operations.RiskMedium,
			Reversible: true,
			Verifiable: true,
		}, &containerDelete{p: p, kind: "shelves"}),
	}
}

// ContainerCreateParams is a new book, chapter or shelf.
type ContainerCreateParams struct {
	Name        string    `json:"name" jsonschema:"what to call it"`
	Description string    `json:"description,omitempty" jsonschema:"a plain-text description"`
	BookID      int       `json:"book_id,omitempty" jsonschema:"for a chapter, the book it belongs to; required there and ignored elsewhere"`
	Books       []int     `json:"books,omitempty" jsonschema:"for a shelf, the ids of the books to put on it, in order"`
	Tags        []tagPair `json:"tags,omitempty" jsonschema:"tags to set; list_tag_names shows the vocabulary already in use"`
}

type containerCreate struct {
	p    *Plugin
	kind string
}

func (h *containerCreate) Plan(ctx context.Context, params ContainerCreateParams) (plugins.Plan[entityState], error) {
	var plan plugins.Plan[entityState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		return plan, fmt.Errorf("bookstack: a %s needs a name", singular(h.kind))
	}
	if h.kind == "chapters" && params.BookID <= 0 {
		return plan, fmt.Errorf("bookstack: a chapter needs the book it goes in; " +
			"list_books reports the ids")
	}

	desired := entityState{
		Exists: true, Name: name, Description: params.Description,
		BookID: params.BookID, Tags: wantTagStrings(params.Tags), Books: params.Books,
	}
	changes := []operations.Change{{Field: singular(h.kind), From: nil, To: name}}
	if params.Description != "" {
		changes = append(changes, operations.Change{Field: "description", From: nil, To: params.Description})
	}
	impact := fmt.Sprintf("Adds a new, empty %s called %q.", singular(h.kind), name)
	if h.kind == "chapters" {
		where, err := h.p.describeParent(ctx, params.BookID, 0)
		if err != nil {
			return plan, err
		}
		changes = append(changes, operations.Change{Field: "book", From: nil, To: where})
		impact = fmt.Sprintf("Adds a new, empty chapter %q to %s.", name, where)
	}
	if h.kind == "shelves" && len(params.Books) > 0 {
		named, err := h.p.describeBooks(ctx, params.Books)
		if err != nil {
			return plan, err
		}
		changes = append(changes, operations.Change{Field: "books", From: nil, To: named})
		impact = fmt.Sprintf("Adds a new shelf %q holding %d book(s). The books "+
			"themselves are unchanged.", name, len(params.Books))
	}
	if len(desired.Tags) > 0 {
		changes = append(changes, operations.Change{Field: "tags", From: nil, To: desired.Tags})
	}

	return plugins.Plan[entityState]{
		Before:        entityState{Exists: false},
		Desired:       desired,
		Preconditions: map[string]any{"exists": false},
		Changes:       changes,
		Impact:        impact,
	}, nil
}

func (h *containerCreate) Apply(ctx context.Context, params ContainerCreateParams, _ plugins.Plan[entityState]) (plugins.ApplyResult, error) {
	payload := map[string]any{"name": strings.TrimSpace(params.Name)}
	if params.Description != "" {
		payload["description"] = params.Description
	}
	if h.kind == "chapters" {
		payload["book_id"] = params.BookID
	}
	if h.kind == "shelves" && params.Books != nil {
		payload["books"] = params.Books
	}
	if len(params.Tags) > 0 {
		payload["tags"] = apiTags(params.Tags)
	}
	raw, err := h.p.client.send(ctx, "POST", "/api/"+h.kind, payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *containerCreate) Observe(ctx context.Context, params ContainerCreateParams) (entityState, error) {
	id, err := h.p.findByName(ctx, h.kind, strings.TrimSpace(params.Name), params.BookID)
	if err != nil || id == 0 {
		return entityState{Exists: false}, err
	}
	return h.p.readEntity(ctx, h.kind, id)
}

// ContainerUpdateParams changes a book, chapter or shelf. What is left out is
// left alone.
type ContainerUpdateParams struct {
	ID          int       `json:"id,omitempty" jsonschema:"the item's numeric id"`
	Slug        string    `json:"slug,omitempty" jsonschema:"the item's slug, if you have that instead"`
	URL         string    `json:"url,omitempty" jsonschema:"a BookStack link to the item"`
	Name        string    `json:"name,omitempty" jsonschema:"a new name; leave out to keep the current one"`
	Description string    `json:"description,omitempty" jsonschema:"a new description"`
	BookID      int       `json:"book_id,omitempty" jsonschema:"for a chapter, move it to this book"`
	Books       []int     `json:"books,omitempty" jsonschema:"for a shelf, replace the books on it with these ids"`
	Tags        []tagPair `json:"tags,omitempty" jsonschema:"replace the tags with these"`
}

type containerUpdate struct {
	p    *Plugin
	kind string
}

func (h *containerUpdate) Plan(ctx context.Context, params ContainerUpdateParams) (plugins.Plan[entityState], error) {
	var plan plugins.Plan[entityState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	id, err := h.p.locate(ctx, h.kind, params.ID, params.Slug, params.URL)
	if err != nil {
		return plan, err
	}
	before, err := h.p.readEntity(ctx, h.kind, id)
	if err != nil {
		return plan, err
	}
	if !before.Exists {
		return plan, fmt.Errorf("bookstack: there is no %s %d to update", singular(h.kind), id)
	}

	desired := before
	changes := []operations.Change{}
	if n := strings.TrimSpace(params.Name); n != "" && n != before.Name {
		desired.Name = n
		changes = diffField(changes, "name", before.Name, n)
	}
	if d := params.Description; d != "" && d != before.Description {
		desired.Description = d
		changes = diffText(changes, "description", before.Description, d)
	}
	if h.kind == "chapters" && params.BookID > 0 && params.BookID != before.BookID {
		desired.BookID = params.BookID
		to, err := h.p.describeParent(ctx, params.BookID, 0)
		if err != nil {
			return plan, err
		}
		from, _ := h.p.describeParent(ctx, before.BookID, 0)
		changes = diffField(changes, "book", from, to)
	}
	if h.kind == "shelves" && params.Books != nil {
		// Whole-set: BookStack replaces the shelf's books with what is sent,
		// so a caller meaning to add one has to send the others too. Saying
		// so in the diff is what stops that mistake being invisible.
		fromNamed, _ := h.p.describeBooks(ctx, before.Books)
		toNamed, err := h.p.describeBooks(ctx, params.Books)
		if err != nil {
			return plan, err
		}
		desired.Books = params.Books
		changes = diffField(changes, "books", fromNamed, toNamed)
	}
	if params.Tags != nil {
		want := wantTagStrings(params.Tags)
		desired.Tags = want
		changes = diffField(changes, "tags",
			strings.Join(before.Tags, ", "), strings.Join(want, ", "))
	}
	if len(changes) == 0 {
		return plan, fmt.Errorf("bookstack: nothing to change on %s %d — the values "+
			"given match what is already there", singular(h.kind), id)
	}

	impact := fmt.Sprintf("Changes %s %q.", singular(h.kind), before.Name)
	if h.kind == "shelves" && params.Books != nil {
		impact += " Setting books replaces the whole set rather than adding to it; " +
			"the books themselves are not changed."
	}
	return plugins.Plan[entityState]{
		Before:        before,
		Desired:       desired,
		Preconditions: preconditionsFor(before),
		Changes:       changes,
		Impact:        impact,
		Rollback: ContainerUpdateParams{
			ID: id, Name: before.Name, Description: before.Description,
			BookID: before.BookID, Books: before.Books,
		},
	}, nil
}

func (h *containerUpdate) Apply(ctx context.Context, params ContainerUpdateParams, plan plugins.Plan[entityState]) (plugins.ApplyResult, error) {
	id := plan.Before.ID
	if id == 0 {
		id = params.ID
	}
	payload := map[string]any{}
	if n := strings.TrimSpace(params.Name); n != "" {
		payload["name"] = n
	}
	if params.Description != "" {
		payload["description"] = params.Description
	}
	if h.kind == "chapters" && params.BookID > 0 {
		payload["book_id"] = params.BookID
	}
	if h.kind == "shelves" && params.Books != nil {
		payload["books"] = params.Books
	}
	if params.Tags != nil {
		payload["tags"] = apiTags(params.Tags)
	}
	raw, err := h.p.client.send(ctx, "PUT", "/api/"+h.kind+"/"+strconv.Itoa(id), payload)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return applied(raw)
}

func (h *containerUpdate) Observe(ctx context.Context, params ContainerUpdateParams) (entityState, error) {
	id, err := h.p.locate(ctx, h.kind, params.ID, params.Slug, params.URL)
	if err != nil {
		return entityState{}, err
	}
	return h.p.readEntity(ctx, h.kind, id)
}

// ContainerDeleteParams names a book, chapter or shelf to remove.
type ContainerDeleteParams struct {
	ID   int    `json:"id,omitempty" jsonschema:"the item's numeric id"`
	Slug string `json:"slug,omitempty" jsonschema:"the item's slug, if you have that instead"`
	URL  string `json:"url,omitempty" jsonschema:"a BookStack link to the item"`
}

type containerDelete struct {
	p    *Plugin
	kind string
}

func (h *containerDelete) Plan(ctx context.Context, params ContainerDeleteParams) (plugins.Plan[entityState], error) {
	var plan plugins.Plan[entityState]
	if err := h.p.mutationReady(); err != nil {
		return plan, err
	}
	id, err := h.p.locate(ctx, h.kind, params.ID, params.Slug, params.URL)
	if err != nil {
		return plan, err
	}
	before, err := h.p.readEntity(ctx, h.kind, id)
	if err != nil {
		return plan, err
	}
	if !before.Exists {
		return plan, fmt.Errorf("bookstack: there is no %s %d to delete", singular(h.kind), id)
	}

	changes := []operations.Change{{Field: singular(h.kind), From: before.Name, To: nil}}
	impact := fmt.Sprintf("Removes %s %q. It goes to the recycle bin, so "+
		"list_recycle_bin will show it and restore_from_recycle_bin can put it "+
		"back.", singular(h.kind), before.Name)

	// How much goes with it. This is the number that changes somebody's mind,
	// and it is not visible from the name.
	switch h.kind {
	case "books", "chapters":
		pages, err := h.p.countPages(ctx, h.kind, id)
		if err != nil {
			return plan, err
		}
		changes = append(changes, operations.Change{
			Field: "pages that go with it", From: pages, To: 0,
		})
		impact = fmt.Sprintf("Removes %s %q and the %d page(s) in it. All of it "+
			"goes to the recycle bin, so it can be restored.",
			singular(h.kind), before.Name, pages)
	case "shelves":
		changes = append(changes, operations.Change{
			Field: "books on it", From: len(before.Books), To: len(before.Books),
		})
		impact = fmt.Sprintf("Removes shelf %q. The %d book(s) on it are not "+
			"deleted — a shelf is a grouping rather than a container.",
			before.Name, len(before.Books))
	}

	return plugins.Plan[entityState]{
		Before:        before,
		Desired:       entityState{Exists: false, ID: id},
		Preconditions: preconditionsFor(before),
		Changes:       changes,
		Impact:        impact,
	}, nil
}

func (h *containerDelete) Apply(ctx context.Context, params ContainerDeleteParams, plan plugins.Plan[entityState]) (plugins.ApplyResult, error) {
	id := plan.Before.ID
	if id == 0 {
		id = params.ID
	}
	_, err := h.p.client.send(ctx, "DELETE", "/api/"+h.kind+"/"+strconv.Itoa(id), nil)
	h.p.noted(err)
	if err != nil {
		return plugins.ApplyResult{}, wrapIndeterminate(err)
	}
	return plugins.ApplyResult{UpstreamRef: strconv.Itoa(id)}, nil
}

func (h *containerDelete) Observe(ctx context.Context, params ContainerDeleteParams) (entityState, error) {
	id := params.ID
	if id == 0 {
		var err error
		id, err = h.p.locate(ctx, h.kind, params.ID, params.Slug, params.URL)
		if err != nil {
			return entityState{Exists: false}, nil
		}
	}
	return h.p.readEntity(ctx, h.kind, id)
}

// --- shared -----------------------------------------------------------------

// findByName looks for something a create has just made.
func (p *Plugin) findByName(ctx context.Context, kind, name string, bookID int) (int, error) {
	q := filterByName(name)
	if bookID > 0 && kind == "chapters" {
		q.Set("filter[book_id]", strconv.Itoa(bookID))
	}
	q.Set("sort", "-created_at")
	pg, err := p.client.list(ctx, "/api/"+kind, q, 5)
	p.noted(err)
	if err != nil {
		return 0, err
	}
	type row struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	rows, err := decodeRows[row](pg)
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

// countPages says how much goes with a book or chapter that is being deleted.
func (p *Plugin) countPages(ctx context.Context, kind string, id int) (int, error) {
	q := filterByName("")
	switch kind {
	case "books":
		q.Set("filter[book_id]", strconv.Itoa(id))
	case "chapters":
		q.Set("filter[chapter_id]", strconv.Itoa(id))
	default:
		return 0, nil
	}
	// One row is enough: the listing reports the total regardless.
	pg, err := p.client.list(ctx, "/api/pages", q, 1)
	p.noted(err)
	if err != nil {
		return 0, err
	}
	return pg.total, nil
}

// describeBooks names a set of book ids, so a shelf's diff is readable.
func (p *Plugin) describeBooks(ctx context.Context, ids []int) (string, error) {
	if len(ids) == 0 {
		return "(none)", nil
	}
	names := make([]string, 0, len(ids))
	for _, id := range ids {
		b, err := p.readEntity(ctx, "books", id)
		if err != nil {
			return "", err
		}
		if !b.Exists {
			return "", fmt.Errorf("bookstack: there is no book %d", id)
		}
		names = append(names, b.Name)
	}
	return strings.Join(names, ", "), nil
}
