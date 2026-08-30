package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Index holds every catalogue's servers locally and answers from that.
//
// The arrangement it replaces proxied each request straight through to four
// third parties and merged whatever came back. That is the right shape for a
// catalogue somebody reads constantly and the wrong one here, for three
// reasons that were all visible on the page:
//
// A page was as long as the catalogues happened to make it. Each source was
// asked for a window, entries this host cannot import were dropped, and what
// survived was however many survived -- so Smithery contributed ten rows and
// Docker contributed two, from identical code. Nobody reading that page could
// tell it from broken paging.
//
// The size of the catalogue could only be estimated. A source that pages an
// opaque cursor cannot say how many servers it holds, so the total was sampled
// and extrapolated, and the page had to spend a sentence apologising for it.
//
// And search meant four different things at once, because each catalogue
// implements its own.
//
// Enumerating once and answering locally settles all three: pages are a
// consistent length because this host decides the length, the count is a
// count, and searching is one rule applied to one set.
//
// What it cannot do is make a catalogue hand over more than it will. Smithery
// publishes ten thousand servers and lists five hundred; the official registry
// pages twenty thousand and publishes no total at all. So the number this
// reports is what this host can actually offer, which is exact, rather than
// what exists in the world, which no caller here can know.
type Index struct {
	sources []Client
	ttl     time.Duration
	perPage int
	maxPer  int
	maxReqs int
	log     *slog.Logger
	now     func() time.Time

	mu       sync.Mutex
	snap     *Snapshot
	building bool
	// done is closed when a build finishes, so callers waiting on the first
	// snapshot are woken without polling. Replaced per build.
	done chan struct{}
}

// Snapshot is every catalogue as of one enumeration.
type Snapshot struct {
	At      time.Time
	Sources []SourceIndex
}

// SourceIndex is one catalogue's servers and the counts that describe them.
type SourceIndex struct {
	Source string
	// Entries is everything walked, addable or not. The filter belongs to the
	// query rather than to the index: a listing hides what cannot be imported
	// and a detail lookup must still find it.
	Entries []Entry
	// Listed is what the catalogue says it holds altogether, zero where it
	// does not say. It is the only figure here that is somebody else's claim.
	Listed int
	// Fetched is how many documents were actually walked, and Addable how
	// many of those this host would accept. Both are counted, not sampled.
	Addable     int
	OK          bool
	Err         string
	Note        string
	Uses        bool
	RetrievedAt time.Time
}

// Fetched is how many entries were walked.
func (s SourceIndex) Fetched() int { return len(s.Entries) }

// IndexOptions configures an Index.
type IndexOptions struct {
	// TTL is how long a snapshot is served before a refresh is started. Zero
	// takes DefaultTTL, which is a day.
	TTL time.Duration
	// PerPage is how many entries a source is asked for at a time while
	// enumerating. Zero takes a sensible page.
	PerPage int
	// MaxPerSource bounds how many entries are held for one catalogue, and
	// MaxRequests bounds how many requests one enumeration may make of it.
	// Both are guards on a third party's catalogue growing without limit --
	// twenty thousand entries is a browse, two million is this host's memory.
	MaxPerSource int
	MaxRequests  int
	Log          *slog.Logger
	Now          func() time.Time
}

const (
	// defaultIndexPerPage is what a source is asked for at a time. Large
	// enough that enumerating is tens of requests rather than hundreds, and
	// inside what every catalogue here accepts.
	defaultIndexPerPage = 100
	// defaultMaxPerSource bounds one catalogue's share of memory.
	defaultMaxPerSource = 40000
	// defaultMaxRequests bounds one enumeration. At the page size above it is
	// the same bound stated the other way, and it is what stops a catalogue
	// that never returns an empty cursor from looping forever.
	defaultMaxRequests = 500
)

// NewIndex returns an index over the given catalogues. It fetches nothing
// until something asks it to.
func NewIndex(sources []Client, opts IndexOptions) *Index {
	ix := &Index{
		sources: sources,
		ttl:     opts.TTL,
		perPage: opts.PerPage,
		maxPer:  opts.MaxPerSource,
		maxReqs: opts.MaxRequests,
		log:     opts.Log,
		now:     opts.Now,
	}
	if ix.ttl <= 0 {
		ix.ttl = DefaultTTL
	}
	if ix.perPage <= 0 {
		ix.perPage = defaultIndexPerPage
	}
	if ix.maxPer <= 0 {
		ix.maxPer = defaultMaxPerSource
	}
	if ix.maxReqs <= 0 {
		ix.maxReqs = defaultMaxRequests
	}
	if ix.log == nil {
		ix.log = slog.New(slog.DiscardHandler)
	}
	if ix.now == nil {
		ix.now = time.Now
	}
	return ix
}

