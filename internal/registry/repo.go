package registry

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spoked/mcpd/internal/mcpservers"
)

// Repo is a catalogue an operator hosts themselves, fetched as a tarball.
//
// The other four sources answer "what exists in the world". This one answers a
// different question -- "what are we allowed to run here" -- and that is why it
// is worth having despite overlapping with them. A list in a git repository has
// review, history and blame already attached, which is the shape that suits an
// operator who has to justify the list to somebody else.
//
// A tarball rather than the git protocol, and no git dependency. Every host
// people actually use serves one over HTTPS: GitHub at
// /repos/{owner}/{repo}/tarball/{ref}, GitLab and Gitea at their own archive
// paths, and any static file server at whatever it is asked for. Implementing
// enough of git to clone a repository in order to read a directory of JSON
// files would be a large dependency bought for nothing.
//
// The entries are server.json documents -- the format this host already parses,
// already validates and already imports. Inventing an mcpd-specific catalogue
// schema would mean an operator maintaining their list in a format only this
// program reads, and a second parser to keep in step with the first.
type Repo struct {
	name      string
	url       func(context.Context) string
	token     func(context.Context) string
	client    *http.Client
	userAgent string

	mu        sync.RWMutex
	entries   []Entry
	documents map[string][]byte
	fetchedAt time.Time
	err       error
}

// RepoOptions configures the source.
type RepoOptions struct {
	// Name is what the entries say they came from. "self-hosted" rather than a
	// hostname, because the point is that this list is the operator's own.
	Name string
	// URL and Token are read per fetch, so an operator changing either does
	// not have to restart anything.
	URL       func(context.Context) string
	Token     func(context.Context) string
	UserAgent string
}

const (
	// repoFetchTimeout bounds one fetch. Generous: a large archive over a slow
	// link is still a legitimate catalogue.
	repoFetchTimeout = 2 * time.Minute
	// maxRepoArchive bounds the compressed download.
	maxRepoArchive = 64 << 20
	// maxRepoUnpacked bounds what the archive expands to, so a tarball that
	// decompresses to a hundred gigabytes costs a refused fetch rather than a
	// filled disk. Nothing is written to disk here -- the documents are held
	// in memory -- which makes the bound a memory limit and therefore one this
	// host must set rather than the party supplying the archive.
	maxRepoUnpacked = 32 << 20
	// maxRepoEntries bounds how many documents one catalogue contributes.
	maxRepoEntries = 2000
)

// NewRepo builds the source. It reaches nothing until Refresh is called.
func NewRepo(opts RepoOptions) *Repo {
	name := opts.Name
	if name == "" {
		name = "self-hosted"
	}
	return &Repo{
		name:      name,
		url:       opts.URL,
		token:     opts.Token,
		userAgent: opts.UserAgent,
		client: &http.Client{
			Timeout: repoFetchTimeout,
			// Redirects are followed, which is required: GitHub's tarball
			// endpoint answers 302 with a signed URL on another host. Go
			// strips the Authorization header across hosts, which is the
			// behaviour wanted here -- the signed URL carries its own
			// authority and this host's token has no business travelling to
			// whatever the redirect names.
		},
		documents: map[string][]byte{},
	}
}

func (r *Repo) Source() string { return r.name }

// Configured reports whether an address has been set.
func (r *Repo) Configured(ctx context.Context) bool { return r.url(ctx) != "" }

