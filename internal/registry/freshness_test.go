package registry

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
)

// TestReadFreshness pins what this host takes from a response's own headers.
//
// The three catalogues measured say three different things, so a single
// hardcoded TTL would be wrong in both directions at once -- too eager for the
// one asking not to be cached, and throwing away the four hours the other
// explicitly grants.
func TestReadFreshness(t *testing.T) {
	tests := []struct {
		name           string
		header         http.Header
		wantTTL        time.Duration
		wantStaleWhile time.Duration
		wantNoStore    bool
		wantValidators Validators
	}{
		{
			// The official MCP registry, measured: no Cache-Control, no
			// validator of any kind. Nothing to honour, so the cache's
			// configured default stands in.
			name:   "the official registry says nothing",
			header: http.Header{"Vary": {"Origin"}},
		},
		{
			// Docker's catalogue, measured: both validators, no
			// Cache-Control. So the TTL is the default and the *refresh* is
			// cheap, which is the half that matters for a 567 KiB document.
			name: "docker's CDN offers validators and no policy",
			header: http.Header{
				"Etag":          {`"05fe93d27d48c1506895018751b33613"`},
				"Last-Modified": {"Sat, 22 Aug 2026 06:14:13 GMT"},
			},
			wantValidators: Validators{
				ETag:         `"05fe93d27d48c1506895018751b33613"`,
				LastModified: "Sat, 22 Aug 2026 06:14:13 GMT",
			},
		},
		{
			// Smithery, measured. s-maxage wins over max-age because mcpd is
			// a shared cache serving every administrator of a deployment, not
			// one person's browser -- taking the sixty seconds meant for a
			// browser would throw away the four hours offered to us.
			name: "a shared-cache directive beats the browser one",
			header: http.Header{"Cache-Control": {
				"public, max-age=60, s-maxage=14400, stale-while-revalidate=86400"}},
			wantTTL: 4 * time.Hour,
			// Granted as twenty-four hours; the ceiling is applied where the
			// entry is built, not here, so that what the catalogue said and
			// what this host will do with it stay separable.
			wantStaleWhile: 24 * time.Hour,
		},
		{
			// PulseMCP, measured. no-cache means revalidate before use, and
			// with no validator offered there is nothing to revalidate with --
			// so it becomes a very short TTL rather than being ignored.
			name:    "no-cache with no validator becomes a short life",
			header:  http.Header{"Cache-Control": {"no-cache"}},
			wantTTL: noCacheTTL,
		},
		{
			name:        "no-store is not held at all",
			header:      http.Header{"Cache-Control": {"no-store, max-age=600"}},
			wantNoStore: true,
		},
		{
			name:    "max-age alone",
			header:  http.Header{"Cache-Control": {"max-age=300"}},
			wantTTL: 5 * time.Minute,
		},
		{
			// Age is how long the answer sat in somebody else's cache before
			// reaching this one. Ignoring it is the difference between "fresh
			// for ten minutes" and "fresh for ten minutes from whenever the
			// CDN fetched it".
			name: "age is taken off what is left",
			header: http.Header{
				"Cache-Control": {"max-age=600"},
				"Age":           {"540"},
			},
			wantTTL: time.Minute,
		},
		{
			name: "an answer older than its own max-age has nothing left",
			header: http.Header{
				"Cache-Control": {"max-age=600"},
				"Age":           {"99999"},
			},
			wantTTL: 0,
		},
		{
			name:    "a directive this host cannot read is not guessed at",
			header:  http.Header{"Cache-Control": {"max-age=soon, s-maxage=-4"}},
			wantTTL: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := readFreshness(&http.Response{Header: tc.header})
			if got.TTL != tc.wantTTL {
				t.Errorf("ttl = %v, want %v", got.TTL, tc.wantTTL)
			}
			if got.StaleWhile != tc.wantStaleWhile {
				t.Errorf("stale-while = %v, want %v", got.StaleWhile, tc.wantStaleWhile)
			}
			if got.NoStore != tc.wantNoStore {
				t.Errorf("no-store = %v, want %v", got.NoStore, tc.wantNoStore)
			}
			if got.Validators != tc.wantValidators {
				t.Errorf("validators = %+v, want %+v", got.Validators, tc.wantValidators)
			}
		})
	}
}

