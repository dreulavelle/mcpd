package registry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCatalogue records what it was asked and answers whatever a test set.
type fakeCatalogue struct {
	mu    sync.Mutex
	calls int
	page  Page
	entry Detail
	err   error
	// freshness is what the catalogue claims about reusing its answers.
	freshness Freshness
	// sentValidators is what the last conditional call carried, so a test can
	// show that a validator held from the previous answer is actually sent.
	sentValidators Validators
	// notModified makes the next conditional call answer 304.
	notModified bool
	// blocked holds a call in flight until the channel is closed, so a test
	// can arrange for several callers to arrive during one refresh.
	blocked chan struct{}
}

func (f *fakeCatalogue) block(release chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocked = release
}

// wait releases the lock while it blocks, so a held call does not stop the
// callers this test is about from reaching the cache.
func (f *fakeCatalogue) wait() {
	f.mu.Lock()
	release := f.blocked
	f.mu.Unlock()
	if release != nil {
		<-release
	}
}

func (f *fakeCatalogue) Source() string { return "fake" }

func (f *fakeCatalogue) List(ctx context.Context, q Query) (Page, error) {
	return f.ListIfChanged(ctx, q, Validators{})
}

func (f *fakeCatalogue) ListIfChanged(_ context.Context, _ Query, v Validators) (Page, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.sentValidators = v
	if f.err != nil {
		return Page{}, f.err
	}
	if f.notModified && !v.empty() {
		return Page{Freshness: f.freshness}, ErrNotModified
	}
	page := f.page
	page.Freshness = f.freshness
	return page, nil
}

func (f *fakeCatalogue) Get(ctx context.Context, name string) (Detail, error) {
	return f.GetIfChanged(ctx, name, Validators{})
}

func (f *fakeCatalogue) GetIfChanged(_ context.Context, _ string, v Validators) (Detail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.sentValidators = v
	if f.err != nil {
		return Detail{}, f.err
	}
	if f.notModified && !v.empty() {
		return Detail{Freshness: f.freshness}, ErrNotModified
	}
	entry := f.entry
	entry.Freshness = f.freshness
	return entry, nil
}

func (f *fakeCatalogue) setFreshness(fr Freshness) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.freshness = fr
}

func (f *fakeCatalogue) sayNotModified() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notModified = true
}

func (f *fakeCatalogue) lastValidators() Validators {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sentValidators
}

func (f *fakeCatalogue) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeCatalogue) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newFixture() (*fakeCatalogue, *clock, *Cached) {
	return newFixtureIn(nil)
}

func newFixtureIn(store *CacheStore) (*fakeCatalogue, *clock, *Cached) {
	up := &fakeCatalogue{
		page:  Page{Source: "fake", Entries: []Entry{{Name: "io.example/weather"}}},
		entry: Detail{Entry: Entry{Name: "io.example/weather", Source: "fake"}},
	}
	c := &clock{t: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)}
	return up, c, NewCached(up, CacheOptions{
		Store:      store,
		DefaultTTL: 15 * time.Minute,
		// The same as the listing, so that the tests below which move the
		// clock past the TTL mean the same thing for both. The longer default
		// detail TTL has its own test.
		DetailTTL: 15 * time.Minute,
		Now:       c.now,
	})
}

// Constructing the cache reaches nothing. This is what keeps a catalogue that
// is down, or a deployment with no route to one, from costing a boot: the
// first fetch happens on the first request that needs one.
func TestCached_ConstructionTouchesNothing(t *testing.T) {
	up, _, _ := newFixture()
	if up.count() != 0 {
		t.Fatalf("the catalogue was called %d times at construction", up.count())
	}
}

func TestCached_ServesAFreshAnswerWithoutAskingAgain(t *testing.T) {
	up, clk, cache := newFixture()
	ctx := context.Background()

	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(time.Minute)
	page, err := cache.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if up.count() != 1 {
		t.Errorf("the catalogue was called %d times, want 1", up.count())
	}
	if page.Stale {
		t.Error("an answer inside the TTL is not stale")
	}
}

// The rule that matters most here. A third party being down is not this
// host's failure, and an operator looking at a stale list can still see what
// is there and still decide to import one -- which an error page does not
// allow. What is not acceptable is pretending the data is current.
func TestCached_ServesStaleWhenTheCatalogueIsDown(t *testing.T) {
	up, clk, cache := newFixture()
	ctx := context.Background()

	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	fetchedAt := clk.t

	clk.t = clk.t.Add(time.Hour)
	up.fail(errors.New("registry: connection refused"))

	page, err := cache.List(ctx, Query{})
	if err != nil {
		t.Fatalf("a catalogue that is down must not become an error: %v", err)
	}
	if !page.Stale {
		t.Error("data served after the TTL from a failed refresh must say it is stale")
	}
	if !page.RetrievedAt.Equal(fetchedAt.UTC()) {
		t.Errorf("retrieved_at = %s, want the moment it was actually fetched (%s)",
			page.RetrievedAt, fetchedAt.UTC())
	}
	if len(page.Entries) != 1 {
		t.Errorf("the stale page lost its entries: %+v", page.Entries)
	}
}

// With nothing cached there is nothing honest to serve, so the failure is
// reported. It is the caller's job to render that as a bad gateway rather than
// as a fault in this host.
func TestCached_ReportsFailureWithNothingCached(t *testing.T) {
	up, _, cache := newFixture()
	up.fail(errors.New("registry: connection refused"))

	if _, err := cache.List(context.Background(), Query{}); err == nil {
		t.Fatal("a first fetch that fails with an empty cache must return the error")
	}
}

