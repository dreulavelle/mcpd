package bookstack

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Finding a thing when what somebody has is not its id.
//
// The id is what the API takes and what a listing reports, but it is almost
// never what a person has in hand. What they have is a link they pasted out of
// a browser, or a slug out of one. So every get_ tool here takes any of the
// three, and this is the one place that turns the other two into an id.
//
// A slug is unique within its book rather than across the instance, so a page
// slug on its own can match two pages in two books. That is refused with both
// named rather than guessed at, in the same way an ambiguous customer name is
// refused elsewhere: answering from the wrong book is worse than not
// answering.

// locate turns whatever a caller supplied into an id.
//
// kind is the entity in the API's own words: shelves, books, chapters, pages.
func (p *Plugin) locate(ctx context.Context, kind string, id int, slug, link string) (int, error) {
	if id > 0 {
		return id, nil
	}
	slug = strings.TrimSpace(slug)
	link = strings.TrimSpace(link)

	if link != "" {
		got, err := p.fromURL(ctx, kind, link)
		if err != nil {
			return 0, err
		}
		return got, nil
	}
	if slug != "" {
		return p.bySlug(ctx, kind, slug, 0)
	}
	return 0, fmt.Errorf("bookstack: say which %s, with id, slug or url. A listing "+
		"or search_content reports the id; the url is what you would paste out of "+
		"a browser", singular(kind))
}

// fromURL reads a BookStack link.
//
// The paths are /shelves/{slug}, /books/{slug},
// /books/{book}/chapter/{slug} and /books/{book}/page/{slug}. A page or
// chapter link carries its book, which is what makes the lookup unambiguous --
// so the book is resolved first and the slug looked up within it.
func (p *Plugin) fromURL(ctx context.Context, kind, link string) (int, error) {
	u, err := url.Parse(link)
	if err != nil {
		return 0, fmt.Errorf("bookstack: %q is not a URL", link)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	// An instance served under a sub-path has extra segments in front, so the
	// scan starts at the first segment that names an entity rather than at 0.
	for i, seg := range parts {
		switch seg {
		case "shelves", "bookshelves":
			if i+1 < len(parts) {
				return p.expect(ctx, kind, "shelves", parts[i+1], 0)
			}
		case "books":
			if i+1 >= len(parts) {
				continue
			}
			bookSlug := parts[i+1]
			// /books/{book}/page/{slug} and /books/{book}/chapter/{slug}.
			if i+3 < len(parts) {
				switch parts[i+2] {
				case "page":
					bookID, err := p.bySlug(ctx, "books", bookSlug, 0)
					if err != nil {
						return 0, err
					}
					return p.expect(ctx, kind, "pages", parts[i+3], bookID)
				case "chapter":
					bookID, err := p.bySlug(ctx, "books", bookSlug, 0)
					if err != nil {
						return 0, err
					}
					return p.expect(ctx, kind, "chapters", parts[i+3], bookID)
				}
			}
			return p.expect(ctx, kind, "books", bookSlug, 0)
		}
	}
	return 0, fmt.Errorf("bookstack: %q does not look like a link to a shelf, book, "+
		"chapter or page. Those look like /books/some-book/page/some-page", link)
}

// expect resolves a slug, refusing a link that points at the wrong kind of
// thing -- a book link handed to get_page, say, which would otherwise read
// whatever page happened to share the slug.
func (p *Plugin) expect(ctx context.Context, want, got, slug string, bookID int) (int, error) {
	if want != got {
		return 0, fmt.Errorf("bookstack: that link points at a %s, not a %s. Use "+
			"get_%s for it", singular(got), singular(want), singular(got))
	}
	return p.bySlug(ctx, got, slug, bookID)
}

// bySlug looks one thing up by its slug, within a book when there is one.
func (p *Plugin) bySlug(ctx context.Context, kind, slug string, bookID int) (int, error) {
	q := url.Values{}
	q.Set("filter[slug]", slug)
	if bookID > 0 {
		q.Set("filter[book_id]", strconv.Itoa(bookID))
	}
	// Three rather than one: enough to tell "found it" from "this slug is
	// ambiguous", which are different answers.
	pg, err := p.client.list(ctx, "/api/"+kind, q, 3)
	p.noted(err)
	if err != nil {
		return 0, err
	}
	type row struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		BookID int    `json:"book_id"`
	}
	rows := make([]row, 0, len(pg.rows))
	for _, raw := range pg.rows {
		var r row
		if err := json.Unmarshal(raw, &r); err != nil {
			return 0, fmt.Errorf("bookstack: could not read the lookup result: %w", err)
		}
		rows = append(rows, r)
	}
	switch len(rows) {
	case 1:
		return rows[0].ID, nil
	case 0:
		return 0, fmt.Errorf("bookstack: no %s has the slug %q, or the token's user "+
			"cannot see it", singular(kind), slug)
	}
	// A slug is unique within a book, not across the instance. Naming the
	// books is what lets the caller ask again with the one they meant.
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, fmt.Sprintf("id %d (in book %d)", r.ID, r.BookID))
	}
	return 0, fmt.Errorf("bookstack: the slug %q matches more than one %s: %s. Do "+
		"not pick one -- ask again with the id, or with the full url which carries "+
		"the book", slug, singular(kind), strings.Join(names, ", "))
}

// singular is the API's plural turned into the word an error should use.
func singular(kind string) string {
	switch kind {
	case "shelves":
		return "shelf"
	case "books":
		return "book"
	case "chapters":
		return "chapter"
	case "pages":
		return "page"
	}
	return strings.TrimSuffix(kind, "s")
}

// sortOrders are the listing orders BookStack accepts here.
//
// A closed set because BookStack ignores a sort it does not recognise and
// answers 200: a caller's typo would silently return a different order than
// they asked for, and a truncated listing in the wrong order is a wrong
// answer rather than a partial one.
var sortOrders = map[string]bool{
	"-updated_at": true, "updated_at": true,
	"-created_at": true, "created_at": true,
	"name": true, "-name": true,
}

// defaultSort puts the most recently changed first.
//
// It matters most exactly when a listing is truncated: with a ceiling of
// twenty over three hundred pages, "the twenty most recently touched" is an
// answer and "twenty arbitrary pages" is not.
const defaultSort = "-updated_at"

// applySort validates a caller's order and sets it on the query.
func applySort(q url.Values, order string) error {
	order = strings.TrimSpace(order)
	if order == "" {
		order = defaultSort
	}
	if !sortOrders[order] {
		return fmt.Errorf("bookstack: %q is not an order this accepts; use "+
			"-updated_at, updated_at, -created_at, created_at, name or -name", order)
	}
	q.Set("sort", order)
	return nil
}
