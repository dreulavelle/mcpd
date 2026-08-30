package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/spoked/mcpd/internal/admin"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/registry"
	"github.com/spoked/mcpd/internal/settings"
)

// buildCatalog assembles the public catalogues this deployment browses.
//
// Order is preference order, and it decides two things: which copy of a server
// appearing in more than one catalogue survives deduplication, and which
// catalogue answers a lookup for a name several of them know. The rule behind
// the order is one thing -- how far the entry is from the party that operates
// the server -- applied four times:
//
//  1. **The official registry.** The publisher registered the server there
//     themselves. First party, and the document is theirs.
//  2. **PulseMCP.** An aggregator, but a pass-through one: it speaks the
//     Generic MCP Registry API and hands back the publisher's own server.json
//     unchanged, mostly mirrored from the official registry. Somebody else's
//     copy of a first-party document.
//  3. **Docker.** Curated, useful, and not a server.json at all -- an entry is
//     translated into one here. A document this host composed from a third
//     party's description of somebody else's server.
//  4. **Smithery.** Furthest, and differently so. A Smithery entry does not
//     describe the publisher's endpoint; it describes Smithery's hosted proxy
//     in front of it, dialled with Smithery's credential rather than the
//     publisher's. It is the least direct relationship to the operator of the
//     server, so it loses every tie.
//
// In practice the fourth rarely competes, and that is worth knowing rather
// than assuming: deduplication across catalogues is by address, and every
// Smithery entry is addressed at server.smithery.ai/{name}/mcp. So a Smithery
// row and an official row for what is recognisably the same project do not
// merge -- and should not. Dialling the publisher's own endpoint with the
// publisher's key and dialling Smithery's gateway with a Smithery key are two
// different servers by every test that matters here: different address,
// different credential, different party to trust. Merging them on the strength
// of a similar name would hide one of two real choices. Where the four
// actually collide is official against PulseMCP, which mirrors it, and that is
// exactly the pair the order above resolves.
//
// Each source gets its own cache and they share one memory bound. Its own
// cache, because a catalogue that is down should be that catalogue's staleness
// rather than the page's, and because how long each answer stays fresh is what
// that catalogue's own Cache-Control said. One bound, because the thing being
// bounded is this process's memory and it does not care which catalogue filled
// it -- a per-source cap is a cap a fourth source silently quadruples. The cap
// is unchanged at four sources for that reason: it bounds the process, and the
// process did not get more memory by being pointed at more catalogues.
//
// Nothing here reaches the network. A source is constructed, not contacted.
func buildCatalog(cfg config.Catalog, repo *registry.Repo, observe registry.CacheObserver, log *slog.Logger) *registry.Index {
	agent := "mcpd/" + Version
	shared := registry.NewCacheStore(0)
	options := registry.CacheOptions{Store: shared, Observe: observe}

	var sources []registry.Client
	// The operator's own list goes first, which is a statement about trust
	// rather than about freshness. Preference order decides which copy of a
	// server survives deduplication, and a document somebody here put under
	// review beats every third party's description of the same thing --
	// including the publisher's own registration, because the question this
	// list answers is not "what exists" but "what are we allowed to run".
	//
	// Uncached, unlike the four below. It is already held in memory and
	// refreshed on its own schedule; a cache in front would be a second
	// staleness with no fetch to save.
	//
	// Added before the switch below and not governed by it. `catalog.enabled`
	// says whether this host browses the *public* catalogues, which is a
	// question about reaching third parties; an operator who turned those off
	// and then wrote their own list has answered it, and disabling the thing
	// they built on purpose would be silent and unexplainable.
	if repo != nil {
		sources = append(sources, repo)
	}
	if cfg.Enabled() {
		sources = append(sources, publicSources(cfg, agent, options, log)...)
	}
	if len(sources) == 0 {
		// Nothing configured at all: no self-hosted list and every public
		// catalogue off. Nil rather than an empty index, so the handler says
		// "no server catalogue is configured" instead of failing every browse.
		return nil
	}
	// An index rather than a per-request merge. It enumerates each catalogue
	// once a day and answers from what it holds, which is what makes a page a
	// consistent length, a count a count, and a search one rule -- see
	// registry.Index. Nothing is fetched until somebody opens the Marketplace,
	// so a host nobody browses pays for none of it.
	return registry.NewIndex(sources, registry.IndexOptions{Log: log})
}