// A name the catalogue does not have is an answer. Serving a stale hit for it
// would resurrect a withdrawn entry and hand its document to an import form.
func TestCached_DoesNotResurrectAWithdrawnEntry(t *testing.T) {
	up, clk, cache := newFixture()
	ctx := context.Background()

	if _, err := cache.Get(ctx, "io.example/weather"); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(time.Hour)
	up.fail(ErrNotFound)

	if _, err := cache.Get(ctx, "io.example/weather"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound rather than the stale entry", err)
	}
	// And the stale copy is gone, replaced by the short memory of the refusal,
	// so a later failure cannot serve it either.
	up.fail(errors.New("registry: connection refused"))
	if _, err := cache.Get(ctx, "io.example/weather"); err == nil {
		t.Fatal("the withdrawn entry was kept and served")
	}
}

// A cancelled request is this host giving up, not the catalogue failing.
// Answering it with stale data answers a question nobody is still asking.
func TestCached_DoesNotServeStaleForACancelledRequest(t *testing.T) {
	up, clk, cache := newFixture()

	if _, err := cache.List(context.Background(), Query{}); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(time.Hour)
	up.fail(context.Canceled)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.List(ctx, Query{}); err == nil {
		t.Fatal("a cancelled request must not be answered from the cache")
	}
}

// The key includes the search term, and a search term is whatever somebody
// typed. Without a bound that is an allocation driven by keystrokes.
func TestCached_IsBounded(t *testing.T) {
	up, clk, cache := newFixture()
	ctx := context.Background()

	for i := range DefaultMaxCacheEntries + 50 {
		clk.t = clk.t.Add(time.Second)
		if _, err := cache.List(ctx, Query{Search: string(rune('a'+i%26)) + time.Duration(i).String()}); err != nil {
			t.Fatal(err)
		}
	}
	if held := cache.store.Len(); held > DefaultMaxCacheEntries {
		t.Errorf("the cache holds %d entries, want at most %d", held, DefaultMaxCacheEntries)
	}
	if up.count() != DefaultMaxCacheEntries+50 {
		t.Errorf("calls = %d, want one per distinct search", up.count())
	}
}

// The bound is on the process, not on each catalogue.
//
// A cap that every source gets its own copy of is a cap a fourth source
// silently quadruples, and what is being bounded -- this process's memory --
// does not care which catalogue filled it.
func TestCacheStore_BoundsEveryCatalogueTogether(t *testing.T) {
	shared := NewCacheStore(20)
	_, firstClock, first := newFixtureIn(shared)
	_, secondClock, second := newFixtureIn(shared)
	ctx := context.Background()

	for i := range 40 {
		firstClock.t = firstClock.t.Add(time.Second)
		secondClock.t = secondClock.t.Add(time.Second)
		term := Query{Search: "term" + time.Duration(i).String()}
		if _, err := first.List(ctx, term); err != nil {
			t.Fatal(err)
		}
		if _, err := second.List(ctx, term); err != nil {
			t.Fatal(err)
		}
	}
	if held := shared.Len(); held > 20 {
		t.Errorf("two catalogues hold %d entries between them, want at most 20", held)
	}
}

// A caller must not be able to edit the cache through the slice it was handed.
func TestCached_HandsOutCopies(t *testing.T) {
	_, _, cache := newFixture()
	ctx := context.Background()

	first, err := cache.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	first.Entries[0].Name = "tampered"

	second, err := cache.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Entries[0].Name != "io.example/weather" {
		t.Errorf("the cache was edited through a returned slice: %q", second.Entries[0].Name)
	}
}

// The key has to stand for the request that will actually be made. Two
// searches differing only in whitespace are one upstream query, and caching
// them separately means two fetches, two entries, and a bound on the entry
// count that bounds nothing about their size.
func TestCached_KeysOnTheNormalisedQuery(t *testing.T) {
	up, _, cache := newFixture()
	ctx := context.Background()

	for _, q := range []Query{
		{Search: "weather"},
		{Search: "weather "},
		{Search: "  weather"},
		{Search: "weather\n"},
	} {
		if _, err := cache.List(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if up.count() != 1 {
		t.Errorf("the catalogue was called %d times, want 1 for one query", up.count())
	}

	// A limit outside the permitted range means "no usable preference", which
	// is what zero means, so those are one request too.
	for _, q := range []Query{{Limit: 0}, {Limit: -5}, {Limit: MaxEntriesPerPage + 1}} {
		if _, err := cache.List(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if up.count() != 2 {
		t.Errorf("calls = %d, want one more for the unbounded-limit query", up.count())
	}

	// A cursor past the bound is dropped on the way out, so it must not file
	// the first page under the key of a page it is not.
	long := Query{Cursor: strings.Repeat("x", maxCursorRunes+1)}
	if _, err := cache.List(ctx, long); err != nil {
		t.Fatal(err)
	}
	if up.count() != 2 {
		t.Errorf("calls = %d, want the dropped cursor to hit the first page's entry", up.count())
	}
}

// The key is bounded because the query is, and nothing longer than the bounds
// can be held however long the input was.
func TestCached_KeysAreBounded(t *testing.T) {
	_, _, cache := newFixture()
	huge := Query{
		Search: strings.Repeat("q", 100_000),
		Cursor: strings.Repeat("c", 100_000),
	}
	if _, err := cache.List(context.Background(), huge); err != nil {
		t.Fatal(err)
	}
	for _, key := range cache.store.Keys() {
		if len(key) > maxQueryRunes*4+maxCursorRunes*4+64 {
			t.Errorf("a cache key is %d bytes; the bounds should have cut it", len(key))
		}
	}
}
