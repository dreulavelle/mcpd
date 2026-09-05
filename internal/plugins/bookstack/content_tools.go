package bookstack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/plugins"
)

// The tools for reading the knowledge base itself: shelves hold books, books
// hold chapters and pages, chapters hold pages.
//
// Listings are deliberately shallow -- a page row says where it lives and when
// it was touched, not what it says -- because the question "what do we have
// written down about X" is answered by search_content, and pulling the body of
// every page to answer it would fill a context window with the wrong pages.

func (p *Plugin) registerContentTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_shelves",
		Title: "List shelves",
		Description: "The shelves in the knowledge base. A shelf is a grouping " +
			"of books; get_shelf says which books are on one.",
		Idempotent: true,
	}, p.listShelves)

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "get_shelf",
		Title:       "Get one shelf",
		Description: "One shelf, with the books on it.",
		Idempotent:  true,
	}, p.getShelf)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_books",
		Title: "List books",
		Description: "The books in the knowledge base, newest activity first. " +
			"Narrow with query to match a name.",
		Idempotent: true,
	}, p.listBooks)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_book",
		Title: "Get one book",
		Description: "One book, with its chapters and pages in order — the " +
			"table of contents, not the text.",
		Idempotent: true,
	}, p.getBook)

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "list_chapters",
		Title:       "List chapters",
		Description: "Chapters, optionally only those in one book.",
		Idempotent:  true,
	}, p.listChapters)

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "get_chapter",
		Title:       "Get one chapter",
		Description: "One chapter, with the pages in it.",
		Idempotent:  true,
	}, p.getChapter)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_pages",
		Title: "List pages",
		Description: "Pages, optionally only those in one book or chapter. Says " +
			"where each lives and when it was last changed, not what it says — " +
			"use get_page for the text, or search_content to find it by content.",
		Idempotent: true,
	}, p.listPages)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_page",
		Title: "Get one page",
		Description: "One page with its text. format chooses markdown (the " +
			"default), html, or plaintext. Long pages are cut and say so.",
		Idempotent: true,
	}, p.getPage)
}

// --- shelves ----------------------------------------------------------------

type shelfRow struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	OwnedBy     int    `json:"owned_by"`
}

