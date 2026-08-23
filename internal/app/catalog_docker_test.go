package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/registry"
)

// dockerCatalogueServing serves Docker's real catalogue format with one
// entry's address pointed at a server this test can answer for.
//
// The bytes are Docker's own, copied from their published catalogue -- only
// the URL moves, because a test cannot dial mcp.docs.astro.build. Writing the
// document from a reading of their spec would have tested this host against
// the wrong shape: the entry spec in docker/mcp-gateway and the built
// catalogue have materially diverged.
func dockerCatalogueServing(t *testing.T, endpoint string) *httptest.Server {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(
		"..", "registry", "testdata", "docker", "catalog-excerpt.yaml"))
	if err != nil {
		t.Fatalf("read Docker's catalogue fixture: %v", err)
	}
	body := strings.Replace(string(raw), "https://mcp.docs.astro.build/mcp", endpoint, 1)
	if body == string(raw) {
		t.Fatal("the fixture no longer carries the address this test replaces")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// TestDockerCatalogEntry_FlowsIntoTheOrdinaryImportPath.
//
// Docker's format is not server.json, so an entry is translated into one.
// What must not happen is a second import route: the composed document goes to
// the same endpoint a paste goes to, is validated by the same parser, and ends
// in the same row with the same discovery and the same per-tool approval.
func TestDockerCatalogEntry_FlowsIntoTheOrdinaryImportPath(t *testing.T) {
	rs := newRemote(t, map[string]string{"searchAstroDocs": "Reads the Astro docs."})
	catalogue := dockerCatalogueServing(t, rs.server.URL)

	client := registry.NewDocker(registry.DockerOptions{
		CatalogURL: catalogue.URL, HTTPClient: catalogue.Client(),
	})
	detail, err := client.Get(context.Background(), "astro-docs")
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	if !detail.Addable {
		t.Fatalf("the entry must be addable; reason: %s", detail.Reason)
	}
	if detail.Source != "docker/mcp-registry" {
		t.Errorf("source = %q, want the entry to say where it came from", detail.Source)
	}

	host := newAppIn(t, t.TempDir())
	mustImport(t, host, "astro", detail.Document)
	mustDiscover(t, host, "astro")
	mustEnable(t, host, "astro", "searchAstroDocs")

	// Prefixed with the instance name, the way every mounted tool is.
	if got := mountedTools(host, "astro"); len(got) != 1 || got[0] != "astro_searchAstroDocs" {
		t.Errorf("tools = %v, want the one that was approved", got)
	}

	// Stored verbatim as composed. The import path hashes what it stores and
	// every state change after that carries the hash, so a document that were
	// re-encoded on the way through would be a different document.
	srv, ok := host.mcpServer("astro")
	if !ok {
		t.Fatal("the imported server is not there")
	}
	if string(srv.Document) != string(detail.Document) {
		t.Errorf("stored = %s\nwant   = %s", srv.Document, detail.Document)
	}
	if srv.SchemaVersion != mcpservers.SchemaURI {
		t.Errorf("schema_version = %q, want the format the composed document declares",
			srv.SchemaVersion)
	}
}

// TestDockerCatalogEntry_ALocalContainerIsListedAndNotOffered.
//
// Three quarters of Docker's catalogue is something to run locally. Those are
// shown with the reason rather than filtered out -- "why is the thing I came
// for not here" is a worse question than a row that answers it -- but the
// import path must refuse them if one ever reached it.
func TestDockerCatalogEntry_ALocalContainerIsListedAndNotOffered(t *testing.T) {
	rs := newRemote(t, map[string]string{"noop": "Nothing."})
	catalogue := dockerCatalogueServing(t, rs.server.URL)
	client := registry.NewDocker(registry.DockerOptions{
		CatalogURL: catalogue.URL, HTTPClient: catalogue.Client(),
	})

	detail, err := client.Get(context.Background(), "SQLite")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Addable {
		t.Fatal("a container Docker runs locally was offered as addable")
	}
	if len(detail.Document) != 0 {
		t.Error("a document was handed over for an entry that cannot be imported")
	}
	if !strings.Contains(detail.Reason, "does not run packaged servers") {
		t.Errorf("reason = %q, want it to say why", detail.Reason)
	}
}

// TestCatalog_EverySourceOffLeavesNoCatalogue.
//
// A deployment with no route to the internet should get an endpoint that says
// there is no catalogue, not one that reports a third party as unreachable
// every time somebody opens the page.
func TestCatalog_EverySourceOffLeavesNoCatalogue(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if catalog := buildCatalog(config.Catalog{}, nil, log); catalog != nil {
		t.Fatalf("catalog = %+v, want none when every source is off", catalog)
	}
	if api := catalogAPI(nil); api.List != nil || api.Get != nil || api.Source != nil {
		t.Errorf("api = %+v, want the handler to see no catalogue at all", api)
	}

	// And one source on is one source, not four.
	only := buildCatalog(config.Catalog{Official: true}, nil, log)
	if got := only.Sources(); len(got) != 1 || got[0] != "registry.modelcontextprotocol.io" {
		t.Errorf("sources = %v, want just the official registry", got)
	}
	t.Cleanup(func() { _ = only.Close() })

	both := buildCatalog(config.Catalog{Official: true, Docker: true}, nil, log)
	t.Cleanup(func() { _ = both.Close() })
	want := []string{"registry.modelcontextprotocol.io", "docker/mcp-registry"}
	if got := both.Sources(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sources = %v, want %v in preference order", got, want)
	}
}

// TestCatalog_PreferenceOrderIsByDistanceFromThePublisher.
//
// The order is not alphabetical and not the order the sources were written.
// It is how far an entry is from the party that operates the server: the
// official registry is where a publisher registers their own, PulseMCP passes
// that same document through, Docker's entry is a document this host composed
// from a third party's description, and a Smithery entry describes Smithery's
// proxy in front of the server rather than the server. Deduplication keeps the
// first claim on an identity, so this order is the whole of the tie-break rule
// and a reshuffle here silently changes which copy of a server an operator
// imports.
func TestCatalog_PreferenceOrderIsByDistanceFromThePublisher(t *testing.T) {
	t.Setenv("MCPD_TEST_PULSEMCP_KEY", "a-key")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	all := buildCatalog(config.Catalog{
		Official: true, Docker: true, Smithery: true, PulseMCP: true,
		PulseMCPAPIKeyRef: "env:MCPD_TEST_PULSEMCP_KEY",
		PulseMCPTenant:    "a-tenant",
	}, nil, log)
	if all == nil {
		t.Fatal("catalog = nil, want four sources")
	}
	t.Cleanup(func() { _ = all.Close() })

	want := []string{
		"registry.modelcontextprotocol.io",
		"api.pulsemcp.com",
		"docker/mcp-registry",
		"registry.smithery.ai",
	}
	if got := all.Sources(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sources = %v, want %v in preference order", got, want)
	}
}

// TestCatalog_PulseMCPWithAnUnreadableKeyIsLeftOut.
//
// It is the one source that cannot answer without a credential, so a reference
// that will not resolve leaves it out rather than mounting a catalogue that
// would 401 every page. The others are unaffected -- one source's problem is
// one source's.
func TestCatalog_PulseMCPWithAnUnreadableKeyIsLeftOut(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	catalog := buildCatalog(config.Catalog{
		Official:          true,
		PulseMCP:          true,
		PulseMCPAPIKeyRef: "env:MCPD_TEST_A_VARIABLE_THAT_IS_NOT_SET",
		PulseMCPTenant:    "a-tenant",
	}, nil, log)
	if catalog == nil {
		t.Fatal("catalog = nil, want the official registry to survive")
	}
	t.Cleanup(func() { _ = catalog.Close() })

	if got := catalog.Sources(); len(got) != 1 || got[0] != "registry.modelcontextprotocol.io" {
		t.Errorf("sources = %v, want PulseMCP left out and the rest kept", got)
	}

	// And a deployment whose only source drops out gets "no catalogue", not a
	// catalogue that fails every page.
	alone := buildCatalog(config.Catalog{
		PulseMCP:          true,
		PulseMCPAPIKeyRef: "env:MCPD_TEST_A_VARIABLE_THAT_IS_NOT_SET",
		PulseMCPTenant:    "a-tenant",
	}, nil, log)
	if alone != nil {
		t.Errorf("catalog = %+v, want none when the only source could not be built", alone)
	}
}
