package registry

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"
)

// DefaultTTL is how long a catalogue answer is reused.
//
// A registry of twenty thousand entries changes a handful of times an hour and
// nothing an operator does depends on seeing a publication the minute it
// lands, so this trades freshness nobody needs for a page that renders without
// a network call.
const DefaultTTL = 15 * time.Minute

// maxCacheEntries bounds how many answers are held.
//
// The key includes a search term, and a search term is whatever somebody typed,
// so an unbounded cache is an unbounded allocation driven by keystrokes. The
// size of a single key is bounded separately, by normalising a query before it
// becomes one -- both halves are needed, since a cap on the count of unbounded
// keys is not a cap on anything. When it is full the oldest fetch is dropped;
// this is a cache in front of a browse, not a store anything depends on.
const maxCacheEntries = 256

// Cached wraps a catalogue with a TTL and the rule that matters most here: a
// catalogue that cannot be reached serves what it last saw, marked stale.
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
	ttl      time.Duration
	now      func() time.Time

	mu      sync.Mutex
	entries map[string]*cacheEntry
}

type cacheEntry struct {
	page      Page
	detail    Detail
	fetchedAt time.Time
}

// NewCached puts a TTL cache in front of a catalogue. A zero ttl takes
// DefaultTTL; now may be nil.
func NewCached(upstream Client, ttl time.Duration, now func() time.Time) *Cached {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Cached{
		upstream: upstream,
		ttl:      ttl,
		now:      now,
		entries:  make(map[string]*cacheEntry),
	}
}

// Source names the catalogue underneath.
func (c *Cached) Source() string { return c.upstream.Source() }

// List returns one page, from the cache when it is fresh and from the
// catalogue otherwise. An upstream failure with something cached is not an
// error: the stale page is returned and says so.
func (c *Cached) List(ctx context.Context, q Query) (Page, error) {
	// Normalised before it becomes a key, so the key stands for the request
	// that will actually be made. Raw query-string text would file "weather "
	// and "weather" as two answers to one question, put page one under the
	// key of a page it is not when an over-long cursor is dropped downstream,
	// and leave the size of a key set by whoever typed it.
	q = q.Normalised()
	key := "list\x00" + q.Search + "\x00" + q.Cursor + "\x00" + strconv.Itoa(q.Limit)

	if hit, fresh := c.lookup(key); fresh {
		return c.staleness(hit.page, hit.fetchedAt), nil
	}

	page, err := c.upstream.List(ctx, q)
	if err != nil {
		// A cancelled request is this host giving up, not the catalogue
		// failing, and answering it with stale data would be answering a
		// question nobody is still asking.
		// Expired is exactly the state that matters here: the point of the
		// stale path is to serve what is past its TTL when the refresh that
		// would have replaced it failed.
		if hit, _ := c.lookup(key); hit != nil && !isCancelled(ctx, err) {
			return c.staleness(hit.page, hit.fetchedAt), nil
		}
		return Page{}, err
	}
	c.store(key, &cacheEntry{page: page, fetchedAt: c.now()})
	return c.staleness(page, c.now()), nil
}

// Get returns one entry, with the same staleness rule as List.
func (c *Cached) Get(ctx context.Context, name string) (Detail, error) {
	// Bounded for the same reason, and by the same rule the client applies: a
	// name it would refuse to ask for must not become an entry held here.
	name = clean(name, maxNameRunes)
	key := "get\x00" + name

	if hit, fresh := c.lookup(key); fresh {
		return c.detailStaleness(hit.detail, hit.fetchedAt), nil
	}

	detail, err := c.upstream.Get(ctx, name)
	if err != nil {
		// A name the catalogue does not have is an answer, not a failure. It
		// is passed through so a stale hit cannot resurrect a withdrawn entry.
		if errors.Is(err, ErrNotFound) {
			c.forget(key)
			return Detail{}, err
		}
		if hit, _ := c.lookup(key); hit != nil && !isCancelled(ctx, err) {
			return c.detailStaleness(hit.detail, hit.fetchedAt), nil
		}
		return Detail{}, err
	}
	c.store(key, &cacheEntry{detail: detail, fetchedAt: c.now()})
	return c.detailStaleness(detail, c.now()), nil
}

// lookup returns a cached entry and whether it is still within the TTL.
//
// Two results because presence and freshness are different questions and both
// paths need one of them. A nil entry means nothing is held; a non-nil entry
// with fresh false is expired, which is exactly what gets served when the
// refresh that should have replaced it failed.
func (c *Cached) lookup(key string) (*cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hit, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	return hit, c.now().Sub(hit.fetchedAt) < c.ttl
}

func (c *Cached) store(key string, entry *cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCacheEntries {
		c.evictOldestLocked()
	}
	c.entries[key] = entry
}

func (c *Cached) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// evictOldestLocked drops the least recently fetched entry. A linear scan over
// a few hundred entries, on the miss path only, is cheaper than maintaining an
// order that nothing else needs.
func (c *Cached) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for k, v := range c.entries {
		if oldestKey == "" || v.fetchedAt.Before(oldest) {
			oldestKey, oldest = k, v.fetchedAt
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

func (c *Cached) staleness(page Page, fetchedAt time.Time) Page {
	page.RetrievedAt = fetchedAt.UTC()
	page.Stale = c.now().Sub(fetchedAt) >= c.ttl
	page.Entries = append([]Entry(nil), page.Entries...)
	return page
}

func (c *Cached) detailStaleness(detail Detail, fetchedAt time.Time) Detail {
	detail.RetrievedAt = fetchedAt.UTC()
	detail.Stale = c.now().Sub(fetchedAt) >= c.ttl
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