// ShelfRow is one shelf as a list shows it.
type ShelfRow struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug,omitempty"`
	Description string `json:"description,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// ShelvesResult is the shelf list.
type ShelvesResult struct {
	// Notes say what to call next. A listing carries no text, and a model that
	// is not told so will answer from the names alone.
	Notes   []string   `json:"notes,omitempty"`
	Shelves []ShelfRow `json:"shelves"`
	Count   int        `json:"count"`
	truncation
}

type listArgs struct {
	Query string `json:"query,omitempty" jsonschema:"only items whose name contains this"`
	Sort  string `json:"sort,omitempty" jsonschema:"order: -updated_at (default, most recently changed first), updated_at, -created_at, created_at, name or -name"`
	Limit int    `json:"limit,omitempty" jsonschema:"most rows to return; the instance's ceiling applies"`
}

func (p *Plugin) listShelves(ctx context.Context, args listArgs) (ShelvesResult, error) {
	if err := p.ready(); err != nil {
		return ShelvesResult{}, err
	}
	q := filterByName(args.Query)
	if err := applySort(q, args.Sort); err != nil {
		return ShelvesResult{}, err
	}
	pg, err := p.client.list(ctx, "/api/shelves", q, args.Limit)
	p.noted(err)
	if err != nil {
		return ShelvesResult{}, err
	}
	raw, err := decodeRows[shelfRow](pg)
	if err != nil {
		return ShelvesResult{}, err
	}
	rows := make([]ShelfRow, 0, len(raw))
	for _, s := range raw {
		rows = append(rows, ShelfRow{
			ID: s.ID, Name: s.Name, Slug: s.Slug,
			Description: s.Description, UpdatedAt: s.UpdatedAt,
		})
	}
	rows, cut := bound(rows, pg)
	out := ShelvesResult{Shelves: rows, Count: len(rows), truncation: cut}
	if len(rows) > 0 {
		out.Notes = append(out.Notes, "get_shelf lists the books on one of these.")
	}
	out.Notes = append(out.Notes, narrowing(cut)...)
	return out, nil
}

// idArgs names one thing, by whichever of the three the caller has.
//
// The id is what a listing reports; the url is what somebody pastes out of a
// browser; the slug is what is in that url. Any one of them is enough.
type idArgs struct {
	ID   int    `json:"id,omitempty" jsonschema:"the item's numeric id, as a listing or search_content reports it"`
	Slug string `json:"slug,omitempty" jsonschema:"the item's slug, if you have that instead of an id"`
	URL  string `json:"url,omitempty" jsonschema:"a BookStack link to the item, as pasted from a browser"`
}

// ShelfDetail is one shelf and the books on it.
type ShelfDetail struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Slug        string    `json:"slug,omitempty"`
	Description string    `json:"description,omitempty"`
	Books       []ItemRef `json:"books"`
	Tags        []tag     `json:"tags,omitempty"`
	CreatedAt   string    `json:"created_at,omitempty"`
	UpdatedAt   string    `json:"updated_at,omitempty"`
	OwnedBy     *userRef  `json:"owned_by,omitempty"`
	UpdatedBy   *userRef  `json:"updated_by,omitempty"`
}

// ItemRef names one thing inside another: a book on a shelf, a page in a book.
type ItemRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
	Type string `json:"type,omitempty"`
	// URL is where a person opens it, which is what somebody asks for once
	// they have the answer.
	URL string `json:"url,omitempty"`
}

func (p *Plugin) getShelf(ctx context.Context, args idArgs) (ShelfDetail, error) {
	if err := p.ready(); err != nil {
		return ShelfDetail{}, err
	}
	id, err := p.locate(ctx, "shelves", args.ID, args.Slug, args.URL)
	if err != nil {
		return ShelfDetail{}, err
	}
	raw, err := p.client.get(ctx, "/api/shelves/"+strconv.Itoa(id), nil)
	p.noted(err)
	if err != nil {
		return ShelfDetail{}, describeMissing(err, "shelf", id)
	}
	var d struct {
		ID          int      `json:"id"`
		Name        string   `json:"name"`
		Slug        string   `json:"slug"`
		Description string   `json:"description"`
		CreatedAt   string   `json:"created_at"`
		UpdatedAt   string   `json:"updated_at"`
		OwnedBy     *userRef `json:"owned_by"`
		UpdatedBy   *userRef `json:"updated_by"`
		Tags        []tag    `json:"tags"`
		Books       []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		} `json:"books"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return ShelfDetail{}, fmt.Errorf("bookstack: could not read the shelf: %w", err)
	}
	out := ShelfDetail{
		ID: d.ID, Name: d.Name, Slug: d.Slug, Description: d.Description,
		Tags: d.Tags, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		OwnedBy: d.OwnedBy, UpdatedBy: d.UpdatedBy,
		Books: make([]ItemRef, 0, len(d.Books)),
	}
	for _, b := range d.Books {
		out.Books = append(out.Books, ItemRef{ID: b.ID, Name: b.Name, Slug: b.Slug, Type: "book"})
	}
	return out, nil
}

// --- books ------------------------------------------------------------------

type bookRow struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	OwnedBy     int    `json:"owned_by"`
}

