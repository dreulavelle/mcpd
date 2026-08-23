package registry

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"
)

// DefaultTTL is how long a catalogue answer is reused when the catalogue says
// nothing about it.
//
// A fallback, not a policy. Where a catalogue sends Cache-Control it is
// honoured instead -- see readFreshness -- and this is what is left for the
// official registry, which sends no ETag, no Last-Modified and no
// Cache-Control at all. Twenty thousand entries change a handful of times an
// hour and nothing an operator does depends on seeing a publication the minute
// it lands, so this trades freshness nobody needs for a page that renders
// without a network call.
const DefaultTTL = 15 * time.Minute

// DefaultDetailTTL is how long one server.json is reused.
//
// Longer than a listing, because it is a different question with a different
// answer. A listing is "what is there now" and changes whenever anybody
// publishes anything; a detail is "what does this one server say", keyed by a
// stable name, and changes when its publisher cuts a release. Holding the
// second for as long as the first refetches a document that has not moved.
const DefaultDetailTTL = time.Hour

// negativeTTL is how long "no such server" is remembered.
//
// Seconds, because a name that answers 404 today is a server somebody
// publishes tomorrow, and a wrong negative is the one cache mistake an
// operator cannot work around. Long enough only to stop a dashboard retrying a
// broken link from turning it into a request per render.
const negativeTTL = 30 * time.Second

// staleServeCeiling bounds how long a stale answer may be served while a
// refresh runs, whatever a catalogue grants.
//
// Smithery grants twenty-four hours of stale-while-revalidate. That is
// generous of them and too long to act on unqualified: a catalogue this host
// has not successfully reached in a day is one whose page should say so rather
// than quietly reading a day old. Six hours is long enough that the window
// does real work and short enough that "stale" still means something.
const staleServeCeiling = 6 * time.Hour

// backgroundRefreshTimeout bounds one refresh running behind a served answer.
//
// Nobody is waiting for it, which is exactly why it needs a bound: an
// unbounded background fetch is a goroutine that outlives the reason it
// started.
const backgroundRefreshTimeout = 30 * time.Second

// DefaultMaxCacheEntries bounds how many answers are held, across every
// catalogue.
//
// Across, not per catalogue. A cap that each source gets its own copy of is a
// cap a fourth source silently quadruples, and the thing being bounded -- this
// process's memory -- does not care which catalogue filled it.
//
// The key includes a search term, and a search term is whatever somebody
// typed, so an unbounded cache is an unbounded allocation driven by
// keystrokes. The size of a single key is bounded separately, by normalising a
// query before it becomes one; both halves are needed, since a cap on the
// count of unbounded keys is not a cap on anything.
const DefaultMaxCacheEntries = 256

// CacheStore is the memory every catalogue cache shares.
//
// One store behind several Cached instances, so the bound is on the process
// rather than on each source, and so eviction drops the least useful entry
// anywhere rather than the least useful entry of whichever source happened to
// fill up.
type CacheStore struct {
	limit int

	mu      sync.Mutex
	entries map[string]*cacheEntry
}

// NewCacheStore builds the shared cache. A limit of zero takes
// DefaultMaxCacheEntries.
func NewCacheStore(limit int) *CacheStore {
	if limit <= 0 {
		limit = DefaultMaxCacheEntries
	}
	return &CacheStore{limit: limit, entries: make(map[string]*cacheEntry)}
}

// Len reports how many answers are held, for a test that has something to say
// about the bound.
func (s *CacheStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// cacheEntry is one held answer.
//
// Immutable once stored: a renewal replaces it rather than editing it, so a
// reader holding a pointer cannot see a half-updated entry.
type cacheEntry struct {
	page   Page
	detail Detail
	// err is a remembered refusal. Only ErrNotFound is ever held, and only for
	// negativeTTL: a catalogue being down is not an answer and must not become
	// one.
	err        error
	validators Validators
	fetchedAt  time.Time
	// ttl and staleWhile are what the catalogue said about this answer, or the
	// configured defaults where it said nothing. Held per entry because they
	// arrived with the entry.
	ttl        time.Duration
	staleWhile time.Duration
}

func (s *CacheStore) get(key string) *cacheEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[key]
}

func (s *CacheStore) put(key string, entry *cacheEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, replacing := s.entries[key]; !replacing && len(s.entries) >= s.limit {
		s.evictOldestLocked()
	}
	s.entries[key] = entry
}

