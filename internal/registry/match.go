package registry

import "strings"

// searchTerms splits what somebody typed into the words that must all match.
//
// All of them, not any: "syncro rmm" asking for entries mentioning either word
// returns every RMM in the catalogue, which is the behaviour that made search
// feel like it was ignoring the query. Requiring every term is what makes
// adding a word narrow the result, which is the only thing anybody expects a
// second word to do.
func searchTerms(search string) []string {
	fields := strings.Fields(strings.ToLower(search))
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			terms = append(terms, f)
		}
	}
	return terms
}

// matchesSearch reports whether an entry answers the query.
//
// Matched here rather than left to the catalogues, because there are four of
// them and they do not agree on what a search is. One matches names, another
// runs a relevance engine that returns its whole catalogue ranked, and a page
// merged from both is mostly the second's idea of "related" -- so searching
// for "syncro" returned a page on which nothing said syncro anywhere. A local
// filter is the only place the answer can be made to mean one thing.
//
// The fields searched are the ones an operator can see on the row: the
// catalogue's name, the title, and the description. Not the URL -- a host name
// matching a term the description never mentions is a hit nobody can account
// for by looking at the result.
func matchesSearch(e Entry, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	// One lowered haystack per entry rather than per term: the fields are
	// short, and lowering four strings once beats lowering them per word.
	haystack := strings.ToLower(
		e.Name + "\x00" + e.SuggestedName + "\x00" + e.Title + "\x00" + e.Description)
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

// keepMatching filters entries in place-safe fashion, returning a new slice.
func keepMatching(entries []Entry, terms []string) []Entry {
	if len(terms) == 0 {
		return entries
	}
	out := entries[:0:0]
	for _, e := range entries {
		if matchesSearch(e, terms) {
			out = append(out, e)
		}
	}
	return out
}
