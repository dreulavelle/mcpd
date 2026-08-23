package registry

// Ordering a page, and scoping one to a catalogue. The helpers -- stubSource,
// at, names, statusOf -- are multi_test.go's.
//
// What every test here is really defending is one sentence: an order this host
// shows has to be one it can stand behind. A page that looks sorted and is not
// is worse than a page that is not sorted, because nothing on it says which it
// is.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// used is an entry with a call count, as Smithery's rows carry one.
func used(source, name string, uses int64) Entry {
	e := addableAt(source, name)
	count := uses
	e.Uses = &count
	return e
}

// TestSort_MostUsedCoversOnlyTheCataloguesThatPublishAFigure.
//
// The one thing this feature must not do. Three of the four catalogues publish
// no count of how often a server is called, and there is nowhere honest to put
// them in an order built on one -- below a server with a single call says this
// host measured them at zero, and it did not. So they are left out, and the
// response says they were left out: a page that is shorter because most of the
// catalogues are missing reads as "there is nothing else" unless it says
// otherwise.
func TestSort_MostUsedCoversOnlyTheCataloguesThatPublishAFigure(t *testing.T) {
	multi := NewMulti(
		&stubSource{name: officialSource, pages: map[string]Page{
			"": {Entries: []Entry{addableAt(officialSource, "weather")}},
		}},
		&stubSource{name: dockerSource, pages: map[string]Page{
			"": {Entries: []Entry{addableAt(dockerSource, "linear")}},
		}},
		&stubSource{name: smitherySource, ranks: true, pages: map[string]Page{
			"": {Entries: []Entry{
				used(smitherySource, "quiet", 12),
				used(smitherySource, "busy", 87_579),
			}},
		}},
	)

	page, err := multi.List(context.Background(), Query{Sort: SortMostUsed})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range page.Entries {
		if e.Source != smitherySource {
			t.Fatalf("entries = %v, want only the catalogue that publishes a figure", names(page))
		}
	}
	if got := names(page); len(got) != 2 || !strings.HasSuffix(got[0], ":busy") {
		t.Errorf("entries = %v, want the most used first", got)
	}

	// Both silent catalogues are accounted for, each saying why it is absent.
	for _, source := range []string{officialSource, dockerSource} {
		status, ok := statusOf(page, source)
		switch {
		case !ok:
			t.Errorf("%s is missing from the response entirely", source)
		case status.Entries != 0:
			t.Errorf("%s contributed %d entries to a most-used page", source, status.Entries)
		case status.Note == "":
			t.Errorf("%s is absent with nothing saying why", source)
		case status.Uses:
			t.Errorf("%s claims to publish a usage figure", source)
		}
	}
	if status, _ := statusOf(page, smitherySource); !status.Uses {
		t.Error("the catalogue that publishes the figure does not say so")
	}
}

// TestSort_MostUsedNeverReadsAnAbsentFigureAsZero.
//
// The same distinction one level down. A ranked catalogue that reports a count
// for some rows and not others must have the silent ones sort *after* a server
// measured at zero, not with it: nought calls is a measurement and no figure
// is not.
func TestSort_MostUsedNeverReadsAnAbsentFigureAsZero(t *testing.T) {
	silent := addableAt(smitherySource, "silent")
	entries := []Entry{silent, used(smitherySource, "never-called", 0), used(smitherySource, "busy", 9)}
	sortEntries(entries, SortMostUsed)

	if got := entries[len(entries)-1].Name; got != "silent" {
		t.Fatalf("last entry = %q, want the one with no figure at all", got)
	}
	if got := entries[0].Name; got != "busy" {
		t.Errorf("first entry = %q, want the most used", got)
	}
}

// TestSort_RecentlyUpdatedPutsAnUndatedEntryLast.
//
// For the same reason, and it is not hypothetical: a Smithery entry fetched by
// name carries no date, because the entry route does not publish one. Sorting
// a zero time as a time would say the server had not been touched since 1970
// and file it under the oldest thing in the catalogue.
func TestSort_RecentlyUpdatedPutsAnUndatedEntryLast(t *testing.T) {
	at := func(name string, when time.Time) Entry {
		e := addableAt(officialSource, name)
		e.UpdatedAt = when
		return e
	}
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		at("undated", time.Time{}),
		at("old", now.Add(-72*time.Hour)),
		at("new", now),
	}
	sortEntries(entries, SortRecentlyUpdated)

	if got := []string{entries[0].Name, entries[1].Name, entries[2].Name}; got[0] != "new" ||
		got[1] != "old" || got[2] != "undated" {
		t.Errorf("order = %v, want newest first and the undated one last", got)
	}
}