// evictOldestLocked drops the least recently fetched entry. A linear scan over
// a few hundred entries, on the miss path only, is cheaper than maintaining an
// order that nothing else needs.
func (s *CacheStore) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for k, v := range s.entries {
		if oldestKey == "" || v.fetchedAt.Before(oldest) {
			oldestKey, oldest = k, v.fetchedAt
		}
	}
	if oldestKey != "" {
		delete(s.entries, oldestKey)
	}
}

// Cached wraps a catalogue with the freshness the catalogue itself asked for,
// and the rule that matters most here: a catalogue that cannot be reached
// serves what it last saw, marked stale.
//
// Nothing about browsing a third party's list is worth a 500. An operator
// looking at a stale catalogue can still see what is there and still decide to
// import one; an operator looking at an error page can do neither, and the
// fault is not in this deployment. Saying "this is what we last saw, at 10:04"
// is the honest version of both.
//
// Nothing here runs at startup. The first fetch happens on the first request
// that needs one, so a registry that is down or a network that is absent costs
// a page and not a boot.
type Cached struct {
	upstream Client
	// conditional is upstream again when it can revalidate, nil otherwise.
	conditional Revalidating
	store       *CacheStore
	defaultTTL  time.Duration
	detailTTL   time.Duration
	now         func() time.Time

	// Background refreshes are owned here so that they cannot outlive
	// shutdown: at most one goroutine per key, and Close cancels every one of
	// them and waits.
	refreshMu sync.Mutex
	refreshes map[string]bool
	ctx       context.Context
	stop      context.CancelFunc
	wg        sync.WaitGroup
}

// CacheOptions configures the cache. Every field has a working default, so the
// zero value is usable and a test can replace exactly what it needs.
type CacheOptions struct {
	// Store is the shared memory bound. Nil gives this cache one of its own,
	// which is right for a test and wrong for a deployment.
	Store *CacheStore
	// DefaultTTL applies to a listing when the catalogue's response says
	// nothing. Zero takes DefaultTTL.
	DefaultTTL time.Duration
	// DetailTTL applies to one server.json, on the same terms. Zero takes
	// DefaultDetailTTL.
	DetailTTL time.Duration
	// Now may be nil.
	Now func() time.Time
}

