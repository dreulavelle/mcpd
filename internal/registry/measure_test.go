//go:build measure

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A measurement, not a test. Run with:
//
//	go test ./internal/registry -tags measure -run Measure -v
//
// Three catalogues behind a plausible round trip, holding entries of the shape
// the real ones hold: about half of them unaddable, descriptions the length
// the catalogues actually publish.

const (
	roundTrip     = 120 * time.Millisecond
	officialTotal = 300
	dockerTotal   = 320
	smitheryTotal = 500
)

var blurb = strings.Repeat("what this server does, at the length a catalogue writes it. ", 3)

// oldMerge is what Multi.List did before: hand the caller's limit to every
// source, take one page from each concurrently, concatenate, deduplicate,
// return the lot -- and carry one cursor per source, the source's own, with
// nothing to say how far into a page the last one stopped.
//
// Concurrent, because the old one was: comparing a sequential rewrite against
// a concurrent original would credit this change with a saving it did not
// make. What actually changed is how much comes back and where page two comes
// from, not how the fan-out is shaped.
func oldMerge(ctx context.Context, sources []Client, q Query, cursors map[string]string) (Page, map[string]string) {
	pages := make([]Page, len(sources))
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Add(1)
		go func(i int, s Client) {
			defer wg.Done()
			got, err := s.List(ctx, Query{
				Search: q.Search, Limit: q.Limit, Cursor: cursors[s.Source()],
			})
			if err == nil {
				pages[i] = got
			}
		}(i, s)
	}
	wg.Wait()

	page := Page{Entries: []Entry{}}
	next := map[string]string{}
	seen := map[string]bool{}
	for i, s := range sources {
		for _, e := range pages[i].Entries {
			if e.Source == "" {
				e.Source = s.Source()
			}
			if key := identity(e); !seen[key] {
				seen[key] = true
				page.Entries = append(page.Entries, e)
			}
		}
		if pages[i].NextCursor != "" {
			next[s.Source()] = pages[i].NextCursor
		}
	}
	return page, next
}

func slowly(t *testing.T, body func(*http.Request) []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(roundTrip)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body(r))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// officialLike pages a cursor, half its rows package-only.
func officialLike(t *testing.T) *Official {
	srv := slowly(t, func(r *http.Request) []byte {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 || limit > MaxEntriesPerPage {
			limit = 30
		}
		from, _ := strconv.Atoi(r.URL.Query().Get("cursor"))
		rows := make([]string, 0, limit)
		for i := from; i < from+limit && i < officialTotal; i++ {
			doc := fmt.Sprintf(
				`{"$schema":"https://static.modelcontextprotocol.io/schemas/2025-07-09/server.schema.json",`+
					`"name":"io.example.publisher-%03d/server","title":"Example Server %03d",`+
					`"description":%q,"version":"1.2.3",`, i, i, blurb)
			if i%2 == 0 {
				doc += fmt.Sprintf(`"remotes":[{"type":"streamable-http","url":"https://s%03d.example/mcp"}]}`, i)
			} else {
				doc += `"packages":[{"registryType":"npm","identifier":"@example/x","version":"1.0.0"}]}`
			}
			rows = append(rows, fmt.Sprintf(
				`{"server":%s,"_meta":{"io.modelcontextprotocol.registry/official":`+
					`{"status":"active","isLatest":true,"publishedAt":"2026-01-01T00:00:00Z",`+
					`"updatedAt":"2026-01-01T00:00:00Z"}}}`, doc))
		}
		next := ""
		if from+limit < officialTotal {
			next = strconv.Itoa(from + limit)
		}
		return []byte(fmt.Sprintf(`{"servers":[%s],"metadata":{"nextCursor":%q}}`,
			strings.Join(rows, ","), next))
	})
	return NewOfficial(OfficialOptions{BaseURL: srv.URL, HTTPClient: srv.Client()})
}

// dockerLike serves the whole catalogue in one document, as Docker does.
func dockerLike(t *testing.T) *Docker {
	var b strings.Builder
	b.WriteString("registry:\n")
	for i := range dockerTotal {
		kind := "remote"
		if i%2 == 1 {
			kind = "server"
		}
		fmt.Fprintf(&b, "  entry-%03d:\n    type: %s\n    title: Entry %03d\n"+
			"    description: %q\n    dateAdded: \"2026-01-01T00:00:00Z\"\n"+
			"    icon: https://icons.example/%03d.png\n", i, kind, i, blurb, i)
		if kind == "remote" {
			fmt.Fprintf(&b, "    remote:\n      transport_type: streamable-http\n"+
				"      url: https://d%03d.example/mcp\n", i)
		}
	}
	body := []byte(b.String())
	srv := slowly(t, func(*http.Request) []byte { return body })
	return NewDocker(DockerOptions{CatalogURL: srv.URL + "/catalog.yaml", HTTPClient: srv.Client()})
}

