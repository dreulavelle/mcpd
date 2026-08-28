package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func pulseFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "pulsemcp", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}

func newPulseMCP(t *testing.T, handler http.HandlerFunc) *PulseMCP {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewPulseMCP(PulseMCPOptions{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		APIKey:     "a-key",
		Tenant:     "a-tenant",
		Limit:      MaxEntriesPerPage,
	})
}

// servePulseMCP answers with PulseMCP's own published example payloads.
func servePulseMCP(t *testing.T) *PulseMCP {
	t.Helper()
	list := pulseFixture(t, "list-example.json")
	detail := pulseFixture(t, "detail-example.json")
	return newPulseMCP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The header the live API sends, so the freshness path in this test is
		// the one the live API drives.
		w.Header().Set("Cache-Control", "no-cache")
		if strings.Contains(r.URL.Path, "/versions/") {
			_, _ = w.Write(detail)
			return
		}
		_, _ = w.Write(list)
	})
}

// TestPulseMCP_ReadsTheGenericRegistryShape.
//
// v0.1 implements the same wire contract the official registry serves, which
// is why there is no translation step in pulsemcp.go: a document arrives as
// its publisher wrote it and is passed through, not composed. This is the
// check that the shared reader in generic.go actually reads PulseMCP's
// version of it -- including the lifecycle facts, which live under PulseMCP's
// own _meta key rather than the official registry's.
func TestPulseMCP_ReadsTheGenericRegistryShape(t *testing.T) {
	page, err := servePulseMCP(t).List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 {
		t.Fatalf("entries = %d, want the fixture's one; got %v", len(page.Entries), namesOf(page))
	}
	entry := page.Entries[0]
	if entry.Name != "io.github.modelcontextprotocol/filesystem" {
		t.Errorf("name = %q, want the document's own name passed through", entry.Name)
	}
	if entry.Source != pulseMCPSource {
		t.Errorf("source = %q, want the entry to say where it came from", entry.Source)
	}
	// The lifecycle block is PulseMCP's, under PulseMCP's key. Read from the
	// official registry's key it would be absent, and the row would be
	// withheld as not-active.
	if entry.UpdatedAt.IsZero() {
		t.Error("updated_at is zero, so the lifecycle facts were not read")
	}
	// The fixture's document offers streamable-http, so it is addable. Its
	// remotes declare no headers and no variables, so what this host can say
	// about credentials is that the document does not say -- not that there
	// are none. Most published documents shaped like this one answer 401.
	if !entry.Addable {
		t.Fatalf("the entry must be addable; reason: %s", entry.Reason)
	}
	if entry.Auth != AuthUnknown {
		t.Errorf("auth = %q, want %q for a document that declares no inputs at all",
			entry.Auth, AuthUnknown)
	}
	if page.NextCursor == "" {
		t.Error("the cursor the far end offered was dropped")
	}
}

// TestPulseMCP_SendsItsCredentialsAndAsksForLatestOnly.
//
// The two headers are the whole of what makes this source different from the
// official registry on the wire, so they are worth pinning. version=latest is
// asked for and then not relied on -- dedupe runs regardless -- for the reason
// the official registry gives: one row per name is a promise, and a page
// listing the same server four times is what its failure looks like.
func TestPulseMCP_SendsItsCredentialsAndAsksForLatestOnly(t *testing.T) {
	var got atomic.Value
	list := pulseFixture(t, "list-example.json")
	client := newPulseMCP(t, func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Clone(context.Background()))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(list)
	})
	if _, err := client.List(context.Background(), Query{Search: "files"}); err != nil {
		t.Fatal(err)
	}

	req := got.Load().(*http.Request)
	if key := req.Header.Get("X-API-Key"); key != "a-key" {
		t.Errorf("X-API-Key = %q, want the configured key", key)
	}
	if tenant := req.Header.Get("X-Tenant-ID"); tenant != "a-tenant" {
		t.Errorf("X-Tenant-ID = %q, want the configured tenant", tenant)
	}
	if !strings.HasPrefix(req.URL.Path, "/v0.1/servers") {
		t.Errorf("path = %q, want the v0.1 endpoint", req.URL.Path)
	}
	if v := req.URL.Query().Get("version"); v != "latest" {
		t.Errorf("version = %q, want latest", v)
	}
	if s := req.URL.Query().Get("search"); s != "files" {
		t.Errorf("search = %q, want the query passed upstream", s)
	}
}