// BookRow is one book as a list shows it.
type BookRow struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug,omitempty"`
	Description string `json:"description,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// BooksResult is the book list.
type BooksResult struct {
	// Notes say what to call next. A listing carries no text, and a model that
	// is not told so will answer from the names alone.
	Notes []string  `json:"notes,omitempty"`
	Books []BookRow `json:"books"`
	Count int       `json:"count"`
	truncation
}

func (p *Plugin) listBooks(ctx context.Context, args listArgs) (BooksResult, error) {
	if err := p.ready(); err != nil {
		return BooksResult{}, err
	}
	q := filterByName(args.Query)
	if err := applySort(q, args.Sort); err != nil {
		return BooksResult{}, err
	}
	pg, err := p.client.list(ctx, "/api/books", q, args.Limit)
	p.noted(err)
	if err != nil {
		return BooksResult{}, err
	}
	raw, err := decodeRows[bookRow](pg)
	if err != nil {
		return BooksResult{}, err
	}
	rows := make([]BookRow, 0, len(raw))
	for _, b := range raw {
		rows = append(rows, BookRow{
			ID: b.ID, Name: b.Name, Slug: b.Slug,
			Description: b.Description, UpdatedAt: b.UpdatedAt,
		})
	}
	rows, cut := bound(rows, pg)
	out := BooksResult{Books: rows, Count: len(rows), truncation: cut}
	if len(rows) > 0 {
		out.Notes = append(out.Notes,
			"get_book returns a book's table of contents; get_page reads a page's text.",
			"To find something by what it says rather than by title, use search_content.")
	}
	out.Notes = append(out.Notes, narrowing(cut)...)
	return out, nil
}

// BookDetail is one book and what is in it.
type BookDetail struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Slug        string   `json:"slug,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []tag    `json:"tags,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
	OwnedBy     *userRef `json:"owned_by,omitempty"`
	UpdatedBy   *userRef `json:"updated_by,omitempty"`
	// Contents is the table of contents in display order: chapters with their
	// pages, and the pages that sit directly in the book.
	Contents []ContentItem `json:"contents"`
}

// ContentItem is one entry in a book's table of contents: a chapter, or a page
// sitting directly in the book.
//
// Pages holds a distinct type rather than nesting ContentItem inside itself.
// BookStack nests exactly one level -- a chapter contains pages and a page
// contains nothing -- so the recursion would describe a shape that cannot
// occur, and a self-referential type has no finite JSON schema for a model to
// read.
type ContentItem struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Slug  string `json:"slug,omitempty"`
	URL   string `json:"url,omitempty"`
	Draft bool   `json:"draft,omitempty"`
	// Pages are the pages inside a chapter. Empty for a page.
	Pages []PageItem `json:"pages,omitempty"`
}

// PageItem is one page inside a chapter.
type PageItem struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug,omitempty"`
	URL   string `json:"url,omitempty"`
	Draft bool   `json:"draft,omitempty"`
}