// TestSort_ByNameFollowsWhatTheRowSays.
//
// Sorting on the identifier would file "io.github.foo/weather" under I while
// the row on the page reads "Weather", which is a list that looks unsorted to
// the person reading it.
func TestSort_ByNameFollowsWhatTheRowSays(t *testing.T) {
	titled := func(name, title string) Entry {
		e := addableAt(officialSource, name)
		e.Title = title
		return e
	}
	entries := []Entry{
		titled("io.github.foo/weather", "Weather"),
		titled("zz.example/atlas", "Atlas"),
		titled("aa.example/plain", ""),
	}
	sortEntries(entries, SortName)

	// Case-insensitive, and by the label rather than the identifier: "Weather"
	// sorts under W even though its name begins "io.github".
	want := []string{"aa.example/plain", "Atlas", "Weather"}
	for i, e := range entries {
		if label(e) != want[i] {
			t.Fatalf("order = %v, want %v",
				[]string{label(entries[0]), label(entries[1]), label(entries[2])}, want)
		}
	}
}

// TestSort_AnUnknownOrderIsRefused.
//
// Not replaced with the default. A request for "mostused" answered with the
// ordinary listing is a page that looks sorted and is not, and the caller has
// no way to tell the difference.
func TestSort_AnUnknownOrderIsRefused(t *testing.T) {
	for _, raw := range []string{"mostused", "popular", "most_used", "-name"} {
		if _, err := ParseSort(raw); !errors.Is(err, ErrUnknownSort) {
			t.Errorf("ParseSort(%q) = %v, want a refusal", raw, err)
		}
	}
	for _, raw := range []string{"", "default", "most-used", " Most-Used ", "name", "recently-updated"} {
		if _, err := ParseSort(raw); err != nil {
			t.Errorf("ParseSort(%q) = %v, want it accepted", raw, err)
		}
	}
	// "default" and an empty parameter are one order, not two.
	got, err := ParseSort("default")
	if err != nil || got != SortDefault {
		t.Errorf("ParseSort(\"default\") = %q, %v; want the default order", got, err)
	}
}

// TestSort_MostUsedWithNoRankedCatalogueIsRefused.
//
// A deployment that has switched off every catalogue publishing a usage figure
// cannot serve this order. Answering with an empty page would read as "the
// catalogue holds nothing", which is a different and much worse claim.
func TestSort_MostUsedWithNoRankedCatalogueIsRefused(t *testing.T) {
	multi := NewMulti(&stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{addableAt(officialSource, "weather")}},
	}})
	_, err := multi.List(context.Background(), Query{Sort: SortMostUsed})
	if !errors.Is(err, ErrSortUnavailable) {
		t.Fatalf("err = %v, want ErrSortUnavailable", err)
	}
}

// TestScope_OneCatalogueIsTheWholeListing.
//
// The honest grouping. The sources differ in kind -- who runs the server, who
// holds the credential, who vouched for the entry -- so scoping to one is a
// real cut, and the others are not merely sorted lower.
func TestScope_OneCatalogueIsTheWholeListing(t *testing.T) {
	multi := NewMulti(
		&stubSource{name: officialSource, pages: map[string]Page{
			"": {Entries: []Entry{addableAt(officialSource, "weather")}},
		}},
		&stubSource{name: dockerSource, pages: map[string]Page{
			"": {Entries: []Entry{addableAt(dockerSource, "linear")}},
		}},
	)

	page, err := multi.List(context.Background(), Query{Source: dockerSource})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(page); len(got) != 1 || got[0] != dockerSource+":linear" {
		t.Fatalf("entries = %v, want Docker's alone", got)
	}
	// The catalogue the caller scoped away is not listed back at them: they
	// asked for one, and an account of the others is noise rather than the
	// missing-source warning it would be mistaken for.
	if _, ok := statusOf(page, officialSource); ok {
		t.Error("a catalogue the caller scoped away is reported as though it had been asked")
	}
}

// TestScope_AnUnknownCatalogueIsRefused.
//
// Not silently ignored. Answering a request to see one catalogue with a page
// of all four, and nothing saying the filter was dropped, is the same class of
// mistake as a misspelled selector in an approval rule quietly matching
// everything.
func TestScope_AnUnknownCatalogueIsRefused(t *testing.T) {
	multi := NewMulti(&stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{addableAt(officialSource, "weather")}},
	}})
	_, err := multi.List(context.Background(), Query{Source: "registry.example.invalid"})
	if !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("err = %v, want ErrUnknownSource", err)
	}
	// Case is not identity here: an operator typing a catalogue's name should
	// not have to match its capitalisation.
	page, err := multi.List(context.Background(), Query{Source: strings.ToUpper(officialSource)})
	if err != nil || len(page.Entries) != 1 {
		t.Errorf("a differently-cased name gave %v, %v", names(page), err)
	}
}