// TestCached_HonoursTheCataloguesOwnTTL. A catalogue that says how long its
// answer stays true is believed, in preference to a number chosen here.
func TestCached_HonoursTheCataloguesOwnTTL(t *testing.T) {
	up, clk, cache := newFixture()
	ctx := context.Background()
	up.setFreshness(Freshness{TTL: 4 * time.Hour})

	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	// Well past the fifteen-minute default, and well inside the four hours the
	// catalogue granted.
	clk.t = clk.t.Add(time.Hour)
	page, err := cache.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if up.count() != 1 {
		t.Errorf("calls = %d, want the catalogue's own four hours to be honoured", up.count())
	}
	if page.Stale {
		t.Error("an answer inside the granted window is not stale")
	}
}

// TestCached_DoesNotHoldWhatItWasAskedNotTo.
func TestCached_DoesNotHoldWhatItWasAskedNotTo(t *testing.T) {
	up, _, cache := newFixture()
	ctx := context.Background()
	up.setFreshness(Freshness{NoStore: true})

	for range 3 {
		if _, err := cache.List(ctx, Query{}); err != nil {
			t.Fatal(err)
		}
	}
	if up.count() != 3 {
		t.Errorf("calls = %d, want one per request for a no-store answer", up.count())
	}
}

// TestCached_ServesStaleAndRefreshesBehindIt is stale-while-revalidate.
//
// Smithery grants a day of it explicitly. It is what makes a search across
// several catalogues feel immediate: the answer already held goes out at once
// and the refresh that replaces it runs behind, so nobody waits on a network
// call for a list they can already see.
func TestCached_ServesStaleAndRefreshesBehindIt(t *testing.T) {
	up, clk, cache := newFixture()
	t.Cleanup(func() { _ = cache.Close() })
	ctx := context.Background()
	up.setFreshness(Freshness{TTL: time.Minute, StaleWhile: time.Hour})

	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	// Past the entry's lifetime, inside its stale window. The catalogue asked
	// for a minute and got the fixture's fifteen: the configured lifetime is a
	// floor, so a catalogue asking to be re-fetched sooner is overridden.
	clk.t = clk.t.Add(20 * time.Minute)

	page, err := cache.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("the stale answer was not served: %+v", page.Entries)
	}
	if !page.Stale {
		t.Error("an answer served past its TTL must say it is stale")
	}

	// The refresh runs behind it. Close waits for it, which is also what
	// proves it is owned rather than loose.
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if up.count() != 2 {
		t.Errorf("calls = %d, want a refresh to have run behind the served answer", up.count())
	}
}

