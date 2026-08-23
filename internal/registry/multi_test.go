package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// stubSource is a catalogue in a test's hand: it answers with what it was
// given, or fails.
type stubSource struct {
	name  string
	pages map[string]Page
	err   error
	// details is what Get returns, keyed by name.
	details map[string]Detail
	// calls counts List calls, so a test can show that an exhausted source is
	// not asked again.
	calls int
	// delay makes this source slow, for the tests about the fan-out budget.
	delay time.Duration
	// closed records that Close reached it.
	closed bool
}

func (s *stubSource) Close() error {
	s.closed = true
	return nil
}

func (s *stubSource) Source() string { return s.name }

func (s *stubSource) List(_ context.Context, q Query) (Page, error) {
	s.calls++
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.err != nil {
		return Page{}, s.err
	}
	page, ok := s.pages[q.Cursor]
	if !ok {
		return Page{Source: s.name, RetrievedAt: time.Now().UTC()}, nil
	}
	page.Source = s.name
	if page.RetrievedAt.IsZero() {
		page.RetrievedAt = time.Now().UTC()
	}
	return page, nil
}

func (s *stubSource) Get(_ context.Context, name string) (Detail, error) {
	if s.err != nil {
		return Detail{}, s.err
	}
	detail, ok := s.details[name]
	if !ok {
		return Detail{}, ErrNotFound
	}
	return detail, nil
}

func at(source, name, url string) Entry {
	return Entry{Name: name, SuggestedName: SuggestName(name), URL: url, Source: source, Addable: url != ""}
}

func names(page Page) []string {
	out := make([]string, 0, len(page.Entries))
	for _, e := range page.Entries {
		out = append(out, e.Source+":"+e.Name)
	}
	return out
}

func statusOf(page Page, source string) (SourceStatus, bool) {
	for _, s := range page.Sources {
		if s.Source == source {
			return s, true
		}
	}
	return SourceStatus{}, false
}

// TestMulti_ACrossSourceDuplicateYieldsOnePreferredEntry.
//
// The same server is in both catalogues under two names -- the official
// registry calls it app.linear/linear and Docker calls it linear -- so the
// identity that joins them is the address, which is the same string in both
// and is also the thing that actually matters: two entries resolving to one
// endpoint are one server, and importing both mounts the same upstream twice.
// The official registry wins, because that is where a publisher registers a
// server themselves.
func TestMulti_ACrossSourceDuplicateYieldsOnePreferredEntry(t *testing.T) {
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{
			at(officialSource, "app.linear/linear", "https://mcp.linear.app/mcp"),
			at(officialSource, "com.apify/apify-mcp-server", "https://mcp.apify.com"),
		}},
	}}
	docker := &stubSource{name: dockerSource, pages: map[string]Page{
		"": {Entries: []Entry{
			// Same server, different name, and the address written a little
			// differently: uppercase host and a trailing slash are noise.
			at(dockerSource, "linear", "https://MCP.Linear.app/mcp/"),
			at(dockerSource, "apify", "https://mcp.apify.com"),
			at(dockerSource, "astro-docs", "https://mcp.docs.astro.build/mcp"),
		}},
	}}

	page, err := NewMulti(official, docker).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	// Round-robin, so the second catalogue is reached before the first has
	// been exhausted -- but the duplicates are still the official registry's,
	// because preference order decides which copy survives even when it does
	// not decide the reading order.
	want := []string{
		officialSource + ":app.linear/linear",
		dockerSource + ":astro-docs",
		officialSource + ":com.apify/apify-mcp-server",
	}
	if strings.Join(names(page), ",") != strings.Join(want, ",") {
		t.Errorf("entries = %v, want %v", names(page), want)
	}

	// And the per-source accounting says who contributed what, so "this
	// source answered" is checkable rather than a flag.
	if s, _ := statusOf(page, officialSource); s.Entries != 2 {
		t.Errorf("official contributed %d, want 2", s.Entries)
	}
	if s, _ := statusOf(page, dockerSource); s.Entries != 1 {
		t.Errorf("docker contributed %d, want 1", s.Entries)
	}
}

