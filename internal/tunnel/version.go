package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// The tunnel client is compiled into mcpd, so its version is fixed at build
// time and cannot change while the process runs. That is the point of
// embedding: the code that runs is the code that was built and reviewed.
//
// What mcpd can usefully do is say when a newer release exists, so an operator
// knows to rebuild. It reports; it never downloads or executes anything. A
// service that fetched and ran new code at startup would mean two machines
// with identical images behaving differently, and an upstream compromise
// reaching production without anyone deploying.

// DefaultReleaseURL is GitHub's latest-release endpoint for the tunnel client.
const DefaultReleaseURL = "https://api.github.com/repos/openai/tunnel-client/releases/latest"

// VersionInfo reports the embedded version against the newest release.
type VersionInfo struct {
	// Embedded is the version compiled into this binary.
	Embedded string `json:"embedded"`
	// Latest is the newest published release, empty if the check did not run
	// or could not complete.
	Latest string `json:"latest,omitempty"`
	// UpdateAvailable is true when they differ.
	UpdateAvailable bool `json:"update_available"`
	// CheckedAt records when the comparison last succeeded.
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	// Note explains what to do, since an operator cannot act on a version
	// string alone.
	Note string `json:"note,omitempty"`
}

// EmbeddedVersion reads the compiled-in tunnel client version from the build
// info, so it cannot drift from what is actually linked.
func EmbeddedVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range info.Deps {
		if dep.Path == "github.com/openai/tunnel-client" {
			if dep.Replace != nil {
				return dep.Replace.Version
			}
			return dep.Version
		}
	}
	return "unknown"
}

// CheckLatest compares the embedded version against the newest release.
//
// A failure is not an error the caller should act on: an air-gapped host, a
// rate limit, or GitHub being down are all ordinary, and none of them should
// affect startup. The result simply reports what it knows.
func CheckLatest(ctx context.Context, client *http.Client, releaseURL string, log *slog.Logger) VersionInfo {
	info := VersionInfo{Embedded: EmbeddedVersion()}
	if releaseURL == "" {
		releaseURL = DefaultReleaseURL
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return info
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "mcpd")

	resp, err := client.Do(req)
	if err != nil {
		log.DebugContext(ctx, "could not check for a newer tunnel client", "error", err)
		return info
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.DebugContext(ctx, "tunnel client release check returned an unexpected status",
			"status", resp.StatusCode)
		return info
	}

	var release struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return info
	}
	if release.Draft || release.Prerelease || release.TagName == "" {
		return info
	}

	now := time.Now()
	info.Latest = release.TagName
	info.CheckedAt = &now
	info.UpdateAvailable = normalizeVersion(release.TagName) != normalizeVersion(info.Embedded)

	if info.UpdateAvailable {
		info.Note = fmt.Sprintf(
			"mcpd was built against tunnel-client %s and %s is available. The client is "+
				"compiled in, so updating means rebuilding mcpd: "+
				"go get github.com/openai/tunnel-client@%s",
			info.Embedded, info.Latest, info.Latest)
	}
	return info
}

// normalizeVersion strips a leading v so v0.0.12 and 0.0.12 compare equal.
func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// Checker runs the release check periodically.
//
// Daily rather than hourly: a new release is not urgent, the answer changes
// slowly, and an unauthenticated GitHub API has a rate limit shared by
// everything behind the same address.
type Checker struct {
	client     *http.Client
	log        *slog.Logger
	interval   time.Duration
	releaseURL string

	mu     sync.RWMutex
	latest VersionInfo
}

// NewChecker builds a periodic release checker.
func NewChecker(client *http.Client, log *slog.Logger, interval time.Duration) *Checker {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	return &Checker{
		client:     client,
		log:        log,
		interval:   interval,
		releaseURL: DefaultReleaseURL,
		latest:     VersionInfo{Embedded: EmbeddedVersion()},
	}
}

// Info returns the most recent result.
func (c *Checker) Info() VersionInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest
}

// Run checks at startup and then on the interval until ctx is cancelled.
func (c *Checker) Run(ctx context.Context) error {
	c.check(ctx)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			c.check(ctx)
		}
	}
}

func (c *Checker) check(ctx context.Context) {
	info := CheckLatest(ctx, c.client, c.releaseURL, c.log)

	c.mu.Lock()
	c.latest = info
	c.mu.Unlock()

	if info.UpdateAvailable {
		c.log.InfoContext(ctx, "a newer tunnel client is available",
			"embedded", info.Embedded, "latest", info.Latest,
			"action", "rebuild mcpd to pick it up")
	}
}