// TestPulseMCP_UnconfiguredSaysSoRatherThanCallingAnyway.
//
// The source is off by default because v0.1 authenticates every request. One
// switched on without credentials must say what it needs, where somebody can
// see it -- on the page -- rather than sending an unauthenticated request that
// comes back 401 and reads as the catalogue being down.
func TestPulseMCP_UnconfiguredSaysSoRatherThanCallingAnyway(t *testing.T) {
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called.Store(true)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	for _, tc := range []struct {
		name   string
		key    string
		tenant string
	}{
		{"neither", "", ""},
		{"no key", "", "a-tenant"},
		{"no tenant", "a-key", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := NewPulseMCP(PulseMCPOptions{
				BaseURL: server.URL, HTTPClient: server.Client(),
				APIKey: tc.key, Tenant: tc.tenant,
			})
			_, err := client.List(context.Background(), Query{})
			if !errors.Is(err, ErrPulseMCPUnconfigured) {
				t.Errorf("List err = %v, want ErrPulseMCPUnconfigured", err)
			}
			_, err = client.Get(context.Background(), "io.example/x")
			if !errors.Is(err, ErrPulseMCPUnconfigured) {
				t.Errorf("Get err = %v, want ErrPulseMCPUnconfigured", err)
			}
		})
	}
	if called.Load() {
		t.Error("a request was sent with no credentials to send")
	}
}

// TestPulseMCP_ARefusedCredentialSaysWhichEndIsAtFault.
//
// A 401 from a catalogue is this deployment's configuration, not a third party
// having a bad day, and an operator reading "answered 401 Unauthorized" has no
// reason to connect it to a key they never set.
func TestPulseMCP_ARefusedCredentialSaysWhichEndIsAtFault(t *testing.T) {
	client := newPulseMCP(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"Invalid or missing API key","code":"unauthorized"}`))
	})
	_, err := client.List(context.Background(), Query{})
	if err == nil {
		t.Fatal("a refused credential was not reported")
	}
	for _, want := range []string{"401", "pulsemcp_api_key_ref", "pulsemcp_tenant"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
	// The far end's error text is a third party's prose and is not passed
	// through; the status is the fact.
	if strings.Contains(err.Error(), "Invalid or missing API key") {
		t.Errorf("err = %v, want the far end's body left out of it", err)
	}
}

// TestPulseMCP_NoCacheWithNoValidatorIsHandledByTheOrdinaryCache.
//
// PulseMCP sends `cache-control: no-cache` and offers no ETag, which is a
// shape the cache already has an answer for: `no-cache` with nothing to
// revalidate against becomes a minute, rather than being ignored or taken as
// "never store". Confirmed here rather than special-cased in the source.
func TestPulseMCP_NoCacheWithNoValidatorIsHandledByTheOrdinaryCache(t *testing.T) {
	var calls atomic.Int32
	list := pulseFixture(t, "list-example.json")
	upstream := newPulseMCP(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(list)
	})

	cached := NewCached(upstream, CacheOptions{Store: NewCacheStore(0)})
	t.Cleanup(func() { _ = cached.Close() })

	for range 3 {
		if _, err := cached.List(context.Background(), Query{}); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 -- no-cache is held for %s, not ignored",
			got, noCacheTTL)
	}
}

// TestPulseMCPGet_WithholdsANonActiveEntry.
//
// An entry the catalogue does not show must not be reachable by typing its
// name: a withdrawn server is withheld, not merely hidden.
func TestPulseMCPGet_WithholdsANonActiveEntry(t *testing.T) {
	raw := pulseFixture(t, "detail-example.json")
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(body["_meta"], &meta); err != nil {
		t.Fatal(err)
	}
	meta[pulseMCPMetaKey] = json.RawMessage(`{"status":"deleted","isLatest":true}`)
	body["_meta"] = mustMarshal(t, meta)
	withdrawn := mustMarshal(t, body)

	client := newPulseMCP(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(withdrawn)
	})
	if _, err := client.Get(context.Background(), "io.github.modelcontextprotocol/filesystem"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound for a withdrawn entry", err)
	}
}

// TestPulseMCP_RefusesARedirect.
//
// It matters more here than for the sources that carry no credential: this one
// sends a tenant key on every request, and Go's own defence against handing it
// to a redirect target is only as good as the redirect never being followed.
func TestPulseMCP_RefusesARedirect(t *testing.T) {
	var leaked atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "" {
			leaked.Store(true)
		}
		_, _ = w.Write([]byte(`{"servers":[],"metadata":{}}`))
	}))
	t.Cleanup(elsewhere.Close)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	t.Cleanup(server.Close)

	client := NewPulseMCP(PulseMCPOptions{
		BaseURL: server.URL, APIKey: "a-key", Tenant: "a-tenant",
	})
	_, err := client.List(context.Background(), Query{})
	if err == nil || !strings.Contains(err.Error(), "refused a redirect") {
		t.Errorf("err = %v, want a refused redirect", err)
	}
	if leaked.Load() {
		t.Error("the tenant key was sent to the redirect target")
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
