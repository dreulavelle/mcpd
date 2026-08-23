package app

import (
	"log/slog"

	"github.com/spoked/mcpd/internal/admin"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/registry"
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
func buildCatalog(cfg config.Catalog, log *slog.Logger) *registry.Multi {
	if !cfg.Enabled() {
		return nil
	}
	agent := "mcpd/" + Version
	shared := registry.NewCacheStore(0)
	options := registry.CacheOptions{Store: shared}

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
	if len(sources) == 0 {
		// Every configured source dropped out -- today that means PulseMCP
		// alone, switched on with a credential that would not resolve. Nil
		// rather than an empty Multi, so the handler says "no server catalogue
		// is configured" instead of failing every browse.
		return nil
	}
	return registry.NewMulti(sources...)
}

// catalogAPI hands the dashboard the catalogue, or nothing when every source
// is switched off.
//
// A zero CatalogAPI is what the handler checks for, so the absence has to
// survive as nil functions rather than as a Multi with no sources -- which
// would answer a browse with an error instead of with "no server catalogue is
// configured".
func catalogAPI(catalog *registry.Multi) admin.CatalogAPI {
	if catalog == nil {
		return admin.CatalogAPI{}
	}
	return admin.CatalogAPI{
		List:   catalog.List,
		Get:    catalog.Get,
		Source: catalog.Source,
	}
}
