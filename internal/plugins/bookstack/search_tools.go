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

// The tool that answers "what do we have written down about this".
//
// It is the one read that goes through BookStack's own index rather than a
// listing, so it is the only one that can answer on content rather than on
// name. Everything else here narrows what it returns.

func (p *Plugin) registerSearchTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "search_content",
		Title: "Search the knowledge base",
		Description: "Search shelves, books, chapters and pages by content and " +
			"name. Supports BookStack's own syntax: {created_by:me}, " +
			"{updated_after:2026-01-01}, [tagname=value], \"exact phrase\", " +
			"-excluded. Returns a short preview of where each hit matched.",
		Idempotent: true,
	}, p.searchContent)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "list_tag_names",
		Title: "List tag names",
		Description: "The tag names in use, with how many things carry each. " +
			"Read this before tagging something, so a new page joins the " +
			"vocabulary already in use rather than inventing a synonym.",
		Idempotent: true,
	}, p.listTagNames)

	plugins.Tool(r, plugins.ToolSpec{
		Name:        "list_tag_values",
		Title:       "List a tag's values",
		Description: "The values set for one tag name, with how often each is used.",
		Idempotent:  true,
	}, p.listTagValues)
}

type searchArgs struct {
	Query string `json:"query" jsonschema:"what to search for; BookStack's search syntax works here"`
	Limit int    `json:"limit,omitempty" jsonschema:"most results to return; the instance's ceiling applies"`
}

// SearchHit is one thing the search matched.
type SearchHit struct {
	ID   int    `json:"id"`
	Type string `json:"type"`
	Name string `json:"name"`
	// URL is where a person opens it. The most useful field in the answer,
	// because the next thing anybody does with a search result is look at it.
	URL       string `json:"url,omitempty"`
	BookID    int    `json:"book_id,omitempty"`
	BookName  string `json:"book_name,omitempty"`
	ChapterID int    `json:"chapter_id,omitempty"`
	Tags      []tag  `json:"tags,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	// Preview is BookStack's own snippet of where the query matched, with the
	// matching words wrapped in <strong>. Carried as it comes: the markup is
	// what says which part matched.
	Preview string `json:"preview,omitempty"`
}

// SearchResult is what a search found.
type SearchResult struct {
	// Notes say what to call next, and what to try when nothing matched.
	Notes   []string    `json:"notes,omitempty"`
	Query   string      `json:"query"`
	Results []SearchHit `json:"results"`
	Count   int         `json:"count"`
	truncation
}

func (p *Plugin) searchContent(ctx context.Context, args searchArgs) (SearchResult, error) {
	if err := p.ready(); err != nil {
		return SearchResult{}, err
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return SearchResult{}, fmt.Errorf("bookstack: a search needs something to look for")
	}

	limit := args.Limit
	if limit <= 0 || limit > p.cfg.MaxItems {
		limit = p.cfg.MaxItems
	}
	// Search pages by page number rather than offset, and caps a page at 100.
	// It is the one listing that does not take count/offset, so it is walked
	// here rather than by the client's list.
	const searchPageSize = 100
	var hits []SearchHit
	total := 0
	for pageNo := 1; len(hits) < limit; pageNo++ {
		want := limit - len(hits)
		if want > searchPageSize {
			want = searchPageSize
		}
		q := url.Values{}
		q.Set("query", query)
		q.Set("count", strconv.Itoa(want))
		q.Set("page", strconv.Itoa(pageNo))

		raw, err := p.client.get(ctx, "/api/search", q)
		p.noted(err)
		if err != nil {
			return SearchResult{}, err
		}
		var body struct {
			Data  []searchRow `json:"data"`
			Total int         `json:"total"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return SearchResult{}, fmt.Errorf("bookstack: could not read the search results: %w", err)
		}
		total = body.Total
		for _, row := range body.Data {
			hits = append(hits, row.hit())
		}
		if len(body.Data) < want || len(hits) >= total {
			break
		}
	}

	rows, cut := bound(hits, page{total: total, more: total > len(hits)})
	out := SearchResult{Query: query, Results: rows, Count: len(rows), truncation: cut}
	switch {
	case len(rows) > 0:
		// The preview is a fragment around the match, not the page. Answering
		// from it is the mistake this note exists to stop.
		out.Notes = append(out.Notes,
			"Previews show only the text around each match. Call get_page with a "+
				"hit's id to read the whole page before answering from it.")
		if cut.Truncated {
			out.Notes = append(out.Notes,
				"There are more matches than were returned. Narrow the query, or "+
					"raise limit, rather than assuming these are the best ones.")
		}
	default:
		// Nothing matched. A chat agent's next move is usually to give up or
		// to try the same words again, and neither is right: BookStack's
		// index is exact-ish, and the syntax is where the leverage is.
		out.Notes = append(out.Notes,
			"Nothing matched. Try fewer or more general words — BookStack matches "+
				"terms rather than meaning, so a synonym often finds what a phrase "+
				"does not.",
			"Filters that help: [tagname] or [tagname=value] for tagged content, "+
				"{updated_after:2026-01-01} for recent changes, \"exact phrase\" for "+
				"a literal string.",
			"list_tag_names shows the tag vocabulary actually in use; list_books "+
				"shows what the knowledge base is organised into.")
	}
	return out, nil
}

