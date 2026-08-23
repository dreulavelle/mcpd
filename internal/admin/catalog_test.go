package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/registry"
)

func newCatalogDashboard(t *testing.T, role auth.Role, api CatalogAPI) *Server {
	t.Helper()
	return NewServer(Options{
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier:      roleVerifier{role: role},
		ServerCatalog: api,
	})
}

func okCatalog() CatalogAPI {
	page := registry.Page{
		Source: "registry.modelcontextprotocol.io",
		Entries: []registry.Entry{{
			Name: "io.example/weather", SuggestedName: "weather",
			Title: "Weather", Version: "1.0.0", Addable: true,
		}},
		NextCursor:  "io.example/weather:1.0.0",
		RetrievedAt: time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC),
	}
	return CatalogAPI{
		List: func(context.Context, registry.Query) (registry.Page, error) { return page, nil },
		Get: func(_ context.Context, name string) (registry.Detail, error) {
			if name != "io.example/weather" {
				return registry.Detail{}, registry.ErrNotFound
			}
			return registry.Detail{
				Entry:       page.Entries[0],
				Document:    json.RawMessage(`{"name":"io.example/weather"}`),
				Source:      page.Source,
				RetrievedAt: page.RetrievedAt,
			}, nil
		},
	}
}

// Browsing the catalogue makes this host reach a third party from inside the
// deployment, which is a request an operator should not be able to cause. What
// comes back is public; the network call is the privilege.
func TestCatalogRoutes_NeedAdministrator(t *testing.T) {
	for _, path := range []string{"/api/catalog", "/api/catalog/io.example/weather"} {
		t.Run(path, func(t *testing.T) {
			user := newCatalogDashboard(t, auth.RoleUser, okCatalog())
			if got := request(t, user, http.MethodGet, path, nil).Code; got != http.StatusForbidden {
				t.Errorf("as a user: status = %d, want 403", got)
			}
			admin := newCatalogDashboard(t, auth.RoleAdmin, okCatalog())
			if got := request(t, admin, http.MethodGet, path, nil).Code; got != http.StatusOK {
				t.Errorf("as an administrator: status = %d, want 200", got)
			}
		})
	}
}

