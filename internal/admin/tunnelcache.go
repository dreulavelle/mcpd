package admin

import (
	"context"
	"time"

	"github.com/spoked/mcpd/internal/cachestore"
	"github.com/spoked/mcpd/internal/tunnel"
)

// tunnelListTTL is how long one account's tunnel listing is reused.
//
// Short, because the page it feeds is the one somebody watches while making a
// tunnel, and a tunnel that does not appear for a minute reads as a failure.
// Long enough that opening the page, choosing an account and pressing Add is
// one call to OpenAI rather than three -- the dashboard polls this endpoint,
// and every poll was a request per configured account.
const tunnelListTTL = 20 * time.Second

// tunnelListStale is how long a held listing may still be served after it has
// gone stale, when OpenAI cannot be reached.
//
// A listing is not an authority on anything: it is what to offer in a select.
// Showing a minute-old one beats blanking the page because a request timed
// out, so long as it is only ever the fallback.
const tunnelListStale = 5 * time.Minute

// listTunnels returns one account's tunnels, from cache when it can.
//
// Keyed by account because each is a separate organisation with its own
// credential, and one account's expired admin key must not evict or poison
// another's answer.
//
// The single-flight group matters more here than the cache does: several
// browsers on the dashboard, or one browser polling while an operator presses
// Add, would otherwise each open their own request for the same listing.
func (s *Server) listTunnels(ctx context.Context, accountID string, dir *tunnel.Directory) ([]tunnel.TunnelInfo, error) {
	return s.listTunnelsFrom(ctx, accountID, dir.List)
}

// listTunnelsFrom is listTunnels with the fetch supplied, so the caching is
// testable without an organisation to call.
func (s *Server) listTunnelsFrom(ctx context.Context, accountID string, fetch func(context.Context) ([]tunnel.TunnelInfo, error)) ([]tunnel.TunnelInfo, error) {
	if s.tunnelCache == nil {
		return fetch(ctx)
	}
	now := time.Now()

	if e := s.tunnelCache.Get(accountID); e != nil {
		if e.State(now) == cachestore.Fresh {
			return e.Value.([]tunnel.TunnelInfo), nil
		}
	}

	value, _, err := s.tunnelGroup.Do(ctx, accountID, 30*time.Second,
		func(ctx context.Context) (any, error) {
			list, err := fetch(ctx)
			if err != nil {
				return nil, err
			}
			s.tunnelCache.Put(accountID, &cachestore.Entry{
				Value:      list,
				FetchedAt:  time.Now(),
				TTL:        tunnelListTTL,
				StaleWhile: tunnelListStale,
			})
			return list, nil
		})
	if err == nil {
		return value.([]tunnel.TunnelInfo), nil
	}

	// The fetch failed. A stale answer is better than none: the page is a
	// chooser, and an operator mid-way through making a tunnel should not lose
	// the list because one poll timed out. Reported as success deliberately --
	// the alternative is an error banner over a working page.
	if e := s.tunnelCache.Get(accountID); e != nil && e.State(now) != cachestore.Expired {
		return e.Value.([]tunnel.TunnelInfo), nil
	}
	return nil, err
}

// forgetTunnels drops an account's held listing.
//
// Called after a tunnel is made or deleted, because the next thing that
// happens is the page reloading, and showing a listing that predates the
// change is how a tunnel somebody just created appears not to exist.
func (s *Server) forgetTunnels(accountID string) {
	if s.tunnelCache == nil {
		return
	}
	s.tunnelCache.Put(accountID, &cachestore.Entry{
		Value:     []tunnel.TunnelInfo{},
		FetchedAt: time.Time{},
	})
}
