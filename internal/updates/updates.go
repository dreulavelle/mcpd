// Package updates asks where releases are published what the current version
// is, and says nothing about installing one.
//
// Checking and applying are deliberately different problems. A check is an
// outbound HTTPS request that can be switched off; applying an update means
// replacing a running binary or a container image, which needs privileges this
// host drops on purpose. Conflating them would put a button in the dashboard
// that could only ever work by handing mcpd the ability to rewrite its own
// deployment.
package updates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultRepository is where mcpd's own releases live.
const DefaultRepository = "dreulavelle/mcpd"

// maxBody bounds what is read from the release API.
//
// Release notes are prose written by whoever cut the release, so there is no
// size the format guarantees. A megabyte is far more than any changelog and
// far less than a response that could hurt this process.
const maxBody = 1 << 20

// Release is one published version.
type Release struct {
	Version     string    `json:"version"`
	Name        string    `json:"name,omitempty"`
	URL         string    `json:"url,omitempty"`
	PublishedAt time.Time `json:"published_at,omitzero"`
	Notes       string    `json:"notes,omitempty"`
	Prerelease  bool      `json:"prerelease,omitempty"`
}

// Status is what the dashboard renders.
type Status struct {
	// Enabled reports whether checking is switched on. A disabled check is
	// reported rather than hidden, so an operator wondering why the version
	// never changes can see that nothing is asking.
	Enabled bool `json:"enabled"`
	// Current is the running version, as built in.
	Current string `json:"current"`
	// Latest is the newest published release, empty if nothing is known yet.
	Latest string `json:"latest,omitempty"`
	// UpdateAvailable is false when the comparison could not be made, which
	// is not the same as being up to date -- Newer carries that distinction.
	UpdateAvailable bool `json:"update_available"`
	// Newer are the releases between Current and Latest, newest first, so the
	// dashboard can show what an upgrade would actually bring rather than
	// only the newest release's notes.
	Newer []Release `json:"newer,omitempty"`
	// CheckedAt is when the answer below was fetched.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// Error is the last failure, if the last attempt failed. Reported rather
	// than logged only: a check that has been silently failing for a month
	// looks exactly like a host that is up to date.
	Error string `json:"error,omitempty"`
	// Comparable reports whether Current could be understood as a version at
	// all. A development build cannot be compared with a release, and saying
	// so is better than claiming an update is available on every start.
	Comparable bool `json:"comparable"`
}

// Config is what a Checker needs, resolved fresh on every check so that
// turning the feature on takes effect without a restart.
type Config struct {
	Enabled    bool
	Repository string
	Interval   time.Duration
}

// Checker fetches and caches the release list.
type Checker struct {
	current string
	client  *http.Client
	now     func() time.Time
	// resolve reads the current configuration. A function rather than a
	// struct field because these are settings an operator edits while the
	// process runs.
	resolve func() Config

	mu     sync.Mutex
	cached Status
	// inflight guards against several dashboard tabs each triggering their
	// own request to the same endpoint.
	inflight bool
}

// New builds a Checker. A nil client takes a sensible default: this talks to
// one public HTTPS endpoint and should not wait long enough to hold a page.
func New(current string, resolve func() Config, client *http.Client, now func() time.Time) *Checker {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	return &Checker{current: current, client: client, now: now, resolve: resolve}
}

// Status returns what is known, fetching if the cache is cold or stale.
//
// A failed fetch does not discard a previous good answer: the version that was
// current an hour ago is better information than nothing, and the error is
// carried alongside it rather than in place of it.
func (c *Checker) Status(ctx context.Context, force bool) Status {
	cfg := c.resolve()

	c.mu.Lock()
	cached := c.cached
	busy := c.inflight
	c.mu.Unlock()

	cached.Enabled = cfg.Enabled
	cached.Current = c.current
	cached.Comparable = parse(c.current) != nil

	if !cfg.Enabled {
		// Nothing is asking, so nothing can be claimed. The cached answer is
		// kept but not presented as current.
		cached.UpdateAvailable = false
		return cached
	}
	if busy || (!force && !c.stale(cached, cfg.Interval)) {
		return cached
	}

	fresh, err := c.fetch(ctx, cfg)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.cached.Enabled = true
		c.cached.Current = c.current
		c.cached.Comparable = cached.Comparable
		c.cached.Error = err.Error()
		return c.cached
	}
	c.cached = fresh
	return c.cached
}

