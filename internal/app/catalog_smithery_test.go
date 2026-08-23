package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/registry"
	"github.com/spoked/mcpd/internal/settings"
)

// smitheryRegistryServing answers Smithery's listing and entry routes from the
// vendored fixtures.
//
// The bytes are Smithery's own. Writing them from a reading of the brief would
// have tested this host against a shape nobody serves, which is the mistake
// the Docker fixture's own comment records.
func smitheryRegistryServing(t *testing.T) *httptest.Server {
	t.Helper()
	read := func(name string) []byte {
		raw, err := os.ReadFile(filepath.Join("..", "registry", "testdata", "smithery", name))
		if err != nil {
			t.Fatalf("read Smithery's fixture: %v", err)
		}
		return raw
	}
	page1, beyond, detail := read("list-page1.json"), read("list-page-beyond-cap.json"), read("detail-brave.json")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/servers/brave":
			_, _ = w.Write(detail)
		case r.URL.Path == "/servers" && r.URL.Query().Get("page") == "1":
			_, _ = w.Write(page1)
		case r.URL.Path == "/servers":
			_, _ = w.Write(beyond)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// smitheryGatewayServing stands in for server.smithery.ai: an MCP server that
// answers only when the Smithery key is presented, which is what the real
// gateway does -- 401 invalid_token without an Authorization header.
func smitheryGatewayServing(t *testing.T, key string, tools map[string]string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	upstream := newRemote(t, tools)
	seen := &atomic.Value{}
	seen.Store("")

	target, err := url.Parse(upstream.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		seen.Store(auth)
		if auth != "Bearer "+key {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(
				`{"error":"invalid_token","error_description":"Missing Authorization header"}`))
			return
		}
		// Past the gate it is an ordinary MCP server, which is the shape the
		// real gateway presents once the key is accepted.
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(gateway.Close)
	return gateway, seen
}

// TestSmitheryCatalogEntry_FlowsIntoTheOrdinaryImportPath.
//
// The claim the whole source rests on, end to end and through nothing special.
// A Smithery row is translated into a server.json, and that document goes to
// the same import endpoint a paste goes to, is validated by the same parser,
// derives its settings the same way, discovers the same way, and mounts behind
// the same per-tool approval.
//
// The credential is the part worth walking all the way through. The composed
// document names the key and never holds one; importing it produces an
// encrypted settings field; filling that field in is what makes discovery
// succeed; and the value reaches the far end as the Authorization header
// Smithery's gateway demands. If any link in that chain were missing, the
// symptom would be an Add button that produces a server answering 401.
func TestSmitheryCatalogEntry_FlowsIntoTheOrdinaryImportPath(t *testing.T) {
	const key = "sk-smithery-fixture-key"
	gateway, seen := smitheryGatewayServing(t, key,
		map[string]string{"brave_web_search": "Searches the web."})
	catalogue := smitheryRegistryServing(t)

	client := registry.NewSmithery(registry.SmitheryOptions{
		BaseURL:    catalogue.URL,
		GatewayURL: gateway.URL,
		HTTPClient: catalogue.Client(),
	})
	detail, err := client.Get(context.Background(), "brave")
	if err != nil {
		t.Fatalf("catalogue: %v", err)
	}
	if !detail.Addable {
		t.Fatalf("the entry must be addable; reason: %s", detail.Reason)
	}
	if detail.Source != "registry.smithery.ai" {
		t.Errorf("source = %q, want the entry to say where it came from", detail.Source)
	}
	if detail.Auth != registry.AuthAPIKey {
		t.Errorf("auth = %q, want the row to say a key is needed before anybody clicks Add", detail.Auth)
	}

	// The document must not carry the key. It is stored verbatim and hashed,
	// so a credential written into one would be a credential at rest in a
	// document rather than in the encrypted settings store.
	if strings.Contains(string(detail.Document), key) {
		t.Fatalf("the composed document carries a credential:\n%s", detail.Document)
	}

	host := newAppIn(t, t.TempDir())
	mustImport(t, host, "brave", detail.Document)

	// Importing derived a settings field for the key, and derived it as a
	// secret -- which is what makes it encrypted at rest and withheld on read.
	srv, ok := host.mcpServer("brave")
	if !ok {
		t.Fatal("the imported server is not there")
	}
	fields, err := host.mcpFields(srv)
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	var secretKey string
	for _, f := range fields {
		if f.Kind == settings.KindSecret {
			secretKey = f.Key
		}
	}
	if secretKey == "" {
		t.Fatalf("no secret field; the Smithery key has nowhere encrypted to live. fields: %+v", fields)
	}
	if secretKey != smitheryKeySettingName {
		t.Errorf("secret field = %q, want %q", secretKey, smitheryKeySettingName)
	}

	// Stored verbatim as composed. The import path hashes what it stores, so a
	// document re-encoded on the way through would be a different document.
	if string(srv.Document) != string(detail.Document) {
		t.Errorf("stored = %s\nwant   = %s", srv.Document, detail.Document)
	}

	// Before the key is set, discovery fails -- and it fails here rather than
	// at the far end. The header declares its variable required, so the host
	// refuses to resolve the request at all instead of dialling Smithery
	// without a credential and reading back a 401. That is the better of the
	// two: an unauthenticated request to a third party is one this host had no
	// reason to make, and "nothing is configured for SMITHERY_API_KEY" names
	// the field to fill in where "401 invalid_token" would not.
	_, err = host.DiscoverMCPServer(context.Background(), "tester", "brave")
	if err == nil {
		t.Fatal("discovery succeeded with no credential")
	}
	if !strings.Contains(err.Error(), smitheryKeyVariable) {
		t.Errorf("err = %v, want it to name the unset field", err)
	}
	if got, _ := seen.Load().(string); got != "" {
		t.Errorf("the gateway was dialled anyway, with %q", got)
	}

	// The operator types the key into the dashboard. Every other credential in
	// this host arrives the same way.
	if err := host.settings.Apply(context.Background(), "tester", []settings.Change{{
		Key: settings.PluginSettingKey("brave", secretKey), Value: key,
	}}); err != nil {
		t.Fatalf("set the key: %v", err)
	}

	mustDiscover(t, host, "brave")
	mustEnable(t, host, "brave", "brave_web_search")

	if got := mountedTools(host, "brave"); len(got) != 1 || got[0] != "brave_brave_web_search" {
		t.Errorf("tools = %v, want the one that was approved", got)
	}
	if got, _ := seen.Load().(string); got != "Bearer "+key {
		t.Errorf("the gateway saw %q, want the operator's key as a bearer token", got)
	}
	if srv.SchemaVersion != mcpservers.SchemaURI {
		t.Errorf("schema_version = %q, want the format the composed document declares",
			srv.SchemaVersion)
	}
}

// The composed document's SMITHERY_API_KEY variable, in the two spellings it
// is seen under. Written out rather than derived, so that a change to how a
// variable name becomes a field key is caught here rather than shrugged at:
// mcpservers.SettingKey prefixes by role, because a variable and a header may
// share a name and mean different things.
const (
	smitheryKeySettingName = "var_smithery_api_key"
	smitheryKeyVariable    = "SMITHERY_API_KEY"
)

// TestSmitheryCatalogEntry_TheWrongKeyIsRefusedByTheGateway.
//
// The other half of the credential story. The test above shows the right key
// reaching the far end; this shows that reaching it is what the far end
// actually checks, so "the gateway saw the key" is a claim about
// authentication rather than about a header that happened to be copied.
func TestSmitheryCatalogEntry_TheWrongKeyIsRefusedByTheGateway(t *testing.T) {
	gateway, seen := smitheryGatewayServing(t, "sk-the-right-key",
		map[string]string{"brave_web_search": "Searches the web."})
	catalogue := smitheryRegistryServing(t)

	client := registry.NewSmithery(registry.SmitheryOptions{
		BaseURL: catalogue.URL, GatewayURL: gateway.URL, HTTPClient: catalogue.Client(),
	})
	detail, err := client.Get(context.Background(), "brave")
	if err != nil {
		t.Fatal(err)
	}

	host := newAppIn(t, t.TempDir())
	mustImport(t, host, "brave", detail.Document)
	if err := host.settings.Apply(context.Background(), "tester", []settings.Change{{
		Key: settings.PluginSettingKey("brave", smitheryKeySettingName), Value: "sk-the-wrong-key",
	}}); err != nil {
		t.Fatalf("set the key: %v", err)
	}

	if _, err := host.DiscoverMCPServer(context.Background(), "tester", "brave"); err == nil {
		t.Fatal("discovery succeeded with the wrong key")
	}
	if got, _ := seen.Load().(string); got != "Bearer sk-the-wrong-key" {
		t.Errorf("the gateway saw %q, want the configured value carried as a bearer token", got)
	}
}

// TestSmitheryCatalogEntry_ARowSmitheryDoesNotHostIsListedAndNotOffered.
//
// This host does not run packaged servers. Those are shown with the reason
// rather than filtered out -- "why is the thing I came for not here" is a worse
// question than a row that answers it -- and nothing hands the import path a
// document for one.
func TestSmitheryCatalogEntry_ARowSmitheryDoesNotHostIsListedAndNotOffered(t *testing.T) {
	catalogue := smitheryRegistryServing(t)
	client := registry.NewSmithery(registry.SmitheryOptions{
		BaseURL:    catalogue.URL,
		HTTPClient: catalogue.Client(),
	})

	page, err := client.List(context.Background(), registry.Query{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range page.Entries {
		if e.Name != "gautamgb/mcpindex" {
			continue
		}
		found = true
		if e.Addable {
			t.Error("a server Smithery does not host was offered as addable")
		}
		if !strings.Contains(e.Reason, "does not run packaged servers") {
			t.Errorf("reason = %q, want it to say why", e.Reason)
		}
	}
	if !found {
		t.Error("the row was filtered out instead of explained")
	}

	// And the page says its listing is bounded, so five hundred rows are not
	// mistaken for ten thousand servers.
	if len(page.Sources) != 1 || page.Sources[0].Note == "" {
		t.Errorf("sources = %+v, want the browse to say it is truncated", page.Sources)
	}
}
