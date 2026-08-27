package cnmaestro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
)

// clock is a hand-wound time source, so a TTL test is arithmetic rather than a
// sleep.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *clock {
	return &clock{at: time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// recordingObserver collects what the cache reported, so a test can assert on
// the numbers an operator would see rather than only on request counts.
type recordingObserver struct {
	mu     sync.Mutex
	events map[string]int
}

func newRecorder() *recordingObserver {
	return &recordingObserver{events: map[string]int{}}
}

func (r *recordingObserver) CacheEvent(_, kind, event string) {
	r.mu.Lock()
	r.events[kind+"/"+event]++
	r.mu.Unlock()
}

func (r *recordingObserver) count(kind, event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[kind+"/"+event]
}

// cachingClient builds a client with caching on and a clock a test can wind.
func cachingClient(t *testing.T, f *fakeAPI, obs *recordingObserver, mutate func(*Config)) (*Client, *clock) {
	t.Helper()
	cfg := Config{
		BaseURL:               f.server.URL,
		ClientID:              "client-id",
		ClientSecret:          "client-secret",
		RequestsPerSecond:     1000,
		InventoryCacheSeconds: intPtr(300),
		DeviceCacheSeconds:    intPtr(15),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	cfg.withDefaults()

	clk := newClock()
	// A typed nil inside a non-nil interface is the trap here: the cache
	// checks for nil and would call through to a nil receiver.
	var observer plugins.CacheObserver
	if obs != nil {
		observer = obs
	}
	cache := newReadCache("cnmaestro", cfg, clk.now, observer)
	// The token manager gets real time: expiry is not what these test, and a
	// frozen clock would make every call look like the first.
	return NewClient(f.server.Client(), cfg, "client-id", "client-secret",
		discardLogger(), time.Now, cache, nil), clk
}

// devicePage answers a device listing with one record.
func devicePage(f *fakeAPI, path string) {
	f.handle(path, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":   []map[string]any{{"mac": "AA:BB:CC:DD:EE:FF", "status": "online"}},
			"paging": map[string]any{"total": 1, "limit": 100, "offset": 0},
		})
	})
}