func (c *Checker) stale(s Status, interval time.Duration) bool {
	if s.CheckedAt.IsZero() {
		return true
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return c.now().Sub(s.CheckedAt) >= interval
}

func (c *Checker) fetch(ctx context.Context, cfg Config) (Status, error) {
	c.mu.Lock()
	c.inflight = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.inflight = false
		c.mu.Unlock()
	}()

	repo := strings.TrimSpace(cfg.Repository)
	if repo == "" {
		repo = DefaultRepository
	}
	if strings.Count(repo, "/") != 1 || strings.HasPrefix(repo, "/") || strings.HasSuffix(repo, "/") {
		return Status{}, fmt.Errorf("repository %q is not owner/name", repo)
	}

	url := "https://api.github.com/repos/" + repo + "/releases?per_page=20"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Status{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "mcpd/"+c.current)

	resp, err := c.client.Do(req)
	if err != nil {
		return Status{}, fmt.Errorf("asking %s for releases: %w", repo, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Status{}, fmt.Errorf("reading the release list: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return Status{}, fmt.Errorf("no repository %s, or it publishes no releases", repo)
	}
	if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "rate limit") {
		// Unauthenticated GitHub requests are rate limited per address, and a
		// shared egress address makes that easy to hit. Said plainly so it is
		// not read as the repository being gone.
		return Status{}, errors.New("GitHub is rate limiting this address; the next check should succeed")
	}
	if resp.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("the release list answered HTTP %d", resp.StatusCode)
	}

	var raw []struct {
		TagName     string `json:"tag_name"`
		Name        string `json:"name"`
		HTMLURL     string `json:"html_url"`
		Body        string `json:"body"`
		Draft       bool   `json:"draft"`
		Prerelease  bool   `json:"prerelease"`
		PublishedAt string `json:"published_at"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Status{}, fmt.Errorf("the release list was not the JSON expected: %w", err)
	}

	out := Status{
		Enabled:    true,
		Current:    c.current,
		CheckedAt:  c.now(),
		Comparable: parse(c.current) != nil,
	}
	running := parse(c.current)

	for _, r := range raw {
		// A draft is not published and a prerelease is not what an operator
		// running a deployment should be told is available.
		if r.Draft || r.Prerelease {
			continue
		}
		v := parse(r.TagName)
		if v == nil {
			continue
		}
		rel := Release{
			Version:    strings.TrimPrefix(r.TagName, "v"),
			Name:       r.Name,
			URL:        r.HTMLURL,
			Notes:      r.Body,
			Prerelease: r.Prerelease,
		}
		if t, err := time.Parse(time.RFC3339, r.PublishedAt); err == nil {
			rel.PublishedAt = t
		}
		if out.Latest == "" || compare(v, parse(out.Latest)) > 0 {
			out.Latest = rel.Version
		}
		if running != nil && compare(v, running) > 0 {
			out.Newer = append(out.Newer, rel)
		}
	}

	// Newest first: an operator reads what is coming next before what came
	// four releases ago. Sorted rather than assumed -- the API happens to
	// answer in this order today, and a listing that quietly reversed if it
	// stopped doing so would put the oldest release at the top of an "update
	// available" panel.
	sort.SliceStable(out.Newer, func(i, j int) bool {
		return compare(parse(out.Newer[i].Version), parse(out.Newer[j].Version)) > 0
	})
	out.UpdateAvailable = running != nil && len(out.Newer) > 0
	return out, nil
}

// version is a parsed semantic version, reduced to what an ordering needs.
type version struct{ major, minor, patch int }

// parse reads vMAJOR.MINOR.PATCH, tolerating the v and any build or
// prerelease suffix. It returns nil for anything it cannot order, which
// includes "dev" -- a build that does not name a version must not be reported
// as behind one.
func parse(s string) *version {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return nil
	}
	// A -dirty, -rc1 or +build suffix does not change which release a version
	// sits after.
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return nil
	}
	out := &version{}
	fields := []*int{&out.major, &out.minor, &out.patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		*fields[i] = n
	}
	return out
}

func compare(a, b *version) int {
	if a == nil || b == nil {
		return 0
	}
	switch {
	case a.major != b.major:
		return sign(a.major - b.major)
	case a.minor != b.minor:
		return sign(a.minor - b.minor)
	default:
		return sign(a.patch - b.patch)
	}
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
	}
}
