package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// pagingCatalogue is a catalogue that hands out entries a page at a time,
// exactly as the real ones do.
type pagingCatalogue struct {
	name string
	all  []Entry
	// listed is what it claims to hold, which need not be len(all) -- Smithery
	// publishes ten thousand and lists five hundred.
	listed int
	// failAfter makes the nth request fail, for the source that dies halfway.
	failAfter int
	requests  atomic.Int64
	pageSize  int
}

func (c *pagingCatalogue) Source() string { return c.name }

func (c *pagingCatalogue) List(_ context.Context, q Query) (Page, error) {
	n := c.requests.Add(1)
	if c.failAfter > 0 && int(n) > c.failAfter {
		return Page{}, errors.New("the catalogue stopped answering")
	}

	from := 0
	if q.Cursor != "" {
		from, _ = strconv.Atoi(q.Cursor)
	}
	size := c.pageSize
	if size <= 0 {
		size = q.Limit
	}
	to := from + size
	if to > len(c.all) {
		to = len(c.all)
	}
	next := ""
	if to < len(c.all) {
		next = strconv.Itoa(to)
	}
	listed := c.listed
	if listed == 0 {
		listed = len(c.all)
	}
	return Page{
		Source: c.name, Entries: c.all[from:to], NextCursor: next,
		RetrievedAt: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
		Sources: []SourceStatus{{
			Source: c.name, OK: true, Entries: to - from, Total: listed,
		}},
	}, nil
}

func (c *pagingCatalogue) Get(_ context.Context, name string) (Detail, error) {
	for _, e := range c.all {
		if e.Name == name {
			return Detail{Entry: e}, nil
		}
	}
	return Detail{}, ErrNotFound
}

// entries builds n entries, of which every addableEvery-th is importable.
func entries(prefix string, n, addableEvery int) []Entry {
	out := make([]Entry, n)
	for i := range out {
		out[i] = Entry{
			Name:    fmt.Sprintf("%s/%03d", prefix, i),
			Title:   fmt.Sprintf("%s %d", prefix, i),
			Source:  prefix,
			Addable: addableEvery > 0 && i%addableEvery == 0,
		}
	}
	return out
}

func newIndex(t *testing.T, sources ...Client) *Index {
	t.Helper()
	return NewIndex(sources, IndexOptions{PerPage: 10})
}