// searchRow is one result as BookStack sends it. The shape varies by type --
// a page carries a book, a book carries neither -- so every nested part is
// optional.
type searchRow struct {
	ID        int    `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	BookID    int    `json:"book_id"`
	ChapterID int    `json:"chapter_id"`
	UpdatedAt string `json:"updated_at"`
	Tags      []tag  `json:"tags"`
	Book      *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"book"`
	PreviewHTML *struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	} `json:"preview_html"`
}

func (r searchRow) hit() SearchHit {
	h := SearchHit{
		ID: r.ID, Type: r.Type, Name: r.Name, URL: r.URL,
		BookID: r.BookID, ChapterID: r.ChapterID,
		UpdatedAt: r.UpdatedAt, Tags: r.Tags,
	}
	if r.Book != nil {
		h.BookID, h.BookName = r.Book.ID, r.Book.Name
	}
	if r.PreviewHTML != nil {
		h.Preview = strings.TrimSpace(r.PreviewHTML.Content)
	}
	return h
}

// --- tags -------------------------------------------------------------------

type tagNameRow struct {
	Name         string `json:"name"`
	Values       int    `json:"values"`
	Usages       int    `json:"usages"`
	PageCount    int    `json:"page_count"`
	ChapterCount int    `json:"chapter_count"`
	BookCount    int    `json:"book_count"`
	ShelfCount   int    `json:"shelf_count"`
}

// TagNamesResult is the tag vocabulary.
type TagNamesResult struct {
	Tags  []tagNameRow `json:"tags"`
	Count int          `json:"count"`
	truncation
}

type limitArgs struct {
	Limit int `json:"limit,omitempty" jsonschema:"most rows to return; the instance's ceiling applies"`
}

func (p *Plugin) listTagNames(ctx context.Context, args limitArgs) (TagNamesResult, error) {
	if err := p.ready(); err != nil {
		return TagNamesResult{}, err
	}
	pg, err := p.client.list(ctx, "/api/tags/names", nil, args.Limit)
	p.noted(err)
	if err != nil {
		return TagNamesResult{}, err
	}
	rows, err := decodeRows[tagNameRow](pg)
	if err != nil {
		return TagNamesResult{}, err
	}
	rows, cut := bound(rows, pg)
	return TagNamesResult{Tags: rows, Count: len(rows), truncation: cut}, nil
}

type tagValuesArgs struct {
	Name  string `json:"name" jsonschema:"the tag name whose values to list"`
	Limit int    `json:"limit,omitempty" jsonschema:"most rows to return; the instance's ceiling applies"`
}

type tagValueRow struct {
	Value  string `json:"value"`
	Usages int    `json:"usages"`
}

// TagValuesResult is one tag name's values.
type TagValuesResult struct {
	Name   string        `json:"name"`
	Values []tagValueRow `json:"values"`
	Count  int           `json:"count"`
	truncation
}

func (p *Plugin) listTagValues(ctx context.Context, args tagValuesArgs) (TagValuesResult, error) {
	if err := p.ready(); err != nil {
		return TagValuesResult{}, err
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return TagValuesResult{}, fmt.Errorf("bookstack: a tag name is required; " +
			"list_tag_names has them")
	}
	q := url.Values{}
	q.Set("name", name)
	pg, err := p.client.list(ctx, "/api/tags/values-for-name", q, args.Limit)
	p.noted(err)
	if err != nil {
		return TagValuesResult{}, err
	}
	rows, err := decodeRows[tagValueRow](pg)
	if err != nil {
		return TagValuesResult{}, err
	}
	rows, cut := bound(rows, pg)
	return TagValuesResult{Name: name, Values: rows, Count: len(rows), truncation: cut}, nil
}
