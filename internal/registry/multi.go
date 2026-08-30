package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Multi serves one catalogue built from several.
//
// The handler behind it and the import path in front of it know nothing about
// how many catalogues there are; a source is added or turned off by changing
// what is passed here. Sources are listed in preference order, most preferred
// first, and that order decides two things: which copy of a server appearing
// in two catalogues survives deduplication, and which catalogue answers a Get
// for a name both of them have.
//
// A source's failure is that source's. One catalogue being unreachable
// produces a shorter list that says which one is missing, not a page that
// refuses to render; a page is an error only when nothing answered. That is
// the same judgement Cached makes about staleness, one level up: the operator
// looking at a partial catalogue can still see what is there and still decide
// to import one, and the fault is not in this deployment.
type Multi struct {
	sources []Client
	budget  time.Duration
}

// DefaultFanOutBudget bounds how long one page waits on every catalogue
// together.
//
// Shorter than the fifteen seconds a single source's own HTTP client allows,
// and deliberately: that timeout is what one request may take, this is what a
// person waiting for a page may be asked to spend. A source slower than this
// is dropped from the page it was too slow for -- its own request runs on and
// fills the cache, so the next page has it.
const DefaultFanOutBudget = 8 * time.Second

// NewMulti composes catalogues in preference order, most preferred first.
func NewMulti(sources ...Client) *Multi {
	kept := make([]Client, 0, len(sources))
	for _, s := range sources {
		if s != nil {
			kept = append(kept, s)
		}
	}
	return &Multi{sources: kept, budget: DefaultFanOutBudget}
}

// WithBudget replaces how long a page waits on its sources. For tests, and for
// a deployment whose catalogues are behind a slow link.
func (m *Multi) WithBudget(budget time.Duration) *Multi {
	if budget > 0 {
		m.budget = budget
	}
	return m
}

// Close releases every source that holds something to release -- the caches,
// and the background refreshes they own. Called on the way out.
func (m *Multi) Close() error {
	var errs []error
	for _, s := range m.sources {
		if closer, ok := s.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Sources names the catalogues, in preference order.
func (m *Multi) Sources() []string { return sourceNames(m.sources) }

// ReportsUses is true when any catalogue behind this one publishes a usage
// figure, so that a Multi composed into another Multi keeps the capability
// rather than losing it at the join.
func (m *Multi) ReportsUses() bool {
	for _, s := range m.sources {
		if reportsUses(s) {
			return true
		}
	}
	return false
}

// ErrUnknownSource reports a catalogue this host is not configured with.
var ErrUnknownSource = errors.New("registry: unknown catalogue")

// ErrSortUnavailable reports an order no configured catalogue can support.
var ErrSortUnavailable = errors.New("registry: that order is not available here")

// scopeFor decides which catalogues one listing covers, and accounts for the
// ones it does not.
//
// Two things narrow a listing, and they are narrowed differently on purpose.
//
// The caller's own scope is exact: name a catalogue and that is the listing.
// A name this host does not have is refused rather than ignored, because
// ignoring it would answer a request to see one catalogue with a page of all
// of them and nothing saying the filter had been dropped.
//
// The most-used order narrows by capability. Every catalogue but one
// publishes no count of how often a server is called, and there is no honest
// place to put them in an order built on one: sorting them below a server with
// a single call says this host measured them at zero, and it did not. So they
// are left out of that listing and reported as left out, which is the same
// judgement the response already makes about a catalogue that failed. What is
// bought is that the order shown is real -- and where exactly one catalogue
// publishes the figure, as today, it is a total order over that catalogue
// rather than a rearrangement of one page.
func (m *Multi) scopeFor(q Query) (covered []Client, excluded []SourceStatus, err error) {
	covered = m.sources
	if q.Source != "" {
		scoped := false
		for _, s := range m.sources {
			if strings.EqualFold(s.Source(), q.Source) {
				covered, scoped = []Client{s}, true
				break
			}
		}
		if !scoped {
			return nil, nil, fmt.Errorf("%w %q; this host browses %s",
				ErrUnknownSource, q.Source, strings.Join(m.Sources(), ", "))
		}
	}
	if q.Sort != SortMostUsed {
		return covered, nil, nil
	}
	ranked := make([]Client, 0, len(covered))
	for _, s := range covered {
		if reportsUses(s) {
			ranked = append(ranked, s)
			continue
		}
		excluded = append(excluded, SourceStatus{
			Source: s.Source(), OK: true,
			Note: "does not publish how often a server is used, so it is not in this order",
		})
	}
	if len(ranked) == 0 {
		return nil, nil, fmt.Errorf(
			"%w: no catalogue here publishes how often a server is used", ErrSortUnavailable)
	}
	return ranked, excluded, nil
}

// sourceNames names a set of catalogues, in the order given.
func sourceNames(sources []Client) []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.Source())
	}
	return out
}