// TestMulti_EntriesWithNoAddressAreNotMergedOnAName.
//
// Nothing can establish that two unreachable entries are the same server. A
// package-only entry from one catalogue and a container from the other may
// share a slug and be different things, and merging them would hide one.
func TestMulti_EntriesWithNoAddressAreNotMergedOnAName(t *testing.T) {
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{at(officialSource, "sqlite", "")}},
	}}
	docker := &stubSource{name: dockerSource, pages: map[string]Page{
		"": {Entries: []Entry{at(dockerSource, "sqlite", "")}},
	}}

	// Both of these are unaddable -- at() gives an entry with no address no
	// document to be addable from -- so a listing does not show them at all.
	// The question this test is about is whether they are *merged*, which is
	// only visible with the flag that keeps them.
	multi := NewMulti(official, docker)
	page, err := multi.List(context.Background(), Query{IncludeUnaddable: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 {
		t.Errorf("entries = %v, want both kept", names(page))
	}

	page, err = multi.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 0 {
		t.Errorf("entries = %v, want none: a listing drops what cannot be added", names(page))
	}
}

// TestMulti_OneSourceDownStillAnswers, and the response says which.
//
// A shorter list that does not name the missing catalogue reads as "there is
// nothing else" rather than as "we could not ask", and the fault is not in
// this deployment either way.
func TestMulti_OneSourceDownStillAnswers(t *testing.T) {
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{at(officialSource, "io.example/one", "https://one.example/mcp")}},
	}}
	docker := &stubSource{name: dockerSource,
		err: errors.New("registry: docker/mcp-registry could not be reached: dial tcp: refused")}

	page, err := NewMulti(official, docker).List(context.Background(), Query{})
	if err != nil {
		t.Fatalf("one source being down must not fail the page: %v", err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %v, want the one the working source gave", names(page))
	}

	good, ok := statusOf(page, officialSource)
	if !ok || !good.OK || good.Entries != 1 {
		t.Errorf("official status = %+v, want it to have answered with one entry", good)
	}
	bad, ok := statusOf(page, dockerSource)
	if !ok {
		t.Fatal("the failing source is not reported; the page silently lost a catalogue")
	}
	if bad.OK {
		t.Error("the failing source reports OK")
	}
	if !strings.Contains(bad.Error, "could not be reached") {
		t.Errorf("error = %q, want the reason an operator would act on", bad.Error)
	}

	// The reverse, so that neither source is the one that happens to work.
	page, err = NewMulti(
		&stubSource{name: officialSource, err: errors.New("registry: registry.modelcontextprotocol.io answered 503")},
		&stubSource{name: dockerSource, pages: map[string]Page{
			"": {Entries: []Entry{at(dockerSource, "astro-docs", "https://mcp.docs.astro.build/mcp")}},
		}},
	).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Source != dockerSource {
		t.Errorf("entries = %v, want Docker's", names(page))
	}
}

// TestMulti_EverySourceDownIsAnError. There is no partial page to serve
// honestly, and both reasons are named so an operator does not fix one and
// find the other still broken.
func TestMulti_EverySourceDownIsAnError(t *testing.T) {
	_, err := NewMulti(
		&stubSource{name: officialSource, err: errors.New("official is down")},
		&stubSource{name: dockerSource, err: errors.New("docker is down")},
	).List(context.Background(), Query{})
	if err == nil {
		t.Fatal("want an error when nothing answered")
	}
	for _, want := range []string{"official is down", "docker is down"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to name %q", err, want)
		}
	}
}

