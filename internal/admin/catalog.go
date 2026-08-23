package admin

import (
	"context"
	"errors"
	"net/http"

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
}

// catalogPageLimit bounds a page. The registry caps its own at a hundred, and
// asking for more than a screenful of a third party's list is a lot of prose
// to render for a person who is going to pick one of them.
const (
	catalogDefaultLimit = 30
	catalogMaxLimit     = 100
)

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
	page, err := s.opts.ServerCatalog.List(r.Context(), registry.Query{
		Search: r.URL.Query().Get("q"),
		Cursor: r.URL.Query().Get("cursor"),
		Limit:  parseLimit(r.URL.Query().Get("limit"), catalogDefaultLimit, catalogMaxLimit),
	})
	if err != nil {
		// The catalogue is somebody else's service, and it being unreachable
		// is not a fault in this host. 502 says which end failed, and the
		// message is what an operator would need to decide whether to wait.
		s.opts.Log.Warn("could not read the server catalogue", "error", err)
		s.writeError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	// Entries is never null. A page rendering `entries.map` over null is a
	// blank screen with an error in a console nobody has open.
	if page.Entries == nil {
		page.Entries = []registry.Entry{}
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
	detail, err := s.opts.ServerCatalog.Get(r.Context(), name)
	switch {
	case errors.Is(err, registry.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound,
			"the catalogue has no active server by that name")
		return
	case err != nil:
		s.opts.Log.Warn("could not read a catalogue entry", "server", name, "error", err)
		s.writeError(w, r, http.StatusBadGateway, err.Error())
		return
	}
	s.writeJSON(w, r, http.StatusOK, detail)
}
