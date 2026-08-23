package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/registry"
)

// TestCatalogEntry_FlowsIntoTheOrdinaryImportPath is the check that the
// catalogue is a way to find a document rather than a second way to install
// one. Picking an entry must end in exactly the state pasting that same
// server.json by hand ends in -- same row, same derived settings, same tools
// -- because there is one import path and the catalogue hands it bytes.
func TestCatalogEntry_FlowsIntoTheOrdinaryImportPath(t *testing.T) {
	rs := newRemote(t, map[string]string{"getWeather": "Reads the forecast."})
	pasted := rs.document()

	// A catalogue serving that same document, wrapped the way the official
	// registry wraps one.
	catalogue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `{"server":%s,"_meta":{"io.modelcontextprotocol.registry/official":{
			"status":"active","isLatest":true,
			"publishedAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}}`, pasted)
	}))
	t.Cleanup(catalogue.Close)

	client := registry.NewOfficial(registry.OfficialOptions{
		BaseURL: catalogue.URL, HTTPClient: catalogue.Client(),
	})
	detail, err := client.Get(context.Background(), "io.example/fixture")
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	if !detail.Addable {
		t.Fatalf("the fixture must be addable; reason: %s", detail.Reason)
	}

	// Two hosts over two databases, so neither can see the other's state: one
	// imports the catalogue's bytes, the other the operator's paste.
	fromCatalogue := newAppIn(t, t.TempDir())
	byHand := newAppIn(t, t.TempDir())

	mustImport(t, fromCatalogue, "weather", detail.Document)
	mustImport(t, byHand, "weather", pasted)

	// Same lifecycle from there. Nothing about the catalogue changes what
	// discovery finds or what an administrator has to approve.
	mustDiscover(t, fromCatalogue, "weather")
	mustDiscover(t, byHand, "weather")
	mustEnable(t, fromCatalogue, "weather", "getWeather")
	mustEnable(t, byHand, "weather", "getWeather")

	if got, want := mountedTools(fromCatalogue, "weather"), mountedTools(byHand, "weather"); !reflect.DeepEqual(got, want) {
		t.Errorf("tools from the catalogue = %v, by hand = %v", got, want)
	}

	// The stored document is the one that was handed over, verbatim. If these
	// diverged, the catalogue would be re-encoding somebody else's document
	// on the way through, which is how a field this build does not model gets
	// silently dropped.
	srv, ok := fromCatalogue.mcpServer("weather")
	if !ok {
		t.Fatal("the imported server is not there")
	}
	var stored, offered map[string]any
	if err := json.Unmarshal(srv.Document, &stored); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(pasted, &offered); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, offered) {
		t.Errorf("stored document = %v, want the catalogue's %v", stored, offered)
	}

	// And the settings the two derive are the same, since they are derived
	// from the same document by the same code.
	catFields, err := fromCatalogue.mcpFields(srv)
	if err != nil {
		t.Fatal(err)
	}
	handSrv, _ := byHand.mcpServer("weather")
	handFields, err := byHand.mcpFields(handSrv)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(catFields, handFields) {
		t.Errorf("derived settings differ: %v vs %v", catFields, handFields)
	}
}

// A refusal from the import path is the same refusal whether the document was
// pasted or picked. The catalogue does not get to relax anything.
func TestCatalogEntry_RefusalsAreTheImportPath(t *testing.T) {
	a := newAppIn(t, t.TempDir())
	// A document with no remotes: this host does not run packaged servers.
	doc := fmt.Appendf(nil, `{
		"$schema": %q,
		"name": "io.example/filesystem",
		"description": "Reads local files.",
		"version": "1.0.0",
		"packages": [{"registryType":"npm","identifier":"@example/fs","version":"1.0.0"}]
	}`, mcpservers.SchemaURI)

	if err := a.ImportMCPServer(context.Background(), "tester", "files", doc); err == nil {
		t.Fatal("a package-only document must be refused at import")
	}
}

// SuggestedName is offered to prefill the import form, so it has to be a name
// that form accepts. The two rules live in different packages and would
// otherwise drift apart in silence.
func TestSuggestedNamesAreLegalInstanceNames(t *testing.T) {
	for _, name := range []string{
		"io.github.example/weather",
		"ac.inference.sh/mcp",
		"io.example/My_Cool.Server",
		"io.example/3d-tools",
		"io.example/a",
		"io.example/!!!",
		"com.example/a-very-long-server-name-that-goes-on-and-on-forever",
		"no-slash-at-all",
	} {
		suggested := registry.SuggestName(name)
		if !instanceNamePattern.MatchString(suggested) {
			t.Errorf("SuggestName(%q) = %q, which the import form refuses", name, suggested)
		}
	}
}
