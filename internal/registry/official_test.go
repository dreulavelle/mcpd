package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/mcpservers"
)

// --- fixtures ---------------------------------------------------------------

// remoteEntry is one registry row for a server this host can reach.
func remoteEntry(name, version, status string, latest bool, published string) string {
	return fmt.Sprintf(`{
	  "server": {
	    "$schema": %q,
	    "name": %q,
	    "title": "Weather",
	    "description": "Reads the forecast.",
	    "version": %q,
	    "remotes": [{"type": "streamable-http", "url": "https://weather.example/mcp"}]
	  },
	  "_meta": {"io.modelcontextprotocol.registry/official": {
	    "status": %q, "isLatest": %t, "publishedAt": %q, "updatedAt": %q
	  }}
	}`, mcpservers.SchemaURI, name, version, status, latest, published, published)
}

// packageEntry is a server published only as something to run locally. This
// host does not run those, which is the case the catalogue has to be honest
// about rather than offer an Add button for.
func packageEntry(name string) string {
	return fmt.Sprintf(`{
	  "server": {
	    "$schema": %q,
	    "name": %q,
	    "title": "Filesystem",
	    "description": "Reads local files.",
	    "version": "1.0.0",
	    "packages": [{"registryType": "npm", "identifier": "@example/fs", "version": "1.0.0"}]
	  },
	  "_meta": {"io.modelcontextprotocol.registry/official": {
	    "status": "active", "isLatest": true,
	    "publishedAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"
	  }}
	}`, mcpservers.SchemaURI, name)
}

func listBody(cursor string, entries ...string) string {
	return fmt.Sprintf(`{"servers":[%s],"metadata":{"nextCursor":%q}}`,
		strings.Join(entries, ","), cursor)
}

// newRegistry stands up a fake catalogue. The handler receives each request so
// a test can assert what was asked for as well as what came back.
func newRegistry(t *testing.T, handler http.HandlerFunc) *Official {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewOfficial(OfficialOptions{BaseURL: srv.URL, HTTPClient: srv.Client()})
}

// --- tests ------------------------------------------------------------------

// The registry stores every version of every server. Without deduplication a
// page shows the same server once per release, which reads as a broken list.
func TestList_KeepsOneEntryPerServer(t *testing.T) {
	r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listBody("",
			remoteEntry("io.example/weather", "1.0.0", "active", false, "2026-01-01T00:00:00Z"),
			remoteEntry("io.example/weather", "2.0.0", "active", true, "2026-03-01T00:00:00Z"),
			remoteEntry("io.example/weather", "1.5.0", "active", false, "2026-02-01T00:00:00Z"),
		)))
	})

	page, err := r.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(page.Entries), page.Entries)
	}
	if got := page.Entries[0].Version; got != "2.0.0" {
		t.Errorf("version = %q, want the one the registry marks latest", got)
	}
}

// isLatest is the registry's own answer and wins. When it is absent from every
// row -- which is what a registry that has stopped setting it looks like --
// the most recently published one is the honest second choice.
func TestList_PrefersLatestThenNewest(t *testing.T) {
	r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listBody("",
			remoteEntry("io.example/weather", "1.0.0", "active", false, "2026-01-01T00:00:00Z"),
			remoteEntry("io.example/weather", "3.0.0", "active", false, "2026-05-01T00:00:00Z"),
			remoteEntry("io.example/weather", "2.0.0", "active", false, "2026-02-01T00:00:00Z"),
		)))
	})

	page, err := r.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Version != "3.0.0" {
		t.Fatalf("entries = %+v, want only 3.0.0", page.Entries)
	}
}

// A withdrawn server is withheld rather than shown greyed out. The answer to
// "should I install the thing its author retired" is not a nuance to render.
func TestList_WithholdsEverythingButActive(t *testing.T) {
	r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listBody("",
			remoteEntry("io.example/gone", "1.0.0", "deprecated", true, "2026-01-01T00:00:00Z"),
			remoteEntry("io.example/here", "1.0.0", "active", true, "2026-01-01T00:00:00Z"),
		)))
	})

	page, err := r.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Name != "io.example/here" {
		t.Fatalf("entries = %+v, want only the active one", page.Entries)
	}
}

