package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// addableAt is an entry a listing will actually show: it has an address, so it
// deduplicates by address, and it is addable, so the filter keeps it.
func addableAt(source, name string) Entry {
	return Entry{
		Name:          name,
		SuggestedName: SuggestName(name),
		URL:           "https://" + strings.ReplaceAll(name, "/", ".") + ".example/mcp",
		Source:        source,
		Addable:       true,
	}
}

// window builds one source page of n addable entries, plus a cursor to the
// next one when there is a next one.
func window(source, prefix string, n int, next string) Page {
	entries := make([]Entry, 0, n)
	for i := range n {
		entries = append(entries, addableAt(source, fmt.Sprintf("%s-%02d", prefix, i)))
	}
	return Page{Source: source, Entries: entries, NextCursor: next}
}

// TestMulti_TheLimitBoundsTheMergedPage.
//
// The bug this file exists for. The limit used to be handed to every source
// and applied by each of them independently, so a page of three catalogues
// answered a request for ten with thirty and a request for thirty with ninety.
// The API said one thing, the page rendered three times as much, and an
// operator who read "ninety" concluded the catalogues held ninety servers
// between them rather than twelve thousand.
func TestMulti_TheLimitBoundsTheMergedPage(t *testing.T) {
	for _, limit := range []int{1, 5, 10, 30} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			multi := NewMulti(
				&stubSource{name: officialSource, pages: map[string]Page{
					"": window(officialSource, "official", 100, ""),
				}},
				&stubSource{name: dockerSource, pages: map[string]Page{
					"": window(dockerSource, "docker", 100, ""),
				}},
				&stubSource{name: smitherySource, pages: map[string]Page{
					"": window(smitherySource, "smithery", 100, ""),
				}},
			)
			page, err := multi.List(context.Background(), Query{Limit: limit})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Entries) != limit {
				t.Errorf("asked for %d, got %d entries", limit, len(page.Entries))
			}
		})
	}
}

// TestMulti_APageOfTenIsTenUsableRows.
//
// Roughly half of what the catalogues publish only runs locally. Filtering in
// the browser would mean a page of ten arriving as four; filtering here, ahead
// of the paging, is what makes ten rows ten rows.
func TestMulti_APageOfTenIsTenUsableRows(t *testing.T) {
	// Every other entry is one this host would refuse.
	entries := make([]Entry, 0, 60)
	for i := range 60 {
		e := addableAt(officialSource, fmt.Sprintf("srv-%02d", i))
		if i%2 == 1 {
			e.Addable, e.Reason, e.URL = false, "only runs locally", ""
		}
		entries = append(entries, e)
	}
	multi := NewMulti(&stubSource{name: officialSource, pages: map[string]Page{
		"": {Source: officialSource, Entries: entries},
	}})

	page, err := multi.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 10 {
		t.Fatalf("entries = %d, want 10", len(page.Entries))
	}
	for _, e := range page.Entries {
		if !e.Addable {
			t.Errorf("%q is on the page and cannot be added", e.Name)
		}
	}

	// The machinery is not weakened, only the listing's use of it: asked for,
	// the refusals come back with their reasons intact.
	page, err = multi.List(context.Background(), Query{Limit: 10, IncludeUnaddable: true})
	if err != nil {
		t.Fatal(err)
	}
	refused := 0
	for _, e := range page.Entries {
		if !e.Addable {
			refused++
			if e.Reason == "" {
				t.Errorf("%q is refused with no reason", e.Name)
			}
		}
	}
	if refused == 0 {
		t.Error("include_unaddable returned nothing unaddable")
	}
}