// Source names the composite, for a caller that wants one string.
func (m *Multi) Source() string {
	names := m.Sources()
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names, ", ")
}

// How much Multi asks of each source, to fill a page of its own.
//
// The limit a caller gives bounds the page it gets back. It cannot also be
// what each source is asked for: three sources each honouring "ten"
// independently returned thirty rows, which is why a request for ten used to
// answer with thirty and a request for thirty with ninety. An operator reading
// that concluded the catalogues held ninety servers between them.
//
// So each source is asked for more than the page needs and the merged result
// is paged down here. How much more is a trade: too little and a page of ten
// costs several round trips, too much and the first page waits on rows nobody
// will scroll to.
const (
	// sourceOverFetch is the multiple of the page asked of each source.
	// Roughly half of what the catalogues publish only runs locally and is
	// not listed, and a few more rows are a preferred source's entry arriving
	// twice, so a page of ten is filled comfortably out of twenty.
	sourceOverFetch = 2
	// minSourceFetch is the floor, and it is what makes paging cheap. A
	// window of twenty serves two pages of ten and the second of them waits
	// on nobody: the source's own cursor has not moved, so the second page is
	// the same cached answer read from a different offset.
	minSourceFetch = 20
	// maxFillRounds bounds how many times one request goes back to a source
	// for more. Two extra rounds finish a page that filtering emptied; past
	// that, a search matching nothing addable would walk a whole catalogue at
	// a third party's expense to prove it.
	maxFillRounds = 3
)

// sourceFetchFor says how many rows to ask one source for.
func sourceFetchFor(limit int) int {
	fetch := limit * sourceOverFetch
	if fetch < minSourceFetch {
		fetch = minSourceFetch
	}
	if fetch > MaxEntriesPerPage {
		fetch = MaxEntriesPerPage
	}
	return fetch
}