// Sources names the catalogues, in configured order.
func (ix *Index) Sources() []string { return sourceNames(ix.sources) }

// Source names the index itself, for a Page assembled from several.
func (ix *Index) Source() string {
	if len(ix.sources) == 1 {
		return ix.sources[0].Source()
	}
	return "catalogues"
}

// ReportsUses reports whether any catalogue publishes a usage figure.
func (ix *Index) ReportsUses() bool {
	for _, s := range ix.sources {
		if reportsUses(s) {
			return true
		}
	}
	return false
}

// Snapshot returns what is currently held, building one if there is none.
func (ix *Index) Snapshot(ctx context.Context) (*Snapshot, error) {
	if len(ix.sources) == 0 {
		return nil, errors.New("registry: no catalogue is configured")
	}

	ix.mu.Lock()
	snap, building, done := ix.snap, ix.building, ix.done
	stale := snap == nil || ix.now().Sub(snap.At) >= ix.ttl
	switch {
	case snap != nil && !stale:
		ix.mu.Unlock()
		return snap, nil
	case building:
		ix.mu.Unlock()
		// Somebody else is already asking the catalogues. With a snapshot in
		// hand there is no reason to wait for theirs; without one there is
		// nothing to serve, so wait rather than starting a second enumeration
		// of the same catalogues.
		if snap != nil {
			return snap, nil
		}
		select {
		case <-done:
			return ix.current()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	ix.building = true
	ix.done = make(chan struct{})
	done = ix.done
	ix.mu.Unlock()

	// A refresh behind a served snapshot is nobody's request, so it gets its
	// own context: the browser that happened to trigger it navigating away
	// must not abandon an enumeration every other reader is waiting on.
	buildCtx := ctx
	var cancel context.CancelFunc
	if snap != nil {
		buildCtx, cancel = context.WithTimeout(
			context.WithoutCancel(ctx), backgroundRefreshTimeout*4)
	}

	build := func() {
		defer func() {
			if cancel != nil {
				cancel()
			}
			ix.mu.Lock()
			ix.building = false
			close(done)
			ix.mu.Unlock()
		}()
		built := ix.enumerate(buildCtx)
		ix.mu.Lock()
		// A build that reached nothing at all does not replace a snapshot that
		// did. Serving an empty catalogue because a refresh failed is worse
		// than serving a day-old one.
		if ix.snap == nil || anyOK(built) {
			ix.snap = built
		}
		ix.mu.Unlock()
	}

	if snap != nil {
		go build()
		return snap, nil
	}
	build()
	return ix.current()
}

// Invalidate drops the held enumeration so the next read rebuilds it.
//
// For a source that changes without being asked -- the operator's own list,
// re-read on its own schedule. Without this the index would keep answering
// from an enumeration taken before the change, and a server somebody added to
// their catalogue would appear up to a day later for no reason they could see.
//
// It drops rather than rebuilds: enumerating every catalogue is work, and
// doing it because a file changed would make one source's schedule everybody
// else's. The next browse pays for it, which is the same deal every other
// staleness here strikes.
func (ix *Index) Invalidate() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.snap = nil
}

func (ix *Index) current() (*Snapshot, error) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.snap == nil {
		return nil, errors.New("registry: no catalogue could be read")
	}
	return ix.snap, nil
}

func anyOK(s *Snapshot) bool {
	for _, src := range s.Sources {
		if src.OK {
			return true
		}
	}
	return false
}

// enumerate walks every catalogue to the end, or to its bound.
//
// Concurrently, because they are four independent third parties and doing them
// in turn makes the first build as slow as the sum of them. One failing is
// recorded on that source and costs the others nothing.
func (ix *Index) enumerate(ctx context.Context) *Snapshot {
	out := &Snapshot{At: ix.now(), Sources: make([]SourceIndex, len(ix.sources))}

	var wg sync.WaitGroup
	for i, src := range ix.sources {
		wg.Add(1)
		go func(i int, src Client) {
			defer wg.Done()
			out.Sources[i] = ix.walk(ctx, src)
		}(i, src)
	}
	wg.Wait()
	return out
}

