package graylog

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/cachestore"
	"github.com/spoked/mcpd/internal/plugins"
)

// maxCacheEntries bounds one instance's held answers. Per instance rather than
// per process: two instances are two credentials reading two installations,
// and a shared bound would let a busy one evict a quiet one's answers.
const maxCacheEntries = 128

// fetchCeiling bounds a fetch that has outlived the caller who started it.
//
// A shared fetch belongs to whoever is still waiting rather than to whoever
// asked first, so it does not inherit that caller's cancellation. It has to
// inherit a deadline from somewhere, and this is it.
const fetchCeiling = 2 * time.Minute

// kindConfig is the one cache class this plugin has, and it is also the
// metrics label.
//
// One class rather than observium's two, because the line here falls in a
// different place. observium splits what a poller writes from what an operator
// arranges. Graylog's equivalent of "what the poller writes" is the log data
// itself, and none of that is cacheable at all -- so what is left is one
// class: how the installation is arranged.
const kindConfig = "config"

// readCache holds recent answers to the Graylog reads that may be reused.
//
// # What may be reused, and what may not
//
// The rule is an allow-list: an endpoint cacheTTL does not recognise is
// fetched every time, so a tool added later cannot quietly start being served
// from memory.
//
// Nothing that answers "what is happening" is on it, and the omissions are the
// point:
//
//   - /search/messages and /search/aggregate. A search is a question about
//     now. Answering "no errors in the last fifteen minutes" from a copy made
//     five minutes ago is the worst answer this integration can give, because
//     it is indistinguishable from a true one.
//   - /events/search. Same reason, more sharply: this is the alert list, and
//     a model reads it to find out whether something is on fire.
//   - /system, /cluster, /system/cluster/nodes, /system/indexer/cluster/health
//     and /system/notifications. These are read precisely when somebody
//     suspects the installation is unwell, which is the moment a held answer
//     is wrong. /system is on the list twice over: it is also the startup
//     probe, and a liveness check answered from memory is not one.
//
// What is left changes when a person changes it: the streams that exist, the
// alert rules, the field names, the inputs, the index sets.
//
// # Why a key needs no principal
//
// The key is built from the request that will actually be made. Every caller
// of one plugin instance reaches Graylog with the same credential, so two
// callers producing the same key produce byte-identical upstream requests and
// therefore identical responses -- which is the only condition under which a
// shared cache is not an access-control hole. Authorization has already run:
// a caller who may not reach this plugin never gets as far as the tool.
//
// That property is load-bearing and it is a property of *this* plugin. Graylog
// authorises per stream, so if its credential ever became per-caller -- a
// token derived from the principal, so that Graylog's own permissions did the
// filtering -- this shape would become unsafe and the principal would have to
// enter the key.
type readCache struct {
	plugin string
	store  *cachestore.Store
	group  cachestore.Group
	cfg    Config
	now    func() time.Time
	obs    plugins.CacheObserver
}

func newReadCache(plugin string, cfg Config, now func() time.Time, obs plugins.CacheObserver) *readCache {
	if now == nil {
		now = time.Now
	}
	return &readCache{
		plugin: plugin,
		store:  cachestore.NewBounded(maxCacheEntries, maxCacheBytes),
		cfg:    cfg,
		now:    now,
		obs:    obs,
	}
}

// cacheTTL says how long a read of path may be reused, and zero means never.
func (c Config) cacheTTL(path string) time.Duration {
	switch path {
	case "/streams/paginated", "/events/definitions", "/views/fields",
		"/system/inputs", "/system/indices/index_sets":
		return c.CacheTTL()
	}
	// One stream or one event definition by id, and nothing deeper. Matching
	// on shape rather than on a list of names means a sub-collection added
	// later is refused rather than silently cached.
	for _, prefix := range []string{"/streams/", "/events/definitions/"} {
		if rest, ok := strings.CutPrefix(path, prefix); ok && !strings.Contains(rest, "/") {
			return c.CacheTTL()
		}
	}
	return 0
}

// reuse returns a cached answer for this request, or runs fetch and holds what
// it returns.
//
// A miss is single-flighted: a model fanning out three tool calls that all
// need the stream list should cost one request, not three identical ones.
//
// Nothing stale is ever served. The reader here is a model about to act on
// what it is told, and "this is what Graylog looked like a while ago" is not a
// safer answer than waiting.
func (c *readCache) reuse(ctx context.Context, method, path string, params url.Values,
	body []byte, fetch func(context.Context) (any, error)) (any, error) {

	ttl := c.cfg.cacheTTL(path)
	if ttl <= 0 {
		return fetch(ctx)
	}
	key := requestDigest(method, path, params, body)

	if hit := c.store.Get(key); hit != nil && hit.State(c.now()) == cachestore.Fresh {
		c.event(plugins.CacheHit)
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
			// A failure is not an answer and is not held. A Graylog that is
			// down should be reported as down on every call rather than
			// remembered as an installation with no streams.
			return nil, err
		}
		c.store.Put(key, &cachestore.Entry{
			Value: v, FetchedAt: c.now(), TTL: ttl, Bytes: heldBytes(v),
		})
		return v, nil
	})
	if err != nil {
		return nil, err
	}
	if shared {
		c.event(plugins.CacheShared)
	} else {
		c.event(plugins.CacheMiss)
	}
	return value, nil
}

func (c *readCache) event(event string) {
	if c.obs == nil {
		return
	}
	c.obs.CacheEvent(c.plugin, kindConfig, event)
}

// maxCacheBytes bounds the memory those entries may occupy.
//
// A count alone is not a bound here. One held answer is a whole walk of a
// paginated collection -- up to max_items records, each as wide as the upstream
// makes them -- so the same 256 entries are a few megabytes on a small estate
// and well past a hundred on a large one. The number that matters to a host
// running inside somebody's memory limit is the second one, and nothing was
// watching it.
const maxCacheBytes = 32 << 20

// heldBytes is roughly what an answer occupies, for the store's size bound.
//
// Encoded, because that is the only honest measure of a value whose shape
// varies per record; a count of items would call a listing of banners the same
// size as a listing of identifiers. It runs on the miss path only, after a
// round trip that cost far more than this does.
func heldBytes(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}
