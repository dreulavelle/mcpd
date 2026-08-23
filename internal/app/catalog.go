package app

import (
	"github.com/spoked/mcpd/internal/admin"
	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/registry"
)

// buildCatalog assembles the public catalogues this deployment browses.
//
// Order is preference order, and the official registry comes first
// deliberately: it is where a publisher registers a server themselves, so when
// the same server appears in both, the entry kept is the one from the party
// that operates it rather than a third party's description of it. Docker's
// catalogue is curated and useful and is still somebody's account of somebody
// else's server.
//
// Each source gets its own cache and they share one memory bound. Its own
// cache, because a catalogue that is down should be that catalogue's staleness
// rather than the page's, and because how long each answer stays fresh is what
// that catalogue's own Cache-Control said. One bound, because the thing being
// bounded is this process's memory and it does not care which catalogue filled
// it -- a per-source cap is a cap a fourth source silently quadruples.
//
// Nothing here reaches the network. A source is constructed, not contacted.
func buildCatalog(cfg config.Catalog) *registry.Multi {
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
	if cfg.Docker {
		sources = append(sources, registry.NewCached(
			registry.NewDocker(registry.DockerOptions{UserAgent: agent}), options))
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