// List returns one page: the merged, deduplicated, filtered result, bounded by
// the caller's limit.
//
// The limit means what it says here and nowhere else. Each source is asked for
// a window of its own (see sourceFetchFor), the windows are merged in
// preference order, entries this host could not import are dropped unless the
// caller asked for them, duplicates are removed, and what survives is handed
// out limit at a time. Everything else about paging follows from that being
// the arrangement rather than the other way round.
//
// Sources are read round-robin rather than in preference order, so a page of
// ten across four catalogues is a few from each. Preference order still
// decides *which copy of a duplicate survives* -- that is what claimIdentities
// is for -- but letting it decide the reading order too would mean the second
// catalogue was never reached until the first's twenty-four thousand entries
// ran out, which is the same as not having it.
//
// Deduplication is over the windows in hand, which is sufficient rather than
// approximate for the case it is for. A search asks every source the same
// question and every source answers with its matches at once, so two copies of
// a server land in the same merge and one is removed -- the only situation in
// which a person can see that there are two. Reconciling a browse across
// twenty-four thousand entries would mean holding both catalogues whole.
//
// Query.Source and Query.Sort are read here and nowhere below: a catalogue's
// own client is asked a plain question, and which catalogues to ask and how to
// arrange the answer are this level's decisions. Both narrow before the fetch
// -- see scopeFor -- and only the arrangement happens after it.
func (m *Multi) List(ctx context.Context, q Query) (Page, error) {
	if len(m.sources) == 0 {
		return Page{}, errors.New("registry: no catalogue is configured")
	}
	q = q.Normalised()
	if !q.Sort.known() {
		return Page{}, fmt.Errorf("%w %q", ErrUnknownSort, q.Sort)
	}
	covered, excluded, err := m.scopeFor(q)
	if err != nil {
		return Page{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	fetch := sourceFetchFor(limit)

	// A cursor that decoded to nothing usable -- garbage, one written by a
	// host configured with different sources, one written by an older build,
	// or one belonging to a different question -- restarts the listing rather
	// than paging nothing. Restarting is a state the caller already handles;
	// an empty page with no explanation is not.
	resume := decodeMultiCursor(q.Cursor, sourceNames(covered), fingerprint(q))
	resuming := len(resume) > 0

	walkers := make([]*sourceWalk, len(covered))
	for i, source := range covered {
		w := &sourceWalk{
			client: source, index: i, name: source.Source(),
			terms: searchTerms(q.Search),
			// Read from the source rather than from its answer, so that it is
			// still reported for a catalogue that failed or was exhausted on
			// an earlier page. Whether a catalogue publishes a usage figure is
			// a fact about the catalogue, not about one response.
			uses: reportsUses(source),
		}
		if resuming {
			pos, listed := resume[w.name]
			// A source absent from the cursor had nothing further last time.
			// Asking it again would restart it from the beginning and repeat
			// its first window under every subsequent cursor.
			w.done, w.pos = !listed, pos
		}
		walkers[i] = w
	}

	// One deadline for the whole request rather than one per round, because
	// what is being bounded is how long a person waits for a page. A source
	// slower than this is dropped from the page it was too slow for; its own
	// request runs on and fills the cache, so the next page has it.
	deadline := time.Now().Add(m.budget)
	entries := make([]Entry, 0, limit)
	seen := map[string]bool{}

	for round := 0; round < maxFillRounds; round++ {
		var pending []*sourceWalk
		for _, w := range walkers {
			if w.needsFetch() {
				pending = append(pending, w)
			}
		}
		if len(pending) > 0 {
			if err := m.fill(ctx, q, fetch, pending, deadline); err != nil {
				return Page{}, err
			}
		}
		claimed := claimIdentities(walkers)
		for len(entries) < limit {
			took := false
			for _, w := range walkers {
				if len(entries) >= limit {
					break
				}
				if entry, ok := w.take(claimed, seen, q.IncludeUnaddable); ok {
					entries = append(entries, entry)
					took = true
				}
			}
			if !took {
				break
			}
		}
		if len(entries) >= limit {
			break
		}
		// Another round only where one would find something, and only while
		// there is budget left to spend on asking.
		refillable := false
		for _, w := range walkers {
			if w.needsFetch() {
				refillable = true
				break
			}
		}
		if !refillable || !time.Now().Before(deadline) {
			break
		}
	}

	// Sorted after the page is assembled, which is the only place it can
	// honestly happen and is why the label on the control matters. Which
	// entries reach this line was decided by the round-robin merge over the
	// window each source handed back; a *global* order would mean holding
	// twenty-four thousand entries from behind four opaque cursors, and no
	// source offers to sort for us. So this orders the rows on the page.
	//
	// Most used is the exception, and only because it was narrowed first: a
	// most-used listing covers the catalogues that publish a figure, and
	// where that is one catalogue the rows arrive in its own total order and
	// stay in it page after page. That is a real global ordering over what is
	// shown, and it is the only one here that is.
	//
	// It cannot disturb the cursor. Every position was recorded as rows were
	// taken from the windows; rearranging the taken rows afterwards changes
	// what a reader sees and nothing about where each source resumes.
	sortEntries(entries, q.Sort)

	page := Page{Source: m.Source(), Entries: entries}
	next := multiCursor{Query: fingerprint(q), Positions: map[string]sourcePosition{}}
	answered := 0
	var failures []error
	oldest := time.Time{}

	for _, w := range walkers {
		switch {
		case w.err != nil:
			failures = append(failures, w.err)
			page.Sources = append(page.Sources, SourceStatus{
				Source: w.name,
				Uses:   w.uses,
				Error:  clean(w.err.Error(), maxReasonRunes),
			})
			// A source that failed keeps the place it came in with, so the
			// next page asks it again. Dropping it -- which is what happened
			// before -- turned one bad minute into the permanent end of that
			// catalogue for the rest of the listing.
			next.set(w.name, w.pos)
		case w.done && !w.answered:
			// Exhausted on an earlier page. Reported so that the response
			// still accounts for every configured source rather than looking
			// as though one had vanished.
			page.Sources = append(page.Sources, SourceStatus{
				Source: w.name, OK: true, Uses: w.uses})
		default:
			answered++
			page.Stale = page.Stale || w.stale
			if !w.retrieved.IsZero() && (oldest.IsZero() || w.retrieved.Before(oldest)) {
				oldest = w.retrieved
			}
			page.Sources = append(page.Sources, SourceStatus{
				Source: w.name, OK: true, Stale: w.stale,
				RetrievedAt: w.retrieved,
				Entries:     w.emitted,
				Judged:      w.judged,
				Addable:     w.addable,
				Total:       w.total,
				Uses:        w.uses,
				// Carried across rather than recomputed. The status this
				// builds is this level's -- how many entries survived
				// deduplication is not something a source can know -- but a
				// note is the source's own account of its answer, and
				// dropping it here would silence the one catalogue that has
				// something to say the moment it is merged with another.
				Note: w.note,
			})
			if !w.done {
				next.set(w.name, w.pos)
			}
		}
	}

	if answered == 0 {
		// Nothing answered, so there is no partial page to serve honestly.
		// Every failure is named rather than only the first, so an operator
		// does not fix one catalogue and find the other still broken.
		return Page{}, joinSourceErrors(failures)
	}

	// The catalogues this order left out, after the ones that answered. They
	// are reported for the same reason a failed source is: a page that is
	// shorter because three of four catalogues are not in it reads as "there
	// is nothing else" unless it says which are missing and why. A source the
	// *caller* scoped away is not here -- they asked for one catalogue, and
	// listing the others back at them is noise rather than an account.
	page.Sources = append(page.Sources, excluded...)

	page.NextCursor = next.encode()
	page.Addable = estimateAddable(page.Sources)
	page.RetrievedAt = oldest
	if page.RetrievedAt.IsZero() {
		page.RetrievedAt = time.Now().UTC()
	}
	return page, nil
}

// fill asks several sources for their next window at once, bounded as a whole.
//
// Concurrent because a page across four catalogues must not cost four round
// trips one after another -- with a cold cache that is the difference between
// a page and a wait. Bounded because the slowest catalogue must not decide how
// long a page takes.
//
// Results come back over a buffered channel rather than into the walkers
// directly, because a goroutine still running when the deadline passes would
// otherwise be writing to something already being read. The buffer is one slot
// per source, so a late answer is delivered to nobody and the goroutine ends
// rather than blocking.
func (m *Multi) fill(ctx context.Context, q Query, limit int, pending []*sourceWalk, deadline time.Time) error {
	type arrival struct {
		w    *sourceWalk
		page Page
		err  error
	}
	arrivals := make(chan arrival, len(pending))
	for _, w := range pending {
		w.arrived = false
		go func(w *sourceWalk) {
			page, err := w.client.List(ctx, Query{
				Search: q.Search, Cursor: w.pos.Cursor, Limit: limit,
			})
			arrivals <- arrival{w: w, page: page, err: err}
		}(w)
	}

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for range pending {
		select {
		case a := <-arrivals:
			a.w.receive(a.page, a.err)
		case <-timer.C:
			for _, w := range pending {
				if !w.arrived {
					w.receive(Page{}, fmt.Errorf(
						"registry: %s did not answer within %s", w.name, m.budget))
				}
			}
			return nil
		case <-ctx.Done():
			// The caller gave up, which is different from a catalogue being
			// slow: there is nobody left to serve a partial page to.
			return ctx.Err()
		}
	}
	return nil
}

// estimateAddable is roughly how many servers across these catalogues could
// be imported here.
//
// A floor and an estimate, and the shape of it is forced by what the sources
// will say. Only two of the public catalogues report how much they hold: Smithery sends a
// totalCount, and Docker's catalogue arrives as one document whose length is
// the count. The official registry and PulseMCP page an opaque cursor and
// report no size at all, so nothing here knows how many of their twenty-odd
// thousand entries exist -- they contribute only what this host has actually
// looked at.
//
// Addability cannot be counted either, because counting it means parsing every
// document: mcpservers.Parse and mcpremote.Fields over twenty-four thousand
// server.json files is not a thing to do behind a page load, and no catalogue
// publishes the answer. So what is done instead is the honest cheap thing: the
// ratio *is* measured, over the documents this host parsed anyway while
// building the page, and applied to the size the source reported.
//
// Two consequences, both stated rather than hidden. Smithery's measured ratio
// comes from the five hundred rows its listing will page to, which are its
// most popular and so likelier to be deployed than its tail -- the
// extrapolation is optimistic for Smithery. And the two sources that report no
// size are counted at what was seen, which is a small fraction of what they
// hold. The first pushes the number up and the second, much harder, pushes it
// down; the result is a lower bound in practice, which is why it is rounded
// down and rendered with a "+".
//
// Rounded to two significant figures above a thousand, deliberately. A page
// that says "7,900+" is read as the estimate it is; one that says "7,952"
// claims a precision nothing here has.
func estimateAddable(sources []SourceStatus) int {
	total := 0
	for _, s := range sources {
		switch {
		case !s.OK || s.Judged <= 0:
			// A source that did not answer contributes nothing, and the
			// status beside it says so. Carrying a remembered figure for it
			// would be the one thing worse than a smaller number: a total
			// that does not move when a catalogue goes down.
		case s.Total > s.Judged:
			total += int(math.Round(float64(s.Total) * float64(s.Addable) / float64(s.Judged)))
		default:
			total += s.Addable
		}
	}
	return floorSignificant(total)
}

// floorSignificant rounds down to two significant figures, above a thousand.
//
// Below that the number is small enough to be read as what it is, and rounding
// 137 to 130 would discard a figure this host actually counted.
func floorSignificant(n int) int {
	if n < 1000 {
		return n
	}
	scale := 1
	for n/scale >= 100 {
		scale *= 10
	}
	return (n / scale) * scale
}

// --- reading one source, one window at a time -------------------------------

// sourceWalk is one source's place in a listing.
//
// The thing it exists to hold is the pair: which of the source's own windows
// is being read, and how far into it. Before this, the merged cursor carried
// only the first, which is all that is needed when every row of every window
// is handed out. It is not enough once the merged page is bounded and filtered
// -- stopping halfway through a source's window and resuming at the start of
// the next one would silently skip the other half.
type sourceWalk struct {
	client Client
	index  int
	name   string

	// pos is where this source resumes: its own cursor for the window being
	// read, and how many of that window's rows are already accounted for --
	// handed out, filtered away, or dropped as a duplicate.
	pos sourcePosition
	// window is the rows at pos.Cursor, and held says they are loaded.
	window []Entry
	held   bool
	// next is the source's cursor after window. Empty ends the source.
	next string
	// done is a source with nothing further anywhere.
	done bool

	// terms are the words every entry from this source must carry, from the
	// query being served. Held per walk so that filtering happens on this
	// host's own copy of a window rather than on the cache's shared page.
	terms []string

	// uses is whether this catalogue publishes how often a server is called,
	// asked of the source itself rather than read from an answer.
	uses      bool
	arrived   bool
	answered  bool
	err       error
	stale     bool
	retrieved time.Time
	note      string
	total     int
	judged    int
	addable   int
	emitted   int
}

// needsFetch reports a source that could produce more but is holding nothing.
func (w *sourceWalk) needsFetch() bool {
	return !w.done && w.err == nil && !w.held
}

// receive takes one source's answer.
func (w *sourceWalk) receive(page Page, err error) {
	w.arrived = true
	if err != nil {
		w.err = err
		return
	}
	w.answered = true
	// Copied rather than referenced. The page underneath may be the cache's
	// own, shared with every other request holding it, and the Source below
	// is written into the rows.
	w.window = append([]Entry(nil), page.Entries...)
	for i := range w.window {
		if w.window[i].Source == "" {
			w.window[i].Source = w.name
		}
	}
	// What the catalogue sent that does not actually answer the question is
	// dropped here, on this host's own copy. A page can come back shorter than
	// the source's window as a result, which is the same thing deduplication
	// already does to it -- and the cursor is fingerprinted with the query, so
	// the offsets recorded against a filtered window are only ever read back
	// under the query that produced it.
	w.window = keepMatching(w.window, w.terms)
	w.next = page.NextCursor
	w.held = true
	w.stale = page.Stale
	w.retrieved = page.RetrievedAt
	// A window shorter than the offset means the source's answer changed
	// under the cursor -- it was refetched and came back with fewer rows.
	// Clamping is the honest recovery: what is skipped was already handed
	// out, and what is left is read from where there is something to read.
	if w.pos.Offset > len(w.window) {
		w.pos.Offset = len(w.window)
	}
	for _, s := range page.Sources {
		if s.Source == w.name {
			w.note, w.total, w.judged, w.addable = s.Note, s.Total, s.Judged, s.Addable
			break
		}
	}
	w.advance()
}

// advance moves to the next window once the current one is used up.
func (w *sourceWalk) advance() {
	if !w.held || w.pos.Offset < len(w.window) {
		return
	}
	if w.next == "" {
		w.done, w.held, w.window = true, false, nil
		return
	}
	w.pos = sourcePosition{Cursor: w.next}
	w.window, w.next, w.held = nil, "", false
}

// take hands out this source's next eligible row, if it is holding one.
//
// Rows passed over on the way are accounted for as surely as the one returned:
// an entry this host would refuse, or one a preferred source is going to show
// instead, has been dealt with and must not be offered again on the next page.
func (w *sourceWalk) take(claimed map[string]int, seen map[string]bool, includeUnaddable bool) (Entry, bool) {
	for w.held && w.pos.Offset < len(w.window) {
		entry := w.window[w.pos.Offset]
		w.pos.Offset++
		key := identity(entry)
		switch {
		case seen[key]:
			// Already handed out on this page, by this source or another.
		case claimed[key] != w.index:
			// A more preferred source is holding this same server.
		case !entry.Addable && !includeUnaddable:
			// Not something a click could add. Half of what the catalogues
			// publish only runs locally, and a page of ten that spends five
			// rows saying so is a page of five.
		default:
			seen[key] = true
			w.emitted++
			w.advance()
			return entry, true
		}
	}
	w.advance()
	return Entry{}, false
}

// claimIdentities decides, across every window currently held, which source
// owns each server.
//
// Preference order, first claim wins -- the same rule that decides which
// catalogue answers a Get, applied to the merge. It is computed up front
// rather than as rows are taken because rows are taken round-robin: without
// it, the least preferred catalogue holding a server early in its window would
// hand it out before the most preferred catalogue reached its own copy, and
// the preference order would decide nothing.
func claimIdentities(walkers []*sourceWalk) map[string]int {
	claimed := map[string]int{}
	for _, w := range walkers {
		if !w.held {
			continue
		}
		for _, entry := range w.window[w.pos.Offset:] {
			key := identity(entry)
			if _, taken := claimed[key]; !taken {
				claimed[key] = w.index
			}
		}
	}
	return claimed
}

// Get returns one entry, from the first source that has it.
//
// Preference order, so a name both catalogues know is answered by the more
// trusted one -- the same rule the merge applies, applied to the lookup, so
// that clicking an entry cannot hand back a different catalogue's copy of it.
func (m *Multi) Get(ctx context.Context, name string) (Detail, error) {
	var failures []error
	for _, source := range m.sources {
		detail, err := source.Get(ctx, name)
		switch {
		case err == nil:
			if detail.Entry.Source == "" {
				detail.Entry.Source = source.Source()
			}
			return detail, nil
		case errors.Is(err, ErrNotFound):
			// Not this catalogue's; ask the next.
			continue
		default:
			// A source that is down must not turn into "no such server",
			// which would read as the entry having been withdrawn. It is kept
			// and reported only if no other source has the name.
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return Detail{}, joinSourceErrors(failures)
	}
	return Detail{}, ErrNotFound
}

// noteFrom finds what a source said about its own page.
//
// By name rather than by position, because a source reports itself in its own
// Sources slice and nothing promises that slice has exactly one element or
// that the element is first.
func noteFrom(page Page, source string) string {
	for _, s := range page.Sources {
		if s.Source == source {
			return s.Note
		}
	}
	return ""
}

// identity is what makes two entries from two catalogues the same server.
//
// The endpoint, when there is one. A name cannot do this job: the official
// registry names a server in reverse-DNS with the publisher's domain
// ("app.linear/linear") and Docker names it with a bare slug ("linear"), so
// the same server has two names that no rule turns into each other. The
// address it is dialled at is the same string in both catalogues for thirty-two
// of the entries they share, and it is also the thing that actually matters:
// two entries that resolve to one endpoint are one server however they are
// named, and importing both would mount the same upstream twice.
//
// An entry with no endpoint -- a package-only server, a Docker container, a
// document this host refused -- falls back to the source and name, which makes
// it unique to its own catalogue. That is right rather than a compromise:
// nothing can establish that two unreachable entries are the same server, and
// merging them on a name that happened to match would hide one of them.
func identity(e Entry) string {
	if endpoint := normaliseEndpoint(e.URL); endpoint != "" {
		return "url\x00" + endpoint
	}
	return "name\x00" + e.Source + "\x00" + strings.ToLower(e.Name)
}

// normaliseEndpoint reduces a URL to the parts that decide whether two of them
// address the same server: scheme, host, port and path.
//
// Case and a trailing slash are noise -- "https://MCP.Linear.app/mcp/" and
// "https://mcp.linear.app/mcp" are one address. A query string is not noise
// and is kept, because it can select a tenant. A URL that will not parse is
// not normalised into something that might collide with a real one; it is
// dropped, and the entry falls back to its name.
func normaliseEndpoint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !defaultPort(u.Scheme, port) {
		host += ":" + port
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/")
	out := strings.ToLower(u.Scheme) + "://" + host + path
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

func defaultPort(scheme, port string) bool {
	return (strings.EqualFold(scheme, "https") && port == "443") ||
		(strings.EqualFold(scheme, "http") && port == "80")
}

// joinSourceErrors reports every catalogue that failed, in one message.
func joinSourceErrors(failures []error) error {
	var reasons []string
	for _, err := range failures {
		if err != nil {
			reasons = append(reasons, err.Error())
		}
	}
	if len(reasons) == 0 {
		return errors.New("registry: no catalogue answered")
	}
	sort.Strings(reasons)
	return errors.New(strings.Join(reasons, "; "))
}

// --- the composite cursor ---------------------------------------------------

// sourcePosition is where one source's listing has reached.
//
// Two fields, and the second is the whole reason this type exists. A source's
// own cursor says which of its windows to read; it cannot say how much of that
// window has already been handed out, and once the merged page is bounded and
// filtered it very often stops halfway through one. Carrying the cursor alone
// would resume at the start of the *next* window and lose everything after the
// stopping point.
//
// The offset is an index into a window this host asked for and will ask for
// again, not an offset into the catalogue. Re-asking is what makes it sound:
// the source is behind a cache, so continuing a half-read window costs nothing
// upstream, and re-reading rows that were already handed out is prevented by
// the offset rather than by hoping the far end returns the same page twice.
type sourcePosition struct {
	// Cursor is the source's own cursor for the window being read -- not the
	// one after it. Empty is the source's first window.
	Cursor string `json:"c,omitempty"`
	// Offset is how many of that window's rows are already accounted for.
	Offset int `json:"o,omitempty"`
}

// multiCursor carries one position per source, and the question they are
// positions in.
//
// Opaque to everyone outside this file, and it has to be: a source's cursor
// belongs to that source and means nothing here. What this adds is which
// source each one came from, so that a page continues each catalogue where it
// left off rather than restarting one of them.
//
// A source absent from Positions has no further pages. That is why it is a map
// rather than a list: adding or removing a source between two requests changes
// a list's positions and would silently hand one source's cursor to another.
//
// Query is a fingerprint of the question, and it exists because "no further
// pages" is only true of the question that was asked. Continuing a most-used
// listing -- which covers one catalogue -- with a cursor from an unscoped one
// would read the other three as exhausted and drop them from the rest of the
// listing with nothing saying so. The same hazard has always been there for a
// changed search term; the fingerprint closes both.
type multiCursor struct {
	Query     string                    `json:"q,omitempty"`
	Positions map[string]sourcePosition `json:"p,omitempty"`
}

func (c multiCursor) set(source string, pos sourcePosition) { c.Positions[source] = pos }

// encode renders the cursor for the wire. Empty when nothing has more pages,
// which is how a caller learns the listing has ended.
func (c multiCursor) encode() string {
	if len(c.Positions) == 0 {
		return ""
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// fingerprint identifies the question a cursor belongs to.
//
// Everything that decides which rows a page is assembled from, and nothing
// that does not. The limit is left out deliberately: a caller may ask for
// twenty rows and then for ten, and every position stays exactly as valid,
// because an offset counts rows accounted for in a source's window rather than
// pages handed out.
//
// A hash rather than the values, because a search term is a hundred and
// twenty-eight runes of somebody's text and a cursor is a thing that travels
// in a URL. Collisions cost a resumed listing that should have restarted,
// which is the failure this already had everywhere; sixty-four bits is far
// more than enough to make that not the interesting case.
func fingerprint(q Query) string {
	h := fnv.New64a()
	for _, part := range []string{
		q.Search, string(q.Sort), strings.ToLower(q.Source),
		strconv.FormatBool(q.IncludeUnaddable),
	} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

// decodeMultiCursor reads a composite cursor.
//
// A cursor that will not decode, that names a source this host is not
// configured with, that an older build wrote in a shape this one does not
// read, or that belongs to a different question, yields an empty one: the
// listing restarts rather than paging a catalogue with a token from somewhere
// else. Restarting is a state the caller already handles; handing an arbitrary
// string to a third party's pagination is not.
func decodeMultiCursor(encoded string, sources []string, want string) map[string]sourcePosition {
	out := map[string]sourcePosition{}
	if opaque(encoded, maxCursorRunes) == "" {
		return out
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) > maxCursorRunes*len(sources)+len(sources)*64+64 {
		return out
	}
	var decoded multiCursor
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return out
	}
	if decoded.Query != want {
		return out
	}
	known := make(map[string]bool, len(sources))
	for _, s := range sources {
		known[s] = true
	}
	for source, pos := range decoded.Positions {
		if !known[source] {
			continue
		}
		// A negative offset is not a position; an over-long cursor is one the
		// source would not be asked with anyway. Either way the source
		// restarts rather than being handed nonsense.
		if pos.Offset < 0 || pos.Offset > MaxEntriesPerPage {
			continue
		}
		if pos.Cursor != "" && opaque(pos.Cursor, maxCursorRunes) == "" {
			continue
		}
		out[source] = pos
	}
	return out
}

// compile-time check that the multiplexer is itself a catalogue, which is what
// lets it be composed and cached like any other.
var _ Client = (*Multi)(nil)