// publicSources builds the catalogues this deployment browses on the internet.
func publicSources(cfg config.Catalog, agent string, options registry.CacheOptions, log *slog.Logger) []registry.Client {
	var sources []registry.Client
	if cfg.Official {
		sources = append(sources, registry.NewCached(
			registry.NewOfficial(registry.OfficialOptions{UserAgent: agent}), options))
	}
	if cfg.PulseMCP {
		// The key is resolved here rather than held as a reference, because
		// the client sends it on every request and re-reading a file per page
		// would be a fetch this host cannot report a failure from. A reference
		// that will not resolve is reported and the source is left out: a
		// catalogue that would 401 every page is worse than one that is
		// absent, and config validation has already refused to start when the
		// source is on with no reference at all.
		key, err := config.NewSecretResolver().Resolve(cfg.PulseMCPAPIKeyRef)
		switch {
		case err != nil:
			log.Warn("the PulseMCP catalogue is switched on but its api key could "+
				"not be read; the source is left out", "error", err)
		default:
			sources = append(sources, registry.NewCached(
				registry.NewPulseMCP(registry.PulseMCPOptions{
					UserAgent: agent,
					APIKey:    key,
					Tenant:    cfg.PulseMCPTenant,
				}), options))
		}
	}
	if cfg.Docker {
		sources = append(sources, registry.NewCached(
			registry.NewDocker(registry.DockerOptions{UserAgent: agent}), options))
	}
	if cfg.Smithery {
		sources = append(sources, registry.NewCached(
			registry.NewSmithery(registry.SmitheryOptions{UserAgent: agent}), options))
	}
	return sources
}

// catalogAPI hands the dashboard the catalogue, or nothing when every source
// is switched off.
//
// A zero CatalogAPI is what the handler checks for, so the absence has to
// survive as nil functions rather than as an index with no sources -- which
// would answer a browse with an error instead of with "no server catalogue is
// configured".
func catalogAPI(catalog *registry.Index) admin.CatalogAPI {
	if catalog == nil {
		return admin.CatalogAPI{}
	}
	return admin.CatalogAPI{
		List:   catalog.List,
		Get:    catalog.Get,
		Source: catalog.Source,
	}
}

// refreshRepoCatalog re-reads the operator's own list on a schedule.
//
// On a timer rather than on each browse, for the reason the entries are served
// from memory: opening the Marketplace should not depend on a git host being
// up. A fetch that fails leaves the previous list in place and is reported
// through the source's status, so the page can say the list is not being
// confirmed rather than showing an empty catalogue.
//
// The loop wakes far more often than it fetches, and that is the point. An
// operator who has just pasted an address expects the list to appear, not to
// arrive in six hours -- so whether a fetch is due is decided per pass from
// when the last one succeeded, rather than by how long the timer was set to
// when the loop last went round. Waking on a short tick and doing nothing is
// what makes changing the setting take effect.
func (a *App) refreshRepoCatalog(ctx context.Context) error {
	const tick = time.Minute

	// A first pass shortly after start, before the tick, so a host that has
	// been restarted has its list back promptly.
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		timer.Reset(tick)

		if !a.repoCatalog.Configured(ctx) {
			continue
		}
		status := a.repoCatalog.Status(ctx)
		hours := a.settings.Int(ctx, settings.KeyCatalogRepoHours, 6)

		switch {
		case status.FetchedAt.IsZero():
			// Never read: an address that has just been set, or a restart.
		case hours <= 0:
			// Zero is "only on a restart", and one has already happened.
			continue
		case time.Since(status.FetchedAt) < time.Duration(hours)*time.Hour:
			continue
		}

		if err := a.repoCatalog.Refresh(ctx); err != nil {
			a.log.WarnContext(ctx, "could not read your own server catalogue; "+
				"the previous list is still being offered", "error", err)
			continue
		}
		// The index answers from an enumeration it holds for a day, so a list
		// that has just changed has to drop it -- otherwise a server somebody
		// added to their catalogue appears up to a day later.
		if a.catalog != nil {
			a.catalog.Invalidate()
		}
		a.log.InfoContext(ctx, "read your own server catalogue",
			"entries", a.repoCatalog.Status(ctx).Entries)
	}
}
