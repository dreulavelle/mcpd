package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/mcpservers"
)

// newDocker serves the vendored excerpt of Docker's real catalogue.
//
// The fixture is Docker's own bytes, not a shape written from a reading of
// their spec: the two have drifted -- the entry spec in docker/mcp-gateway
// describes a config array and a metadata block that the built catalogue does
// not use -- and a fixture written from the spec would have tested this
// parser against the wrong document.
func newDocker(t *testing.T, handler http.HandlerFunc) *Docker {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewDocker(DockerOptions{
		CatalogURL: server.URL + "/catalog.yaml",
		HTTPClient: server.Client(),
		Limit:      MaxEntriesPerPage,
	})
}

func dockerFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "docker", "catalog-excerpt.yaml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

func serveDockerCatalog(t *testing.T) *Docker {
	t.Helper()
	body := dockerFixture(t)
	return newDocker(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(body)
	})
}

func entryNamed(page Page, name string) (Entry, bool) {
	for _, e := range page.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// TestDocker_OnlyRemoteEntriesAreAddable is the rule the whole source exists
// under: Docker's catalogue is mostly containers, and this host does not run
// containers.
//
// A `server` or `poci` entry is still listed. Withholding it would leave an
// operator wondering why the thing they can see in Docker Desktop is not here,
// and the answer -- it runs locally and this host connects over the network --
// is worth one line of prose. What must not happen is an Add button that
// cannot work.
func TestDocker_OnlyRemoteEntriesAreAddable(t *testing.T) {
	page, err := serveDockerCatalog(t).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		wantAddable bool
		wantSaid    string
	}{
		{name: "context7", wantAddable: true},
		{name: "apify", wantAddable: true},
		{name: "astro-docs", wantAddable: true},
		// type: server. A container.
		{name: "SQLite", wantAddable: false, wantSaid: "does not run packaged servers"},
		// type: poci. A command.
		{name: "curl", wantAddable: false, wantSaid: "does not run packaged servers"},
		// type: remote, but sse. Refused by the import path's own parser,
		// in the import path's own words.
		{name: "dodo-payments", wantAddable: false, wantSaid: "streamable-http"},
		// type: remote and streamable-http, but the only credential comes
		// from a flow Docker's gateway performs.
		{name: "linear", wantAddable: false, wantSaid: "OAuth"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry, ok := entryNamed(page, tc.name)
			if !ok {
				t.Fatalf("%q is not listed; the catalogue should show what it cannot add", tc.name)
			}
			if entry.Addable != tc.wantAddable {
				t.Fatalf("addable = %v, want %v (reason %q)", entry.Addable, tc.wantAddable, entry.Reason)
			}
			if entry.Source != dockerSource {
				t.Errorf("source = %q, want %q", entry.Source, dockerSource)
			}
			if tc.wantAddable {
				if entry.Reason != "" {
					t.Errorf("reason = %q, want none on an addable entry", entry.Reason)
				}
				return
			}
			if !strings.Contains(entry.Reason, tc.wantSaid) {
				t.Errorf("reason = %q, want it to mention %q", entry.Reason, tc.wantSaid)
			}
		})
	}
}