// Status is what the dashboard says about this catalogue.
type RepoStatus struct {
	Configured bool      `json:"configured"`
	Entries    int       `json:"entries"`
	FetchedAt  time.Time `json:"fetched_at,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Status reports the last fetch.
func (r *Repo) Status(ctx context.Context) RepoStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	status := RepoStatus{
		Configured: r.url(ctx) != "",
		Entries:    len(r.entries),
		FetchedAt:  r.fetchedAt,
	}
	if r.err != nil {
		status.Error = r.err.Error()
	}
	return status
}

// Refresh fetches the archive and replaces what this source offers.
//
// All or nothing. A fetch that fails leaves the previous entries in place --
// a catalogue that briefly could not be reached should not empty an operator's
// allowlist -- and the failure is reported through Status so the page can say
// the list is not being confirmed.
func (r *Repo) Refresh(ctx context.Context) error {
	url := r.url(ctx)
	if url == "" {
		r.mu.Lock()
		r.entries, r.documents, r.err = nil, map[string][]byte{}, nil
		r.mu.Unlock()
		return nil
	}

	entries, documents, err := r.fetch(ctx, url)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
	if err != nil {
		return err
	}
	r.entries, r.documents, r.fetchedAt = entries, documents, time.Now()
	return nil
}

func (r *Repo) fetch(ctx context.Context, url string) ([]Entry, map[string][]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, repoFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: build request: %w", err)
	}
	if token := r.token(ctx); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if r.userAgent != "" {
		req.Header.Set("User-Agent", r.userAgent)
	}
	// GitHub's API needs this to serve the archive rather than JSON.
	req.Header.Set("Accept", "application/vnd.github+json, application/octet-stream, */*")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("registry: fetch the catalogue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		// The status and nothing else. A private repository answering 404
		// rather than 403 is the ordinary case and the message says so, but
		// the body could be anything and is not copied.
		return nil, nil, fmt.Errorf(
			"registry: the catalogue answered %s. A private repository that is "+
				"not reachable with this token answers 404 rather than 403",
			resp.Status)
	}

	return readArchive(io.LimitReader(resp.Body, maxRepoArchive), r.name)
}

// readArchive turns a tarball into entries.
//
// Every JSON file is tried as a server.json and anything that is not one is
// skipped rather than refused: a repository holding a README, a licence and a
// workflow beside its documents is the ordinary shape of one, and a catalogue
// that failed to load because of a lint config would be useless.
func readArchive(body io.Reader, source string) ([]Entry, map[string][]byte, error) {
	gz, err := gzip.NewReader(body)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"registry: this is not a gzipped tar archive. A catalogue address "+
				"should point at a tarball -- for GitHub that is "+
				"/repos/{owner}/{repo}/tarball/{ref}: %w", err)
	}
	defer gz.Close()

	var (
		entries   []Entry
		documents = map[string][]byte{}
		unpacked  int64
	)
	archive := tar.NewReader(gz)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("registry: read the catalogue archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || !strings.HasSuffix(header.Name, ".json") {
			continue
		}
		// Nothing is written to disk, so a traversing path cannot escape
		// anywhere -- but a name is still used as a key and shown to an
		// operator, so it is cleaned rather than trusted.
		if strings.Contains(path.Clean(header.Name), "..") {
			continue
		}
		if len(entries) >= maxRepoEntries {
			return nil, nil, fmt.Errorf(
				"registry: the catalogue holds more than %d documents", maxRepoEntries)
		}

		remaining := maxRepoUnpacked - unpacked
		if remaining <= 0 {
			return nil, nil, fmt.Errorf(
				"registry: the catalogue expands to more than %d MiB", maxRepoUnpacked>>20)
		}
		document, err := io.ReadAll(io.LimitReader(archive, remaining+1))
		if err != nil {
			return nil, nil, fmt.Errorf("registry: read %s: %w", header.Name, err)
		}
		unpacked += int64(len(document))
		if int64(len(document)) > remaining {
			return nil, nil, fmt.Errorf(
				"registry: the catalogue expands to more than %d MiB", maxRepoUnpacked>>20)
		}

		entry, ok := entryFromDocument(document, source)
		if !ok {
			continue
		}
		if _, seen := documents[entry.Name]; seen {
			// Two files describing the same server. The first wins, so a
			// catalogue's own ordering decides rather than tar's.
			continue
		}
		documents[entry.Name] = document
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, documents, nil
}

// entryFromDocument describes one document the way every other source does.
func entryFromDocument(document []byte, source string) (Entry, bool) {
	var probe struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Version     string `json:"version"`
		Title       string `json:"title"`
	}
	if err := json.Unmarshal(document, &probe); err != nil || probe.Name == "" {
		// Not a server.json, or not one with an identity. Skipped, not
		// refused: a repository has other JSON in it.
		return Entry{}, false
	}
	if _, err := mcpservers.Parse(document); err != nil {
		// Parsed here as well as in describe, because a file that is JSON with
		// a name but is not a server document should not appear at all --
		// unlike a real document this host merely cannot dial, which appears
		// with its reason.
		return Entry{}, false
	}

	transport, url, addable, reason, auth := describe(document)
	title := probe.Title
	if title == "" {
		title = probe.Name
	}
	return Entry{
		Name:          probe.Name,
		SuggestedName: SuggestName(probe.Name),
		Title:         title,
		Description:   probe.Description,
		Version:       probe.Version,
		Transport:     transport,
		URL:           url,
		Addable:       addable,
		Reason:        reason,
		Auth:          auth,
		Source:        source,
	}, true
}

// List returns a page from what the last fetch loaded.
//
// Served from memory and never from the network: an operator's own list is
// small, and a page request that reached out to a git host would make browsing
// the marketplace depend on that host being up.
func (r *Repo) List(ctx context.Context, q Query) (Page, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	matched := make([]Entry, 0, len(r.entries))
	needle := strings.ToLower(q.Search)
	for _, e := range r.entries {
		if needle == "" ||
			strings.Contains(strings.ToLower(e.Name), needle) ||
			strings.Contains(strings.ToLower(e.Title), needle) {
			matched = append(matched, e)
		}
	}

	limit := q.Limit
	if limit <= 0 || limit > len(matched) {
		limit = len(matched)
	}
	// Where to resume. The cursor is an entry's name, and the entry it names
	// may be gone -- a refresh between two pages is exactly what this source
	// does on a timer. So it resumes at the first name that sorts at or after
	// the cursor rather than at an exact match: falling back to the start
	// would hand a paging caller the first page again, with the same cursor
	// on it, and a client that walked to the end would never get there.
	//
	// Entries are sorted by name, which is what makes the search meaningful
	// and what makes the resumption point stable across a refresh.
	start := 0
	if q.Cursor != "" {
		start = sort.Search(len(matched), func(i int) bool {
			return matched[i].Name >= q.Cursor
		})
	}
	end := min(start+limit, len(matched))

	page := Page{
		Source:      r.name,
		Entries:     matched[start:end],
		RetrievedAt: r.fetchedAt,
	}
	if end < len(matched) {
		page.NextCursor = matched[end].Name
	}
	return page, nil
}

// Get returns one entry and its document.
func (r *Repo) Get(ctx context.Context, name string) (Detail, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, e := range r.entries {
		if e.Name != name {
			continue
		}
		return Detail{
			Entry:       e,
			Document:    json.RawMessage(r.documents[name]),
			RetrievedAt: r.fetchedAt,
		}, nil
	}
	return Detail{}, fmt.Errorf("%w: %s", ErrNotFound, name)
}
