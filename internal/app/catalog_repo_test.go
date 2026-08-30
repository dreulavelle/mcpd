package app

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/spoked/mcpd/internal/config"
	"github.com/spoked/mcpd/internal/registry"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func aRepo() *registry.Repo {
	return registry.NewRepo(registry.RepoOptions{
		URL:   func(context.Context) string { return "https://example.test/catalog.tar.gz" },
		Token: func(context.Context) string { return "" },
	})
}

// The bug this exists for. `catalog.enabled` says whether this host browses
// the public catalogues, which is a question about reaching third parties. An
// operator who turned those off and then wrote their own list has answered it,
// and having their own list silently disabled by the same flag would be
// unexplainable from the dashboard.
func TestCatalog_YourOwnListSurvivesThePublicOnesBeingOff(t *testing.T) {
	catalog := buildCatalog(config.Catalog{}, aRepo(), nil, quietLog())
	if catalog == nil {
		t.Fatal("a self-hosted catalogue was dropped because the public ones are off")
	}
	t.Cleanup(func() { _ = catalog.Close() })

	if got := catalog.Sources(); len(got) != 1 || got[0] != "self-hosted" {
		t.Errorf("sources = %v, want just the operator's own list", got)
	}
}

// Preference order decides which copy of a server survives deduplication, and
// the list somebody here reviewed beats every third party's description of it.
func TestCatalog_YourOwnListIsPreferred(t *testing.T) {
	catalog := buildCatalog(config.Catalog{Official: true, Docker: true},
		aRepo(), nil, quietLog())
	if catalog == nil {
		t.Fatal("catalog = nil")
	}
	t.Cleanup(func() { _ = catalog.Close() })

	got := catalog.Sources()
	if len(got) != 3 || got[0] != "self-hosted" {
		t.Fatalf("sources = %v, want the operator's own list first", got)
	}
}

// Nothing configured at all is still "no catalogue", not an empty one: the
// handler says so rather than answering every browse with nothing.
func TestCatalog_NothingConfiguredAtAll(t *testing.T) {
	if catalog := buildCatalog(config.Catalog{}, nil, nil, quietLog()); catalog != nil {
		t.Fatalf("catalog = %+v, want none when nothing is configured", catalog)
	}
}
