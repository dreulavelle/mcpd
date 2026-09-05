package bookstack

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// Run against a real BookStack. Skipped unless one is supplied, so it costs
// nothing in CI and is there when somebody has an instance:
//
//	BOOKSTACK_TEST_HOST=http://10.0.0.1 BOOKSTACK_TEST_TOKEN_ID=… \
//	  BOOKSTACK_TEST_TOKEN_SECRET=… \
//	  go test ./internal/plugins/bookstack/ -run Integration -v
//
// This is the half a fake cannot reach. The fixtures in this package answer
// with what the API documentation says; these prove the instance agrees.
//
// Every test here reads. Nothing in this file creates, changes or deletes
// anything, because the instance somebody runs it against is their real
// knowledge base.
func integrationPlugin(t *testing.T) *Plugin {
	t.Helper()
	host := os.Getenv("BOOKSTACK_TEST_HOST")
	id := os.Getenv("BOOKSTACK_TEST_TOKEN_ID")
	secret := os.Getenv("BOOKSTACK_TEST_TOKEN_SECRET")
	if host == "" || id == "" || secret == "" {
		t.Skip("set BOOKSTACK_TEST_HOST, BOOKSTACK_TEST_TOKEN_ID and BOOKSTACK_TEST_TOKEN_SECRET to run against a real instance")
	}
	p, err := New(plugins.Deps{
		Instance: "bookstack",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:      time.Now,
	}, Config{Host: host, TokenID: id, TokenSecret: secret})
	if err != nil {
		t.Fatalf("building the plugin: %v", err)
	}
	return p
}

func TestIntegrationReadsTheKnowledgeBase(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := p.Check(ctx); got.State != plugins.HealthState("healthy") {
		t.Fatalf("Check after a successful start: %+v", got)
	}

	info, err := p.getSystem(ctx, systemArgs{})
	if err != nil {
		t.Fatalf("get_system: %v", err)
	}
	if info.Version == "" {
		t.Fatal("the instance reported no version")
	}
	t.Logf("instance is BookStack %s", info.Version)

	shelves, err := p.listShelves(ctx, listArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_shelves: %v", err)
	}
	t.Logf("list_shelves returned %d of %d", shelves.Count, shelves.Total)

	books, err := p.listBooks(ctx, listArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_books: %v", err)
	}
	t.Logf("list_books returned %d of %d", books.Count, books.Total)
	for _, b := range books.Books {
		if b.ID == 0 || b.Name == "" {
			t.Fatal("a book row came back without an id or a name")
		}
	}

	// The table of contents, which is the shape most likely to differ between
	// the documentation and a real instance.
	if books.Count > 0 {
		book, err := p.getBook(ctx, idArgs{ID: books.Books[0].ID})
		if err != nil {
			t.Fatalf("get_book: %v", err)
		}
		if book.ID != books.Books[0].ID {
			t.Fatal("get_book answered about a different book")
		}
		t.Logf("get_book returned %d contents entries", len(book.Contents))
	}

	chapters, err := p.listChapters(ctx, inBookArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_chapters: %v", err)
	}
	t.Logf("list_chapters returned %d of %d", chapters.Count, chapters.Total)

	pages, err := p.listPages(ctx, pagesArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_pages: %v", err)
	}
	t.Logf("list_pages returned %d of %d", pages.Count, pages.Total)

	// One page in each format. The markdown case matters most: a page written
	// in the WYSIWYG editor has no markdown, and this is the assertion that
	// the fallback says so rather than answering with an empty body.
	if pages.Count > 0 {
		id := pages.Pages[0].ID
		for _, format := range []string{"markdown", "html", "plaintext"} {
			got, err := p.getPage(ctx, getPageArgs{ID: id, Format: format})
			if err != nil {
				t.Fatalf("get_page(%s): %v", format, err)
			}
			if got.ID != id {
				t.Fatalf("get_page(%s) answered about a different page", format)
			}
			if got.Format == "" {
				t.Fatalf("get_page(%s) did not say which format it returned", format)
			}
			t.Logf("get_page(%s) -> format %s, %d bytes, truncated=%v",
				format, got.Format, len(got.Content), got.ContentTruncated)
		}
	}

	// Search is the only read that goes through BookStack's index rather than
	// a listing, and the result shape differs per hit type.
	hits, err := p.searchContent(ctx, searchArgs{Query: "the", Limit: 5})
	if err != nil {
		t.Fatalf("search_content: %v", err)
	}
	t.Logf("search_content returned %d of %d", hits.Count, hits.Total)
	for _, h := range hits.Results {
		if h.Type == "" || h.ID == 0 {
			t.Fatal("a search hit came back without a type or an id")
		}
	}

	tags, err := p.listTagNames(ctx, limitArgs{Limit: 10})
	if err != nil {
		t.Fatalf("list_tag_names: %v", err)
	}
	t.Logf("list_tag_names returned %d", tags.Count)
	if tags.Count > 0 {
		values, err := p.listTagValues(ctx, tagValuesArgs{Name: tags.Tags[0].Name, Limit: 5})
		if err != nil {
			t.Fatalf("list_tag_values: %v", err)
		}
		t.Logf("list_tag_values returned %d", values.Count)
	}

	comments, err := p.listComments(ctx, onPageArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_comments: %v", err)
	}
	t.Logf("list_comments returned %d", comments.Count)

	attachments, err := p.listAttachments(ctx, onPageArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_attachments: %v", err)
	}
	t.Logf("list_attachments returned %d", attachments.Count)

	images, err := p.listImages(ctx, limitArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_images: %v", err)
	}
	t.Logf("list_images returned %d", images.Count)
}