func (p *Plugin) getBook(ctx context.Context, args idArgs) (BookDetail, error) {
	if err := p.ready(); err != nil {
		return BookDetail{}, err
	}
	id, err := p.locate(ctx, "books", args.ID, args.Slug, args.URL)
	if err != nil {
		return BookDetail{}, err
	}
	raw, err := p.client.get(ctx, "/api/books/"+strconv.Itoa(id), nil)
	p.noted(err)
	if err != nil {
		return BookDetail{}, describeMissing(err, "book", id)
	}
	var d struct {
		ID          int           `json:"id"`
		Name        string        `json:"name"`
		Slug        string        `json:"slug"`
		Description string        `json:"description"`
		CreatedAt   string        `json:"created_at"`
		UpdatedAt   string        `json:"updated_at"`
		OwnedBy     *userRef      `json:"owned_by"`
		UpdatedBy   *userRef      `json:"updated_by"`
		Tags        []tag         `json:"tags"`
		Contents    []contentNode `json:"contents"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return BookDetail{}, fmt.Errorf("bookstack: could not read the book: %w", err)
	}
	return BookDetail{
		ID: d.ID, Name: d.Name, Slug: d.Slug, Description: d.Description,
		Tags: d.Tags, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		OwnedBy: d.OwnedBy, UpdatedBy: d.UpdatedBy,
		Contents: toContents(d.Contents),
	}, nil
}

// contentNode is a book's contents entry as BookStack sends it.
type contentNode struct {
	ID    int           `json:"id"`
	Name  string        `json:"name"`
	Slug  string        `json:"slug"`
	Type  string        `json:"type"`
	URL   string        `json:"url"`
	Draft bool          `json:"draft"`
	Pages []contentNode `json:"pages"`
}

func toContents(in []contentNode) []ContentItem {
	out := make([]ContentItem, 0, len(in))
	for _, n := range in {
		item := ContentItem{
			ID: n.ID, Name: n.Name, Type: n.Type,
			Slug: n.Slug, URL: n.URL, Draft: n.Draft,
		}
		for _, pg := range n.Pages {
			item.Pages = append(item.Pages, PageItem{
				ID: pg.ID, Name: pg.Name, Slug: pg.Slug, URL: pg.URL, Draft: pg.Draft,
			})
		}
		out = append(out, item)
	}
	return out
}

// --- chapters ---------------------------------------------------------------

type chapterRow struct {
	ID          int    `json:"id"`
	BookID      int    `json:"book_id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	BookSlug    string `json:"book_slug"`
}

// ChapterRow is one chapter as a list shows it.
type ChapterRow struct {
	ID          int    `json:"id"`
	BookID      int    `json:"book_id"`
	Name        string `json:"name"`
	Slug        string `json:"slug,omitempty"`
	Description string `json:"description,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// ChaptersResult is the chapter list.
type ChaptersResult struct {
	// Notes say what to call next. A listing carries no text, and a model that
	// is not told so will answer from the names alone.
	Notes    []string     `json:"notes,omitempty"`
	Chapters []ChapterRow `json:"chapters"`
	Count    int          `json:"count"`
	truncation
}

type inBookArgs struct {
	BookID int    `json:"book_id,omitempty" jsonschema:"only items in this book"`
	Query  string `json:"query,omitempty" jsonschema:"only items whose name contains this"`
	Sort   string `json:"sort,omitempty" jsonschema:"order: -updated_at (default, most recently changed first), updated_at, -created_at, created_at, name or -name"`
	Limit  int    `json:"limit,omitempty" jsonschema:"most rows to return; the instance's ceiling applies"`
}

func (p *Plugin) listChapters(ctx context.Context, args inBookArgs) (ChaptersResult, error) {
	if err := p.ready(); err != nil {
		return ChaptersResult{}, err
	}
	q := filterByName(args.Query)
	if err := applySort(q, args.Sort); err != nil {
		return ChaptersResult{}, err
	}
	if args.BookID > 0 {
		q.Set("filter[book_id]", strconv.Itoa(args.BookID))
	}
	pg, err := p.client.list(ctx, "/api/chapters", q, args.Limit)
	p.noted(err)
	if err != nil {
		return ChaptersResult{}, err
	}
	raw, err := decodeRows[chapterRow](pg)
	if err != nil {
		return ChaptersResult{}, err
	}
	rows := make([]ChapterRow, 0, len(raw))
	for _, c := range raw {
		rows = append(rows, ChapterRow{
			ID: c.ID, BookID: c.BookID, Name: c.Name, Slug: c.Slug,
			Description: c.Description, UpdatedAt: c.UpdatedAt,
		})
	}
	rows, cut := bound(rows, pg)
	out := ChaptersResult{Chapters: rows, Count: len(rows), truncation: cut}
	if len(rows) > 0 {
		out.Notes = append(out.Notes, "get_chapter lists the pages in one of these.")
	} else if args.BookID > 0 {
		// Not every book has chapters: pages can sit straight in a book, and
		// an empty answer here reads as "this book is empty" when it is not.
		out.Notes = append(out.Notes, "No chapters here. Pages can sit directly "+
			"in a book without one, so try list_pages with the same book_id.")
	}
	out.Notes = append(out.Notes, narrowing(cut)...)
	return out, nil
}

// ChapterDetail is one chapter and the pages in it.
type ChapterDetail struct {
	ID          int        `json:"id"`
	BookID      int        `json:"book_id"`
	Name        string     `json:"name"`
	Slug        string     `json:"slug,omitempty"`
	Description string     `json:"description,omitempty"`
	Tags        []tag      `json:"tags,omitempty"`
	CreatedAt   string     `json:"created_at,omitempty"`
	UpdatedAt   string     `json:"updated_at,omitempty"`
	OwnedBy     *userRef   `json:"owned_by,omitempty"`
	UpdatedBy   *userRef   `json:"updated_by,omitempty"`
	Pages       []PageItem `json:"pages"`
}

func (p *Plugin) getChapter(ctx context.Context, args idArgs) (ChapterDetail, error) {
	if err := p.ready(); err != nil {
		return ChapterDetail{}, err
	}
	id, err := p.locate(ctx, "chapters", args.ID, args.Slug, args.URL)
	if err != nil {
		return ChapterDetail{}, err
	}
	raw, err := p.client.get(ctx, "/api/chapters/"+strconv.Itoa(id), nil)
	p.noted(err)
	if err != nil {
		return ChapterDetail{}, describeMissing(err, "chapter", id)
	}
	var d struct {
		ID          int           `json:"id"`
		BookID      int           `json:"book_id"`
		Name        string        `json:"name"`
		Slug        string        `json:"slug"`
		Description string        `json:"description"`
		CreatedAt   string        `json:"created_at"`
		UpdatedAt   string        `json:"updated_at"`
		OwnedBy     *userRef      `json:"owned_by"`
		UpdatedBy   *userRef      `json:"updated_by"`
		Tags        []tag         `json:"tags"`
		Pages       []contentNode `json:"pages"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return ChapterDetail{}, fmt.Errorf("bookstack: could not read the chapter: %w", err)
	}
	out := ChapterDetail{
		ID: d.ID, BookID: d.BookID, Name: d.Name, Slug: d.Slug,
		Description: d.Description, Tags: d.Tags,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		OwnedBy: d.OwnedBy, UpdatedBy: d.UpdatedBy,
	}
	for _, pg := range d.Pages {
		out.Pages = append(out.Pages, PageItem{
			ID: pg.ID, Name: pg.Name, Slug: pg.Slug, URL: pg.URL, Draft: pg.Draft,
		})
	}
	return out, nil
}