// The win: a second identical read costs nothing upstream.
func TestReadCache_ASecondIdenticalReadDoesNotReachUpstream(t *testing.T) {
	f := newFakeAPI(t)
	devicePage(f, "/devices")
	obs := newRecorder()
	c, _ := cachingClient(t, f, obs, nil)

	for range 5 {
		if _, err := c.List(context.Background(), "/devices", url.Values{}, 0); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.dataRequests.Load(); got != 1 {
		t.Errorf("the upstream saw %d requests, want 1", got)
	}
	if obs.count(kindDevice, "hit") != 4 {
		t.Errorf("recorded %d hits, want 4", obs.count(kindDevice, "hit"))
	}
	if obs.count(kindDevice, "miss") != 1 {
		t.Errorf("recorded %d misses, want 1", obs.count(kindDevice, "miss"))
	}
}

// Different filters are different questions and must not share an answer.
func TestReadCache_DifferentFiltersAreDifferentKeys(t *testing.T) {
	f := newFakeAPI(t)
	devicePage(f, "/devices")
	c, _ := cachingClient(t, f, nil, nil)

	for _, network := range []string{"north", "south", "north"} {
		if _, err := c.List(context.Background(), "/devices",
			url.Values{"network": {network}}, 0); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.dataRequests.Load(); got != 2 {
		t.Errorf("the upstream saw %d requests, want 2", got)
	}
}

// The key is built from the request that will actually be made, so naming the
// configured account explicitly is the same question as leaving it out. Getting
// this wrong would not be a correctness bug -- it would just be a cache that
// misses -- but it is the property that makes the key mean what it claims to.
func TestReadCache_KeysOnTheResolvedRequestNotTheArguments(t *testing.T) {
	f := newFakeAPI(t)
	devicePage(f, "/devices")
	c, _ := cachingClient(t, f, nil, func(cfg *Config) {
		cfg.ManagedAccount = "Acme"
	})

	ctx := context.Background()
	if _, err := c.List(ctx, "/devices", url.Values{}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(ctx, "/devices", url.Values{managedAccountKV: {"Acme"}}, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.dataRequests.Load(); got != 1 {
		t.Errorf("the upstream saw %d requests; naming the configured account "+
			"should resolve to the same request", got)
	}
}

// A different account is a different estate, and must never be answered from
// another one's entry.
func TestReadCache_AnotherAccountIsAnotherAnswer(t *testing.T) {
	f := newFakeAPI(t)
	devicePage(f, "/devices")
	c, _ := cachingClient(t, f, nil, nil)

	ctx := context.Background()
	for _, account := range []string{"Acme", "Globex", "Acme"} {
		if _, err := c.List(ctx, "/devices", url.Values{managedAccountKV: {account}}, 0); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.dataRequests.Load(); got != 2 {
		t.Errorf("the upstream saw %d requests, want 2", got)
	}
}

// The allow-list, stated as a test. A stale alarm list says the network is fine
// at the moment it stopped being fine, and a statistic is a reading rather than
// a record -- so neither is ever held, whatever the configured windows say.
func TestReadCache_WhatIsNeverCached(t *testing.T) {
	cfg := Config{
		InventoryCacheSeconds: intPtr(300),
		DeviceCacheSeconds:    intPtr(15),
	}
	cfg.withDefaults()

	cached := []string{
		"/networks",
		"/msp/managed_accounts",
		"/wifi_enterprise/wlans",
		"/wifi_enterprise/ap_groups",
		"/networks/north/sites",
		"/networks/north/towers",
		"/devices",
		"/devices/" + url.PathEscape("AA:BB:CC:DD:EE:FF"),
		"/devices/AA-BB-CC-DD-EE-FF",
	}
	for _, path := range cached {
		if cfg.cacheTTL(path) <= 0 {
			t.Errorf("%s should be reusable", path)
		}
	}

	never := []string{
		// Is something wrong right now.
		"/alarms", "/alarms/history", "/events",
		// Who is connected right now.
		"/devices/clients", "/devices/wired_clients", "/devices/mesh/peers",
		"/devices/" + url.PathEscape("AA:BB:CC:DD:EE:FF") + "/clients",
		// Readings, not records.
		"/devices/statistics",
		"/devices/" + url.PathEscape("AA:BB:CC:DD:EE:FF") + "/statistics",
		"/devices/" + url.PathEscape("AA:BB:CC:DD:EE:FF") + "/performance",
		// Deny by default: an endpoint nobody has thought about.
		"/some/new/endpoint", "/devices/whatever_comes_next",
	}
	for _, path := range never {
		if ttl := cfg.cacheTTL(path); ttl != 0 {
			t.Errorf("%s must never be reused, got %s", path, ttl)
		}
	}
}

// Switching a class off switches it off, and switching one off leaves the other
// alone.
func TestReadCache_ConfigurationSwitchesEachClassOffSeparately(t *testing.T) {
	cfg := Config{DeviceCacheSeconds: intPtr(0)}
	cfg.withDefaults()
	if cfg.cacheTTL("/devices") != 0 {
		t.Error("device caching should be off")
	}
	if cfg.cacheTTL("/networks") != defaultInventoryTTL {
		t.Errorf("inventory caching should be untouched, got %s", cfg.cacheTTL("/networks"))
	}

	// Absent is not the same as zero: it takes the default.
	var unset Config
	unset.withDefaults()
	if unset.cacheTTL("/devices") != defaultDeviceTTL {
		t.Errorf("an unconfigured instance should take the default, got %s",
			unset.cacheTTL("/devices"))
	}
}

// An answer stops being reusable when its window closes.
func TestReadCache_ExpiresAndRefetches(t *testing.T) {
	f := newFakeAPI(t)
	devicePage(f, "/devices")
	c, clk := cachingClient(t, f, nil, nil)

	ctx := context.Background()
	if _, err := c.List(ctx, "/devices", url.Values{}, 0); err != nil {
		t.Fatal(err)
	}
	clk.advance(14 * time.Second)
	if _, err := c.List(ctx, "/devices", url.Values{}, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.dataRequests.Load(); got != 1 {
		t.Fatalf("inside the window the upstream saw %d requests, want 1", got)
	}

	clk.advance(2 * time.Second)
	if _, err := c.List(ctx, "/devices", url.Values{}, 0); err != nil {
		t.Fatal(err)
	}
	if got := f.dataRequests.Load(); got != 2 {
		t.Errorf("past the window the upstream saw %d requests, want 2", got)
	}
}

// Nothing stale is ever served. There is no window after expiry in which a held
// answer goes out while a refresh runs behind it -- the reader here is a model
// about to act, and "this is what the estate looked like a while ago" is not a
// safer answer than waiting.
func TestReadCache_NeverServesAnExpiredAnswer(t *testing.T) {
	f := newFakeAPI(t)
	var status atomic.Value
	status.Store("online")
	f.handle("/devices", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":   []map[string]any{{"mac": "AA:BB:CC:DD:EE:FF", "status": status.Load()}},
			"paging": map[string]any{"total": 1},
		})
	})
	c, clk := cachingClient(t, f, nil, nil)

	ctx := context.Background()
	if _, err := c.List(ctx, "/devices", url.Values{}, 0); err != nil {
		t.Fatal(err)
	}
	status.Store("offline")
	clk.advance(time.Minute)

	page, err := c.List(ctx, "/devices", url.Values{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := page.Items[0]["status"]; got != "offline" {
		t.Errorf("status = %v; an expired answer was served instead of refetched", got)
	}
}

// A failure is not an answer. An upstream that is down must be reported as down
// on every call rather than remembered as an empty estate.
func TestReadCache_DoesNotHoldAFailure(t *testing.T) {
	f := newFakeAPI(t)
	var fail atomic.Bool
	fail.Store(true)
	f.handle("/devices", func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream down"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":   []map[string]any{{"mac": "AA:BB:CC:DD:EE:FF"}},
			"paging": map[string]any{"total": 1},
		})
	})
	c, _ := cachingClient(t, f, nil, nil)

	ctx := context.Background()
	if _, err := c.List(ctx, "/devices", url.Values{}, 0); err == nil {
		t.Fatal("a failing upstream must fail the call")
	}
	fail.Store(false)
	page, err := c.List(ctx, "/devices", url.Values{}, 0)
	if err != nil {
		t.Fatalf("the next call should reach the recovered upstream: %v", err)
	}
	if len(page.Items) != 1 {
		t.Errorf("got %d items; a remembered failure was served", len(page.Items))
	}
}

// Concurrent identical reads collapse into one walk. This is the half of the
// win a hit rate does not show: without it, a model fanning out six tool calls
// pays six full paginations of the same estate.
func TestReadCache_ConcurrentIdenticalReadsShareOneWalk(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/devices", func(w http.ResponseWriter, _ *http.Request) {
		// Slow enough that every caller arrives before the first finishes.
		time.Sleep(50 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":   []map[string]any{{"mac": "AA:BB:CC:DD:EE:FF"}},
			"paging": map[string]any{"total": 1},
		})
	})
	obs := newRecorder()
	c, _ := cachingClient(t, f, obs, nil)

	const callers = 6
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = c.List(context.Background(), "/devices", url.Values{}, 0)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}
	if got := f.dataRequests.Load(); got != 1 {
		t.Errorf("the upstream saw %d walks, want 1", got)
	}
	if shared := obs.count(kindDevice, "shared"); shared == 0 {
		t.Error("no caller reported sharing a fetch")
	}
}