// TestDocker_ComposesADocumentTheImportPathAccepts checks the whole point of
// the translation: what Docker describes becomes a server.json, and that
// server.json goes through the unchanged import path.
//
// The assertions are on what an operator ends up configuring, because that is
// what the translation is for. Docker writes a header value as
// ${CONTEXT7_API_KEY}; server.json writes the same thing as a {placeholder}
// with a variable behind it, and the variable has to arrive marked secret --
// otherwise the operator's API key is rendered in the clear and stored
// unencrypted.
func TestDocker_ComposesADocumentTheImportPathAccepts(t *testing.T) {
	detail, err := serveDockerCatalog(t).Get(context.Background(), "context7")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Source != dockerSource {
		t.Errorf("source = %q, want %q", detail.Source, dockerSource)
	}

	doc, err := mcpservers.Parse(detail.Document)
	if err != nil {
		t.Fatalf("the composed document does not import: %v\n%s", err, detail.Document)
	}
	if doc.Schema != mcpservers.SchemaURI {
		t.Errorf("$schema = %q, want the current format", doc.Schema)
	}
	if !strings.HasPrefix(doc.Name, "com.docker.mcp-registry/") {
		t.Errorf("name = %q, want it to say where it came from", doc.Name)
	}
	remote, err := doc.Remote()
	if err != nil {
		t.Fatal(err)
	}
	if remote.URL != "https://mcp.context7.com/mcp" {
		t.Errorf("url = %q, want Docker's own address", remote.URL)
	}

	inputs, err := doc.Inputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %+v, want one", inputs)
	}
	if inputs[0].Key != "var_context7_api_key" {
		t.Errorf("key = %q, want var_context7_api_key", inputs[0].Key)
	}
	if !inputs[0].Input.IsSecret {
		t.Error("the API key is not marked secret; it would be rendered in the clear " +
			"and stored unencrypted")
	}

	// And it resolves: the value the operator types reaches the header Docker
	// said it belongs in.
	endpoint, headers, err := doc.Resolve(map[string]string{"var_context7_api_key": "ctx7-live-key"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if endpoint != "https://mcp.context7.com/mcp" {
		t.Errorf("endpoint = %q", endpoint)
	}
	if headers["CONTEXT7_API_KEY"] != "ctx7-live-key" {
		t.Errorf("headers = %v, want the key in CONTEXT7_API_KEY", headers)
	}

	// The credential is one this host will redact, which is the judgement
	// CredentialValues makes about a header rather than one Docker gets to
	// make in its own catalogue.
	credentials := doc.CredentialValues(map[string]string{"var_context7_api_key": "ctx7-live-key"})
	if len(credentials) != 1 || credentials[0] != "ctx7-live-key" {
		t.Errorf("credentials = %v, want the API key", credentials)
	}
}

// TestDocker_BearerTemplateKeepsItsPrefix is the other header shape, and the
// commoner one: thirteen of Docker's fourteen header-carrying remotes write
// "Bearer ${SOMETHING}". The prefix is part of the header and must survive;
// only the reference becomes a placeholder.
func TestDocker_BearerTemplateKeepsItsPrefix(t *testing.T) {
	detail, err := serveDockerCatalog(t).Get(context.Background(), "apify")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := mcpservers.Parse(detail.Document)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, headers, err := doc.Resolve(map[string]string{"var_apify_api_key": "apify-secret"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if headers["Authorization"] != "Bearer apify-secret" {
		t.Errorf("Authorization = %q, want the Bearer prefix kept", headers["Authorization"])
	}
}

// TestDocker_AnEntryWithNoCredentialAsksForNothing. Seventeen of Docker's
// forty-seven streamable-http remotes need no credential at all, and a form
// with a field nobody can fill would make them look like they did.
func TestDocker_AnEntryWithNoCredentialAsksForNothing(t *testing.T) {
	detail, err := serveDockerCatalog(t).Get(context.Background(), "astro-docs")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := mcpservers.Parse(detail.Document)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	inputs, err := doc.Inputs()
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 0 {
		t.Errorf("inputs = %+v, want none", inputs)
	}
	remote, err := doc.Remote()
	if err != nil {
		t.Fatal(err)
	}
	if len(remote.Headers) != 0 {
		t.Errorf("headers = %+v, want none", remote.Headers)
	}
}

// TestDocker_TheSameEntryComposesTheSameBytes.
//
// The import path hashes the document it stores and every state change after
// that carries the hash in its WHERE clause. A map iterating in a different
// order would make one catalogue entry two documents, and re-importing a
// server after an upgrade would look like a different server.
func TestDocker_TheSameEntryComposesTheSameBytes(t *testing.T) {
	client := serveDockerCatalog(t)
	first, err := client.Get(context.Background(), "context7")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		again, err := client.Get(context.Background(), "context7")
		if err != nil {
			t.Fatal(err)
		}
		if string(again.Document) != string(first.Document) {
			t.Fatalf("the composed document changed between reads:\n%s\n%s",
				first.Document, again.Document)
		}
	}
}

// TestDocker_ProvenanceTravelsWithTheDocument.
//
// MIT asks for attribution and the document outlives the page it was picked
// from: a server imported six months ago should still be able to say where its
// description came from and under what licence.
func TestDocker_ProvenanceTravelsWithTheDocument(t *testing.T) {
	detail, err := serveDockerCatalog(t).Get(context.Background(), "astro-docs")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Meta map[string]struct {
			Source  string `json:"source"`
			Name    string `json:"name"`
			Licence string `json:"licence"`
			Origin  string `json:"origin"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(detail.Document, &doc); err != nil {
		t.Fatal(err)
	}
	provenance, ok := doc.Meta["io.mcpd/catalogue-source"]
	if !ok {
		t.Fatalf("_meta = %+v, want the catalogue recorded", doc.Meta)
	}
	if provenance.Source != dockerSource || provenance.Name != "astro-docs" {
		t.Errorf("provenance = %+v, want docker/mcp-registry and the catalogue's own name", provenance)
	}
	if !strings.Contains(provenance.Licence, "MIT") ||
		!strings.Contains(provenance.Licence, "Docker") {
		t.Errorf("licence = %q, want the MIT notice", provenance.Licence)
	}
	if provenance.Origin != "https://github.com/docker/mcp-registry" {
		t.Errorf("origin = %q", provenance.Origin)
	}
}

// TestDocker_LicenceIsVendoredWithTheFixtures. MIT requires the notice to
// travel with the copy, and the excerpt under testdata is a copy.
func TestDocker_LicenceIsVendoredWithTheFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "docker", "LICENSE"))
	if err != nil {
		t.Fatalf("the vendored excerpt has no licence beside it: %v", err)
	}
	for _, want := range []string{"MIT License", "Copyright (c) 2025 Docker"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the vendored licence does not contain %q", want)
		}
	}
}

// TestDocker_PagesOverASortedCatalogue. Docker hands over every entry in one
// document, so paging is done here; a cursor is the name to resume after
// rather than an offset, so that the catalogue changing between two pages
// cannot silently skip an entry.
func TestDocker_PagesOverASortedCatalogue(t *testing.T) {
	client := serveDockerCatalog(t)
	var seen []string
	cursor := ""
	for i := 0; i < 10; i++ {
		page, err := client.List(context.Background(), Query{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Entries {
			seen = append(seen, e.Name)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	want := []string{"SQLite", "apify", "astro-docs", "context7", "curl", "dodo-payments", "linear"}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("paged over %v, want %v", seen, want)
	}
}

// TestDocker_SearchMatchesTheTitleAsWellAsTheName. A Docker name is a bare
// slug where an official name carries the publisher's domain, so the words an
// operator types are as likely to be in the title.
func TestDocker_SearchMatchesTheTitleAsWellAsTheName(t *testing.T) {
	page, err := serveDockerCatalog(t).List(context.Background(), Query{Search: "Astro Docs"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Entries[0].Name != "astro-docs" {
		t.Fatalf("entries = %+v, want astro-docs", page.Entries)
	}
}

// TestDocker_BoundsTheCatalogue. Registry content is a third party's, arriving
// in whatever quantity they choose to send, and a decoder reading an unbounded
// body is a memory limit set by somebody else.
func TestDocker_BoundsTheCatalogue(t *testing.T) {
	oversized := append([]byte("version: 3\nname: docker-mcp\nregistry:\n"),
		[]byte(strings.Repeat("  x: {}\n", 2))...)
	oversized = append(oversized, []byte("# "+strings.Repeat("p", MaxCatalogBytes))...)

	client := newDocker(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(oversized)
	})
	if _, err := client.List(context.Background(), Query{}); err == nil {
		t.Fatal("an oversized catalogue was accepted")
	} else if !strings.Contains(err.Error(), "more than") {
		t.Errorf("err = %v, want it to say the catalogue was too large", err)
	}
}

// TestDocker_AFailureIsTheCataloguesAndSaysSo.
func TestDocker_AFailureIsTheCataloguesAndSaysSo(t *testing.T) {
	client := newDocker(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	})
	_, err := client.List(context.Background(), Query{})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), dockerSource) {
		t.Errorf("err = %v, want it to name the catalogue that failed", err)
	}
}

// TestDocker_RefusesAHeaderItCannotTranslate.
//
// A brace already in a header value would be read as a server.json placeholder
// by everything downstream, and this host did not put it there. Substituting
// the operator's value into the wrong part of a credential is worse than
// declining to offer the server.
func TestDocker_RefusesAHeaderItCannotTranslate(t *testing.T) {
	body := strings.Replace(string(dockerFixture(t)),
		`CONTEXT7_API_KEY: "${CONTEXT7_API_KEY}"`,
		`CONTEXT7_API_KEY: "{already_braced}"`, 1)
	client := newDocker(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	page, err := client.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := entryNamed(page, "context7")
	if !ok {
		t.Fatal("context7 is not listed")
	}
	if entry.Addable {
		t.Fatal("an untranslatable header was offered as addable")
	}
	if !strings.Contains(entry.Reason, "brace") {
		t.Errorf("reason = %q, want it to name the brace", entry.Reason)
	}
}

// TestDocker_AnUnknownTypeIsNotGuessedAt. Docker may add a fourth kind of
// entry; this host should say it does not know how to reach it rather than
// treating it as one of the three it does know.
func TestDocker_AnUnknownTypeIsNotGuessedAt(t *testing.T) {
	body := strings.Replace(string(dockerFixture(t)),
		"    title: Astro Docs\n    type: remote\n",
		"    title: Astro Docs\n    type: something-new\n", 1)
	client := newDocker(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	})
	page, err := client.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := entryNamed(page, "astro-docs")
	if !ok {
		t.Fatal("astro-docs is not listed")
	}
	if entry.Addable {
		t.Fatal("an entry of an unknown kind was offered as addable")
	}
	if !strings.Contains(entry.Reason, "something-new") {
		t.Errorf("reason = %q, want it to name the kind", entry.Reason)
	}
}

// TestDocker_GetIsAsSelectiveAsList. A name that lists as unavailable must not
// hand back a document by being asked for directly.
func TestDocker_GetIsAsSelectiveAsList(t *testing.T) {
	client := serveDockerCatalog(t)
	for _, name := range []string{"SQLite", "curl", "linear", "dodo-payments"} {
		detail, err := client.Get(context.Background(), name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if detail.Addable || len(detail.Document) != 0 {
			t.Errorf("%s handed back a document it cannot import", name)
		}
	}
	if _, err := client.Get(context.Background(), "no-such-entry"); err == nil {
		t.Error("an unknown name was answered")
	}
}

// TestDocker_ARefusedRemoteStillCarriesItsAddress.
//
// A Docker entry names exactly one remote, and the reasons a remote entry is
// refused are about the credential or the transport -- never about not knowing
// where the server is. Carrying the address matters twice: an operator can see
// what they are being refused, and cross-source deduplication has the one
// identity the two catalogues share. Without it, Docker's OAuth-only `linear`
// and the official registry's `app.linear/linear` are one server listed twice
// under two names no rule turns into each other.
func TestDocker_ARefusedRemoteStillCarriesItsAddress(t *testing.T) {
	page, err := serveDockerCatalog(t).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ name, wantURL, wantTransport string }{
		{"linear", "https://mcp.linear.app/mcp", "streamable-http"},
		{"dodo-payments", "https://mcp.dodopayments.com/sse", "sse"},
		// Not a remote at all, so there is no address to carry.
		{"SQLite", "", ""},
		{"curl", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry, ok := entryNamed(page, tc.name)
			if !ok {
				t.Fatalf("%q is not listed", tc.name)
			}
			if entry.Addable {
				t.Fatalf("%q should not be addable", tc.name)
			}
			if entry.URL != tc.wantURL {
				t.Errorf("url = %q, want %q", entry.URL, tc.wantURL)
			}
			if entry.Transport != tc.wantTransport {
				t.Errorf("transport = %q, want %q", entry.Transport, tc.wantTransport)
			}
		})
	}
}

// TestDocker_DeduplicatesAgainstTheOfficialRegistry is the same rule seen from
// the outside: Docker's copy of a server the official registry also has goes
// away, whichever of the two this host could actually add.
func TestDocker_DeduplicatesAgainstTheOfficialRegistry(t *testing.T) {
	docker := serveDockerCatalog(t)
	official := &stubSource{name: officialSource, pages: map[string]Page{
		"": {Entries: []Entry{
			// The official registry's names for two servers Docker also
			// lists: one Docker could have offered, one it refuses for OAuth.
			at(officialSource, "com.apify/apify-mcp-server", "https://mcp.apify.com"),
			at(officialSource, "app.linear/linear", "https://mcp.linear.app/mcp"),
		}},
	}}

	page, err := NewMulti(official, docker).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range page.Entries {
		if e.Source == dockerSource && (e.Name == "apify" || e.Name == "linear") {
			t.Errorf("%q survived deduplication against the official registry", e.Name)
		}
	}
	if _, ok := entryNamed(page, "app.linear/linear"); !ok {
		t.Error("the preferred catalogue's copy was the one dropped")
	}
	// The rest of Docker's catalogue is untouched.
	if _, ok := entryNamed(page, "astro-docs"); !ok {
		t.Error("an entry only Docker has was dropped")
	}
}
