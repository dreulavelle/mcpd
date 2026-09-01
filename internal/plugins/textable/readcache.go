package textable

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/cachestore"
	"github.com/spoked/mcpd/internal/plugins"
)

// maxCacheEntries bounds one instance's held answers. Per instance rather than
// per process: two instances are two keys reading two tenants, and a shared
// bound would let a busy one evict a quiet one's answers.
const maxCacheEntries = 128

// maxCacheBytes bounds the memory those entries may occupy.
//
// A count alone is not a bound here. One held answer is a whole listing -- up
// to max_items records, each as wide as the upstream makes them -- so the same
// entry count is a few megabytes on a small tenant and far more on a large one.
const maxCacheBytes = 16 << 20

// fetchCeiling bounds a fetch that has outlived the caller who started it.
//
// A shared fetch belongs to whoever is still waiting rather than to whoever
// asked first, so it does not inherit that caller's cancellation. It has to
// inherit a deadline from somewhere, and this is it.
const fetchCeiling = 2 * time.Minute

// kindConfig is the one cache class this plugin has, and it is also the metrics
// label: how the instance is arranged, as opposed to what is in it.
const kindConfig = "config"

// readCache holds recent answers to the Textable reads that may be reused.
//
// # What may be reused, and what may not
//
// The rule is an allow-list: an endpoint cacheTTL does not recognise is fetched
// every time, so a tool added later cannot quietly start being served from
// memory.
//
// The tenant report is the one that matters. It is the directory behind
// list_tenants, get_tenant and list_users, so a model working down through an
// instance asks for it three times in a row, and it is the most expensive read
// here -- it walks every tenant, organization and user. Holding it is most of
// what makes this integration cheap to use.
//
// Two things are deliberately off the list:
//
//   - A contact. Whether somebody has opted out of messages is the field a
//     caller acts on, and it is a legal position rather than a preference. A
//     held answer that says they have not, minutes after they did, is the one
//     wrong answer here with a consequence outside the conversation.
//   - /health. A liveness check answered from memory is not one.
//
// # Why a key needs no principal
//
// The key is built from the request that will actually be made. Every caller of
// one plugin instance reaches Textable with the same key, so two callers
// producing the same cache key produce byte-identical upstream requests and
// therefore identical responses -- which is the only condition under which a
// shared cache is not an access-control hole. Authorization has already run: a
// caller who may not reach this plugin never gets as far as the tool.
//
// That property is load-bearing and it is a property of *this* plugin. Textable
// scopes what a key can see to the account the key belongs to, so if this
// integration ever took a per-caller key -- one account's credential per
// principal, so that Textable's own scoping did the filtering -- this shape
// would become unsafe and the principal would have to enter the key.
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
	case tenantsPath, tenantReportPath, "/api/v2/organizations":
		return c.CacheTTL()
	}
	// One tenant, one organization or one user by id, and nothing deeper.
	// Matching on shape rather than on a list of names means a sub-collection
	// added later -- /api/v2/organizations/{id}/move-warnings, say -- is
	// fetched rather than silently cached under the wrong assumption about how
	// often it changes.
	for _, prefix := range []string{
		tenantReportPath + "/", "/api/v2/organizations/",
	} {
		if rest, ok := strings.CutPrefix(path, prefix); ok && !strings.Contains(rest, "/") {
			return c.CacheTTL()
		}
	}
	return 0
}

// reuse returns a cached answer for this request, or runs fetch and holds what
// it returns.
//
// A miss is single-flighted: a model fanning out three tool calls that all need
// the user list should cost one request, not three identical ones.
//
// Nothing stale is ever served. The reader here is a model about to act on what
// it is told, and "this is what Textable looked like a while ago" is not a
// safer answer than waiting.
func (c *readCache) reuse(ctx context.Context, method, path string, params url.Values,
	fetch func(context.Context) (any, error)) (any, error) {

	ttl := c.cfg.cacheTTL(path)
	if ttl <= 0 {
		return fetch(ctx)
	}
	key := requestDigest(method, path, params)

	if hit := c.store.Get(key); hit != nil && hit.State(c.now()) == cachestore.Fresh {
		c.event(plugins.CacheHit)
		return hit.Value, nil
	}

	value, shared, err := c.group.Do(ctx, key, fetchCeiling, func(ctx context.Context) (any, error) {
		// Re-checked inside the flight: the caller this one is sharing with may
		// have filled the entry between the miss above and getting here.
		if hit := c.store.Get(key); hit != nil && hit.State(c.now()) == cachestore.Fresh {
			return hit.Value, nil
		}
		v, err := fetch(ctx)
		if err != nil {
			// A failure is not an answer and is not held. A Textable that is
			// down should be reported as down on every call rather than
			// remembered as an instance with no users.
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
// url.Values.Encode sorts by name, so two callers who set the same filters in a
// different order produce the same digest and two who set different ones never
// do. Hashed rather than kept whole because a path can carry an id somebody's
// contact record supplied, and a cache key is not a place to accumulate those.
func requestDigest(method, path string, params url.Values) string {
	var b strings.Builder
	b.WriteString(method)
	b.WriteByte(0)
	b.WriteString(path)
	b.WriteByte(0)
	b.WriteString(params.Encode())
	sum := sha256.Sum256([]byte(b.String()))
	return base64.RawStdEncoding.EncodeToString(sum[:])
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