// A registry name carries a slash, which a single path segment cannot hold.
func TestCatalogEntry_NameCarriesASlash(t *testing.T) {
	var asked string
	api := okCatalog()
	inner := api.Get
	api.Get = func(ctx context.Context, name string) (registry.Detail, error) {
		asked = name
		return inner(ctx, name)
	}
	s := newCatalogDashboard(t, auth.RoleAdmin, api)

	w := request(t, s, http.MethodGet, "/api/catalog/io.example/weather", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	if asked != "io.example/weather" {
		t.Errorf("the handler asked for %q, want the whole registry name", asked)
	}

	var body struct {
		Document json.RawMessage `json:"document"`
		Source   string          `json:"source"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Document) == 0 {
		t.Error("the entry must carry the document the import form is prefilled from")
	}
}

// A catalogue that cannot be reached is somebody else's outage. It is reported
// as a bad gateway, never as a fault in this host, and it never takes the
// dashboard down with it.
//
// The body says which catalogue and nothing the catalogue said. The error can
// carry their status line or the address of a redirect they tried to send this
// host on, and the fetch two layers down already discards their response body
// for exactly that reason -- relaying the same text here would put it back.
func TestCatalog_UpstreamFailureIsABadGateway(t *testing.T) {
	const leak = "418 I am a teapot at https://attacker.example/"
	s := newCatalogDashboard(t, auth.RoleAdmin, CatalogAPI{
		Source: func() string { return "registry.modelcontextprotocol.io" },
		List: func(context.Context, registry.Query) (registry.Page, error) {
			return registry.Page{}, errors.New(leak)
		},
		Get: func(context.Context, string) (registry.Detail, error) {
			return registry.Detail{}, errors.New(leak)
		},
	})
	for _, path := range []string{"/api/catalog", "/api/catalog/io.example/weather"} {
		w := request(t, s, http.MethodGet, path, nil)
		if w.Code != http.StatusBadGateway {
			t.Errorf("%s: status = %d, want 502", path, w.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(body["error"], "teapot") ||
			strings.Contains(body["error"], "attacker.example") {
			t.Errorf("%s: the far end's text reached the caller: %q", path, body["error"])
		}
		if !strings.Contains(body["error"], "registry.modelcontextprotocol.io") {
			t.Errorf("%s: the refusal should name the catalogue: %q", path, body["error"])
		}
	}
}

// Without a Source the refusal still reads as a sentence rather than as an
// empty string with a suffix.
func TestCatalog_UpstreamFailureWithoutASourceName(t *testing.T) {
	s := newCatalogDashboard(t, auth.RoleAdmin, CatalogAPI{
		List: func(context.Context, registry.Query) (registry.Page, error) {
			return registry.Page{}, errors.New("boom")
		},
	})
	w := request(t, s, http.MethodGet, "/api/catalog", nil)
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body["error"], "the server catalogue") {
		t.Errorf("error = %q", body["error"])
	}
}

// Stale data is served as data, with the fact that it is stale beside it. A
// page that refuses to render because a third party is down is worse than one
// that says "this is what we last saw".
func TestCatalog_StalenessIsReportedRatherThanHidden(t *testing.T) {
	fetched := time.Date(2026, 8, 22, 9, 0, 0, 0, time.UTC)
	s := newCatalogDashboard(t, auth.RoleAdmin, CatalogAPI{
		List: func(context.Context, registry.Query) (registry.Page, error) {
			return registry.Page{
				Source: "fake", Entries: []registry.Entry{{Name: "io.example/weather"}},
				Stale: true, RetrievedAt: fetched,
			}, nil
		},
	})

	w := request(t, s, http.MethodGet, "/api/catalog", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Stale       bool      `json:"stale"`
		RetrievedAt time.Time `json:"retrieved_at"`
		Entries     []any     `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Stale {
		t.Error("stale data must say so")
	}
	if !body.RetrievedAt.Equal(fetched) {
		t.Errorf("retrieved_at = %s, want %s", body.RetrievedAt, fetched)
	}
	if len(body.Entries) != 1 {
		t.Error("stale data is still data")
	}
}

// An empty list is [] and not null. A page mapping over null renders nothing
// and reports it in a console nobody has open.
func TestCatalog_EmptyListIsAnArray(t *testing.T) {
	s := newCatalogDashboard(t, auth.RoleAdmin, CatalogAPI{
		List: func(context.Context, registry.Query) (registry.Page, error) {
			return registry.Page{Source: "fake"}, nil
		},
	})
	w := request(t, s, http.MethodGet, "/api/catalog", nil)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["entries"]) != "[]" {
		t.Errorf("entries = %s, want []", raw["entries"])
	}
}

func TestCatalogEntry_UnknownNameIsNotFound(t *testing.T) {
	s := newCatalogDashboard(t, auth.RoleAdmin, okCatalog())
	if got := request(t, s, http.MethodGet, "/api/catalog/io.example/nothing", nil).Code; got != http.StatusNotFound {
		t.Errorf("status = %d, want 404", got)
	}
}

// The query reaches the client rather than being dropped on the way.
func TestCatalog_PassesTheSearchAndCursorThrough(t *testing.T) {
	var got registry.Query
	s := newCatalogDashboard(t, auth.RoleAdmin, CatalogAPI{
		List: func(_ context.Context, q registry.Query) (registry.Page, error) {
			got = q
			return registry.Page{Source: "fake"}, nil
		},
	})
	request(t, s, http.MethodGet, "/api/catalog?q=weather&cursor=abc&limit=5", nil)
	if got.Search != "weather" || got.Cursor != "abc" || got.Limit != 5 {
		t.Errorf("query = %+v", got)
	}
}

// A host built without a catalogue says so rather than panicking on a nil
// function.
func TestCatalog_UnconfiguredIsRefusedNotFatal(t *testing.T) {
	s := newCatalogDashboard(t, auth.RoleAdmin, CatalogAPI{})
	for _, path := range []string{"/api/catalog", "/api/catalog/io.example/weather"} {
		if got := request(t, s, http.MethodGet, path, nil).Code; got != http.StatusServiceUnavailable {
			t.Errorf("%s: status = %d, want 503", path, got)
		}
	}
}