/*
The complaint this exists for.

Docker's catalogue holds 317 servers of which this host can import 29, and
Smithery's is almost entirely importable. Asked for a window each and filtered
afterwards, the first contributed two rows and the second ten -- from identical
code. Nobody reading that page could tell it from broken paging.

A page is this host's to size, so it is one length whatever the catalogues are
made of.
*/
func TestAPageIsOneLengthWhateverTheCataloguesHold(t *testing.T) {
	sparse := &pagingCatalogue{name: "sparse", all: entries("sparse", 300, 10)}
	dense := &pagingCatalogue{name: "dense", all: entries("dense", 300, 1)}
	ix := newIndex(t, sparse, dense)

	page, err := ix.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 10 {
		t.Fatalf("got %d entries, want a full page of 10", len(page.Entries))
	}

	// And scoped to the sparse catalogue on its own, which is where it was
	// most obviously wrong: two rows where ten were asked for.
	scoped, err := ix.List(context.Background(), Query{Limit: 10, Source: "sparse"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Entries) != 10 {
		t.Fatalf("the sparse catalogue gave %d of 10 asked for", len(scoped.Entries))
	}
}

// The count is counted. It used to be sampled from one window and
// extrapolated, which is what made the page apologise for it.
func TestTheCountIsCountedRatherThanSampled(t *testing.T) {
	// 300 entries, every tenth importable: exactly 30.
	sparse := &pagingCatalogue{name: "sparse", all: entries("sparse", 300, 10)}
	ix := newIndex(t, sparse)

	page, err := ix.List(context.Background(), Query{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if page.Addable != 30 {
		t.Errorf("addable = %d, want exactly 30", page.Addable)
	}
	if len(page.Sources) != 1 {
		t.Fatalf("got %d sources", len(page.Sources))
	}
	if got := page.Sources[0].Judged; got != 300 {
		t.Errorf("judged = %d, want every entry walked", got)
	}
	if got := page.Sources[0].Addable; got != 30 {
		t.Errorf("addable = %d, want exactly 30", got)
	}
}

// What a catalogue says it holds is its claim and is kept as one, separate
// from what this host counted. Smithery lists 500 of 10,880.
func TestWhatACatalogueClaimsIsKeptApartFromWhatWasCounted(t *testing.T) {
	capped := &pagingCatalogue{name: "capped", all: entries("capped", 50, 1), listed: 10880}
	ix := newIndex(t, capped)

	page, err := ix.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	s := page.Sources[0]
	if s.Total != 10880 {
		t.Errorf("total = %d, want the catalogue's own claim", s.Total)
	}
	if s.Judged != 50 || s.Addable != 50 {
		t.Errorf("judged/addable = %d/%d, want what was actually read", s.Judged, s.Addable)
	}
}

// Search runs over everything held, not over whichever window happened to be
// fetched -- which is what made a term match nothing on page one and something
// on page three.
func TestSearchRunsOverTheWholeCatalogue(t *testing.T) {
	all := entries("cat", 300, 1)
	all[250].Title = "Syncro RMM"
	ix := newIndex(t, &pagingCatalogue{name: "cat", all: all})

	page, err := ix.List(context.Background(), Query{Search: "syncro"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("got %d matches, want the one entry that says syncro", len(page.Entries))
	}
	if page.Entries[0].Title != "Syncro RMM" {
		t.Errorf("matched %q", page.Entries[0].Title)
	}
}

// Paging covers everything once: no entry twice, none skipped.
func TestPagingCoversEverythingExactlyOnce(t *testing.T) {
	ix := newIndex(t, &pagingCatalogue{name: "cat", all: entries("cat", 95, 1)})
	ctx := context.Background()

	seen := map[string]int{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 20 {
			t.Fatal("paging did not end")
		}
		page, err := ix.List(ctx, Query{Limit: 10, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Entries {
			seen[e.Name]++
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	if len(seen) != 95 {
		t.Fatalf("saw %d distinct entries, want all 95", len(seen))
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("%s appeared %d times", name, n)
		}
	}
}

// A cursor from a different question restarts rather than paging the wrong
// list silently.
func TestACursorFromAnotherQueryRestarts(t *testing.T) {
	ix := newIndex(t, &pagingCatalogue{name: "cat", all: entries("cat", 50, 1)})
	ctx := context.Background()

	first, err := ix.List(ctx, Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// The same cursor, handed to a search it did not come from.
	page, err := ix.List(ctx, Query{Limit: 10, Cursor: first.NextCursor, Search: "cat"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) == 0 || page.Entries[0].Name != "cat/000" {
		t.Fatalf("did not restart: first entry %+v", page.Entries)
	}
}

/*
One catalogue failing is not four failing.

They are four independent third parties. A page assembled from the three that
answered is shorter than the catalogue, which is a fact to report -- not a
reason to serve nothing.
*/
func TestOneCatalogueFailingCostsTheOthersNothing(t *testing.T) {
	good := &pagingCatalogue{name: "good", all: entries("good", 30, 1)}
	ix := newIndex(t, good, &alwaysFails{name: "broken"})

	page, err := ix.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatalf("a working catalogue was not served: %v", err)
	}
	if len(page.Entries) != 10 {
		t.Errorf("got %d entries from the catalogue that answered", len(page.Entries))
	}
	var sawFailure bool
	for _, s := range page.Sources {
		if s.Source == "broken" {
			sawFailure = true
			if s.OK {
				t.Error("the broken catalogue is reported as OK")
			}
			if s.Error == "" {
				t.Error("the broken catalogue reported no reason")
			}
		}
	}
	if !sawFailure {
		t.Error("the broken catalogue is missing from the page entirely")
	}
}

// A catalogue that answered forty pages and failed on the forty-first has told
// this host about four hundred servers. Throwing them away to report a clean
// failure serves nobody.
func TestAPartialReadIsKept(t *testing.T) {
	// Two pages of ten, then it stops answering.
	dying := &pagingCatalogue{name: "dying", all: entries("dying", 100, 1), failAfter: 2}
	ix := newIndex(t, dying)

	page, err := ix.List(context.Background(), Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 20 {
		t.Fatalf("kept %d entries, want the 20 that were read", len(page.Entries))
	}
	if page.Sources[0].Note == "" {
		t.Error("a partial read must say it stopped early")
	}
}

// A refresh that reaches nothing does not replace a snapshot that did. Serving
// an empty catalogue because a refresh failed is worse than serving a day-old
// one.
func TestAFailedRefreshKeepsTheLastGoodSnapshot(t *testing.T) {
	src := &flakey{name: "flakey", all: entries("flakey", 30, 1)}
	clock := &clock{t: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)}
	ix := NewIndex([]Client{src}, IndexOptions{
		PerPage: 10, TTL: time.Hour, Now: clock.now,
	})
	ctx := context.Background()

	if _, err := ix.List(ctx, Query{Limit: 5}); err != nil {
		t.Fatal(err)
	}
	// Past its lifetime, and now the catalogue is down.
	src.broken.Store(true)
	clock.t = clock.t.Add(2 * time.Hour)

	page, err := ix.List(ctx, Query{Limit: 5})
	if err != nil {
		t.Fatalf("the held snapshot was not served: %v", err)
	}
	if len(page.Entries) != 5 {
		t.Errorf("got %d entries, want the held snapshot", len(page.Entries))
	}
	if !page.Stale {
		t.Error("a snapshot past its lifetime must say it is stale")
	}
}

// Inside its lifetime, a snapshot is answered from and the catalogues are left
// alone. This is the whole point of the day.
func TestTheCataloguesAreAskedOncePerLifetime(t *testing.T) {
	src := &pagingCatalogue{name: "cat", all: entries("cat", 30, 1)}
	clock := &clock{t: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)}
	ix := NewIndex([]Client{src}, IndexOptions{
		PerPage: 10, TTL: 24 * time.Hour, Now: clock.now,
	})
	ctx := context.Background()

	for range 5 {
		if _, err := ix.List(ctx, Query{Limit: 5}); err != nil {
			t.Fatal(err)
		}
	}
	// Three pages of ten to walk thirty entries, and no more however many
	// times the page is read.
	if got := src.requests.Load(); got != 3 {
		t.Errorf("made %d requests for five reads, want the 3 that enumerate it", got)
	}
}

// A cursor that never ends must not be followed forever.
func TestALoopingCatalogueIsBounded(t *testing.T) {
	ix := NewIndex([]Client{&endless{name: "endless"}}, IndexOptions{
		PerPage: 10, MaxRequests: 5,
	})

	page, err := ix.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Sources[0].Judged > 50 {
		t.Fatalf("walked %d entries past a bound of 5 requests", page.Sources[0].Judged)
	}
	if page.Sources[0].Note == "" {
		t.Error("a listing cut short by a bound must say so")
	}
}

// Entries this host cannot import are held but not listed: a detail lookup has
// to find one, and a listing that spends half its rows on things that cannot
// be added is a listing of half the length.
func TestUnimportableEntriesAreHeldButNotListed(t *testing.T) {
	ix := newIndex(t, &pagingCatalogue{name: "cat", all: entries("cat", 100, 10)})
	ctx := context.Background()

	listed, err := ix.List(ctx, Query{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Entries) != 10 {
		t.Fatalf("listed %d, want the 10 importable ones", len(listed.Entries))
	}
	// MaxEntriesPerPage is the ceiling on a page, so a hundred is the most one
	// request can carry -- asking for more is a query Normalised refuses and
	// replaces with the default.
	withAll, err := ix.List(ctx, Query{Limit: MaxEntriesPerPage, IncludeUnaddable: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(withAll.Entries) != 100 {
		t.Fatalf("asked for everything and got %d of 100", len(withAll.Entries))
	}
}

// alwaysFails is a catalogue that is down.
type alwaysFails struct{ name string }

func (c *alwaysFails) Source() string { return c.name }
func (c *alwaysFails) List(context.Context, Query) (Page, error) {
	return Page{}, errors.New("the catalogue could not be reached")
}
func (c *alwaysFails) Get(context.Context, string) (Detail, error) {
	return Detail{}, errors.New("the catalogue could not be reached")
}

// flakey answers until it is told to stop.
type flakey struct {
	name   string
	all    []Entry
	broken atomic.Bool
}

func (c *flakey) Source() string { return c.name }
func (c *flakey) List(_ context.Context, q Query) (Page, error) {
	if c.broken.Load() {
		return Page{}, errors.New("the catalogue could not be reached")
	}
	return Page{
		Source: c.name, Entries: c.all,
		Sources: []SourceStatus{{Source: c.name, OK: true, Entries: len(c.all)}},
	}, nil
}
func (c *flakey) Get(context.Context, string) (Detail, error) {
	return Detail{}, ErrNotFound
}

// endless never runs out of pages.
type endless struct {
	name string
	n    atomic.Int64
}

func (c *endless) Source() string { return c.name }
func (c *endless) List(_ context.Context, q Query) (Page, error) {
	i := c.n.Add(1)
	return Page{
		Source: c.name,
		Entries: []Entry{{
			Name: fmt.Sprintf("endless/%d", i), Addable: true, Source: c.name,
		}},
		NextCursor: strconv.FormatInt(i, 10),
	}, nil
}
func (c *endless) Get(context.Context, string) (Detail, error) {
	return Detail{}, ErrNotFound
}

/*
The same server published in two catalogues is one server.

The catalogues overlap: one is built from another, and a third lists what it
hosts on behalf of publishers who also registered directly. They do not agree
on names, so identity is the endpoint a row dials -- which is what actually
makes two rows the same thing.

Without this the overlap appears twice on every page, and the count counts it
twice.
*/
func TestOneServerInTwoCataloguesIsListedOnce(t *testing.T) {
	shared := Entry{
		Name: "io.example/weather", Title: "Weather", Addable: true,
		URL: "https://weather.example/mcp",
	}
	// The other catalogue's name for it, and a trailing slash and some
	// capitals in the address, none of which make it a different server.
	sameAgain := Entry{
		Name: "weather", Title: "Weather (hosted)", Addable: true,
		URL: "https://Weather.Example/mcp/",
	}
	first := &pagingCatalogue{name: "first", all: []Entry{shared}}
	second := &pagingCatalogue{name: "second", all: []Entry{sameAgain,
		{Name: "second/only", Title: "Only here", Addable: true,
			URL: "https://only.example/mcp"}}}

	ix := newIndex(t, first, second)
	page, err := ix.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	if len(page.Entries) != 2 {
		t.Fatalf("got %d entries, want the shared server once and the unique one",
			len(page.Entries))
	}
	// The preferred catalogue's copy is the one kept, in configured order.
	var titles []string
	for _, e := range page.Entries {
		titles = append(titles, e.Title)
	}
	if !slices.Contains(titles, "Weather") {
		t.Errorf("titles = %v, want the first catalogue's copy of the shared server", titles)
	}
	if slices.Contains(titles, "Weather (hosted)") {
		t.Errorf("titles = %v, want the duplicate dropped", titles)
	}
}

// The headline counts servers, not rows. Summing what each catalogue
// contributed counts a server published in two of them twice, which would put
// a number above the list that the list cannot contain.
func TestTheCountDoesNotCountADuplicateTwice(t *testing.T) {
	shared := Entry{Name: "a", Addable: true, URL: "https://shared.example/mcp"}
	first := &pagingCatalogue{name: "first", all: []Entry{shared}}
	second := &pagingCatalogue{name: "second", all: []Entry{
		{Name: "b", Addable: true, URL: "https://shared.example/mcp"},
		{Name: "c", Addable: true, URL: "https://unique.example/mcp"},
	}}

	page, err := newIndex(t, first, second).List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Addable != 2 {
		t.Errorf("addable = %d, want 2 distinct servers rather than 3 rows", page.Addable)
	}
	if len(page.Entries) != page.Addable {
		t.Errorf("the count (%d) and the list (%d) disagree",
			page.Addable, len(page.Entries))
	}
}
