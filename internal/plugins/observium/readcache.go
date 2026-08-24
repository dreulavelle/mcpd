package observium

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/cachestore"
	"github.com/spoked/mcpd/internal/plugins"
)

// maxCacheEntries bounds one instance's held answers. Per instance rather than
// per process: two instances are two credentials reading two estates, and a
// shared bound would let a busy one evict a quiet one's answers.
const maxCacheEntries = 256

// fetchCeiling bounds a fetch that has outlived the caller who started it.
//
// A shared fetch belongs to whoever is still waiting rather than to whoever
// asked first, so it does not inherit that caller's cancellation. It has to
// inherit a deadline from somewhere, and this is it.
const fetchCeiling = 2 * time.Minute

// Cache kinds, which are also the metrics label. A class rather than the path,
// because a path carries a device id and a metric labelled with one is a new
// series per device.
const (
	kindInventory = "inventory"
	kindState     = "state"
)

// readCache holds recent answers to Observium reads.
//
// # What may be reused, and what may not
//
// A read tool's result feeds a model that then decides something, so a stale
// answer is not merely out of date -- it can be wrong in a way that changes
// what happens next. The rule is an allow-list: an endpoint cacheTTL does not
// recognise is fetched every time, so a tool added later cannot quietly start
// being served from memory.
//
// # Why a key needs no principal
//
// The key is built from the request that will actually be made. Every caller
// of one plugin instance reaches Observium with the same credential, so two
// callers producing the same key produce byte-identical upstream requests and
// therefore identical responses -- which is the only condition under which a
// shared cache is not an access-control hole. Authorization has already run:
// a caller who may not reach this plugin never gets as far as the tool.
//
// That property is load-bearing and it is a property of *this* plugin. If
// Observium credentials ever became per-caller -- a token derived from the
// principal, so that Observium's own user levels did the filtering -- this
// shape would become unsafe and the principal would have to enter the key.
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
		store:  cachestore.New(maxCacheEntries),
		cfg:    cfg,
		now:    now,
		obs:    obs,
	}
}

// cacheTTL says how long a read of path may be reused, and zero means never.
//
// What is deliberately never cached, and why:
//
//   - /alerts and /alert_log. A model reads these to find out whether
//     something is wrong *now*. A cached "no alerts" says the network is fine
//     at the moment it stopped being fine, which is the worst answer this
//     integration can give. They are also cheap next to a device walk, so
//     there is nothing to buy.
//   - /alert_checks. Read to establish what is *being* watched, usually
//     immediately after somebody changed it.
//
// The split between the other two classes is between what an operator
// configures and what a poller writes. A device's hostname changes when
// somebody renames it; a sensor's reading changes every poll cycle. Holding
// the first for ten minutes costs nothing, and holding the second for ten
// minutes would hand a model a picture of the network that has stopped being
// true.
func (c Config) cacheTTL(path string) time.Duration {
	switch path {
	case "/devices", "/inventory", "/vlans", "/groups", "/addresses", "/neighbours":
		return c.InventoryTTL()
	case "/ports", "/sensors", "/status", "/storage", "/mempools",
		"/processors", "/counters", "/probes", "/printersupplies":
		return c.StateTTL()
	}
	// One device by id or hostname, and nothing deeper. Matching on shape
	// rather than on a list of names means a sub-collection added later is
	// refused rather than silently cached.
	if rest, ok := strings.CutPrefix(path, "/devices/"); ok && !strings.Contains(rest, "/") {
		return c.InventoryTTL()
	}
	return 0
}

// cacheKind classifies a path for the metric. Structural rather than derived
// from the configured durations, which may legitimately be equal.
func cacheKind(path string) string {
	switch path {
	case "/ports", "/sensors", "/status", "/storage", "/mempools",
		"/processors", "/counters", "/probes", "/printersupplies":
		return kindState
	}
	return kindInventory
}

// reuse returns a cached answer for this request, or runs fetch and holds what
// it returns.
//
// A miss is single-flighted: a model fanning out four tool calls that all need
// the device list should cost one walk of Observium's pagination, not four
// identical ones.
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
	kind := cacheKind(path)
	key := cacheKey(kind, path, params)

	if hit := c.store.Get(key); hit != nil && hit.State(c.now()) == cachestore.Fresh {
		c.event(kind, plugins.CacheHit)
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
			// A failure is not an answer and is not held. An Observium that is
			// down should be reported as down on every call rather than
			// remembered as an empty estate.
			return nil, err
		}
		c.store.Put(key, &cachestore.Entry{Value: v, FetchedAt: c.now(), TTL: ttl})
		return v, nil
	})
	if err != nil {
		return nil, err
	}
	if shared {
		c.event(kind, plugins.CacheShared)
	} else {
		c.event(kind, plugins.CacheMiss)
	}
	return value, nil
}

func (c *readCache) event(kind, event string) {
	if c.obs == nil {
		return
	}
	c.obs.CacheEvent(c.plugin, kind, event)
}

// cacheKey builds the key for one upstream request.
//
// url.Values.Encode sorts by key, so two callers who set the same filters in a
// different order produce the same key -- and two who set different ones never
// do.
func cacheKey(kind, path string, params url.Values) string {
	return kind + "\x00" + path + "\x00" + params.Encode()
}