// NewCached puts a cache in front of a catalogue.
func NewCached(upstream Client, opts CacheOptions) *Cached {
	store := opts.Store
	if store == nil {
		store = NewCacheStore(0)
	}
	defaultTTL := opts.DefaultTTL
	if defaultTTL <= 0 {
		defaultTTL = DefaultTTL
	}
	detailTTL := opts.DetailTTL
	if detailTTL <= 0 {
		detailTTL = DefaultDetailTTL
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	conditional, _ := upstream.(Revalidating)
	ctx, stop := context.WithCancel(context.Background())
	return &Cached{
		upstream:    upstream,
		conditional: conditional,
		store:       store,
		defaultTTL:  defaultTTL,
		detailTTL:   detailTTL,
		now:         now,
		refreshes:   make(map[string]bool),
		ctx:         ctx,
		stop:        stop,
	}
}

// Close cancels any refresh running behind a served answer, and waits.
//
// A background refresh has nobody waiting for it, so without this the process
// would be held open on the way out by work whose result nothing will read.
func (c *Cached) Close() error {
	c.stop()
	c.wg.Wait()
	return nil
}

// Source names the catalogue underneath.
func (c *Cached) Source() string { return c.upstream.Source() }

// List returns one page.
//
// Three states, and they are three different questions. Fresh is served.
// Stale within the window the catalogue granted is served *and* refreshed
// behind the answer, which is what makes a search box across several
// catalogues feel immediate. Past that window the fetch is made and waited
// for -- and if it fails, what was held is served anyway and says it is stale.
func (c *Cached) List(ctx context.Context, q Query) (Page, error) {
	// Normalised before it becomes a key, so the key stands for the request
	// that will actually be made. Raw query-string text would file "weather "
	// and "weather" as two answers to one question, put page one under the
	// key of a page it is not when an over-long cursor is dropped downstream,
	// and leave the size of a key set by whoever typed it.
	q = q.Normalised()
	key := c.key("list", q.Search+"\x00"+q.Cursor+"\x00"+strconv.Itoa(q.Limit))

	if !RefreshRequested(ctx) {
		if hit := c.store.get(key); hit != nil && hit.err == nil {
			switch c.state(hit) {
			case entryFresh:
				return c.staleness(hit.page, hit), nil
			case entryServeableStale:
				c.refreshInBackground(key, hit.validators, func(ctx context.Context, v Validators) error {
					_, err := c.fetchList(ctx, key, q, v)
					return err
				})
				return c.staleness(hit.page, hit), nil
			}
		}
	}

	page, err := c.fetchList(ctx, key, q, c.validatorsFor(key))
	if err != nil {
		// A cancelled request is this host giving up, not the catalogue
		// failing, and answering it with stale data would be answering a
		// question nobody is still asking.
		if hit := c.store.get(key); hit != nil && hit.err == nil && !isCancelled(ctx, err) {
			return c.staleness(hit.page, hit), nil
		}
		return Page{}, err
	}
	return page, nil
}

// Get returns one entry, on the same three states as List.
func (c *Cached) Get(ctx context.Context, name string) (Detail, error) {
	// Bounded for the same reason, and by the same rule the client applies: a
	// name it would refuse to ask for must not become an entry held here.
	name = clean(name, maxNameRunes)
	key := c.key("get", name)

	if !RefreshRequested(ctx) {
		if hit := c.store.get(key); hit != nil && c.state(hit) != entryExpired {
			switch {
			case hit.err != nil:
				// A remembered refusal is served only while fresh; it has no
				// stale window, because a name that is about to exist should
				// not keep answering 404 behind a refresh nobody sees.
				if c.state(hit) == entryFresh {
					return Detail{}, hit.err
				}
			case c.state(hit) == entryFresh:
				return c.detailStaleness(hit.detail, hit), nil
			default:
				c.refreshInBackground(key, hit.validators, func(ctx context.Context, v Validators) error {
					_, err := c.fetchDetail(ctx, key, name, v)
					return err
				})
				return c.detailStaleness(hit.detail, hit), nil
			}
		}
	}

	detail, err := c.fetchDetail(ctx, key, name, c.validatorsFor(key))
	if err != nil {
		// A name the catalogue does not have is an answer, not a failure, and
		// it is remembered only for a moment -- see negativeTTL. It is never
		// answered from a stale positive entry, because that would resurrect
		// a withdrawn server.
		if errors.Is(err, ErrNotFound) {
			return Detail{}, err
		}
		if hit := c.store.get(key); hit != nil && hit.err == nil && !isCancelled(ctx, err) {
			return c.detailStaleness(hit.detail, hit), nil
		}
		return Detail{}, err
	}
	return detail, nil
}

// fetchList asks the catalogue and stores what came back.
func (c *Cached) fetchList(ctx context.Context, key string, q Query, v Validators) (Page, error) {
	var page Page
	var err error
	if c.conditional != nil && !v.empty() {
		page, err = c.conditional.ListIfChanged(ctx, q, v)
	} else {
		page, err = c.upstream.List(ctx, q)
	}
	if errors.Is(err, ErrNotModified) {
		// The catalogue confirmed what is held. Nothing travelled but the
		// headers, and the answer's clock starts again.
		if hit := c.renew(key, page.Freshness); hit != nil {
			return c.staleness(hit.page, hit), nil
		}
		// Nothing to renew: the entry was evicted between the lookup and the
		// answer. Ask again unconditionally rather than return an empty page.
		page, err = c.upstream.List(ctx, q)
	}
	if err != nil {
		return Page{}, err
	}
	entry := c.entryFor(page.Freshness, c.defaultTTL)
	entry.page = page
	if !page.Freshness.NoStore {
		c.store.put(key, entry)
	}
	return c.staleness(page, entry), nil
}

// fetchDetail is fetchList for one entry, including the short memory of a name
// the catalogue does not have.
func (c *Cached) fetchDetail(ctx context.Context, key, name string, v Validators) (Detail, error) {
	var detail Detail
	var err error
	if c.conditional != nil && !v.empty() {
		detail, err = c.conditional.GetIfChanged(ctx, name, v)
	} else {
		detail, err = c.upstream.Get(ctx, name)
	}
	if errors.Is(err, ErrNotModified) {
		if hit := c.renew(key, detail.Freshness); hit != nil {
			return c.detailStaleness(hit.detail, hit), nil
		}
		detail, err = c.upstream.Get(ctx, name)
	}
	if errors.Is(err, ErrNotFound) {
		c.store.put(key, &cacheEntry{err: err, fetchedAt: c.now(), ttl: negativeTTL})
		return Detail{}, err
	}
	if err != nil {
		return Detail{}, err
	}
	entry := c.entryFor(detail.Freshness, c.detailTTL)
	entry.detail = detail
	if !detail.Freshness.NoStore {
		c.store.put(key, entry)
	}
	return c.detailStaleness(detail, entry), nil
}

// refreshInBackground runs one refresh behind an answer that has already been
// served.
//
// One per key at a time: a page half a dozen browsers ask for at the same
// moment should cost one upstream request, not six. Bounded by its own
// timeout, and by the cache's context, so it cannot outlive shutdown.
func (c *Cached) refreshInBackground(key string, validators Validators, refresh func(context.Context, Validators) error) {
	c.refreshMu.Lock()
	if c.refreshes[key] {
		c.refreshMu.Unlock()
		return
	}
	select {
	case <-c.ctx.Done():
		// Shutting down. What was served is what the caller gets.
		c.refreshMu.Unlock()
		return
	default:
	}
	c.refreshes[key] = true
	c.wg.Add(1)
	c.refreshMu.Unlock()

	go func() {
		defer c.wg.Done()
		defer func() {
			c.refreshMu.Lock()
			delete(c.refreshes, key)
			c.refreshMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(c.ctx, backgroundRefreshTimeout)
		defer cancel()
		// The error is deliberately dropped. Nobody is waiting for this, the
		// answer it would have replaced has already been served, and the next
		// caller past the stale window fetches synchronously and sees the
		// failure then -- with something to do about it.
		_ = refresh(ctx, validators)
	}()
}

// entryState is how usable a held answer is.
type entryState int

const (
	entryFresh entryState = iota
	entryServeableStale
	entryExpired
)

func (c *Cached) state(hit *cacheEntry) entryState {
	age := c.now().Sub(hit.fetchedAt)
	switch {
	case age < hit.ttl:
		return entryFresh
	case age < hit.ttl+hit.staleWhile:
		return entryServeableStale
	default:
		return entryExpired
	}
}

// entryFor builds an entry from what the catalogue said, falling back to the
// configured default where it said nothing.
func (c *Cached) entryFor(f Freshness, fallback time.Duration) *cacheEntry {
	ttl := f.TTL
	if ttl <= 0 {
		ttl = fallback
	}
	staleWhile := f.StaleWhile
	if staleWhile > staleServeCeiling {
		staleWhile = staleServeCeiling
	}
	return &cacheEntry{
		validators: f.Validators,
		fetchedAt:  c.now(),
		ttl:        ttl,
		staleWhile: staleWhile,
	}
}

// renew restarts a held answer's clock after a 304, keeping the body.
func (c *Cached) renew(key string, f Freshness) *cacheEntry {
	hit := c.store.get(key)
	if hit == nil || hit.err != nil {
		return nil
	}
	renewed := *hit
	renewed.fetchedAt = c.now()
	if f.TTL > 0 {
		renewed.ttl = f.TTL
	}
	if f.StaleWhile > 0 {
		renewed.staleWhile = min(f.StaleWhile, staleServeCeiling)
	}
	// A 304 may carry a new ETag. Keeping the old one would make the next
	// conditional request ask about a version the far end has moved past.
	if !f.Validators.empty() {
		renewed.validators = f.Validators
	}
	c.store.put(key, &renewed)
	return &renewed
}

func (c *Cached) validatorsFor(key string) Validators {
	if hit := c.store.get(key); hit != nil {
		return hit.validators
	}
	return Validators{}
}

// key namespaces an entry by the catalogue it came from, because the store is
// shared and two catalogues may hold the same name.
func (c *Cached) key(kind, rest string) string {
	return c.Source() + "\x00" + kind + "\x00" + rest
}

func (c *Cached) staleness(page Page, entry *cacheEntry) Page {
	page.RetrievedAt = entry.fetchedAt.UTC()
	page.Stale = c.state(entry) != entryFresh
	page.Entries = append([]Entry(nil), page.Entries...)
	// The per-source report is corrected too, not just the page's own flag.
	// A page merged from several catalogues is read one source at a time --
	// which of them is stale is the question being asked -- and a cached page
	// still carrying "fetched just now" from the fetch that filled the cache
	// would answer it wrongly.
	sources := make([]SourceStatus, len(page.Sources))
	copy(sources, page.Sources)
	for i := range sources {
		sources[i].Stale = page.Stale
		sources[i].RetrievedAt = page.RetrievedAt
	}
	page.Sources = sources
	return page
}

func (c *Cached) detailStaleness(detail Detail, entry *cacheEntry) Detail {
	detail.RetrievedAt = entry.fetchedAt.UTC()
	detail.Stale = c.state(entry) != entryFresh
	// The document is copied out rather than shared. A handler that wrote
	// through the slice it was handed would be editing the cache, and the
	// next reader would get the edit.
	detail.Document = append(json.RawMessage(nil), detail.Document...)
	return detail
}

// isCancelled reports that the caller gave up rather than the catalogue
// failing.
func isCancelled(ctx context.Context, err error) bool {
	return ctx.Err() != nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

var _ Client = (*Cached)(nil)
