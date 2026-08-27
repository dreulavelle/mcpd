package registry

import "testing"

// The complaint this exists for: searching for "syncro" returned a page on
// which nothing said syncro anywhere.
//
// Four catalogues do not agree on what a search is. One matches names, another
// runs a relevance engine that happily returns its whole catalogue ranked, and
// a page merged from both is mostly the second's idea of "related". Filtering
// here is the only place the answer can be made to mean one thing.
func TestSearchKeepsOnlyWhatTheTermsAppearIn(t *testing.T) {
	entries := []Entry{
		{Name: "io.syncro/rmm", Title: "Syncro RMM", Description: "Tickets and assets."},
		{Name: "io.github.acme/weather", Title: "Weather", Description: "Forecasts."},
		{Name: "com.example/helpdesk", Title: "Helpdesk", Description: "Sync tickets with Syncro."},
		{Name: "io.github.other/sync", Title: "Sync", Description: "Files, synchronised."},
	}

	kept := keepMatching(entries, searchTerms("syncro"))
	if len(kept) != 2 {
		t.Fatalf("kept %d entries, want the two that say syncro: %+v", len(kept), kept)
	}
	for _, e := range kept {
		if e.Name == "io.github.other/sync" {
			t.Error(`"sync" was matched by a search for "syncro"`)
		}
	}
}

// Every term, not any of them. A second word that widened the result would be
// the opposite of what anybody types one for.
func TestASecondTermNarrows(t *testing.T) {
	entries := []Entry{
		{Name: "io.syncro/rmm", Title: "Syncro RMM", Description: "Remote monitoring."},
		{Name: "io.syncro/billing", Title: "Syncro Billing", Description: "Invoices."},
	}

	if got := keepMatching(entries, searchTerms("syncro")); len(got) != 2 {
		t.Fatalf("one term kept %d, want both", len(got))
	}
	got := keepMatching(entries, searchTerms("syncro billing"))
	if len(got) != 1 || got[0].Name != "io.syncro/billing" {
		t.Fatalf("two terms kept %+v, want only the billing one", got)
	}
}

// The fields searched are the ones an operator can see on the row. A URL
// matching a term nothing visible mentions is a hit nobody can account for by
// looking at the result.
func TestSearchDoesNotMatchFieldsTheRowDoesNotShow(t *testing.T) {
	entries := []Entry{{
		Name: "io.example/thing", Title: "Thing", Description: "Does things.",
		URL: "https://syncro.example.com/mcp",
	}}
	if got := keepMatching(entries, searchTerms("syncro")); len(got) != 0 {
		t.Fatalf("matched on a hidden field: %+v", got)
	}
}

// Case is not a distinction anybody types on purpose.
func TestSearchIgnoresCaseAndSurroundingSpace(t *testing.T) {
	entries := []Entry{{Name: "io.syncro/rmm", Title: "Syncro RMM"}}
	for _, q := range []string{"syncro", "SYNCRO", "  Syncro  ", "sYnCrO"} {
		if got := keepMatching(entries, searchTerms(q)); len(got) != 1 {
			t.Errorf("%q kept %d, want 1", q, len(got))
		}
	}
}

// An empty search browses. Filtering everything away when nobody asked a
// question would empty the page the moment somebody cleared the box.
func TestAnEmptySearchKeepsEverything(t *testing.T) {
	entries := []Entry{{Name: "a"}, {Name: "b"}}
	for _, q := range []string{"", "   "} {
		if got := keepMatching(entries, searchTerms(q)); len(got) != 2 {
			t.Errorf("%q kept %d, want everything", q, len(got))
		}
	}
}

// The suggested local name is searched too: it is what the row offers to call
// the server, and somebody who has seen it once will search for that.
func TestSearchMatchesTheSuggestedName(t *testing.T) {
	entries := []Entry{{Name: "io.github.acme/x", SuggestedName: "syncro", Title: "X"}}
	if got := keepMatching(entries, searchTerms("syncro")); len(got) != 1 {
		t.Fatalf("kept %d, want the entry whose suggested name matches", len(got))
	}
}