// A person almost never has an id. They have a link they pasted, or a slug out
// of one. These are the paths that turn those into a read.
func TestIntegrationFindsThingsWithoutAnID(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	books, err := p.listBooks(ctx, listArgs{Limit: 1})
	if err != nil {
		t.Fatalf("list_books: %v", err)
	}
	if books.Count == 0 {
		t.Skip("the instance holds no books")
	}
	book := books.Books[0]

	// By slug.
	bySlug, err := p.getBook(ctx, idArgs{Slug: book.Slug})
	if err != nil {
		t.Fatalf("get_book by slug: %v", err)
	}
	if bySlug.ID != book.ID {
		t.Fatalf("get_book by slug found id %d, want %d", bySlug.ID, book.ID)
	}

	// By the URL somebody would paste out of a browser.
	info, err := p.getSystem(ctx, systemArgs{})
	if err != nil {
		t.Fatalf("get_system: %v", err)
	}
	byURL, err := p.getBook(ctx, idArgs{URL: info.BaseURL + "/books/" + book.Slug})
	if err != nil {
		t.Fatalf("get_book by url: %v", err)
	}
	if byURL.ID != book.ID {
		t.Fatalf("get_book by url found id %d, want %d", byURL.ID, book.ID)
	}

	// A page URL carries its book, which is what makes the page slug
	// unambiguous -- page slugs are unique per book, not per instance.
	pages, err := p.listPages(ctx, pagesArgs{BookID: book.ID, Limit: 1})
	if err != nil {
		t.Fatalf("list_pages: %v", err)
	}
	if pages.Count > 0 {
		want := pages.Pages[0]
		link := info.BaseURL + "/books/" + book.Slug + "/page/" + want.Slug
		got, err := p.getPage(ctx, getPageArgs{URL: link})
		if err != nil {
			t.Fatalf("get_page by url: %v", err)
		}
		if got.ID != want.ID {
			t.Fatalf("get_page by url found id %d, want %d", got.ID, want.ID)
		}
	}

	// A link to the wrong kind of thing is refused rather than resolved to
	// whatever happens to share the slug.
	if _, err := p.getPage(ctx, getPageArgs{URL: info.BaseURL + "/books/" + book.Slug}); err == nil {
		t.Fatal("a book link handed to get_page should be refused")
	}
	if _, err := p.getPage(ctx, getPageArgs{}); err == nil {
		t.Fatal("get_page with nothing to go on should be refused")
	}
}

// A truncated listing in an arbitrary order is a wrong answer rather than a
// partial one, so the order has to be the one that was asked for.
func TestIntegrationSortsListings(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	newest, err := p.listPages(ctx, pagesArgs{Limit: 3})
	if err != nil {
		t.Fatalf("list_pages: %v", err)
	}
	oldest, err := p.listPages(ctx, pagesArgs{Sort: "updated_at", Limit: 3})
	if err != nil {
		t.Fatalf("list_pages sorted: %v", err)
	}
	if newest.Count > 0 && oldest.Count > 0 && newest.Pages[0].ID == oldest.Pages[0].ID {
		t.Error("the default order and its reverse returned the same first page; " +
			"the sort may not be reaching BookStack")
	}

	// BookStack ignores an order it does not recognise and answers 200, so a
	// typo has to be refused here or it silently returns a different order.
	if _, err := p.listPages(ctx, pagesArgs{Sort: "nonsense"}); err == nil {
		t.Fatal("an unrecognised sort should be refused rather than ignored")
	}

	// The notes are the point of the shallow listing: without them a model
	// answers from the titles.
	if newest.Count > 0 && len(newest.Notes) == 0 {
		t.Error("a page listing should say that it carries no page text")
	}
}

// The reads that need the token owner to be an administrator. A token without
// those permissions answers 403, which is a real answer rather than a failure
// of this integration -- so the test says which it got.
func TestIntegrationReadsPeopleAndPermissions(t *testing.T) {
	p := integrationPlugin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	users, err := p.listUsers(ctx, listArgs{Limit: 5})
	switch {
	case err != nil && isForbidden(err):
		t.Logf("list_users: the token's user cannot manage users, which is a "+
			"valid configuration: %v", err)
		return
	case err != nil:
		t.Fatalf("list_users: %v", err)
	}
	t.Logf("list_users returned %d of %d", users.Count, users.Total)

	roles, err := p.listRoles(ctx, limitArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_roles: %v", err)
	}
	t.Logf("list_roles returned %d", roles.Count)
	if roles.Count > 0 {
		role, err := p.getRole(ctx, idArgs{ID: roles.Roles[0].ID})
		if err != nil {
			t.Fatalf("get_role: %v", err)
		}
		t.Logf("get_role returned %d permissions", len(role.Permissions))
	}

	bin, err := p.listRecycleBin(ctx, limitArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_recycle_bin: %v", err)
	}
	t.Logf("list_recycle_bin returned %d", bin.Count)

	audit, err := p.listAuditEvents(ctx, auditArgs{Limit: 5})
	if err != nil {
		t.Fatalf("list_audit_events: %v", err)
	}
	t.Logf("list_audit_events returned %d of %d", audit.Count, audit.Total)

	books, err := p.listBooks(ctx, listArgs{Limit: 1})
	if err != nil {
		t.Fatalf("list_books: %v", err)
	}
	if books.Count > 0 {
		perms, err := p.getContentPermissions(ctx, contentPermArgs{Type: "book", ID: books.Books[0].ID})
		if err != nil {
			t.Fatalf("get_content_permissions: %v", err)
		}
		t.Logf("get_content_permissions: inheriting=%v, %d role overrides",
			perms.Inheriting, len(perms.RolePermissions))
	}
}
