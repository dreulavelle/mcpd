package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The example is what somebody copies, so it has to load, and it has to say
// nothing about the keys that moved.
//
// The second half is the one that rots. A key left in the example after it
// moved is worse than a stale comment: it is an instruction to write something
// mcpd will read once and then ignore.
func TestTheExampleConfigLoadsAndSaysNothingThatMoved(t *testing.T) {
	t.Setenv("MCPD_TOKEN_CHATGPT", "not-used-here-but-referenced")

	// Copied into a temp directory so a stray .env beside the repo copy cannot
	// change the result.
	raw, err := os.ReadFile(filepath.Join("..", "..", "configs", "example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the example config does not load: %v", err)
	}
	if cfg.Legacy().Any() {
		t.Errorf("the example still sets keys that live in the database: %v",
			cfg.Legacy().Sources())
	}
	if cfg.Server.Listen == "" || cfg.Storage.Path == "" || cfg.SecretKeyRef == "" {
		t.Error("the example no longer sets the keys that do belong in a file")
	}
}