// TestMulti_PagingStopsMidWindowAndResumesThere.
//
// The reason the cursor carries an offset as well as a cursor. A source is
// asked for twenty rows to fill a page of ten, so the page stops halfway
// through its window; resuming at the start of the *next* window -- which is
// all a source's own cursor can say -- would silently drop the other half.
//
// Walked to the end and checked as a whole, because the failure this defends
// against is not a wrong first page. It is a listing that quietly omits every
// second chunk of every catalogue, which nothing but the totals would show.
func TestMulti_PagingStopsMidWindowAndResumesThere(t *testing.T) {
	// Three sources, each with two windows of twenty-five: seventy-five
	// entries, none of which is a duplicate of another.
	sources := make([]Client, 0, 3)
	for _, name := range []string{officialSource, dockerSource, smitherySource} {
		first := window(name, name+"-a", 25, "page-2")
		second := window(name, name+"-b", 25, "")
		sources = append(sources, &stubSource{name: name, pages: map[string]Page{
			"": first, "page-2": second,
		}})
	}
	multi := NewMulti(sources...)

	seen := []string{}
	held := map[string]bool{}
	cursor := ""
	for page := 0; page < 20; page++ {
		answer, err := multi.List(context.Background(), Query{Limit: 10, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range answer.Entries {
			key := e.Source + ":" + e.Name
			if held[key] {
				t.Errorf("%q appeared twice across pages", key)
			}
			held[key] = true
			seen = append(seen, key)
		}
		cursor = answer.NextCursor
		if cursor == "" {
			break
		}
	}
	if cursor != "" {
		t.Fatal("the listing never ended")
	}
	if len(seen) != 150 {
		t.Errorf("walked %d entries, want all 150: paging lost some", len(seen))
	}
}

// TestMulti_ASourceThatFailedIsAskedAgainOnTheNextPage.
//
// A source is dropped from the cursor when it says it has no more. A source
// that could not be reached has said no such thing, and treating one bad
// minute as the end of that catalogue means the rest of the listing silently
// comes from everybody else.
func TestMulti_ASourceThatFailedIsAskedAgainOnTheNextPage(t *testing.T) {
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": window(officialSource, "official", 4, "official-2"),
		"official-2": {Source: officialSource,
			Entries: []Entry{addableAt(officialSource, "official-late")}},
	}}
	docker := &stubSource{name: dockerSource, err: errors.New("dial tcp: refused")}

	multi := NewMulti(official, docker)
	first, err := multi.List(context.Background(), Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == "" {
		t.Fatal("no cursor")
	}
	if docker.calls != 1 {
		t.Fatalf("docker asked %d times on the first page", docker.calls)
	}

	// It comes back for the second page.
	docker.err = nil
	docker.pages = map[string]Page{"": window(dockerSource, "docker", 3, "")}
	second, err := multi.List(context.Background(), Query{Limit: 4, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if docker.calls != 2 {
		t.Errorf("docker asked %d times in total, want the second page to ask it again", docker.calls)
	}
	found := false
	for _, e := range second.Entries {
		if e.Source == dockerSource {
			found = true
		}
	}
	if !found {
		t.Errorf("second page = %v, want Docker's entries once it answered", names(second))
	}
}

// TestMulti_EachSourceIsAskedForMoreThanThePage.
//
// The over-fetch, which is what makes one round trip fill a page after
// filtering, and what makes the page after it free: the source's own cursor
// has not moved, so the second page is the same cached answer read from a
// different offset.
func TestMulti_EachSourceIsAskedForMoreThanThePage(t *testing.T) {
	asked := []int{}
	source := &recordingSource{stubSource: stubSource{
		name:  officialSource,
		pages: map[string]Page{"": window(officialSource, "s", 100, "")},
	}, onList: func(q Query) { asked = append(asked, q.Limit) }}

	multi := NewMulti(source)
	first, err := multi.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != 20 {
		t.Fatalf("source asked with limits %v, want one fetch of 20 for a page of 10", asked)
	}

	if _, err := multi.List(context.Background(), Query{Limit: 10, Cursor: first.NextCursor}); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[1] != 20 {
		t.Errorf("second page asked %v, want the same window again -- the cursor has not moved", asked)
	}
}

type recordingSource struct {
	stubSource
	onList func(Query)
}

func (r *recordingSource) List(ctx context.Context, q Query) (Page, error) {
	r.onList(q)
	return r.stubSource.List(ctx, q)
}

func TestSourceFetchFor(t *testing.T) {
	for _, tc := range []struct{ limit, want int }{
		{1, minSourceFetch}, {10, 20}, {30, 60}, {60, MaxEntriesPerPage}, {100, MaxEntriesPerPage},
	} {
		if got := sourceFetchFor(tc.limit); got != tc.want {
			t.Errorf("sourceFetchFor(%d) = %d, want %d", tc.limit, got, tc.want)
		}
	}
}

// TestEstimateAddable.
//
// The number beside the search box. It scales a source's measured addable
// ratio by the size that source reported, counts at face value the sources
// that report no size, and rounds down -- because a figure that reads as an
// estimate is the only honest way to present one.
func TestEstimateAddable(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources []SourceStatus
		want    int
	}{
		{
			name: "a source that reports a size is extrapolated from what was judged",
			// Two hundred of two hundred and sixty-nine judged rows are
			// addable, over a catalogue of ten thousand.
			sources: []SourceStatus{{Source: smitherySource, OK: true, Judged: 269, Addable: 200, Total: 10498}},
			want:    7800,
		},
		{
			name:    "a source that holds everything it has is counted exactly",
			sources: []SourceStatus{{Source: dockerSource, OK: true, Judged: 317, Addable: 137, Total: 317}},
			want:    137,
		},
		{
			name:    "a source with no size contributes only what was seen",
			sources: []SourceStatus{{Source: officialSource, OK: true, Judged: 20, Addable: 8}},
			want:    8,
		},
		{
			name: "a source that did not answer contributes nothing",
			sources: []SourceStatus{
				{Source: dockerSource, OK: true, Judged: 317, Addable: 137, Total: 317},
				{Source: smitherySource, Error: "could not be reached"},
			},
			want: 137,
		},
		{
			name:    "nothing to say is zero, which the wire omits",
			sources: []SourceStatus{{Source: officialSource, OK: true}},
			want:    0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := estimateAddable(tc.sources); got != tc.want {
				t.Errorf("estimateAddable = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFloorSignificant(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 0}, {7, 7}, {137, 137}, {999, 999},
		{1000, 1000}, {1049, 1000}, {7952, 7900}, {12500, 12000}, {12999, 12000},
	} {
		if got := floorSignificant(tc.in); got != tc.want {
			t.Errorf("floorSignificant(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestMulti_ACursorFromAnOlderBuildRestartsTheListing.
//
// The composite cursor changed shape when it started carrying an offset. One
// written by the previous build is a map of strings where this reads a map of
// objects, and a browser tab left open across a deploy holds one. Restarting
// is a state the caller already handles; a decode error reaching the page is
// not.
func TestMulti_ACursorFromAnOlderBuildRestartsTheListing(t *testing.T) {
	source := &stubSource{name: officialSource, pages: map[string]Page{
		"": window(officialSource, "s", 3, ""),
	}}
	// {"registry.modelcontextprotocol.io":"page-2"}, base64url, no padding.
	old := "eyJyZWdpc3RyeS5tb2RlbGNvbnRleHRwcm90b2NvbC5pbyI6InBhZ2UtMiJ9"
	page, err := NewMulti(source).List(context.Background(), Query{Cursor: old})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 3 {
		t.Errorf("entries = %v, want the listing restarted", names(page))
	}
}

// TestMulti_AnOffsetOutsideAWindowIsNotTrusted. A cursor is opaque to the
// caller and arrives from a URL, so an offset in one is somebody's input.
func TestMulti_AnOffsetOutsideAWindowIsNotTrusted(t *testing.T) {
	for _, offset := range []int{-1, MaxEntriesPerPage + 1, 1 << 20} {
		want := fingerprint(Query{})
		cursor := multiCursor{
			Query:     want,
			Positions: map[string]sourcePosition{officialSource: {Offset: offset}},
		}.encode()
		decoded := decodeMultiCursor(cursor, []string{officialSource}, want)
		if _, listed := decoded[officialSource]; listed {
			t.Errorf("offset %d survived decoding", offset)
		}
	}
}
