package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spoked/mcpd/internal/mcpservers"
	"github.com/spoked/mcpd/internal/plugins/mcpremote"
	"github.com/spoked/mcpd/internal/settings"
)

func smitheryFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "smithery", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

// newSmithery serves the vendored responses from Smithery's real API.
//
// The gateway is pointed somewhere harmless: composing a document is the thing
// under test, and no test dials server.smithery.ai.
func newSmithery(t *testing.T, handler http.HandlerFunc) *Smithery {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewSmithery(SmitheryOptions{
		BaseURL:    server.URL,
		GatewayURL: "https://server.smithery.ai",
		HTTPClient: server.Client(),
		Limit:      MaxEntriesPerPage,
	})
}

// serveSmithery answers a listing from the fixtures: page one has rows, and
// every page after it is the empty answer the real API gives past its cap.
func serveSmithery(t *testing.T) *Smithery {
	t.Helper()
	page1 := smitheryFixture(t, "list-page1.json")
	beyond := smitheryFixture(t, "list-page-beyond-cap.json")
	search := smitheryFixture(t, "search-postgres.json")
	detailBrave := smitheryFixture(t, "detail-brave.json")
	detailLocal := smitheryFixture(t, "detail-not-remote.json")

	return newSmithery(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/servers/"):
			switch strings.TrimPrefix(r.URL.Path, "/servers/") {
			case "brave":
				_, _ = w.Write(detailBrave)
			case "gautamgb/mcpindex":
				_, _ = w.Write(detailLocal)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		case r.URL.Query().Get("q") != "":
			_, _ = w.Write(search)
		case r.URL.Query().Get("page") == "1":
			_, _ = w.Write(page1)
		default:
			_, _ = w.Write(beyond)
		}
	})
}

// TestSmitheryList_ComposesADocumentTheImportPathAccepts.
//
// The whole point of the source. Smithery's format is not server.json, so an
// entry is translated into one, and what makes "offered" and "imports" the
// same set is that the composed document is judged by the calls the import
// endpoint makes rather than by a rule written beside the catalogue.
func TestSmitheryList_ComposesADocumentTheImportPathAccepts(t *testing.T) {
	page, err := serveSmithery(t).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}

	entry, ok := entryNamed(page, "brave")
	if !ok {
		t.Fatalf("brave is not in the page; got %v", namesOf(page))
	}
	if !entry.Addable {
		t.Fatalf("brave must be addable; reason: %s", entry.Reason)
	}
	if entry.Transport != mcpservers.TransportStreamableHTTP {
		t.Errorf("transport = %q, want %q", entry.Transport, mcpservers.TransportStreamableHTTP)
	}
	if want := "https://server.smithery.ai/brave/mcp"; entry.URL != want {
		t.Errorf("url = %q, want the gateway address %q", entry.URL, want)
	}
	if entry.Source != smitherySource {
		t.Errorf("source = %q, want the entry to say where it came from", entry.Source)
	}
	// Every Smithery server needs the operator's key, so every addable entry
	// says so before anybody clicks Add.
	if entry.Auth != AuthAPIKey {
		t.Errorf("auth = %q, want %q", entry.Auth, AuthAPIKey)
	}
}

// TestSmitheryDocument_AsksForTheKeyAsAnEncryptedSettingRatherThanCarryingOne.
//
// The credential rule, checked end to end through the two calls the import
// endpoint makes. The document must name the key and never hold it: a
// server.json is stored verbatim and hashed, so a key written into one would
// be a credential at rest in a document rather than in the settings store.
func TestSmitheryDocument_AsksForTheKeyAsAnEncryptedSettingRatherThanCarryingOne(t *testing.T) {
	detail, err := serveSmithery(t).Get(context.Background(), "brave")
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Addable {
		t.Fatalf("brave must be addable; reason: %s", detail.Reason)
	}

	doc, err := mcpservers.Parse(detail.Document)
	if err != nil {
		t.Fatalf("the composed document must parse the way import parses it: %v", err)
	}
	fields, err := mcpremote.Fields(doc)
	if err != nil {
		t.Fatalf("the composed document must yield a form import can render: %v", err)
	}

	var secret *settings.Field
	for i := range fields {
		if fields[i].Kind == settings.KindSecret {
			secret = &fields[i]
		}
	}
	if secret == nil {
		t.Fatalf("no secret field; the Smithery key would have nowhere encrypted to live. fields: %+v", fields)
	}
	if !secret.Required {
		t.Error("the key field must be required; without it every call is a 401")
	}
	if secret.Default != nil {
		t.Errorf("default = %v, want none -- a secret's default is a credential in a form field", secret.Default)
	}
	if !strings.Contains(secret.Key, "smithery") {
		t.Errorf("key = %q, want it to name the credential it holds", secret.Key)
	}

	// The document names the placeholder and carries no value behind it.
	if !strings.Contains(string(detail.Document), "Bearer {"+smitheryKeyVariable+"}") {
		t.Errorf("document does not carry the placeholder header:\n%s", detail.Document)
	}
	// Whatever an operator eventually types must be treated as sensitive on
	// the way out, which is the check that stops a key reaching a log.
	if got := doc.CredentialValues(map[string]string{secret.Key: "a-real-key"}); len(got) == 0 {
		t.Error("the key is not recognised as a credential, so nothing would redact it")
	}
}