// walk reads one catalogue from its beginning until it ends or a bound stops
// it.
func (ix *Index) walk(ctx context.Context, src Client) SourceIndex {
	name := src.Source()
	held := SourceIndex{Source: name, Uses: reportsUses(src), RetrievedAt: ix.now()}

	seen := map[string]bool{}
	cursor := ""
	for request := 0; request < ix.maxReqs; request++ {
		page, err := src.List(ctx, Query{Limit: ix.perPage, Cursor: cursor})
		if err != nil {
			// Whatever was already read is kept: a catalogue that answered
			// forty pages and failed on the forty-first has told this host
			// about four thousand servers, and throwing them away to report a
			// clean failure serves nobody.
			held.Err = err.Error()
			if len(held.Entries) > 0 {
				held.OK = true
				held.Note = fmt.Sprintf(
					"stopped early: %s", err.Error())
			}
			ix.log.WarnContext(ctx, "a catalogue could not be read to the end",
				"source", name, "held", len(held.Entries), "error", err)
			return held
		}
		held.OK = true
		held.RetrievedAt = page.RetrievedAt
		for _, s := range page.Sources {
			if s.Source == name {
				if s.Total > held.Listed {
					held.Listed = s.Total
				}
				if s.Note != "" {
					held.Note = s.Note
				}
			}
		}

		for _, e := range page.Entries {
			if e.Source == "" {
				e.Source = name
			}
			// A catalogue that repeats an entry across pages -- or one whose
			// cursor loops -- must not grow this without bound.
			if seen[e.Name] {
				continue
			}
			seen[e.Name] = true
			held.Entries = append(held.Entries, e)
		}

		cursor = page.NextCursor
		if cursor == "" {
			break
		}
		if len(held.Entries) >= ix.maxPer {
			held.Note = fmt.Sprintf(
				"stopped at %d servers, which is as many as this host holds "+
					"from one catalogue", ix.maxPer)
			break
		}
		if request == ix.maxReqs-1 {
			held.Note = fmt.Sprintf(
				"stopped after %d requests, which is as far as one refresh reads",
				ix.maxReqs)
		}
	}

	if len(held.Entries) > ix.maxPer {
		held.Entries = held.Entries[:ix.maxPer]
	}
	held.Addable = countAddable(held.Entries)
	// A catalogue that publishes no total has now been counted. Saying it
	// holds what was read is the honest reading only when the read finished.
	if held.Listed == 0 && held.Note == "" {
		held.Listed = len(held.Entries)
	}
	return held
}

// List answers from the snapshot.
func (ix *Index) List(ctx context.Context, q Query) (Page, error) {
	q = q.Normalised()
	if !q.Sort.known() {
		return Page{}, fmt.Errorf("%w %q", ErrUnknownSort, q.Sort)
	}
	snap, err := ix.Snapshot(ctx)
	if err != nil {
		return Page{}, err
	}

	wanted, excluded, err := ix.scope(snap, q)
	if err != nil {
		return Page{}, err
	}

	terms := searchTerms(q.Search)
	var matched []Entry
	// The same server published in two catalogues is one server. Identity is
	// the endpoint it dials where there is one, because that is what actually
	// makes two rows the same thing -- the catalogues do not agree on names.
	//
	// First source wins, in configured order, which is the same preference
	// rule that decides which catalogue answers a Get. Without this the
	// overlap between the official registry and a catalogue built from it
	// appears twice on every page.
	seen := make(map[string]bool)
	statuses := make([]SourceStatus, 0, len(wanted))
	for _, src := range wanted {
		kept := 0
		for _, e := range src.Entries {
			if !e.Addable && !q.IncludeUnaddable {
				continue
			}
			if !matchesSearch(e, terms) {
				continue
			}
			key := identity(e)
			if seen[key] {
				continue
			}
			seen[key] = true
			matched = append(matched, e)
			kept++
		}
		statuses = append(statuses, SourceStatus{
			Source:      src.Source,
			OK:          src.OK,
			Entries:     kept,
			Judged:      src.Fetched(),
			Addable:     src.Addable,
			Total:       src.Listed,
			Uses:        src.Uses,
			Note:        src.Note,
			Error:       src.Err,
			RetrievedAt: src.RetrievedAt,
		})
	}

	// Asking for nothing is asking for the newest first. Every order here ends
	// in a name comparison, so two entries the order cannot separate keep the
	// same places between one page and the next -- without which one of them
	// would appear twice and the other not at all.
	order := q.Sort
	if order == SortDefault {
		order = SortRecentlyUpdated
	}
	sortEntries(matched, order)
	// Named, so that a catalogue missing from an order is a fact on the page
	// rather than a shorter list with no explanation.
	statuses = append(statuses, excluded...)

	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	offset := decodeOffset(q.Cursor, fingerprint(q))
	if offset > len(matched) {
		offset = len(matched)
	}
	end := offset + limit
	if end > len(matched) {
		end = len(matched)
	}

	page := Page{
		Source:      ix.Source(),
		Entries:     append([]Entry(nil), matched[offset:end]...),
		RetrievedAt: snap.At,
		Sources:     statuses,
		// Counted rather than estimated: how many distinct servers, across the
		// catalogues in view, this host would accept an import of.
		//
		// Distinct is the word doing the work. Summing what each catalogue
		// contributed counts a server published in two of them twice, which
		// would put a headline above a list that cannot contain that many.
		Addable: ix.countAddable(wanted),
	}
	if end < len(matched) {
		page.NextCursor = encodeOffset(end, fingerprint(q))
	}
	// Stale means the answer is older than it should be, which here is a
	// snapshot past its lifetime being served while a refresh runs behind it.
	page.Stale = ix.now().Sub(snap.At) >= ix.ttl
	return page, nil
}