// TestMulti_PagesEachSourceWhereItLeftOff.
//
// A source's cursor belongs to that source. The composite carries one per
// source and keys it by name rather than by position, so that turning a source
// off between two requests cannot hand one catalogue's token to another.
func TestMulti_PagesEachSourceWhereItLeftOff(t *testing.T) {
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{at(officialSource, "io.example/a", "https://a.example/mcp")},
			NextCursor: "official-page-2"},
		"official-page-2": {Entries: []Entry{at(officialSource, "io.example/b", "https://b.example/mcp")}},
	}}
	docker := &stubSource{name: dockerSource, pages: map[string]Page{
		"": {Entries: []Entry{at(dockerSource, "one", "https://one.example/mcp")},
			NextCursor: "two"},
		"two": {Entries: []Entry{at(dockerSource, "two", "https://two.example/mcp")}},
	}}
	multi := NewMulti(official, docker)

	// Two, because the limit bounds the merged page and a page big enough to
	// hold everything would simply hold everything: with four entries between
	// them and a page of thirty, one request is the whole listing and there
	// is nothing to resume.
	first, err := multi.List(context.Background(), Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == "" {
		t.Fatal("no cursor although both sources had more")
	}

	second, err := multi.List(context.Background(), Query{Cursor: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{officialSource + ":io.example/b", dockerSource + ":two"}
	if strings.Join(names(second), ",") != strings.Join(want, ",") {
		t.Fatalf("second page = %v, want %v", names(second), want)
	}
	if second.NextCursor != "" {
		t.Errorf("cursor = %q, want the listing to have ended", second.NextCursor)
	}
}

// TestMulti_AnExhaustedSourceIsNotAskedAgain. Asking would restart it from the
// beginning and repeat its first page under every subsequent cursor.
func TestMulti_AnExhaustedSourceIsNotAskedAgain(t *testing.T) {
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{at(officialSource, "io.example/a", "https://a.example/mcp")},
			NextCursor: "official-page-2"},
		"official-page-2": {Entries: []Entry{at(officialSource, "io.example/b", "https://b.example/mcp")}},
	}}
	// Docker ends on its first page.
	docker := &stubSource{name: dockerSource, pages: map[string]Page{
		"": {Entries: []Entry{at(dockerSource, "one", "https://one.example/mcp")}},
	}}
	multi := NewMulti(official, docker)

	first, err := multi.List(context.Background(), Query{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	second, err := multi.List(context.Background(), Query{Cursor: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if docker.calls != 1 {
		t.Errorf("docker was asked %d times, want once: it said it had no more", docker.calls)
	}
	for _, n := range names(second) {
		if strings.HasPrefix(n, dockerSource+":") {
			t.Errorf("the second page repeats %q from an exhausted source", n)
		}
	}
	// It is still accounted for, so the page does not look as though a
	// catalogue had vanished.
	if s, ok := statusOf(second, dockerSource); !ok || !s.OK || s.Entries != 0 {
		t.Errorf("docker status = %+v, want it reported as having nothing more", s)
	}
}

// TestMulti_ACursorFromSomewhereElseRestartsTheListing.
//
// Garbage, or a cursor written by a host configured with different sources.
// Restarting is a state the caller already handles; an empty page with no
// explanation is not, and handing an arbitrary string to a third party's
// pagination is worse than either.
func TestMulti_ACursorFromSomewhereElseRestartsTheListing(t *testing.T) {
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{at(officialSource, "io.example/a", "https://a.example/mcp")}},
	}}
	multi := NewMulti(official)

	stale, _ := json.Marshal(map[string]string{"some.other.registry": "page-9"})
	for _, cursor := range []string{
		"not base64 at all !!",
		encodeForTest(stale),
	} {
		page, err := multi.List(context.Background(), Query{Cursor: cursor})
		if err != nil {
			t.Fatalf("cursor %q: %v", cursor, err)
		}
		if len(page.Entries) != 1 {
			t.Errorf("cursor %q gave %v, want the listing restarted", cursor, names(page))
		}
	}
}

