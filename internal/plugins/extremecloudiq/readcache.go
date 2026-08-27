package extremecloudiq

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
// per process: two instances are two tokens reading two accounts, and a shared
// bound would let a busy one evict a quiet one's answers.
const maxCacheEntries = 128

// maxCacheBytes bounds the memory those entries may occupy.
//
// A count alone is not a bound here. One held answer is a whole walk of a
// paginated collection, so the same entries are a few kilobytes on a small
// estate and megabytes on a large one. The number that matters to a host
// running inside somebody's memory limit is the second one.
const maxCacheBytes = 16 << 20

// fetchCeiling bounds a fetch that has outlived the caller who started it.
//
// A shared fetch belongs to whoever is still waiting rather than to whoever
// asked first, so it does not inherit that caller's cancellation. It has to
// inherit a deadline from somewhere, and this is it.
const fetchCeiling = 2 * time.Minute

// kindConfig is the one cache class this plugin has, and it is also the
// metrics label.
const kindConfig = "config"

// readCache holds recent answers to the reads that may be reused.
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
//   - /devices and /devices/stats. Whether an access point is connected is the
//     single most common thing anybody asks this integration, and a held
//     answer to it is indistinguishable from a true one.
//   - /clients/active and everything counting them. Who is connected changes
//     by the second.
//   - /alerts and /logs/audit. These are read precisely when somebody suspects
//     something is wrong, which is the moment a held answer is wrong.
//   - every per-device history series. They are a window ending now; a cached
//     one is a window ending whenever it was fetched.
//   - /auth/apitoken/info. It is the startup probe, and a liveness check
//     answered from memory is not one.
//
// What is left changes when a person changes it: the site hierarchy, the
// network policies, the SSIDs.
//
// # Why a key needs no principal
//
// The key is built from the request that will actually be made. Every caller
// of one plugin instance reaches the API with the same token, so two callers
// producing the same key produce byte-identical upstream requests and
// therefore identical responses -- which is the only condition under which a
// shared cache is not an access-control hole. Authorization has already run: a
// caller who may not reach this plugin never gets as far as the tool.
//
// That property is load-bearing and it is a property of *this* plugin.
// ExtremeCloud IQ authorises by the scopes a token was issued with, so if the
// credential ever became per-caller this shape would become unsafe and the
// principal would have to enter the key.
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
	case "/locations/tree", "/network-policies", "/ssids":
		return c.CacheTTL()
	}
	// One policy's SSIDs, and nothing deeper. Matching on shape rather than on
	// a list of ids means a sub-collection added later is refused rather than
	// silently cached.
	if rest, ok := strings.CutPrefix(path, "/network-policies/"); ok {
		if strings.HasSuffix(rest, "/ssids") && strings.Count(rest, "/") == 1 {
			return c.CacheTTL()
		}
	}
	return 0
}

// reuse returns a cached answer for this request, or runs fetch and holds what
// it returns.
//
// A miss is single-flighted: a model fanning out three tool calls that all
// need the site list should cost one request, not three identical ones.
//
// Nothing stale is ever served. The reader here is a model about to act on
// what it is told, and "this is what the estate looked like a while ago" is
// not a safer answer than waiting.
func (c *readCache) reuse(ctx context.Context, path string, params url.Values,
	fetch func(context.Context) (any, error)) (any, error) {

	ttl := c.cfg.cacheTTL(path)
	if ttl <= 0 {
		return fetch(ctx)
	}
	key := requestDigest(path, params)

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
			// A failure is not an answer and is not held. An account that is
			// unreachable should be reported as unreachable on every call
			// rather than remembered as an estate with no sites.
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

// requestDigest identifies one request for the cache.
//
// url.Values.Encode sorts by name, so two callers who set the same filters in
// a different order produce the same digest and two who set different ones
// never do.
func requestDigest(path string, params url.Values) string {
	var b strings.Builder
	b.WriteString(path)
	b.WriteByte(0)
	b.WriteString(params.Encode())
	return b.String()
}

// heldBytes is roughly what an answer occupies, for the store's size bound.
//
// Encoded, because that is the only honest measure of a value whose shape
// varies per record. It runs on the miss path only, after a round trip that
// cost far more than this does.
func heldBytes(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}
