package cnmaestro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// manyRecords answers a listing with n records, each padded to roughly size
// bytes, so a byte ceiling can be crossed without needing a real estate.
func manyRecords(f *fakeAPI, path string, n, size int) {
	records := make([]map[string]any, 0, n)
	for i := range n {
		records = append(records, map[string]any{
			"mac":  "AA:BB:CC:DD:EE:FF",
			"note": strings.Repeat("x", size),
			"i":    i,
		})
	}
	f.handle(path, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":   records,
			"paging": map[string]any{"total": n, "limit": 100, "offset": 0},
		})
	})
}

// TestListStopsAtTheByteCeiling is the gap this closes.
//
// The item limit bounds how many records come back and says nothing about how
// large they are, so a listing that satisfied it could still be past what may
// be sent -- and then the client cuts it, mid-JSON, with nothing saying what
// went missing. See plugins.MaxResultBytes.
func TestListStopsAtTheByteCeiling(t *testing.T) {
	f := newFakeAPI(t)
	manyRecords(f, "/devices", 20, 1000)
	c, _ := cachingClient(t, f, nil, nil)

	// Room for a handful of the twenty.
	page, err := c.List(context.Background(), "/devices", url.Values{}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) == 20 {
		t.Fatal("the ceiling did not cut anything")
	}
	if len(page.Items) == 0 {
		t.Fatal("the ceiling cut everything")
	}
	if !page.Truncated {
		t.Error("cut without saying so, which reads as a complete estate")
	}
	encoded, err := json.Marshal(page.Items)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 5000 {
		t.Errorf("kept %d bytes, over the %d ceiling", len(encoded), 5000)
	}
}

// TestListKeepsTheFirstRecordHowLargeItIs prefers one large answer to none.
//
// A caller who asked for one device by address should get it whatever its
// configuration blob looks like; returning nothing because the single matching
// row was big is the worse failure, and it looks like "no such device".
func TestListKeepsTheFirstRecordHowLargeItIs(t *testing.T) {
	f := newFakeAPI(t)
	manyRecords(f, "/devices", 3, 50_000)
	c, _ := cachingClient(t, f, nil, nil)

	page, err := c.List(context.Background(), "/devices", url.Values{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("kept %d records, want the first one only", len(page.Items))
	}
	if !page.Truncated {
		t.Error("kept one of three without saying so")
	}
}

// TestTheByteCeilingDoesNotReachTheCachedEntry is the subtle one.
//
// The cache holds the whole walk. Capping before the copy would store whatever
// the first caller's budget happened to leave, and every later caller would be
// served that smaller answer with nothing saying why -- a bug that only shows
// up as an estate that quietly shrank.
func TestTheByteCeilingDoesNotReachTheCachedEntry(t *testing.T) {
	f := newFakeAPI(t)
	manyRecords(f, "/devices", 20, 1000)
	c, _ := cachingClient(t, f, nil, nil)

	tight, err := c.List(context.Background(), "/devices", url.Values{}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if !tight.Truncated {
		t.Fatal("expected the first read to be cut")
	}

	// Same read, no ceiling. It is served from cache, and must be whole.
	whole, err := c.List(context.Background(), "/devices", url.Values{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(whole.Items) != 20 {
		t.Fatalf("the cached entry came back with %d of 20 records; a previous "+
			"caller's budget reached it", len(whole.Items))
	}
	if whole.Truncated {
		t.Error("a whole answer reported itself as cut")
	}
	if got := f.dataRequests.Load(); got != 1 {
		t.Errorf("the upstream saw %d requests, want 1", got)
	}
}

// TestNoCeilingMeansNoCut keeps the disabled case explicit, because every
// existing caller that passes zero depends on it.
func TestNoCeilingMeansNoCut(t *testing.T) {
	f := newFakeAPI(t)
	manyRecords(f, "/devices", 20, 1000)
	c, _ := cachingClient(t, f, nil, nil)

	page, err := c.List(context.Background(), "/devices", url.Values{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 20 || page.Truncated {
		t.Fatalf("a zero ceiling cut the answer: %d records, truncated=%v",
			len(page.Items), page.Truncated)
	}
}