// TestSmitheryDocument_IsByteStable.
//
// The import path hashes what it stores and every state change after that
// carries the hash, so the same row has to compose the same bytes every time.
// A map iterated in Go's order would not.
func TestSmitheryDocument_IsByteStable(t *testing.T) {
	client := serveSmithery(t)
	first, err := client.Get(context.Background(), "brave")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		again, err := client.Get(context.Background(), "brave")
		if err != nil {
			t.Fatal(err)
		}
		if string(again.Document) != string(first.Document) {
			t.Fatalf("attempt %d composed different bytes:\n %s\n %s", i, first.Document, again.Document)
		}
	}

	// And the listing composes the same document the entry route does, so an
	// operator importing from a row and from its page get one document.
	page, err := client.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := entryNamed(page, "brave")
	if !ok {
		t.Fatal("brave is not in the page")
	}
	if entry.URL != first.URL || entry.Addable != first.Addable {
		t.Errorf("the listing and the entry route disagree: %+v vs %+v", entry, first.Entry)
	}
}

// TestSmitheryList_ABrowseSaysItIsBounded.
//
// Smithery's listing stops after five hundred of ten and a half thousand
// servers. A page whose last row is the five hundredth looks exactly like the
// end of a catalogue, so the page says otherwise. The alternative -- presenting
// the top five hundred as the whole thing -- is the one failure this source
// could have that nobody would ever notice.
func TestSmitheryList_ABrowseSaysItIsBounded(t *testing.T) {
	page, err := serveSmithery(t).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sources) != 1 {
		t.Fatalf("sources = %+v, want one", page.Sources)
	}
	note := page.Sources[0].Note
	if note == "" {
		t.Fatal("a browse says nothing about being truncated")
	}
	for _, want := range []string{"500", "search"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to mention %q", note, want)
		}
	}

	// A search is not truncated -- Smithery answered it over the whole
	// catalogue -- so it does not carry the warning.
	searched, err := serveSmithery(t).List(context.Background(), Query{Search: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	if searched.Sources[0].Note != "" {
		t.Errorf("note = %q, want none on a search", searched.Sources[0].Note)
	}
}

// TestSmitheryList_SearchGoesUpstreamAndReachesWhatBrowsingCannot.
//
// The consequence of the bound above. If q= were used to filter the browsable
// window, a search for a server at position nine thousand would come back
// empty -- which is why the fixture's search hits are five servers no listing
// page returns.
func TestSmitheryList_SearchGoesUpstreamAndReachesWhatBrowsingCannot(t *testing.T) {
	var asked atomic.Value
	asked.Store("")
	page1 := smitheryFixture(t, "list-page1.json")
	beyond := smitheryFixture(t, "list-page-beyond-cap.json")
	search := smitheryFixture(t, "search-postgres.json")

	client := newSmithery(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if q := r.URL.Query().Get("q"); q != "" {
			asked.Store(q)
			_, _ = w.Write(search)
			return
		}
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write(page1)
			return
		}
		_, _ = w.Write(beyond)
	})

	browsed, err := client.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := entryNamed(browsed, "neon"); found {
		t.Fatal("the fixture is wrong: neon must not be in the browsable window")
	}

	page, err := client.List(context.Background(), Query{Search: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	if got := asked.Load().(string); got != "postgres" {
		t.Errorf("q = %q, want the search passed to Smithery rather than applied here", got)
	}
	if _, found := entryNamed(page, "neon"); !found {
		t.Errorf("search did not reach a server outside the browsable window; got %v", namesOf(page))
	}
}

// TestSmitheryList_PastTheCapIsHandledRatherThanRepeated.
//
// Page six of the real API is an empty array with totalPages still reporting
// five. A client that trusted totalCount and kept asking would page forever;
// one that mistook the empty answer for a failure would drop the source.
// Neither: the window ends, and the last page hands back no cursor.
func TestSmitheryList_PastTheCapIsHandledRatherThanRepeated(t *testing.T) {
	client := serveSmithery(t)
	page, err := client.List(context.Background(), Query{Limit: 4})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == "" {
		t.Fatal("the first of two pages hands back no cursor")
	}

	seen := map[string]bool{}
	for _, e := range page.Entries {
		seen[e.Name] = true
	}
	second, err := client.List(context.Background(), Query{Limit: 4, Cursor: page.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range second.Entries {
		if seen[e.Name] {
			t.Errorf("%q appears on both pages", e.Name)
		}
		seen[e.Name] = true
	}
	if second.NextCursor != "" {
		t.Errorf("next_cursor = %q, want the listing to end", second.NextCursor)
	}
	if len(seen) != 8 {
		t.Errorf("saw %d entries over two pages, want the fixture's 8", len(seen))
	}
}

// TestSmitheryList_DropsTheRowsSmitheryRepeats.
//
// Measured, not hypothetical: the 500 rows of the live browse window hold 269
// distinct servers, and pages one and two share 39 of them. Smithery orders by
// popularity and that order is not a total one, so a row lands on two pages.
// Without this the dashboard shows the same server several times, which reads
// as a broken list.
func TestSmitheryList_DropsTheRowsSmitheryRepeats(t *testing.T) {
	page1 := smitheryFixture(t, "list-page1.json")
	var body map[string]any
	if err := json.Unmarshal(page1, &body); err != nil {
		t.Fatal(err)
	}
	// Two pages that overlap completely, the way the real ones partly do.
	body["pagination"] = map[string]any{
		"currentPage": 1, "pageSize": 100, "totalPages": 2, "totalCount": 16,
	}
	repeated, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	client := newSmithery(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(repeated)
	})

	page, err := client.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range page.Entries {
		seen[e.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("%q appears %d times", name, n)
		}
	}
	if len(page.Entries) != 8 {
		t.Errorf("entries = %d, want the 8 distinct servers of a doubled page", len(page.Entries))
	}
}

// TestSmitheryList_SomethingToRunYourselfIsListedWithTheReason.
//
// This host does not run packaged servers. Those are shown with the reason
// rather than filtered out -- "why is the thing I came for not here" is a worse
// question than a row that answers it.
func TestSmitheryList_SomethingToRunYourselfIsListedWithTheReason(t *testing.T) {
	page, err := serveSmithery(t).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := entryNamed(page, "gautamgb/mcpindex")
	if !ok {
		t.Fatalf("the row was filtered out instead of explained; got %v", namesOf(page))
	}
	if entry.Addable {
		t.Fatal("a server Smithery does not host was offered as addable")
	}
	if !strings.Contains(entry.Reason, "does not run packaged servers") {
		t.Errorf("reason = %q, want it to say why", entry.Reason)
	}
	if entry.URL != "" {
		t.Errorf("url = %q, want none for a server with no hosted address", entry.URL)
	}
	// Nothing to say about a credential for something that cannot be added.
	if entry.Auth != "" {
		t.Errorf("auth = %q, want none on a row nobody can import", entry.Auth)
	}

	// And the entry route agrees, so a name typed in cannot get past the list.
	detail, err := serveSmithery(t).Get(context.Background(), "gautamgb/mcpindex")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Addable || len(detail.Document) != 0 {
		t.Errorf("the entry route offered what the listing refused: %+v", detail.Entry)
	}
}

// TestSmitheryList_HostedButNotDeployedIsRefusedWithItsOwnReason.
//
// The row is built rather than taken from a fixture, and deliberately: across
// 2,175 distinct servers reached by search on 2026-08-23, not one had
// `remote: true` with `isDeployed: false`. The branch is still checked,
// because the two are separate claims and only the pair means there is an
// address behind the row right now -- if Smithery ever lists a server it has
// not stood up, this is what stops an Add button that dials nothing.
func TestSmitheryList_HostedButNotDeployedIsRefusedWithItsOwnReason(t *testing.T) {
	body := fmt.Sprintf(`{"servers":[{"qualifiedName":"pending/server",
		"displayName":"Pending","description":"Not up yet.",
		"remote":true,"isDeployed":false,"createdAt":"2026-01-02T03:04:05.000Z"}],
		"pagination":{"currentPage":1,"pageSize":%d,"totalPages":1,"totalCount":1}}`,
		smitheryPageSize)
	client := newSmithery(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	page, err := client.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := entryNamed(page, "pending/server")
	if !ok {
		t.Fatalf("the row was dropped instead of explained; got %v", namesOf(page))
	}
	if entry.Addable {
		t.Fatal("a server with no deployment behind it was offered as addable")
	}
	if !strings.Contains(entry.Reason, "has not deployed it") {
		t.Errorf("reason = %q, want it to name the undeployed case rather than the packaged one", entry.Reason)
	}
}

// TestSmithery_AQualifiedNameWithASlashSurvivesBothPaths.
//
// "onesignal/onesignal" has to survive three different escapings: the gateway
// path it is dialled at, the server.json name, which permits exactly one
// slash and it is not this one, and the entry route the dashboard sends the
// name back to.
func TestSmithery_AQualifiedNameWithASlashSurvivesBothPaths(t *testing.T) {
	page, err := serveSmithery(t).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := entryNamed(page, "onesignal/onesignal")
	if !ok {
		t.Fatalf("the namespaced row is missing; got %v", namesOf(page))
	}
	if !entry.Addable {
		t.Fatalf("it must be addable; reason: %s", entry.Reason)
	}
	if want := "https://server.smithery.ai/onesignal/onesignal/mcp"; entry.URL != want {
		t.Errorf("url = %q, want %q", entry.URL, want)
	}

	// The document's own name: one slash, between the derived namespace and
	// the rest, with the qualifiedName's slash flattened.
	var doc struct {
		Name string `json:"name"`
	}
	detail, err := serveSmithery(t).Get(context.Background(), "brave")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(detail.Document, &doc); err != nil {
		t.Fatal(err)
	}
	if strings.Count(doc.Name, "/") != 1 {
		t.Errorf("document name = %q, want exactly one slash", doc.Name)
	}
	if got := smitheryDocumentName("onesignal/onesignal"); strings.Contains(got, "/") {
		t.Errorf("document name for a namespaced server = %q, want the slash flattened", got)
	}
}

// TestSmithery_BoundsUntrustedText.
//
// A registry entry is somebody else's prose, arriving in whatever quantity
// they choose to send. Every field is bounded and stripped of control and
// invisible-formatting characters before it is stored or returned.
func TestSmithery_BoundsUntrustedText(t *testing.T) {
	long := strings.Repeat("d", maxDescriptionRunes*3)
	body := fmt.Sprintf(`{"servers":[{"qualifiedName":"noisy",
		"displayName":"Ti\ntle‮","description":%q,
		"remote":true,"isDeployed":true,"createdAt":"nonsense"}],
		"pagination":{"currentPage":1,"pageSize":%d,"totalPages":1,"totalCount":1}}`,
		long, smitheryPageSize)
	client := newSmithery(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	page, err := client.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := entryNamed(page, "noisy")
	if !ok {
		t.Fatalf("entry missing; got %v", namesOf(page))
	}
	if strings.ContainsAny(entry.Title, "\n‮") {
		t.Errorf("title = %q, want control and formatting characters replaced", entry.Title)
	}
	if n := len([]rune(entry.Description)); n > maxDescriptionRunes+1 {
		t.Errorf("description is %d runes, want it bounded at %d", n, maxDescriptionRunes)
	}
	// An unreadable timestamp keeps the zero time rather than today's date:
	// "we do not know when this changed" and "it changed just now" are
	// different facts and the second is the one that misleads.
	if !entry.UpdatedAt.IsZero() {
		t.Errorf("updated_at = %v, want the zero time for an unreadable createdAt", entry.UpdatedAt)
	}
}

// TestSmithery_APageIsCappedWhateverTheFarEndSends.
//
// The entry count per page is capped here rather than trusted, because the
// page size this host asks for is a request and not a guarantee.
func TestSmithery_APageIsCappedWhateverTheFarEndSends(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"servers":[`)
	for i := range MaxEntriesPerPage * 3 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"qualifiedName":"s%04d","displayName":"S","description":"d",
			"remote":true,"isDeployed":true}`, i)
	}
	fmt.Fprintf(&b, `],"pagination":{"currentPage":1,"pageSize":%d,"totalPages":1,"totalCount":1}}`,
		smitheryPageSize)
	body := b.String()

	client := newSmithery(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	page, err := client.List(context.Background(), Query{Limit: MaxEntriesPerPage})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) > MaxEntriesPerPage {
		t.Errorf("entries = %d, want at most %d", len(page.Entries), MaxEntriesPerPage)
	}
}

// TestSmithery_AFailedPageFailsTheFetch.
//
// Half a window presented as a whole one is exactly the truncation this source
// exists to be honest about, so a page after the first that will not load is
// fatal to the fetch rather than silently short. The cache above serves what it
// last saw, marked stale, which is the honest answer.
func TestSmithery_AFailedPageFailsTheFetch(t *testing.T) {
	page1 := smitheryFixture(t, "list-page1.json")
	client := newSmithery(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(page1)
	})
	if _, err := client.List(context.Background(), Query{}); err == nil {
		t.Fatal("a window missing four of its five pages was served as though whole")
	}
}

// TestSmithery_ARefusalDoesNotInventACredentialToCheck.
//
// Three sources share one fetch, and only PulseMCP sends a credential. So a
// 401 from Smithery is a third party behaving unexpectedly, not a key to go
// and look for -- and telling an operator to check an api key for a source
// that has no such setting sends them hunting for something that does not
// exist. The advice belongs to the source that can act on it.
func TestSmithery_ARefusalDoesNotInventACredentialToCheck(t *testing.T) {
	client := newSmithery(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	_, err := client.List(context.Background(), Query{})
	if err == nil {
		t.Fatal("a 401 was not reported")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want the status as the fact", err)
	}
	for _, unwanted := range []string{"api key", "api_key_ref", "tenant"} {
		if strings.Contains(strings.ToLower(err.Error()), unwanted) {
			t.Errorf("err = %v, want no mention of %q -- this source has no such setting", err, unwanted)
		}
	}
}

// TestSmitheryGet_AnUnknownNameIsNotFound.
func TestSmitheryGet_AnUnknownNameIsNotFound(t *testing.T) {
	if _, err := serveSmithery(t).Get(context.Background(), "nobody/here"); err == nil {
		t.Fatal("an unknown name did not report itself as unknown")
	}
}

// TestSmithery_RefusesARedirect.
//
// The default client refuses one. There is no credential to leak toward
// Smithery's registry, but a catalogue that suddenly wants to send this host
// somewhere else is a change worth refusing rather than following.
func TestSmithery_RefusesARedirect(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"servers":[],"pagination":{}}`))
	}))
	t.Cleanup(elsewhere.Close)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	t.Cleanup(server.Close)

	client := NewSmithery(SmitheryOptions{BaseURL: server.URL})
	if _, err := client.List(context.Background(), Query{}); err == nil ||
		!strings.Contains(err.Error(), "refused a redirect") {
		t.Errorf("err = %v, want a refused redirect", err)
	}
}

func namesOf(page Page) []string {
	out := make([]string, 0, len(page.Entries))
	for _, e := range page.Entries {
		out = append(out, e.Name)
	}
	return out
}

// TestSmitheryList_IsMostUsedFirst.
//
// The default view of a catalogue with ten thousand servers in it is a sample,
// and an arbitrary sample is worth very little: "here are ten servers" is not
// the same offer as "here are ten servers people use". Smithery counts calls
// to every server it hosts and publishes the count on every listing row, which
// is the only usage signal any of the four catalogues gives, so this source
// orders by it.
//
// The fixture is the live API's own page one, and it is deliberately not in
// use order -- Smithery's paging is by popularity but is not a total order,
// which is the same reason its pages repeat rows. The ordering is rebuilt here
// as a total one, which is also what lets the cursor resume from it.
func TestSmitheryList_IsMostUsedFirst(t *testing.T) {
	page, err := serveSmithery(t).List(context.Background(),
		Query{Limit: MaxEntriesPerPage, IncludeUnaddable: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"brave", "gmail", "googlesheets", "theagenttimes/news",
		"onesignal/onesignal", "subwayinfo", "exa", "gautamgb/mcpindex",
	}
	got := make([]string, 0, len(page.Entries))
	for _, e := range page.Entries {
		got = append(got, e.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v,\nwant  = %v", got, want)
	}
}

// TestSmitheryList_TheCursorResumesTheRanking.
//
// The cursor used to be the last name on the page, which works only while the
// order is the name order. It is now the rank key, for the same reason: it has
// to be a value the next page can search the ordering for, and the ordering is
// no longer alphabetical.
func TestSmitheryList_TheCursorResumesTheRanking(t *testing.T) {
	smithery := serveSmithery(t)
	seen := []string{}
	cursor := ""
	for range 10 {
		page, err := smithery.List(context.Background(),
			Query{Limit: 3, Cursor: cursor, IncludeUnaddable: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Entries {
			seen = append(seen, e.Name)
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	want := []string{
		"brave", "gmail", "googlesheets", "theagenttimes/news",
		"onesignal/onesignal", "subwayinfo", "exa", "gautamgb/mcpindex",
	}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Errorf("paged through %v,\nwant        %v", seen, want)
	}
}

// TestSmitheryList_ReportsWhatSmitheryHolds.
//
// The one source of the four that answers "how many are there". It is the
// larger half of the figure beside the search box, and the reason the figure
// exists at all: a page of ten out of ten thousand looks exactly like a
// catalogue of ten.
func TestSmitheryList_ReportsWhatSmitheryHolds(t *testing.T) {
	page, err := serveSmithery(t).List(context.Background(), Query{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	status := page.Sources[0]
	if status.Total != 10498 {
		t.Errorf("total = %d, want the 10498 the fixture reports", status.Total)
	}
	// Judged is the whole window rather than the page, which is what makes
	// the ratio behind the estimate worth quoting.
	if status.Judged != 8 {
		t.Errorf("judged = %d, want the whole window of 8", status.Judged)
	}
	if status.Addable != 7 {
		t.Errorf("addable = %d, want 7: one of the eight only runs locally", status.Addable)
	}
}

// TestSmitheryList_CarriesTheCallCountOntoTheEntry.
//
// The count was read, used to order the window, and thrown away before
// anything outside this file could see it -- so the page was ordered by a
// number nobody could check. It is now on the entry and rendered beside the
// row, which is the difference between an ordering an operator can verify and
// a badge asking to be believed.
func TestSmitheryList_CarriesTheCallCountOntoTheEntry(t *testing.T) {
	page, err := serveSmithery(t).List(context.Background(),
		Query{Limit: MaxEntriesPerPage, IncludeUnaddable: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) == 0 {
		t.Fatal("no entries")
	}
	head := page.Entries[0]
	switch {
	case head.Uses == nil:
		t.Fatalf("%s carries no call count", head.Name)
	case *head.Uses != 87_579:
		t.Errorf("%s reports %d calls, want the fixture's 87,579", head.Name, *head.Uses)
	}
	for _, e := range page.Entries {
		if e.Uses == nil {
			t.Errorf("%s carries no call count, and every Smithery row has one", e.Name)
		}
	}
	// And the source says it publishes the figure, which is what a most-used
	// listing is narrowed by.
	if status, ok := statusOf(page, smitherySource); !ok || !status.Uses {
		t.Error("Smithery does not report that it publishes a usage figure")
	}
}

// TestSmithery_AFigureThatIsNotOneIsAbsentRatherThanZero.
//
// Untrusted text, and the one value with no honest rendering. Zero would say
// this host measured the server and found nobody had called it, which would be
// a number it had made up.
func TestSmithery_AFigureThatIsNotOneIsAbsentRatherThanZero(t *testing.T) {
	if got := smitheryUses(-1); got != nil {
		t.Errorf("a negative count became %d, want no figure at all", *got)
	}
	got := smitheryUses(0)
	if got == nil || *got != 0 {
		t.Errorf("a measured zero became %v, want zero reported as zero", got)
	}
}