// --- pages ------------------------------------------------------------------

type pageRow struct {
	ID            int    `json:"id"`
	BookID        int    `json:"book_id"`
	ChapterID     int    `json:"chapter_id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Draft         bool   `json:"draft"`
	Template      bool   `json:"template"`
	Priority      int    `json:"priority"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	BookSlug      string `json:"book_slug"`
	RevisionCount int    `json:"revision_count"`
	Editor        string `json:"editor"`
}

// PageRow is one page as a list shows it: where it lives and when it moved,
// not what it says.
type PageRow struct {
	ID        int    `json:"id"`
	BookID    int    `json:"book_id"`
	ChapterID int    `json:"chapter_id,omitempty"`
	Name      string `json:"name"`
	Slug      string `json:"slug,omitempty"`
	// Draft marks a page that has never been published. It is worth carrying
	// because a draft is invisible to everybody but its author, which is the
	// answer to "I wrote that up, why can nobody find it".
	Draft         bool   `json:"draft,omitempty"`
	Template      bool   `json:"template,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	RevisionCount int    `json:"revision_count,omitempty"`
}

// PagesResult is the page list.
type PagesResult struct {
	// Notes say what to call next. A listing carries no text, and a model that
	// is not told so will answer from the names alone.
	Notes []string  `json:"notes,omitempty"`
	Pages []PageRow `json:"pages"`
	Count int       `json:"count"`
	truncation
}

type pagesArgs struct {
	BookID    int    `json:"book_id,omitempty" jsonschema:"only pages in this book"`
	ChapterID int    `json:"chapter_id,omitempty" jsonschema:"only pages in this chapter"`
	Query     string `json:"query,omitempty" jsonschema:"only pages whose name contains this; matches the title only, not the text — use search_content to match on what a page says"`
	Sort      string `json:"sort,omitempty" jsonschema:"order: -updated_at (default, most recently changed first), updated_at, -created_at, created_at, name or -name"`
	Limit     int    `json:"limit,omitempty" jsonschema:"most rows to return; the instance's ceiling applies"`
}

func (p *Plugin) listPages(ctx context.Context, args pagesArgs) (PagesResult, error) {
	if err := p.ready(); err != nil {
		return PagesResult{}, err
	}
	q := filterByName(args.Query)
	if err := applySort(q, args.Sort); err != nil {
		return PagesResult{}, err
	}
	if args.BookID > 0 {
		q.Set("filter[book_id]", strconv.Itoa(args.BookID))
	}
	if args.ChapterID > 0 {
		q.Set("filter[chapter_id]", strconv.Itoa(args.ChapterID))
	}
	pg, err := p.client.list(ctx, "/api/pages", q, args.Limit)
	p.noted(err)
	if err != nil {
		return PagesResult{}, err
	}
	raw, err := decodeRows[pageRow](pg)
	if err != nil {
		return PagesResult{}, err
	}
	rows := make([]PageRow, 0, len(raw))
	for _, pg := range raw {
		rows = append(rows, PageRow{
			ID: pg.ID, BookID: pg.BookID, ChapterID: pg.ChapterID,
			Name: pg.Name, Slug: pg.Slug, Draft: pg.Draft, Template: pg.Template,
			UpdatedAt: pg.UpdatedAt, RevisionCount: pg.RevisionCount,
		})
	}
	rows, cut := bound(rows, pg)
	out := PagesResult{Pages: rows, Count: len(rows), truncation: cut}
	if len(rows) > 0 {
		// The note this whole shape exists for. These rows are titles and
		// locations; nothing here says what any page contains, and a model
		// that is not told so will answer the question from the titles.
		out.Notes = append(out.Notes,
			"These rows carry no page text. Call get_page with an id to read one.",
			"They were matched on title only. To find pages by what they say, use search_content.")
		drafts := 0
		for _, r := range rows {
			if r.Draft {
				drafts++
			}
		}
		if drafts > 0 {
			// A draft is invisible to everybody but its author, which is the
			// answer to "I wrote that up, why can nobody find it".
			out.Notes = append(out.Notes, fmt.Sprintf(
				"%d of these are drafts: unpublished, and visible only to whoever wrote them.", drafts))
		}
	}
	out.Notes = append(out.Notes, narrowing(cut)...)
	return out, nil
}

type getPageArgs struct {
	ID     int    `json:"id,omitempty" jsonschema:"the page's numeric id, as a listing or search_content reports it"`
	Slug   string `json:"slug,omitempty" jsonschema:"the page's slug, if you have that instead of an id"`
	URL    string `json:"url,omitempty" jsonschema:"a BookStack link to the page, as pasted from a browser"`
	Format string `json:"format,omitempty" jsonschema:"markdown (default), html, or plaintext"`
}

// PageDetail is one page with its text.
type PageDetail struct {
	ID        int      `json:"id"`
	BookID    int      `json:"book_id"`
	ChapterID int      `json:"chapter_id,omitempty"`
	Name      string   `json:"name"`
	Slug      string   `json:"slug,omitempty"`
	Draft     bool     `json:"draft,omitempty"`
	Template  bool     `json:"template,omitempty"`
	Tags      []tag    `json:"tags,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	OwnedBy   *userRef `json:"owned_by,omitempty"`
	UpdatedBy *userRef `json:"updated_by,omitempty"`
	// RevisionCount is how many times this page has been saved. It is the
	// number an update's drift check compares, so it is worth showing.
	RevisionCount int `json:"revision_count,omitempty"`
	// Editor is markdown or wysiwyg. It decides which field an update should
	// send: writing markdown to a wysiwyg page discards the formatting.
	Editor  string `json:"editor,omitempty"`
	Format  string `json:"format"`
	Content string `json:"content"`
	// ContentTruncated says the text was cut. Half a procedure presented as a
	// whole one is worse than an answer that admits it stopped.
	ContentTruncated bool `json:"content_truncated,omitempty"`
}