// Get finds one entry's document, asking the catalogue that holds it.
func (ix *Index) Get(ctx context.Context, name string) (Detail, error) {
	var firstErr error
	for _, src := range ix.sources {
		detail, err := src.Get(ctx, name)
		if err == nil {
			return detail, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = ErrNotFound
	}
	return Detail{}, firstErr
}

// Close releases whatever the sources hold.
func (ix *Index) Close() error {
	var errs []error
	for _, s := range ix.sources {
		if c, ok := s.(interface{ Close() error }); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// scope narrows the snapshot to the catalogues the query covers.
//
// Two narrowings, and the second is the interesting one. A named source is the
// obvious cut. Ordering by use is the other: a catalogue that publishes no
// usage figure is left out of that order rather than sorted to the bottom,
// because an entry with no figure has not scored zero -- it has not been
// asked, and ranking it below a server with one recorded call would be this
// host inventing the comparison.
func (ix *Index) scope(snap *Snapshot, q Query) ([]SourceIndex, []SourceStatus, error) {
	covered := snap.Sources
	if named := strings.TrimSpace(q.Source); named != "" {
		found := false
		for _, s := range snap.Sources {
			if strings.EqualFold(s.Source, named) {
				covered, found = []SourceIndex{s}, true
				break
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("%w %q; this host browses %s",
				ErrUnknownSource, named, strings.Join(ix.Sources(), ", "))
		}
	}
	if q.Sort != SortMostUsed {
		return covered, nil, nil
	}

	var ranked []SourceIndex
	var excluded []SourceStatus
	for _, s := range covered {
		if s.Uses {
			ranked = append(ranked, s)
			continue
		}
		excluded = append(excluded, SourceStatus{
			Source: s.Source, OK: true,
			Note: "does not publish how often a server is used, so it is not in this order",
		})
	}
	if len(ranked) == 0 {
		return nil, nil, fmt.Errorf(
			"%w: no catalogue here publishes how often a server is used", ErrSortUnavailable)
	}
	return ranked, excluded, nil
}

// countAddable is how many distinct importable servers these catalogues hold.
//
// Deduplicated the same way the listing is, and deliberately ignoring the
// search: it answers "how much is there", which is a fact about the
// catalogues, not about the query somebody is part-way through typing.
func (ix *Index) countAddable(sources []SourceIndex) int {
	seen := make(map[string]bool)
	for _, src := range sources {
		for _, e := range src.Entries {
			if !e.Addable {
				continue
			}
			seen[identity(e)] = true
		}
	}
	return len(seen)
}

// offsetCursor is a position in this host's own list.
//
// It carries the query's fingerprint for the same reason the merged cursor did:
// an offset into the results of one question means nothing against another, and
// a cursor pasted across a changed search would page through the wrong list
// silently rather than starting again.
type offsetCursor struct {
	Offset int    `json:"o"`
	Query  string `json:"q"`
}

func encodeOffset(offset int, print string) string {
	raw, err := json.Marshal(offsetCursor{Offset: offset, Query: print})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeOffset(cursor, print string) int {
	if cursor == "" {
		return 0
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0
	}
	var c offsetCursor
	if err := json.Unmarshal(raw, &c); err != nil || c.Query != print || c.Offset < 0 {
		// Garbage, or a cursor belonging to a different question. Restarting
		// is a state the caller already handles; paging nothing is not.
		return 0
	}
	return c.Offset
}
