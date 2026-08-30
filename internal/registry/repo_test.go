package registry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/mcpservers"
)

func serverDoc(name string) string {
	return fmt.Sprintf(`{"$schema":%q,"name":%q,"description":"a server",`+
		`"version":"1.0.0","remotes":[{"type":"streamable-http",`+
		`"url":"https://%s.example/mcp"}]}`,
		mcpservers.SchemaURI, name, strings.ReplaceAll(name, "/", "-"))
}

// archiveOf builds a gzipped tar with the named files.
func archiveOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	for _, closer := range []func() error{tw.Close, gz.Close} {
		if err := closer(); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func repoServing(t *testing.T, archive []byte) (*Repo, *httptest.Server, *string) {
	t.Helper()
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Write(archive)
	}))
	t.Cleanup(server.Close)

	repo := NewRepo(RepoOptions{
		URL:   func(context.Context) string { return server.URL },
		Token: func(context.Context) string { return "repo-token" },
	})
	return repo, server, &auth
}

// The entries are server.json documents, which is the format this host already
// parses, validates and imports.
func TestRepoReadsServerDocuments(t *testing.T) {
	repo, _, auth := repoServing(t, archiveOf(t, map[string]string{
		"catalog-main/servers/weather.json": serverDoc("io.example/weather"),
		"catalog-main/servers/tickets.json": serverDoc("io.example/tickets"),
	}))

	if err := repo.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	page, err := repo.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(page.Entries))
	}
	if *auth != "Bearer repo-token" {
		t.Errorf("authorization = %q; a private repository needs it", *auth)
	}
	// Described the same way every other source describes an entry, so the
	// marketplace does not need to know which catalogue a row came from.
	if !page.Entries[0].Addable || page.Entries[0].Transport != "streamable-http" {
		t.Errorf("entry = %+v", page.Entries[0])
	}
}

// A repository holds a README, a licence and a workflow beside its documents.
// A catalogue that failed to load because of a lint config would be useless.
func TestRepoSkipsWhatIsNotAServerDocument(t *testing.T) {
	repo, _, _ := repoServing(t, archiveOf(t, map[string]string{
		"catalog-main/README.md":            "# our catalogue",
		"catalog-main/renovate.json":        `{"extends":["config:base"]}`,
		"catalog-main/.github/workflow.yml": "on: push",
		"catalog-main/servers/weather.json": serverDoc("io.example/weather"),
	}))

	if err := repo.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	page, _ := repo.List(context.Background(), Query{})
	if len(page.Entries) != 1 {
		t.Fatalf("got %d entries, want only the server document", len(page.Entries))
	}
	if page.Entries[0].Name != "io.example/weather" {
		t.Errorf("entry = %s", page.Entries[0].Name)
	}
}

// Nothing is written to disk, but a name is still a key and is still shown.
func TestRepoIgnoresTraversingNames(t *testing.T) {
	repo, _, _ := repoServing(t, archiveOf(t, map[string]string{
		"../../escaped.json":                serverDoc("io.example/escaped"),
		"catalog-main/servers/weather.json": serverDoc("io.example/weather"),
	}))

	if err := repo.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, _ := repo.List(context.Background(), Query{})
	for _, e := range page.Entries {
		if strings.Contains(e.Name, "escaped") {
			t.Fatal("a traversing path contributed an entry")
		}
	}
}

// A catalogue that could not be reached must not empty an operator's
// allowlist. The previous entries stand and the failure is reported.
func TestRepoKeepsWhatItHadWhenAFetchFails(t *testing.T) {
	archive := archiveOf(t, map[string]string{
		"catalog-main/weather.json": serverDoc("io.example/weather"),
	})
	var fail bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(archive)
	}))
	defer server.Close()

	repo := NewRepo(RepoOptions{
		URL:   func(context.Context) string { return server.URL },
		Token: func(context.Context) string { return "" },
	})
	if err := repo.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	fail = true
	if err := repo.Refresh(context.Background()); err == nil {
		t.Fatal("a failed fetch reported success")
	}

	page, _ := repo.List(context.Background(), Query{})
	if len(page.Entries) != 1 {
		t.Fatalf("a failed fetch emptied the catalogue: %d entries", len(page.Entries))
	}
	if status := repo.Status(context.Background()); status.Error == "" {
		t.Error("the failure is not reported, so the page cannot say the list is unconfirmed")
	}
}

// An address that points at something other than a tarball is the likeliest
// mistake, and the message has to say what was expected.
func TestRepoSaysWhenItIsNotATarball(t *testing.T) {
	repo, _, _ := repoServing(t, []byte(`{"not":"a tarball"}`))

	err := repo.Refresh(context.Background())
	if err == nil {
		t.Fatal("a JSON body was accepted as an archive")
	}
	if !strings.Contains(err.Error(), "tarball") {
		t.Errorf("error = %v, want it to say what was expected", err)
	}
}

// Nothing configured is not an error, and must not leave stale entries behind.
func TestRepoWithNoAddress(t *testing.T) {
	repo := NewRepo(RepoOptions{
		URL:   func(context.Context) string { return "" },
		Token: func(context.Context) string { return "" },
	})
	if err := repo.Refresh(context.Background()); err != nil {
		t.Fatalf("an unconfigured catalogue reported an error: %v", err)
	}
	if status := repo.Status(context.Background()); status.Configured {
		t.Error("an unconfigured catalogue reports itself as configured")
	}
	page, _ := repo.List(context.Background(), Query{})
	if len(page.Entries) != 0 {
		t.Error("an unconfigured catalogue offered entries")
	}
}

func TestRepoGetReturnsTheDocument(t *testing.T) {
	repo, _, _ := repoServing(t, archiveOf(t, map[string]string{
		"catalog-main/weather.json": serverDoc("io.example/weather"),
	}))
	if err := repo.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	detail, err := repo.Get(context.Background(), "io.example/weather")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(detail.Document), "io.example/weather") {
		t.Errorf("document = %s", detail.Document)
	}

	if _, err := repo.Get(context.Background(), "io.example/nothing"); err == nil {
		t.Error("an unknown name returned an entry")
	}
}

func TestRepoSearchAndPaging(t *testing.T) {
	repo, _, _ := repoServing(t, archiveOf(t, map[string]string{
		"c/weather.json": serverDoc("io.example/weather"),
		"c/tickets.json": serverDoc("io.example/tickets"),
		"c/alerts.json":  serverDoc("io.example/alerts"),
	}))
	if err := repo.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	found, _ := repo.List(context.Background(), Query{Search: "weather"})
	if len(found.Entries) != 1 {
		t.Errorf("search returned %d entries", len(found.Entries))
	}

	first, _ := repo.List(context.Background(), Query{Limit: 2})
	if len(first.Entries) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %d entries, cursor %q", len(first.Entries), first.NextCursor)
	}
	second, _ := repo.List(context.Background(), Query{Limit: 2, Cursor: first.NextCursor})
	if len(second.Entries) != 1 || second.NextCursor != "" {
		t.Errorf("second page = %d entries, cursor %q", len(second.Entries), second.NextCursor)
	}
}
