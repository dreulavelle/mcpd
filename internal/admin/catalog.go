package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/spoked/mcpd/internal/registry"
)

// CatalogAPI is the slice of a public registry the catalogue pages need.
//
// A pair of functions rather than the registry.Client itself, for the same
// reason every other capability on this server is: the dashboard is a handler
// over an interface, and a test supplies the parts it cares about without
// building an App or a network client.
type CatalogAPI struct {
	// List browses or searches the catalogue.
	List func(ctx context.Context, q registry.Query) (registry.Page, error)
	// Get returns one entry with the server.json that would be imported.
	Get func(ctx context.Context, name string) (registry.Detail, error)
	// Source names the catalogue, so a refusal can say which third party was
	// not reachable without quoting anything that third party said.
	Source func() string
}

// unreachable is what a caller is told when the catalogue could not be read.
//
// A fixed sentence and the catalogue's name, not the error. The error is a
// third party's -- it can carry their status line, or the address of a
// redirect they tried to send this host on -- and the fetch two layers down
// already drains and discards their response body for that reason. Relaying
// the same text in a header-shaped wrapper would put it back.
//
// The detail is not lost, it is filed where it belongs: the log line above
// carries the whole error for the operator who has to diagnose it.
func unreachable(c CatalogAPI) string {
	name := "the server catalogue"
	if c.Source != nil {
		if s := c.Source(); s != "" {
			name = s
		}
	}
	return name + " could not be read just now; nothing here is broken, " +
		"and it is worth trying again shortly"
}

// catalogueNames is what this host browses, for a refusal that has to say so.
//
// The composite's own name is the configured catalogues joined with commas,
// which is already the list. It is this deployment's configuration rather than
// anything a third party said, so it is quoted back plainly.
func catalogueNames(c CatalogAPI) string {
	if c.Source == nil {
		return "nothing"
	}
	if s := c.Source(); s != "" {
		return s
	}
	return "nothing"
}

// catalogPageLimit bounds a page. The registry caps its own at a hundred, and
// asking for more than a screenful of a third party's list is a lot of prose
// to render for a person who is going to pick one of them.
const (
	catalogDefaultLimit = 30
	catalogMaxLimit     = 100
)

// catalogContext adds an explicit refresh to a request when the operator asked
// for one.
//
// The escape hatch for a catalogue that is visibly behind. Every answer here
// is held for as long as the catalogue itself asked, which is right almost
// always and unhelpful in the one case that matters: a server published a
// minute ago that an administrator is standing in front of the dashboard
// waiting for. Rather than shorten the cache for everyone, ?refresh=1 asks
// again now, for one request.
//
// It is CapAdmin like the rest of the catalogue, and that is what keeps it
// from being a way to make this host hammer a third party: the people who can
// press it are the people who could restart the process.
func catalogContext(r *http.Request) context.Context {
	if truthy(r.URL.Query().Get("refresh")) {
		return registry.WithRefresh(r.Context())
	}
	return r.Context()
}

// truthy reads a query-string flag the way a person would write one.
func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// handleListCatalog browses the public registry.
//
// Administrator rather than operator, and the reason is the network rather
// than the data: this endpoint reaches a third party from inside the
// deployment, which is a thing an operator should not be able to make this
// host do. Everything it returns is public.
func (s *Server) handleListCatalog(w http.ResponseWriter, r *http.Request) {
	if s.opts.ServerCatalog.List == nil {
		s.writeError(w, r, http.StatusServiceUnavailable,
			"no server catalogue is configured")
		return
	}
	// An order this host does not have is refused rather than quietly
	// replaced with the default. A page that came back in no particular order
	// when one was asked for looks sorted and is not, and the caller has no
	// way to tell.
	order, err := registry.ParseSort(r.URL.Query().Get("sort"))
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest,
			"there is no such order here; this host sorts by "+
				strings.Join(registry.SortNames(), ", "))
		return
	}
	page, err := s.opts.ServerCatalog.List(catalogContext(r), registry.Query{
		Search: r.URL.Query().Get("q"),
		Cursor: r.URL.Query().Get("cursor"),
		Limit:  parseLimit(r.URL.Query().Get("limit"), catalogDefaultLimit, catalogMaxLimit),
		// One catalogue, or all of them. The four differ in kind -- who runs
		// the server, who holds the credential, who vouched for the entry --
		// which is a more useful cut than a category taxonomy none of them
		// publishes, and it is one this host can answer completely.
		Source: r.URL.Query().Get("source"),
		Sort:   order,
		// Off unless asked for, and there is no control in the dashboard that
		// asks. Roughly half of what the catalogues publish only runs
		// locally, and a page of ten that spends five rows explaining why
		// those five cannot be added is a page of five. The refusal is not
		// hidden -- it is still on the entry, and GET /api/catalog/{name}
		// still gives it in full for anyone who goes looking for one server
		// in particular. This is a choice about what a *listing* is for.
		IncludeUnaddable: truthy(r.URL.Query().Get("include_unaddable")),
	})
	switch {
	// A question this host cannot answer is the caller's mistake, not a third
	// party being down, and answering it with "the catalogue could not be
	// read" would send an operator looking for a network fault that is not
	// there. Both sentences name what this host does have, so the next
	// request can be a correct one.
	case errors.Is(err, registry.ErrUnknownSource):
		s.writeError(w, r, http.StatusBadRequest,
			"there is no catalogue by that name here; this host browses "+
				catalogueNames(s.opts.ServerCatalog))
		return
	case errors.Is(err, registry.ErrSortUnavailable):
		s.writeError(w, r, http.StatusBadRequest,
			"none of the catalogues here says how often a server is used, "+
				"so there is nothing to put in that order")
		return
	case err != nil:
		s.opts.Log.Warn("could not read the server catalogue", "error", err)
		s.writeError(w, r, http.StatusBadGateway, unreachable(s.opts.ServerCatalog))
		return
	}
	// Entries and Sources are never null. A page rendering `entries.map` over
	// null is a blank screen with an error in a console nobody has open, and
	// the same is true of the list of catalogues that answered.
	if page.Entries == nil {
		page.Entries = []registry.Entry{}
	}
	if page.Sources == nil {
		page.Sources = []registry.SourceStatus{}
	}
	s.writeJSON(w, r, http.StatusOK, page)
}

// handleGetCatalogEntry returns one entry's server.json, ready to prefill the
// import form.
//
// The name is a wildcard segment because a registry name carries a slash --
// "io.github.example/weather" -- and a single path segment cannot.
func (s *Server) handleGetCatalogEntry(w http.ResponseWriter, r *http.Request) {
	if s.opts.ServerCatalog.Get == nil {
		s.writeError(w, r, http.StatusServiceUnavailable,
			"no server catalogue is configured")
		return
	}
	name := r.PathValue("name")
	if name == "" {
		s.writeError(w, r, http.StatusBadRequest, "a server name is required")
		return
	}
	detail, err := s.opts.ServerCatalog.Get(catalogContext(r), name)
	switch {
	case errors.Is(err, registry.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound,
			"the catalogue has no active server by that name")
		return
	case err != nil:
		s.opts.Log.Warn("could not read a catalogue entry",
			"server", name, "error", err)
		s.writeError(w, r, http.StatusBadGateway, unreachable(s.opts.ServerCatalog))
		return
	}
	s.writeJSON(w, r, http.StatusOK, detail)
}
