package admin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/cachestore"
	"github.com/spoked/mcpd/internal/tunnel"
)

// countingLister stands in for a Directory, counting how often OpenAI is asked.
type countingLister struct {
	mu    sync.Mutex
	calls int
	err   error
	list  []tunnel.TunnelInfo
}

func (c *countingLister) List(context.Context) ([]tunnel.TunnelInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.list, nil
}

func (c *countingLister) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// The dashboard polls this endpoint, so an uncached listing was a request to
// OpenAI per configured account per poll -- and switching accounts in the form
// was a fresh round trip every time.
func TestATunnelListingIsHeldBriefly(t *testing.T) {
	s := &Server{tunnelCache: cachestore.New(8), tunnelGroup: &cachestore.Group{}}
	src := &countingLister{list: []tunnel.TunnelInfo{{ID: "tunnel_1"}}}

	for range 5 {
		got, err := s.listTunnelsFrom(context.Background(), "acct_a", src.List)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d tunnels", len(got))
		}
	}
	if src.count() != 1 {
		t.Fatalf("asked OpenAI %d times, want 1 -- the listing is not being held", src.count())
	}
}

// Each account is a separate organisation with its own credential, so one
// must never answer for another.
func TestOneAccountsListingIsNotAnothers(t *testing.T) {
	s := &Server{tunnelCache: cachestore.New(8), tunnelGroup: &cachestore.Group{}}
	a := &countingLister{list: []tunnel.TunnelInfo{{ID: "tunnel_a"}}}
	b := &countingLister{list: []tunnel.TunnelInfo{{ID: "tunnel_b"}}}

	gotA, _ := s.listTunnelsFrom(context.Background(), "acct_a", a.List)
	gotB, _ := s.listTunnelsFrom(context.Background(), "acct_b", b.List)
	if len(gotA) != 1 || gotA[0].ID != "tunnel_a" {
		t.Fatalf("account a got %+v", gotA)
	}
	if len(gotB) != 1 || gotB[0].ID != "tunnel_b" {
		t.Fatalf("account b got %+v", gotB)
	}
}

// A tunnel somebody just made must not be missing from the next page load.
func TestMakingATunnelDropsTheHeldListing(t *testing.T) {
	s := &Server{tunnelCache: cachestore.New(8), tunnelGroup: &cachestore.Group{}}
	src := &countingLister{list: []tunnel.TunnelInfo{{ID: "tunnel_1"}}}

	if _, err := s.listTunnelsFrom(context.Background(), "acct_a", src.List); err != nil {
		t.Fatal(err)
	}
	s.forgetTunnels("acct_a")
	if _, err := s.listTunnelsFrom(context.Background(), "acct_a", src.List); err != nil {
		t.Fatal(err)
	}
	if src.count() != 2 {
		t.Fatalf("asked %d times, want 2 -- the listing was not dropped", src.count())
	}
}

// The page is a chooser, not an authority. Losing the list because one poll
// timed out is worse than showing a slightly old one.
func TestAStaleListingIsServedWhenOpenAICannotBeReached(t *testing.T) {
	s := &Server{tunnelCache: cachestore.New(8), tunnelGroup: &cachestore.Group{}}
	src := &countingLister{list: []tunnel.TunnelInfo{{ID: "tunnel_1"}}}

	if _, err := s.listTunnelsFrom(context.Background(), "acct_a", src.List); err != nil {
		t.Fatal(err)
	}
	// Age it past fresh but inside the stale window, then break the upstream.
	e := s.tunnelCache.Get("acct_a")
	s.tunnelCache.Put("acct_a", &cachestore.Entry{
		Value:      e.Value,
		FetchedAt:  time.Now().Add(-tunnelListTTL - time.Second),
		TTL:        tunnelListTTL,
		StaleWhile: tunnelListStale,
	})
	src.err = errors.New("openai is unreachable")

	got, err := s.listTunnelsFrom(context.Background(), "acct_a", src.List)
	if err != nil {
		t.Fatalf("a stale listing should have been served: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
}

// With nothing held, a failure is a failure.
func TestAFailureWithNothingHeldIsReported(t *testing.T) {
	s := &Server{tunnelCache: cachestore.New(8), tunnelGroup: &cachestore.Group{}}
	src := &countingLister{err: errors.New("openai is unreachable")}

	if _, err := s.listTunnelsFrom(context.Background(), "acct_a", src.List); err == nil {
		t.Fatal("a failure with no held answer was reported as success")
	}
}
