package cnmaestro

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/cachestore"
	"github.com/spoked/mcpd/internal/plugins"
)

// maxCacheEntries bounds one instance's held answers.
//
// Per instance rather than per process, unlike the catalogue cache, because
// two instances are two credentials reading two estates and a shared bound
// would let a busy one evict a quiet one's answers. Two hundred and fifty-six
// device listings is already more distinct filter combinations than a
// conversation produces.
const maxCacheEntries = 256

// fetchCeiling bounds a fetch that has outlived the caller who started it.
//
// A shared fetch belongs to whoever is still waiting rather than to whoever
// asked first, so it does not inherit that caller's cancellation. It has to
// inherit a deadline from somewhere, and this is it.
const fetchCeiling = 2 * time.Minute

// Default reuse windows. Both are configurable, and zero switches that class
// off entirely.
const (
	// defaultInventoryTTL applies to what describes how an estate is
	// *arranged*: networks, sites, towers, WLANs, AP groups, tenants. These
	// change when a person changes them, which is not something that happens
	// between two tool calls in one conversation.
	defaultInventoryTTL = 5 * time.Minute

	// defaultDeviceTTL applies to device records: the expensive walk, and the
	// one where staleness is a correctness question rather than a freshness
	// one. Fifteen seconds is chosen against cnMaestro's own behaviour rather
	// than picked for feeling short -- the controller learns a device has gone
	// offline on its own polling interval, measured in minutes, so a fifteen
	// second cache adds nothing measurable to an error the API already has.
	// What it removes is the second, third and fourth full pagination walk of
	// the same estate inside one conversation.
	defaultDeviceTTL = 15 * time.Second
)

// Cache kinds, which are also the metrics label. Deliberately a class rather
// than the path: a path carries a MAC address, and a metric labelled with one
// is a new series per device.
const (
	kindInventory = "inventory"
	kindDevice    = "device"
)

// readCache holds recent answers to cnMaestro reads.
//
// # What may be reused, and what may not
//
// A read tool's result feeds a model that then decides something, so a stale
// answer is not merely out of date -- it can be wrong in a way that changes
// what happens next. The rule is therefore an allow-list, and everything not
// named in it is fetched every time. See cacheTTL for the list and for what
// each refusal is protecting.
//
// # Why a key needs no principal
//
// The key is built from the request that will actually be made: the endpoint
// and the fully resolved query, including the account. Every caller of one
// plugin instance reaches the same upstream with the same credential, so two
// callers producing the same key produce byte-identical upstream requests and
// therefore identical responses -- which is the only condition under which a
// shared cache is not an access-control hole. Authorization has already run
// before any of this: a caller who may not reach this plugin never gets as far
// as the tool, let alone its cache.
//
// That property is load-bearing and it is a property of *this* plugin. A
// plugin whose upstream request varies by caller -- a per-user token, a header
// derived from the principal -- must not reuse this shape without putting the
// caller in the key.
type readCache struct {
	// plugin is the instance name, for the metric. Not in the key: the store
	// belongs to one instance already.
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
// Deny by default. An endpoint this does not recognise is fetched every time,
// so adding one to the client cannot quietly start serving it from memory.
//
// What is deliberately never cached, and why each one:
//
//   - /alarms, /alarms/history, /events. A model reads these to find out
//     whether something is wrong *now*. A cached "no alarms" says the network
//     is fine at the moment it stopped being fine, which is the worst answer
//     this integration can give. They are also cheap next to a device walk, so
//     there is nothing to buy.
//   - anything under a device: clients, statistics, performance. These are
//     live readings rather than records. The difference between "the device"
//     and "what the device is doing" is exactly the difference between what
//     may be reused and what may not.
//   - /devices/statistics, /devices/clients, /devices/wired_clients,
//     /devices/mesh/peers. The same, in collection form.
//   - anything keyed on a time window ending at "now". The key would be
//     different on every call, so a cache would hold memory and never answer
//     from it.
func (c Config) cacheTTL(path string) time.Duration {
	switch path {
	case "/networks", "/msp/managed_accounts",
		"/wifi_enterprise/wlans", "/wifi_enterprise/ap_groups":
		return c.InventoryCacheTTL
	case "/devices":
		return c.DeviceCacheTTL
	}
	if strings.HasPrefix(path, "/networks/") &&
		(strings.HasSuffix(path, "/sites") || strings.HasSuffix(path, "/towers")) {
		return c.InventoryCacheTTL
	}
	// One device by address, and nothing deeper. The segment has to look like
	// a MAC: /devices/clients and /devices/statistics are single segments too,
	// and matching on shape rather than on a list of names means a collection
	// added later is refused rather than silently cached.
	if rest, ok := strings.CutPrefix(path, "/devices/"); ok && !strings.Contains(rest, "/") {
		if mac, err := url.PathUnescape(rest); err == nil && macPattern.MatchString(mac) {
			return c.DeviceCacheTTL
		}
	}
	return 0
}

// cacheKind classifies a path for the metric. Structural rather than derived
// from the configured durations, which may legitimately be equal.
func cacheKind(path string) string {
	if strings.HasPrefix(path, "/devices") {
		return kindDevice
	}
	return kindInventory
}

// do returns a cached answer for key, or runs fetch and holds what it returns.
//
// A miss is single-flighted: a model fanning out six tool calls that all need
// the device list should cost one walk of the upstream's pagination, not six
// identical ones.
//
// Nothing stale is ever served. The catalogue cache serves stale answers
// because a browse page rendering slightly behind is better than not
// rendering; here the reader is a model about to act on what it is told, and
// "this is what the estate looked like a while ago" is not a safer answer than
// waiting.
func (c *readCache) do(ctx context.Context, kind, key string, ttl time.Duration, fetch func(context.Context) (any, error)) (any, error) {
	if ttl <= 0 {
		return fetch(ctx)
	}

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
			// A failure is not an answer and is not held. An upstream that is
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
// do. The resolved account is already in params by the time this runs, which
// is what makes the key stand for the request rather than for the arguments
// somebody typed.
func cacheKey(kind, path string, params url.Values) string {
	return kind + "\x00" + path + "\x00" + params.Encode()
}