// The standing constraint: this host runs remote servers and nothing else. An
// entry published only as a package cannot be added, and the catalogue says so
// rather than offering a button that fails.
func TestList_APackageOnlyEntryIsNotAddable(t *testing.T) {
	r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listBody("",
			packageEntry("io.example/filesystem"),
			remoteEntry("io.example/weather", "1.0.0", "active", true, "2026-01-01T00:00:00Z"),
		)))
	})

	page, err := r.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Entry{}
	for _, e := range page.Entries {
		byName[e.Name] = e
	}

	pkg, ok := byName["io.example/filesystem"]
	if !ok {
		t.Fatal("a package-only entry should still be listed, so its absence is explained")
	}
	if pkg.Addable {
		t.Error("a package-only entry must not be offered as addable")
	}
	if pkg.Reason == "" {
		t.Error("an entry that cannot be added must say why")
	}
	if !strings.Contains(pkg.Reason, "remotes") {
		t.Errorf("reason = %q, want it to name the missing remotes", pkg.Reason)
	}
	if !byName["io.example/weather"].Addable {
		t.Error("a remote entry must be addable")
	}
}

// Addability is decided by the calls the import endpoint makes, not by looking
// for a remotes array. A document with remotes this host will not dial imports
// as a refusal, and offering it would be offering a button that fails.
//
// Both calls, and the second is the one that drifted. Import runs
// mcpremote.Fields after mcpservers.Parse, and Fields refuses documents Parse
// accepts: an input declaring choices whose default is not one of them was
// listed as addable and refused on the way in.
func TestList_AddabilityMatchesWhatImportWouldAccept(t *testing.T) {
	entryFor := func(name, remotes string) string {
		return fmt.Sprintf(`{
		  "server": {
		    "$schema": %q,
		    "name": %q,
		    "description": "A fixture.",
		    "version": "1.0.0",
		    "remotes": %s
		  },
		  "_meta": {"io.modelcontextprotocol.registry/official": {
		    "status": "active", "isLatest": true,
		    "publishedAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"
		  }}
		}`, mcpservers.SchemaURI, name, remotes)
	}

	for _, tc := range []struct {
		name    string
		docName string
		remotes string
		// why names a fragment the reason must carry, so a refusal for the
		// wrong reason does not pass as the right one.
		why string
	}{
		{
			name:    "parse refuses plaintext to a public host",
			docName: "io.example/insecure",
			remotes: `[{"type": "streamable-http", "url": "http://insecure.example/mcp"}]`,
			why:     "plaintext http",
		},
		{
			name:    "fields refuses a default that is not one of the choices",
			docName: "io.example/enum",
			remotes: `[{"type": "streamable-http", "url": "https://enum.example/mcp",
			            "headers": [{"name": "X-Mode", "default": "nope",
			                         "choices": ["a", "b"]}]}]`,
			why: "not one of its choices",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(listBody("", entryFor(tc.docName, tc.remotes))))
			})

			page, err := r.List(context.Background(), Query{})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Entries) != 1 {
				t.Fatalf("entries = %+v", page.Entries)
			}
			got := page.Entries[0]
			if got.Addable {
				t.Fatal("an entry this host would refuse at import must not be offered as addable")
			}
			if !strings.Contains(got.Reason, tc.why) {
				t.Errorf("reason = %q, want it to mention %q", got.Reason, tc.why)
			}
		})
	}
}

// The query the registry is asked is the one that already deduplicates. The
// deduplication above is what happens when that promise is not kept.
func TestList_AsksForLatestOnly(t *testing.T) {
	var got url.Values
	r := newRegistry(t, func(w http.ResponseWriter, req *http.Request) {
		got = req.URL.Query()
		_, _ = w.Write([]byte(listBody("")))
	})

	if _, err := r.List(context.Background(), Query{Search: "weather", Cursor: "c1", Limit: 7}); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"version": "latest", "search": "weather", "cursor": "c1", "limit": "7",
	} {
		if got.Get(key) != want {
			t.Errorf("%s = %q, want %q (query was %v)", key, got.Get(key), want, got)
		}
	}
}

