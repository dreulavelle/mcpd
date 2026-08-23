package registry

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestSafeIconURL.
//
// An icon is the one field here a browser acts on rather than renders: it goes
// into an <img src> on an administrator's page, and it is a URL a third party
// wrote. So the rule is an allow-list, and anything not plainly an ordinary
// https address is omitted -- a missing picture costs a placeholder, and the
// alternatives cost more than a picture is worth.
func TestSafeIconURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"an ordinary https url", "https://avatars.example/u/1?s=200", "https://avatars.example/u/1?s=200"},
		{"trimmed", "  https://icons.example/a.png  ", "https://icons.example/a.png"},
		{"an svg is still just a url here", "https://astro.build/favicon.svg", "https://astro.build/favicon.svg"},

		{"empty", "", ""},
		{"http is mixed content from a dashboard served over TLS", "http://icons.example/a.png", ""},
		{"a data uri would put a third party's bytes in this page's origin",
			"data:image/svg+xml;base64,PHN2Zz48c2NyaXB0Lz48L3N2Zz4=", ""},
		{"javascript", "javascript:alert(1)", ""},
		{"a protocol-relative url has no scheme to check", "//icons.example/a.png", ""},
		{"a relative path is not an address", "/icons/a.png", ""},
		{"opaque, so there is no host", "https:icons.example", ""},
		{"credentials in an image url are a mistake or a trick", "https://user:pw@icons.example/a.png", ""},
		{"no host", "https:///a.png", ""},
		{"a newline could be rendered as something else", "https://icons.example/a\n.png", ""},
		{"not a url at all", "https://icons.example/%zz", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeIconURL(tc.in); got != tc.want {
				t.Errorf("safeIconURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// Bounded, like every other field a catalogue supplies.
	long := "https://icons.example/" + strings.Repeat("a", maxIconURLRunes)
	if got := safeIconURL(long); got != "" {
		t.Errorf("a %d-rune url survived: %q", len([]rune(long)), got[:40])
	}
}

// TestDockerList_SurfacesTheCatalogueIcon, and refuses the ones it should.
func TestDockerList_SurfacesTheCatalogueIcon(t *testing.T) {
	page, err := serveDockerCatalog(t).List(context.Background(), Query{Limit: MaxEntriesPerPage})
	if err != nil {
		t.Fatal(err)
	}
	icons := 0
	for _, e := range page.Entries {
		if e.Icon == "" {
			continue
		}
		icons++
		if !strings.HasPrefix(e.Icon, "https://") {
			t.Errorf("%q has icon %q, which is not an https address", e.Name, e.Icon)
		}
	}
	if icons == 0 {
		t.Fatal("no entry carried an icon, although Docker's catalogue publishes them")
	}
}

// TestSmitheryList_SurfacesTheIcon. Smithery serves one for most rows and null
// for the rest, and a null is an absent field rather than an empty picture.
func TestSmitheryList_SurfacesTheIcon(t *testing.T) {
	page, err := serveSmithery(t).List(context.Background(), Query{Limit: MaxEntriesPerPage})
	if err != nil {
		t.Fatal(err)
	}
	withIcon, without := 0, 0
	for _, e := range page.Entries {
		if e.Icon == "" {
			without++
			continue
		}
		withIcon++
		if !strings.HasPrefix(e.Icon, "https://") {
			t.Errorf("%q has icon %q", e.Name, e.Icon)
		}
	}
	if withIcon == 0 {
		t.Error("no entry carried an icon, although the fixture has several")
	}
	if without == 0 {
		t.Error("every entry carried one; the fixture has a null and it should stay absent")
	}
}

// TestOfficialList_ReadsTheDocumentsOwnIcons.
//
// server.json has carried an `icons` array since the 2025-10-17 format. The
// first usable address is taken; a refused one is skipped rather than ending
// the search, so one bad entry does not cost the good one after it.
func TestOfficialList_ReadsTheDocumentsOwnIcons(t *testing.T) {
	withIcons := func(name, icons string) string {
		return `{
		  "server": {
		    "$schema": "https://static.modelcontextprotocol.io/schemas/2025-10-17/server.schema.json",
		    "name": "` + name + `",
		    "description": "Anything.",
		    "version": "1.0.0",
		    "icons": ` + icons + `,
		    "remotes": [{"type": "streamable-http", "url": "https://` + strings.ReplaceAll(name, "/", ".") + `/mcp"}]
		  },
		  "_meta": {"io.modelcontextprotocol.registry/official": {
		    "status": "active", "isLatest": true,
		    "publishedAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"
		  }}
		}`
	}
	body := listBody("",
		withIcons("io.example/one", `[{"src": "https://icons.example/one.png", "mimeType": "image/png"}]`),
		withIcons("io.example/two", `[{"src": "http://icons.example/two.png"}, {"src": "https://icons.example/two.png"}]`),
		withIcons("io.example/three", `[{"src": "javascript:alert(1)"}]`),
		withIcons("io.example/four", `[]`),
	)
	registry := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	page, err := registry.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"io.example/one": "https://icons.example/one.png",
		// The http one is skipped and the https one after it is taken.
		"io.example/two":   "https://icons.example/two.png",
		"io.example/three": "",
		"io.example/four":  "",
	}
	for _, e := range page.Entries {
		if got := e.Icon; got != want[e.Name] {
			t.Errorf("%s icon = %q, want %q", e.Name, got, want[e.Name])
		}
	}
}
