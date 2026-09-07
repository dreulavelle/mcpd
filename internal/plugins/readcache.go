package plugins

import (
	"context"
	"time"

	"github.com/spoked/mcpd/internal/cachestore"
)

// Holding an upstream's answers for a moment, so that a model asking the same
// question three ways in one turn costs one request rather than three.
//
// Five integrations had each written this out: the same struct, the same
// constructor, the same freshness check, the same single flight with the same
// re-check inside it, the same decision not to hold a failure. What actually
// differed between them was the key, the TTL and the metric label -- which is
// to say, everything the caller already knows and nothing about the mechanism.
// So the mechanism is here and those three are arguments.
//
// What is deliberately *not* here is any notion of which paths are cacheable
// or for how long. That is a judgement about one upstream's data -- how long a
// device inventory stays true is not how long a log search does -- and it
// stays in the plugin that can answer it.

// fetchCeiling bounds a fetch that has outlived the caller who started it.
//
// A shared fetch belongs to whoever is still waiting rather than to whoever
// asked first, so it does not inherit that caller's cancellation. It has to
// inherit a deadline from somewhere, and this is it.
const fetchCeiling = 2 * time.Minute

// ReadCache is one instance's held answers.
//
// Per instance rather than per process: two instances are two credentials
// reading two installations, and a shared bound would let a busy one evict a
// quiet one's answers.
type ReadCache struct {
	// plugin is the instance name, for the metric. Not in the key: the store
	// belongs to one instance already.
	plugin string
	store  *cachestore.Store
	group  cachestore.Group
	now    func() time.Time
	obs    CacheObserver
}

// NewReadCache builds a cache bounded by both a count and a size.
//
// Both bounds are the caller's, because they are a judgement about what that
// upstream's answers weigh: a cache of device inventories and a cache of log
// searches do not want the same ceiling.
func NewReadCache(plugin string, maxEntries, maxBytes int, now func() time.Time, obs CacheObserver) *ReadCache {
	if now == nil {
		now = time.Now
	}
	return &ReadCache{
		plugin: plugin,
		store:  cachestore.NewBounded(maxEntries, maxBytes),
		now:    now,
		obs:    obs,
	}
}

// Do returns a held answer if there is a fresh one, and otherwise fetches.
//
// kind is the metric label: a class of read rather than the path, because a
// path carries an identifier and a metric labelled with one is a new series
// per device. key is the caller's own digest of the request. A ttl of zero or
// less means this read is not cacheable, and the fetch runs unmediated.
//
// Concurrent callers asking the same question share one fetch, so a dashboard
// opening six panels at once is one request upstream rather than six.
func (c *ReadCache) Do(ctx context.Context, kind, key string, ttl time.Duration,
	fetch func(context.Context) (any, error)) (any, error) {

	if ttl <= 0 {
		return fetch(ctx)
	}

	if hit := c.store.Get(key); hit != nil && hit.State(c.now()) == cachestore.Fresh {
		c.event(kind, CacheHit)
		return hit.Value, nil
	}

	value, shared, err := c.group.Do(ctx, key, fetchCeiling, func(ctx context.Context) (any, error) {
		// Re-checked inside the flight: the caller this one is sharing with
		// may have filled the entry between the miss above and getting here.
		if hit := c.store.Get(key); hit != nil && hit.State(c.now()) == cachestore.Fresh {
			return hit.Value, nil
		}
		v, err := fetch(ctx)
		if err != nil {
			// A failure is not an answer and is not held. An upstream that is
			// down should be reported as down on every call rather than
			// remembered as an installation with nothing in it.
			return nil, err
		}
		c.store.Put(key, &cachestore.Entry{
			Value: v, FetchedAt: c.now(), TTL: ttl, Bytes: cachestore.Size(v),
		})
		return v, nil
	})
	if err != nil {
		return nil, err
	}
	if shared {
		c.event(kind, CacheShared)
	} else {
		c.event(kind, CacheMiss)
	}
	return value, nil
}

func (c *ReadCache) event(kind, event string) {
	if c.obs == nil {
		return
	}
	c.obs.CacheEvent(c.plugin, kind, event)
}