// TestMulti_GetPrefersTheFirstSourceThatHasIt, and a source being down does
// not become "no such server" -- which would read as the entry having been
// withdrawn.
func TestMulti_GetPrefersTheFirstSourceThatHasIt(t *testing.T) {
	shared := "shared-name"
	official := &stubSource{name: officialSource, details: map[string]Detail{
		shared: {Entry: at(officialSource, shared, "https://one.example/mcp")},
	}}
	docker := &stubSource{name: dockerSource, details: map[string]Detail{
		shared:        {Entry: at(dockerSource, shared, "https://two.example/mcp")},
		"docker-only": {Entry: at(dockerSource, "docker-only", "https://three.example/mcp")},
	}}
	multi := NewMulti(official, docker)

	got, err := multi.Get(context.Background(), shared)
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry.Source != officialSource {
		t.Errorf("source = %q, want the preferred catalogue", got.Entry.Source)
	}

	got, err = multi.Get(context.Background(), "docker-only")
	if err != nil {
		t.Fatal(err)
	}
	if got.Entry.Source != dockerSource {
		t.Errorf("source = %q, want Docker's", got.Entry.Source)
	}

	if _, err := multi.Get(context.Background(), "nobody-has-this"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}

	// A down source and a name nobody else has: the failure is reported
	// rather than turned into a withdrawal.
	down := NewMulti(&stubSource{name: officialSource, err: errors.New("official is down")})
	_, err = down.Get(context.Background(), "anything")
	if errors.Is(err, ErrNotFound) {
		t.Error("a catalogue being down was reported as the server not existing")
	}
	if err == nil || !strings.Contains(err.Error(), "official is down") {
		t.Errorf("err = %v, want the failure", err)
	}
}

// TestMulti_StalenessIsPerSource. Which catalogue is stale is the question
// being asked of a merged page, and one flag for the whole page cannot answer
// it.
func TestMulti_StalenessIsPerSource(t *testing.T) {
	fresh := time.Now().UTC()
	old := fresh.Add(-2 * time.Hour)
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{at(officialSource, "io.example/a", "https://a.example/mcp")},
			Stale: true, RetrievedAt: old},
	}}
	docker := &stubSource{name: dockerSource, pages: map[string]Page{
		"": {Entries: []Entry{at(dockerSource, "one", "https://one.example/mcp")},
			RetrievedAt: fresh},
	}}

	page, err := NewMulti(official, docker).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if !page.Stale {
		t.Error("the page does not admit that part of it is stale")
	}
	if s, _ := statusOf(page, officialSource); !s.Stale {
		t.Error("the stale source is not marked stale")
	}
	if s, _ := statusOf(page, dockerSource); s.Stale {
		t.Error("the fresh source is marked stale")
	}
	// The page reports the oldest of its parts, because that is the age of
	// the least fresh thing on it.
	if !page.RetrievedAt.Equal(old) {
		t.Errorf("retrieved_at = %v, want the oldest part's %v", page.RetrievedAt, old)
	}
}

// TestMulti_NoSourcesIsAnError rather than an empty page. A deployment that
// turned every catalogue off is reported by the handler as having none
// configured; a Multi with none is a wiring mistake.
func TestMulti_NoSourcesIsAnError(t *testing.T) {
	if _, err := NewMulti().List(context.Background(), Query{}); err == nil {
		t.Error("want an error from a catalogue with no sources")
	}
}