// TestSort_ACursorFromADifferentQuestionRestartsTheListing.
//
// The cursor says where each catalogue resumes and, by omission, which of them
// are exhausted. Both are answers to one particular question. Carrying a
// most-used cursor -- which covers one catalogue -- into an unscoped listing
// would read the other three as finished and drop them from every page after
// it, with nothing saying so. The fingerprint makes that a restart, which is a
// state the caller already handles.
func TestSort_ACursorFromADifferentQuestionRestartsTheListing(t *testing.T) {
	sources := func() []Client {
		return []Client{
			&stubSource{name: officialSource, pages: map[string]Page{
				"": window(officialSource, "official", 40, "page-2"),
			}},
			&stubSource{name: smitherySource, ranks: true, pages: map[string]Page{
				"": {Entries: []Entry{
					used(smitherySource, "busy", 900),
					used(smitherySource, "quiet", 3),
				}},
			}},
		}
	}

	ranked, err := NewMulti(sources()...).List(context.Background(),
		Query{Sort: SortMostUsed, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ranked.NextCursor == "" {
		t.Fatal("the most-used listing ended after one entry")
	}

	// The same cursor, a different question. The official registry must not
	// come back as a catalogue with nothing left in it.
	page, err := NewMulti(sources()...).List(context.Background(),
		Query{Cursor: ranked.NextCursor, Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	official := 0
	for _, e := range page.Entries {
		if e.Source == officialSource {
			official++
		}
	}
	if official == 0 {
		t.Errorf("entries = %v, want the listing restarted rather than a catalogue lost", names(page))
	}
}

// TestSort_OrderingDoesNotChangeWhichEntriesAPageHolds.
//
// Sorting happens after the page is assembled, so it moves rows about and
// nothing else. If it could change the selection it would also have to change
// where each source resumes, and the two would drift.
func TestSort_OrderingDoesNotChangeWhichEntriesAPageHolds(t *testing.T) {
	build := func() *Multi {
		return NewMulti(
			&stubSource{name: officialSource, pages: map[string]Page{
				"": window(officialSource, "official", 12, ""),
			}},
			&stubSource{name: dockerSource, pages: map[string]Page{
				"": window(dockerSource, "docker", 12, ""),
			}},
		)
	}
	plain, err := build().List(context.Background(), Query{Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := build().List(context.Background(), Query{Limit: 8, Sort: SortName})
	if err != nil {
		t.Fatal(err)
	}

	held := map[string]bool{}
	for _, e := range plain.Entries {
		held[e.Source+":"+e.Name] = true
	}
	if len(sorted.Entries) != len(plain.Entries) {
		t.Fatalf("sorted page holds %d entries, plain page %d",
			len(sorted.Entries), len(plain.Entries))
	}
	for _, e := range sorted.Entries {
		if !held[e.Source+":"+e.Name] {
			t.Errorf("%s:%s is on the sorted page and not on the plain one", e.Source, e.Name)
		}
	}
	for i := 1; i < len(sorted.Entries); i++ {
		if strings.ToLower(label(sorted.Entries[i-1])) > strings.ToLower(label(sorted.Entries[i])) {
			t.Fatalf("entries = %v, want them in order", names(sorted))
		}
	}
}

// TestSort_ACacheDoesNotHideWhatItsCatalogueCanDo.
//
// Every source Multi holds is wrapped in a cache. A cache that answered this
// question for itself would report that no catalogue publishes a usage figure
// and empty the most-used listing for a reason nothing on the page could show.
func TestSort_ACacheDoesNotHideWhatItsCatalogueCanDo(t *testing.T) {
	ranked := NewCached(&stubSource{name: smitherySource, ranks: true}, CacheOptions{})
	defer ranked.Close()
	silent := NewCached(&stubSource{name: officialSource}, CacheOptions{})
	defer silent.Close()

	if !reportsUses(ranked) {
		t.Error("a cached catalogue that publishes a usage figure reports that it does not")
	}
	if reportsUses(silent) {
		t.Error("a cached catalogue that publishes nothing claims a usage figure")
	}
}