// A caller appending to what it was handed must not reach the held answer.
func TestReadCache_ACallerCannotAppendIntoTheCache(t *testing.T) {
	f := newFakeAPI(t)
	devicePage(f, "/devices")
	c, _ := cachingClient(t, f, nil, nil)

	ctx := context.Background()
	first, err := c.List(ctx, "/devices", url.Values{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	first.Items = append(first.Items, Record{"mac": "intruder"})
	first.Warnings = append(first.Warnings, "invented")

	second, err := c.List(ctx, "/devices", url.Values{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 {
		t.Errorf("the held answer grew to %d items", len(second.Items))
	}
	if len(second.Warnings) != 0 {
		t.Errorf("the held answer picked up %d warnings", len(second.Warnings))
	}
}

// One device by address is reusable and decodes per caller, so two callers
// wanting different shapes out of the same record both get one.
func TestReadCache_GetIsCachedAndDecodesPerCaller(t *testing.T) {
	f := newFakeAPI(t)
	mac := "AA:BB:CC:DD:EE:FF"
	f.handle("/devices/"+mac, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"mac": mac, "name": "ap-1"},
		})
	})
	c, _ := cachingClient(t, f, nil, nil)

	ctx := context.Background()
	path := "/devices/" + url.PathEscape(mac)

	var asMap Record
	if _, err := c.Get(ctx, path, nil, &asMap); err != nil {
		t.Fatal(err)
	}
	var asStruct struct {
		Name string `json:"name"`
	}
	if _, err := c.Get(ctx, path, nil, &asStruct); err != nil {
		t.Fatal(err)
	}

	if got := f.dataRequests.Load(); got != 1 {
		t.Errorf("the upstream saw %d requests, want 1", got)
	}
	if asMap["name"] != "ap-1" || asStruct.Name != "ap-1" {
		t.Errorf("map = %v, struct = %+v", asMap, asStruct)
	}
}