// TestNormaliseEndpoint pins what counts as the same address.
func TestNormaliseEndpoint(t *testing.T) {
	tests := []struct {
		a, b string
		same bool
	}{
		{"https://mcp.linear.app/mcp", "https://MCP.Linear.app/mcp/", true},
		{"https://mcp.apify.com", "https://mcp.apify.com:443", true},
		{"https://a.example/mcp", "https://a.example/mcp?tenant=one", false},
		{"https://a.example/mcp", "http://a.example/mcp", false},
		{"https://a.example/one", "https://a.example/two", false},
	}
	for _, tc := range tests {
		got := normaliseEndpoint(tc.a) == normaliseEndpoint(tc.b)
		if got != tc.same {
			t.Errorf("%q and %q: same = %v, want %v (%q, %q)",
				tc.a, tc.b, got, tc.same, normaliseEndpoint(tc.a), normaliseEndpoint(tc.b))
		}
	}
	if normaliseEndpoint("not a url") != "" {
		t.Error("an unparseable address was normalised into something that could collide")
	}
}

var _ Client = (*stubSource)(nil)

// encodeForTest renders a composite cursor the way Multi does, so a test can
// build one that names a source this host is not configured with.
func encodeForTest(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

// TestMulti_TheSlowestSourceDoesNotDecideHowLongAPageTakes.
//
// With four catalogues, one having a bad day would otherwise set the speed of
// every page. Past the budget this host stops waiting and serves what arrived,
// and the response says which source did not answer -- because a shorter list
// that does not name the missing catalogue reads as "there is nothing else".
func TestMulti_TheSlowestSourceDoesNotDecideHowLongAPageTakes(t *testing.T) {
	fast := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{at(officialSource, "io.example/one", "https://one.example/mcp")}},
	}}
	slow := &stubSource{name: dockerSource, delay: time.Second, pages: map[string]Page{
		"": {Entries: []Entry{at(dockerSource, "two", "https://two.example/mcp")}},
	}}

	started := time.Now()
	page, err := NewMulti(fast, slow).WithBudget(30*time.Millisecond).
		List(context.Background(), Query{})
	if err != nil {
		t.Fatalf("a slow source must not fail the page: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Errorf("the page took %v, want it bounded by the budget", elapsed)
	}
	if len(page.Entries) != 1 || page.Entries[0].Source != officialSource {
		t.Errorf("entries = %v, want what arrived in time", names(page))
	}
	late, ok := statusOf(page, dockerSource)
	if !ok {
		t.Fatal("the slow source is not reported; the page silently lost a catalogue")
	}
	if late.OK {
		t.Error("a source that did not answer reports OK")
	}
	if !strings.Contains(late.Error, "did not answer within") {
		t.Errorf("error = %q, want it to say the source ran out of time", late.Error)
	}
}

// TestMulti_SourcesAreAskedAtTheSameTime. A search across four catalogues on a
// cold cache must not be four round trips one after another.
func TestMulti_SourcesAreAskedAtTheSameTime(t *testing.T) {
	const delay = 80 * time.Millisecond
	sources := make([]Client, 0, 4)
	for i := range 4 {
		name := fmt.Sprintf("source-%d", i)
		sources = append(sources, &stubSource{
			name:  name,
			delay: delay,
			pages: map[string]Page{"": {Entries: []Entry{
				at(name, fmt.Sprintf("s%d", i), fmt.Sprintf("https://s%d.example/mcp", i)),
			}}},
		})
	}

	started := time.Now()
	page, err := NewMulti(sources...).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	if len(page.Entries) != 4 {
		t.Fatalf("entries = %v, want all four", names(page))
	}
	// Four sequential calls would be four delays. Two is generous slack for a
	// loaded machine and still nowhere near four.
	if elapsed > 2*delay {
		t.Errorf("four catalogues took %v, want them asked concurrently (one is %v)", elapsed, delay)
	}
}

// TestMulti_CloseReachesItsSources, so that the caches' background refreshes
// are shut down with the process rather than left running.
func TestMulti_CloseReachesItsSources(t *testing.T) {
	first := &stubSource{name: officialSource}
	second := &stubSource{name: dockerSource}
	if err := NewMulti(first, second).Close(); err != nil {
		t.Fatal(err)
	}
	if !first.closed || !second.closed {
		t.Errorf("closed = %v, %v; want every source released", first.closed, second.closed)
	}
}