func (p *Plugin) getPage(ctx context.Context, args getPageArgs) (PageDetail, error) {
	if err := p.ready(); err != nil {
		return PageDetail{}, err
	}
	id, err := p.locate(ctx, "pages", args.ID, args.Slug, args.URL)
	if err != nil {
		return PageDetail{}, err
	}
	format := strings.ToLower(strings.TrimSpace(args.Format))
	if format == "" {
		format = "markdown"
	}
	switch format {
	case "markdown", "html", "plaintext":
	default:
		return PageDetail{}, fmt.Errorf("bookstack: format %q is not one this reads; "+
			"use markdown, html or plaintext", args.Format)
	}

	raw, err := p.client.get(ctx, "/api/pages/"+strconv.Itoa(id), nil)
	p.noted(err)
	if err != nil {
		return PageDetail{}, describeMissing(err, "page", id)
	}
	var d struct {
		ID            int      `json:"id"`
		BookID        int      `json:"book_id"`
		ChapterID     int      `json:"chapter_id"`
		Name          string   `json:"name"`
		Slug          string   `json:"slug"`
		Draft         bool     `json:"draft"`
		Template      bool     `json:"template"`
		CreatedAt     string   `json:"created_at"`
		UpdatedAt     string   `json:"updated_at"`
		OwnedBy       *userRef `json:"owned_by"`
		UpdatedBy     *userRef `json:"updated_by"`
		Tags          []tag    `json:"tags"`
		RevisionCount int      `json:"revision_count"`
		Editor        string   `json:"editor"`
		HTML          string   `json:"html"`
		Markdown      string   `json:"markdown"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return PageDetail{}, fmt.Errorf("bookstack: could not read the page: %w", err)
	}

	out := PageDetail{
		ID: d.ID, BookID: d.BookID, ChapterID: d.ChapterID, Name: d.Name,
		Slug: d.Slug, Draft: d.Draft, Template: d.Template, Tags: d.Tags,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		OwnedBy: d.OwnedBy, UpdatedBy: d.UpdatedBy,
		RevisionCount: d.RevisionCount, Editor: d.Editor, Format: format,
	}

	switch format {
	case "html":
		out.Content = d.HTML
	case "markdown":
		// A page written in the WYSIWYG editor has no markdown of its own;
		// BookStack sends an empty string rather than converting. Falling back
		// to the HTML is better than answering with nothing, and saying which
		// was returned is what stops an update being written back in the wrong
		// form.
		if strings.TrimSpace(d.Markdown) == "" {
			out.Content, out.Format = d.HTML, "html"
		} else {
			out.Content = d.Markdown
		}
	case "plaintext":
		text, err := p.client.get(ctx, "/api/pages/"+strconv.Itoa(id)+"/export/plaintext", nil)
		p.noted(err)
		if err != nil {
			return PageDetail{}, err
		}
		out.Content = string(text)
	}
	out.Content, out.ContentTruncated = clip(out.Content)
	return out, nil
}

// --- shared -----------------------------------------------------------------

// filterByName builds the name filter BookStack listings take.
//
// The API's filter syntax is filter[field:operator]=value, and `like` with %
// wildcards is what "contains" means. Without the wildcards it is an exact
// match, which is never what somebody typing a partial name wants.
func filterByName(query string) url.Values {
	q := url.Values{}
	if s := strings.TrimSpace(query); s != "" {
		q.Set("filter[name:like]", "%"+s+"%")
	}
	return q
}

// describeMissing turns a 404 into a sentence naming what was looked for.
func describeMissing(err error, kind string, id int) error {
	if isNotFound(err) {
		return fmt.Errorf("bookstack: there is no %s with id %d, or the token's "+
			"user cannot see it", kind, id)
	}
	return err
}
