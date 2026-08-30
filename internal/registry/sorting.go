package registry

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Sort is the order a caller asks a listing for.
//
// It orders the page that was assembled; it does not decide which entries the
// page holds. That distinction is the whole of the honesty here and is worth
// having in one place: the merged catalogue is twenty-odd thousand entries
// behind four opaque cursors, and nothing this host can do reaches past the
// window each source hands it. See Multi.List for what each value can and
// cannot promise.
type Sort string

const (
	// SortDefault is what a caller who asked for no order gets, which is
	// SortRecentlyUpdated.
	//
	// It used to mean "the order the catalogues produce, interleaved", because
	// a page was assembled from a window of each and there was no better
	// answer available. The index holds every catalogue, so there is: what has
	// changed most recently is the useful first page of a list nobody reads to
	// the end, and it beats an interleaving that was really an artefact of how
	// the pages arrived.
	SortDefault Sort = ""
	// SortMostUsed orders by Entry.Uses, highest first.
	//
	// It narrows the listing to the catalogues that publish a usage figure
	// rather than sorting the ones that do not to the bottom. A source with no
	// figure has not scored zero -- it has not been asked -- and ranking it
	// below a server with one call would be this host inventing the
	// comparison. See Multi.scopeFor.
	SortMostUsed Sort = "most-used"
	// SortRecentlyUpdated orders by Entry.UpdatedAt, newest first. Entries
	// whose catalogue publishes no date go last: an absent date is not an old
	// one, and sorting it as the epoch would say the entry had not been
	// touched since 1970.
	SortRecentlyUpdated Sort = "recently-updated"
	// SortName orders by the label a reader sees -- the title, or the
	// catalogue's own name where there is no title.
	SortName Sort = "name"
)

// ErrUnknownSort reports an order this host does not have.
var ErrUnknownSort = errors.New("registry: unknown sort")

// ParseSort reads the value a caller sent.
//
// An unrecognised order is an error rather than a fall back to the default.
// Falling back would answer a request for "most-used" -- or for "mostused",
// which is the case that actually happens -- with the ordinary listing and
// nothing saying the order had been discarded, which is a page that looks
// sorted and is not.
func ParseSort(raw string) (Sort, error) {
	s := Sort(strings.ToLower(strings.TrimSpace(raw))).normalise()
	if !s.known() {
		return SortDefault, fmt.Errorf("%w %q; this host sorts by %s",
			ErrUnknownSort, clean(raw, maxQueryRunes), strings.Join(SortNames(), ", "))
	}
	return s, nil
}

// SortNames lists the orders a caller may ask for, default first.
func SortNames() []string {
	return []string{"default", string(SortMostUsed), string(SortRecentlyUpdated), string(SortName)}
}

// known reports an order this host implements.
func (s Sort) known() bool {
	switch s {
	case SortDefault, SortMostUsed, SortRecentlyUpdated, SortName:
		return true
	}
	return false
}

// normalise folds the spelling of the default onto the zero value.
//
// "default" is in SortNames because an empty query parameter is an awkward
// thing to write down in a message, so it is read back as the default it
// names and nothing downstream has two ways to say one order.
func (s Sort) normalise() Sort {
	if s == "default" {
		return SortDefault
	}
	return s
}

// sortEntries puts one assembled page in the asked-for order.
//
// Stable, so entries the order cannot separate keep the arrangement the merge
// gave them -- which is the round-robin across catalogues, and is the fairest
// thing to fall back to. Every comparison ends at the catalogue's own name for
// the entry, so the result does not depend on which source happened to answer
// first.
func sortEntries(entries []Entry, by Sort) {
	switch by {
	case SortMostUsed:
		sort.SliceStable(entries, func(i, j int) bool {
			a, b := entries[i], entries[j]
			switch ua, ub := a.Uses, b.Uses; {
			case ua == nil && ub == nil:
				return a.Name < b.Name
			// An entry whose catalogue publishes no figure goes last. It
			// should not be on this page at all -- SortMostUsed narrows the
			// listing to the catalogues that publish one -- but a source that
			// reports a figure for some of its rows and not others must not
			// have the silent ones read as zero.
			case ua == nil:
				return false
			case ub == nil:
				return true
			case *ua != *ub:
				return *ua > *ub
			}
			return a.Name < b.Name
		})
	case SortRecentlyUpdated:
		sort.SliceStable(entries, func(i, j int) bool {
			a, b := entries[i], entries[j]
			switch az, bz := a.UpdatedAt.IsZero(), b.UpdatedAt.IsZero(); {
			case az && bz:
				return a.Name < b.Name
			case az:
				return false
			case bz:
				return true
			case !a.UpdatedAt.Equal(b.UpdatedAt):
				return a.UpdatedAt.After(b.UpdatedAt)
			}
			return a.Name < b.Name
		})
	case SortName:
		sort.SliceStable(entries, func(i, j int) bool {
			a, b := label(entries[i]), label(entries[j])
			if !strings.EqualFold(a, b) {
				return strings.ToLower(a) < strings.ToLower(b)
			}
			return entries[i].Name < entries[j].Name
		})
	}
}

// label is what a reader sees for an entry, which is what "by name" has to
// mean here: sorting on the identifier would file "io.github.foo/weather"
// under I while the row on the page reads "Weather".
func label(e Entry) string {
	if e.Title != "" {
		return e.Title
	}
	return e.Name
}

// UsesReporter is a catalogue that publishes how often each of its servers is
// used, on every row it lists.
//
// Optional, in the way Revalidating is: a source that does not implement it
// publishes no figure, which is every source but one. It is declared by the
// source rather than inferred from the rows, because SortMostUsed has to
// decide which catalogues to ask *before* it holds any rows to look at -- and
// because "this page happened to carry no figure" and "this catalogue
// publishes none" are different facts.
type UsesReporter interface {
	// ReportsUses is true for a catalogue whose listing rows carry Uses.
	ReportsUses() bool
}

// reportsUses answers the question for one source, whether or not it has an
// opinion.
func reportsUses(c Client) bool {
	r, ok := c.(UsesReporter)
	return ok && r.ReportsUses()
}