// smitheryLike serves five pages of a hundred, as Smithery does.
func smitheryLike(t *testing.T) *Smithery {
	srv := slowly(t, func(r *http.Request) []byte {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		rows := make([]string, 0, 100)
		for i := (page - 1) * 100; i < page*100 && i < smitheryTotal; i++ {
			rows = append(rows, fmt.Sprintf(
				`{"qualifiedName":"vendor%03d/server","displayName":"Smithery %03d",`+
					`"description":%q,"iconUrl":"https://icons.example/s%03d.png",`+
					`"verified":%t,"useCount":%d,"remote":%t,"isDeployed":true,`+
					`"createdAt":"2026-01-01T00:00:00Z"}`,
				i, i, blurb, i, i%3 == 0, smitheryTotal-i, i%2 == 0))
		}
		return []byte(fmt.Sprintf(
			`{"servers":[%s],"pagination":{"currentPage":%d,"pageSize":100,`+
				`"totalPages":5,"totalCount":10498}}`, strings.Join(rows, ","), page))
	})
	return NewSmithery(SmitheryOptions{
		BaseURL: srv.URL, GatewayURL: "https://server.smithery.ai", HTTPClient: srv.Client(),
	})
}

func fresh(t *testing.T) ([]Client, *Multi) {
	t.Helper()
	store := NewCacheStore(0)
	opts := CacheOptions{Store: store}
	sources := []Client{
		NewCached(officialLike(t), opts),
		NewCached(dockerLike(t), opts),
		NewCached(smitheryLike(t), opts),
	}
	multi := NewMulti(sources...)
	t.Cleanup(func() { _ = multi.Close() })
	return sources, multi
}

func wire(t *testing.T, page Page) int {
	t.Helper()
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	return len(raw)
}

func TestMeasure_EntriesPerRequestedLimit(t *testing.T) {
	sources, multi := fresh(t)
	t.Log("limit | before: entries / bytes | after: entries / bytes")
	for _, limit := range []int{10, 30, 100} {
		before, _ := oldMerge(context.Background(), sources, Query{Limit: limit}, nil)
		after, err := multi.List(context.Background(), Query{Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%5d | %8d / %7d | %8d / %7d",
			limit, len(before.Entries), wire(t, before), len(after.Entries), wire(t, after))
	}
}

// TestMeasure_TimeToAPage walks two pages on each shape, from cold, on caches
// of its own -- so page one pays for the network on both and page two shows
// what each shape asks for a second time.
func TestMeasure_TimeToAPage(t *testing.T) {
	t.Logf("round trip per source: %s, three sources, concurrent", roundTrip)

	oldSources, _ := fresh(t)
	started := time.Now()
	beforeOne, cursors := oldMerge(context.Background(), oldSources, Query{Limit: 30}, nil)
	beforeOneTook := time.Since(started)
	started = time.Now()
	beforeTwo, _ := oldMerge(context.Background(), oldSources, Query{Limit: 30}, cursors)
	beforeTwoTook := time.Since(started)

	_, multi := fresh(t)
	started = time.Now()
	afterOne, err := multi.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	afterOneTook := time.Since(started)
	started = time.Now()
	afterTwo, err := multi.List(context.Background(), Query{Limit: 10, Cursor: afterOne.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	afterTwoTook := time.Since(started)

	t.Logf("before, limit=30  page 1: %7s  %3d entries  %6d bytes",
		beforeOneTook.Round(time.Millisecond), len(beforeOne.Entries), wire(t, beforeOne))
	t.Logf("before, limit=30  page 2: %7s  %3d entries  %6d bytes",
		beforeTwoTook.Round(time.Millisecond), len(beforeTwo.Entries), wire(t, beforeTwo))
	t.Logf("after,  limit=10  page 1: %7s  %3d entries  %6d bytes",
		afterOneTook.Round(time.Millisecond), len(afterOne.Entries), wire(t, afterOne))
	t.Logf("after,  limit=10  page 2: %7s  %3d entries  %6d bytes",
		afterTwoTook.Round(time.Millisecond), len(afterTwo.Entries), wire(t, afterTwo))
	t.Logf("estimate beside the search box: %d+", afterOne.AddableEstimate)
	for _, s := range afterOne.Sources {
		t.Logf("  %-38s judged %3d  addable %3d  total %5d",
			s.Source, s.Judged, s.Addable, s.Total)
	}
}