// Registry text is a third party's, arriving in whatever quantity they choose.
// A name carrying a newline is a log line broken in two and a row rendered as
// something it is not.
func TestList_BoundsUntrustedText(t *testing.T) {
	long := strings.Repeat("x", 5000)
	entry := fmt.Sprintf(`{
	  "server": {
	    "$schema": %q,
	    "name": "io.example/loud",
	    "title": %q,
	    "description": %q,
	    "version": "1.0.0",
	    "remotes": [{"type": "streamable-http", "url": "https://loud.example/mcp"}]
	  },
	  "_meta": {"io.modelcontextprotocol.registry/official": {
	    "status": "active", "isLatest": true,
	    "publishedAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"
	  }}
	}`, mcpservers.SchemaURI, long, long)

	r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listBody("", entry)))
	})

	page, err := r.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %+v", page.Entries)
	}
	e := page.Entries[0]
	if n := len([]rune(e.Title)); n > maxTitleRunes+1 {
		t.Errorf("title is %d runes, want it bounded to %d", n, maxTitleRunes)
	}
	if n := len([]rune(e.Description)); n > maxDescriptionRunes+1 {
		t.Errorf("description is %d runes, want it bounded to %d", n, maxDescriptionRunes)
	}
}

// A page far larger than any honest one is a memory limit set by somebody
// else. It is refused rather than read.
func TestList_RefusesAnOversizedResponse(t *testing.T) {
	r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"servers":[`))
		for range 40 {
			_, _ = w.Write([]byte(strings.Repeat(" ", 64<<10)))
		}
		_, _ = w.Write([]byte(`],"metadata":{}}`))
	})

	if _, err := r.List(context.Background(), Query{}); err == nil {
		t.Fatal("an oversized page must be refused, not read")
	}
}

// The entry endpoint hands back the document itself, which is what the import
// form is prefilled from.
func TestGet_ReturnsTheDocument(t *testing.T) {
	r := newRegistry(t, func(w http.ResponseWriter, req *http.Request) {
		if want := "/v0/servers/io.example%2Fweather/versions/latest"; req.URL.EscapedPath() != want {
			t.Errorf("path = %q, want %q", req.URL.EscapedPath(), want)
		}
		_, _ = w.Write([]byte(remoteEntry("io.example/weather", "2.0.0", "active", true, "2026-03-01T00:00:00Z")))
	})

	detail, err := r.Get(context.Background(), "io.example/weather")
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Addable {
		t.Error("a remote entry must be addable")
	}
	// The document has to be exactly what the import endpoint accepts, which
	// is the whole point of not writing a second import path.
	if _, err := mcpservers.Parse(detail.Document); err != nil {
		t.Fatalf("the document handed to the import form does not import: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(detail.Document, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["name"] != "io.example/weather" {
		t.Errorf("document names %v", doc["name"])
	}
}

// A withdrawn or unknown name is an answer rather than a failure, and the
// difference decides whether the cache may serve something stale for it.
func TestGet_UnknownNameIsNotFound(t *testing.T) {
	r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := r.Get(context.Background(), "io.example/nothing"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGet_WithholdsANonActiveEntry(t *testing.T) {
	r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(remoteEntry("io.example/gone", "1.0.0", "deleted", true, "2026-01-01T00:00:00Z")))
	})
	if _, err := r.Get(context.Background(), "io.example/gone"); err == nil {
		t.Fatal("a withdrawn entry must not be handed to the import form")
	}
}

// A catalogue that suddenly wants to send this host somewhere else is a change
// worth refusing rather than following.
func TestFetch_RefusesARedirect(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listBody("")))
	}))
	t.Cleanup(elsewhere.Close)

	r := newRegistry(t, func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, elsewhere.URL+"/v0/servers", http.StatusFound)
	})
	// The fake registry's own client follows redirects; the refusal is the
	// client this package builds, so it is used here rather than the fixture's.
	r = NewOfficial(OfficialOptions{BaseURL: r.base})
	if _, err := r.List(context.Background(), Query{}); err == nil {
		t.Fatal("a redirect off the catalogue must be refused")
	}
}

func TestSuggestName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"io.github.example/weather", "weather"},
		{"ac.inference.sh/mcp", "mcp"},
		{"io.example/My_Cool.Server", "my-cool-server"},
		{"io.example/3d-tools", "s-3d-tools"},
		{"io.example/a", "a-server"},
		{"io.example/" + strings.Repeat("long", 20), strings.Repeat("long", 8)},
		{"no-slash-at-all", "no-slash-at-all"},
		{"io.example/!!!", "server"},
	} {
		if got := SuggestName(tc.in); got != tc.want {
			t.Errorf("SuggestName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Roughly a fifth of the remote servers in the official registry were
// published against an earlier server.json format. The pin is not negotiable,
// so what these get is a reason a person can act on -- ask the publisher to
// republish -- rather than two full URLs in a list.
func TestList_AnOlderSchemaSaysSoReadably(t *testing.T) {
	old := `{
	  "server": {
	    "$schema": "https://static.modelcontextprotocol.io/schemas/2025-09-29/server.schema.json",
	    "name": "io.example/older",
	    "description": "Published a while ago.",
	    "version": "1.0.0",
	    "remotes": [{"type": "streamable-http", "url": "https://older.example/mcp"}]
	  },
	  "_meta": {"io.modelcontextprotocol.registry/official": {
	    "status": "active", "isLatest": true,
	    "publishedAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"
	  }}
	}`
	r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listBody("", old)))
	})

	page, err := r.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Addable {
		t.Fatalf("entries = %+v, want one that is not addable", page.Entries)
	}
	reason := page.Entries[0].Reason
	for _, want := range []string{"2025-09-29", "2025-12-11"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason = %q, want it to name %s", reason, want)
		}
	}
	if strings.Contains(reason, "https://") {
		t.Errorf("reason = %q, want the dates rather than two full URLs", reason)
	}
}

// A cursor is the catalogue's, and means nothing here. It survives unchanged
// or it is dropped -- cleaning it the way prose is cleaned would mangle a
// working cursor into a broken one, and the failure would look like pagination
// that stops halfway for no reason.
func TestList_CursorsAreOpaque(t *testing.T) {
	const odd = "io.example/weather:1.0.0+build.2 (rc)"
	var sent string
	r := newRegistry(t, func(w http.ResponseWriter, req *http.Request) {
		sent = req.URL.Query().Get("cursor")
		_, _ = w.Write([]byte(listBody(odd)))
	})

	page, err := r.List(context.Background(), Query{Cursor: odd})
	if err != nil {
		t.Fatal(err)
	}
	if sent != odd {
		t.Errorf("the cursor was rewritten on the way out: %q", sent)
	}
	if page.NextCursor != odd {
		t.Errorf("the cursor was rewritten on the way back: %q", page.NextCursor)
	}

	// One carrying a control character is dropped rather than mangled, which
	// ends the listing instead of pretending to continue it.
	r2 := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listBody("bad\ncursor")))
	})
	page, err = r2.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != "" {
		t.Errorf("next_cursor = %q, want it dropped", page.NextCursor)
	}
}

// Name is the identifier the dashboard sends back to the entry route, so it is
// the one field that must not be cleaned. A truncated or rewritten name is a
// row that 404s when somebody clicks it, and a row that is absent is better
// than one that is dead.
func TestList_ARowWhoseNameCannotSurviveIsDropped(t *testing.T) {
	entryFor := func(name string) string {
		return fmt.Sprintf(`{
		  "server": {
		    "$schema": %q, "name": %s, "title": "Loud",
		    "description": "A fixture.", "version": "1.0.0",
		    "remotes": [{"type": "streamable-http", "url": "https://loud.example/mcp"}]
		  },
		  "_meta": {"io.modelcontextprotocol.registry/official": {
		    "status": "active", "isLatest": true,
		    "publishedAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"
		  }}
		}`, mcpservers.SchemaURI, name)
	}
	overlong, err := json.Marshal("io.example/" + strings.Repeat("n", maxNameRunes))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, raw string }{
		{"a newline in the name", `"io.example/loud\ntitle: something else"`},
		{"a bidirectional override", `"io.example/lo‮ud"`},
		{"past the length bound", string(overlong)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(listBody("", entryFor(tc.raw))))
			})
			page, err := r.List(context.Background(), Query{})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Entries) != 0 {
				t.Errorf("entries = %+v, want the unusable row dropped", page.Entries)
			}
		})
	}

	// And a name that is merely long, but within the bound, is kept whole --
	// dropping those would empty the catalogue rather than protect it.
	ordinary := "io.example/" + strings.Repeat("n", 40)
	r := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(listBody("", entryFor(`"`+ordinary+`"`))))
	})
	page, err := r.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Name != ordinary {
		t.Errorf("entries = %+v, want the name kept whole", page.Entries)
	}
}