// BenchmarkList measures the whole point: walking a paginated estate against
// the same upstream, with the cache on and with it off.
func BenchmarkList(b *testing.B) {
	const pages = 10
	const pageSize = 100

	newAPI := func(tb testing.TB) *fakeAPI {
		f := &fakeAPI{mux: http.NewServeMux(), tokenStatus: http.StatusOK, expiresIn: 3600}
		f.mux.HandleFunc("POST /api/v2/access/token", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "tok", "token_type": "bearer", "expires_in": 3600,
			})
		})
		f.handle("/devices", func(w http.ResponseWriter, r *http.Request) {
			offset := 0
			_, _ = fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
			items := make([]map[string]any, 0, pageSize)
			for i := range pageSize {
				items = append(items, map[string]any{
					"mac":    fmt.Sprintf("AA:BB:CC:%02X:%02X:FF", offset/pageSize, i),
					"status": "online",
					"name":   fmt.Sprintf("device-%d", offset+i),
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data":   items,
				"paging": map[string]any{"total": pages * pageSize, "limit": pageSize, "offset": offset},
			})
		})
		f.server = httptest.NewServer(f.mux)
		tb.Cleanup(f.server.Close)
		return f
	}

	build := func(f *fakeAPI, seconds int) *Client {
		cfg := Config{
			BaseURL:               f.server.URL,
			RequestsPerSecond:     100000,
			PageSize:              pageSize,
			MaxItems:              pages * pageSize,
			InventoryCacheSeconds: intPtr(0),
			DeviceCacheSeconds:    intPtr(seconds),
		}
		cfg.withDefaults()
		var cache *readCache
		if cfg.DeviceCacheTTL > 0 {
			cache = newReadCache("cnmaestro", cfg, time.Now, nil)
		}
		return NewClient(f.server.Client(), cfg, "id", "secret",
			discardLogger(), time.Now, cache, nil)
	}

	for _, tc := range []struct {
		name    string
		seconds int
	}{
		{"uncached", 0},
		{"cached", 60},
	} {
		b.Run(tc.name, func(b *testing.B) {
			f := newAPI(b)
			c := build(f, tc.seconds)
			// Warm the token so the first iteration is not paying for it.
			if _, err := c.List(context.Background(), "/devices", url.Values{}, 0); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for range b.N {
				if _, err := c.List(context.Background(), "/devices", url.Values{}, 0); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(f.dataRequests.Load())/float64(b.N), "upstream-reqs/op")
		})
	}
}