// TestCached_OneRefreshPerKey. A page half a dozen browsers ask for at the same
// moment should cost one upstream request, not six.
func TestCached_OneRefreshPerKey(t *testing.T) {
	up, clk, cache := newFixture()
	t.Cleanup(func() { _ = cache.Close() })
	ctx := context.Background()

	// A refresh that blocks until released, so every caller arrives while it
	// is still in flight.
	release := make(chan struct{})
	up.setFreshness(Freshness{TTL: time.Minute, StaleWhile: time.Hour})
	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	up.block(release)
	// Past the fixture's fifteen-minute floor, not the catalogue's minute.
	clk.t = clk.t.Add(20 * time.Minute)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cache.List(ctx, Query{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(release)
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if up.count() != 2 {
		t.Errorf("calls = %d, want one fetch and one refresh however many asked", up.count())
	}
}

// TestCached_RevalidatesWithTheValidatorItHolds.
//
// This is what makes Docker's catalogue cheap to keep current: the whole
// document is 567 KiB and its CDN answers a conditional request with 304 and a
// few hundred bytes of headers.
func TestCached_RevalidatesWithTheValidatorItHolds(t *testing.T) {
	up, clk, cache := newFixture()
	ctx := context.Background()
	up.setFreshness(Freshness{Validators: Validators{ETag: `"abc"`}})

	first, err := cache.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	fetchedAt := first.RetrievedAt

	clk.t = clk.t.Add(time.Hour)
	up.sayNotModified()

	page, err := cache.List(ctx, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if got := up.lastValidators(); got.ETag != `"abc"` {
		t.Errorf("sent %+v, want the ETag from the previous answer", got)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("a 304 lost the body it was confirming: %+v", page.Entries)
	}
	if page.Stale {
		t.Error("a confirmed answer is current, not stale")
	}
	if !page.RetrievedAt.After(fetchedAt) {
		t.Errorf("retrieved_at = %s, want the clock restarted by the confirmation", page.RetrievedAt)
	}
}

// TestCached_ADetailOutlivesAListing.
//
// Two different questions. A listing is "what is there now" and changes
// whenever anybody publishes anything; one server.json is "what does this one
// say", keyed by a stable name, and changes when its publisher cuts a release.
func TestCached_ADetailOutlivesAListing(t *testing.T) {
	up := &fakeCatalogue{
		page:  Page{Source: "fake", Entries: []Entry{{Name: "io.example/weather"}}},
		entry: Detail{Entry: Entry{Name: "io.example/weather", Source: "fake"}},
	}
	clk := &clock{t: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)}
	// Set explicitly rather than taken from the defaults. Both defaults are a
	// day now, so a test reading them would be asserting that two constants
	// happen to be equal. What matters, and what this defends, is that a
	// listing and a document are separately keyed and separately clocked --
	// so a listing falling due does not drag the document out with it.
	cache := NewCached(up, CacheOptions{
		DefaultTTL: 15 * time.Minute,
		DetailTTL:  time.Hour,
		Now:        clk.now,
	})
	ctx := context.Background()

	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(ctx, "io.example/weather"); err != nil {
		t.Fatal(err)
	}
	// Past the listing's lifetime, inside the document's.
	clk.t = clk.t.Add(30 * time.Minute)

	if _, err := cache.Get(ctx, "io.example/weather"); err != nil {
		t.Fatal(err)
	}
	if up.count() != 2 {
		t.Errorf("calls = %d, want the document to still be held", up.count())
	}
	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	if up.count() != 3 {
		t.Errorf("calls = %d, want the listing to have been fetched again", up.count())
	}
}

// TestCached_ForgetsANotFoundQuickly.
//
// A name that answers 404 today is a server somebody publishes tomorrow, and a
// wrong negative is the one cache mistake an operator cannot work around. It
// is remembered only long enough to stop a dashboard retrying a broken link
// from turning it into a request per render.
func TestCached_ForgetsANotFoundQuickly(t *testing.T) {
	up, clk, cache := newFixture()
	ctx := context.Background()
	up.fail(ErrNotFound)

	for range 3 {
		if _, err := cache.Get(ctx, "io.example/nothing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	}
	if up.count() != 1 {
		t.Errorf("calls = %d, want the refusal remembered for a moment", up.count())
	}

	clk.t = clk.t.Add(negativeTTL + time.Second)
	if _, err := cache.Get(ctx, "io.example/nothing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if up.count() != 2 {
		t.Errorf("calls = %d, want the catalogue asked again once the memory expired", up.count())
	}
	if clk.t.Sub(clk.t) != 0 || negativeTTL > time.Minute {
		t.Errorf("negativeTTL is %v; a refusal must be remembered in seconds", negativeTTL)
	}
}

// TestCached_AnExplicitRefreshBypassesWhatIsHeld.
//
// The escape hatch for a catalogue that is visibly behind: a server published
// a minute ago that an administrator is standing in front of the dashboard
// waiting for. It is CapAdmin like the rest of the catalogue.
func TestCached_AnExplicitRefreshBypassesWhatIsHeld(t *testing.T) {
	up, _, cache := newFixture()
	ctx := context.Background()

	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	if up.count() != 1 {
		t.Fatalf("calls = %d, want the second answered from the cache", up.count())
	}

	if _, err := cache.List(WithRefresh(ctx), Query{}); err != nil {
		t.Fatal(err)
	}
	if up.count() != 2 {
		t.Errorf("calls = %d, want an explicit refresh to ask again", up.count())
	}
	if _, err := cache.Get(WithRefresh(ctx), "io.example/weather"); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(WithRefresh(ctx), "io.example/weather"); err != nil {
		t.Fatal(err)
	}
	if up.count() != 4 {
		t.Errorf("calls = %d, want the refresh to reach the entry as well as the list", up.count())
	}
}

// TestCached_ARefreshDoesNotOutliveShutdown.
//
// Nobody is waiting for a background refresh, which is exactly why it needs an
// owner: an unbounded one is a goroutine holding the process open on the way
// out for a result nothing will read.
func TestCached_ARefreshDoesNotOutliveShutdown(t *testing.T) {
	up, clk, cache := newFixture()
	ctx := context.Background()
	up.setFreshness(Freshness{TTL: time.Minute, StaleWhile: time.Hour})
	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}

	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(10 * time.Minute)
	if _, err := cache.List(ctx, Query{}); err != nil {
		t.Fatal(err)
	}
	// Closed, so nothing was started behind the answer. Close again to prove
	// there is nothing left to wait for.
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if up.count() != 1 {
		t.Errorf("calls = %d, want no refresh started after shutdown", up.count())
	}
}

// TestOfficial_SendsAndUnderstandsConditionalRequests.
//
// The official registry offers no validator today. This is written for the day
// it does, and costs a header the far end ignores until then.
func TestOfficial_SendsAndUnderstandsConditionalRequests(t *testing.T) {
	var sent string
	r := newRegistry(t, func(w http.ResponseWriter, req *http.Request) {
		sent = req.Header.Get("If-None-Match")
		if sent != "" {
			w.Header().Set("ETag", `"v2"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Cache-Control", "s-maxage=3600")
		_, _ = w.Write([]byte(listBody("", remoteEntry(
			"io.example/weather", "1.0.0", "active", true, "2026-01-01T00:00:00Z"))))
	})

	page, err := r.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Freshness.Validators.ETag != `"v1"` {
		t.Errorf("validators = %+v, want the ETag read off the response", page.Freshness.Validators)
	}
	if page.Freshness.TTL != time.Hour {
		t.Errorf("ttl = %v, want the hour the response granted a shared cache", page.Freshness.TTL)
	}

	again, err := r.ListIfChanged(context.Background(), Query{}, page.Freshness.Validators)
	if !errors.Is(err, ErrNotModified) {
		t.Fatalf("err = %v, want ErrNotModified", err)
	}
	if sent != `"v1"` {
		t.Errorf("If-None-Match = %q, want the held ETag", sent)
	}
	if again.Freshness.Validators.ETag != `"v2"` {
		t.Error("a new ETag arriving with a 304 was dropped; the next conditional " +
			"request would ask about a version the far end has moved past")
	}
}

// TestDocker_RevalidatesTheWholeCatalogue. Docker's CDN really does answer a
// conditional request with 304, which is what makes refreshing a 567 KiB
// document cheap enough to do often.
func TestDocker_RevalidatesTheWholeCatalogue(t *testing.T) {
	body := dockerFixture(t)
	var sentModifiedSince string
	client := newDocker(t, func(w http.ResponseWriter, req *http.Request) {
		sentModifiedSince = req.Header.Get("If-Modified-Since")
		if req.Header.Get("If-None-Match") == `"cat-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"cat-1"`)
		w.Header().Set("Last-Modified", "Sat, 22 Aug 2026 06:14:13 GMT")
		_, _ = w.Write(body)
	})

	page, err := client.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if page.Freshness.Validators.ETag != `"cat-1"` {
		t.Fatalf("validators = %+v", page.Freshness.Validators)
	}

	_, err = client.ListIfChanged(context.Background(), Query{}, page.Freshness.Validators)
	if !errors.Is(err, ErrNotModified) {
		t.Fatalf("err = %v, want ErrNotModified", err)
	}
	if sentModifiedSince != "Sat, 22 Aug 2026 06:14:13 GMT" {
		t.Errorf("If-Modified-Since = %q, want both validators sent", sentModifiedSince)
	}
}

// TestWithRefresh_IsRequestScoped. The flag rides the context because Get
// takes a name and no query, and a refresh that worked on the list but not on
// the entry behind it would be a button that half works.
func TestWithRefresh_IsRequestScoped(t *testing.T) {
	if RefreshRequested(context.Background()) {
		t.Error("an ordinary request asks for a refresh")
	}
	if !RefreshRequested(WithRefresh(context.Background())) {
		t.Error("an explicit refresh was not carried")
	}
}
