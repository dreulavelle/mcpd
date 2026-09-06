package backup

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The out-of-process plugins travel in the archive, because an instance
// restored without them comes up configured for integrations that are not on
// the host.
//
// What is easy to get wrong is the mode: the external runner refuses a plugin
// without its executable bit, and every other member is deliberately written as
// 0600 so a restored private key does not become world-readable.

// pluginsIn writes a small plugin tree and returns its directory.
func pluginsIn(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "plugins")
	if err := os.MkdirAll(filepath.Join(dir, "textable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "textable", "textable"),
		[]byte("#!/bin/sh\necho hello\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "textable", "plugin.json"),
		[]byte(`{"name":"textable"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A plugin binary keeps its executable bit all the way through an archive and
// back out of one.
//
// internal/plugins/external refuses to start a plugin whose file has no 0111,
// so dropping the bit turns a restore into a host that comes up with the
// integration configured and unable to run. It is the sort of failure that
// looks like something else entirely.
func TestPluginBinaryKeepsItsExecutableBitThroughCreateAndRestore(t *testing.T) {
	source, opts := instance(t, []byte("SQLite format 3\x00 with plugins"))
	opts.PluginsDir = pluginsIn(t, source)

	archive := create(t, opts)
	manifest, members := read(t, archive, passphrase)

	body, ok := members["plugins/textable/textable"]
	if !ok {
		names := make([]string, 0, len(members))
		for name := range members {
			names = append(names, name)
		}
		t.Fatalf("the archive holds %v, and no plugin", names)
	}
	if !strings.Contains(string(body), "echo hello") {
		t.Errorf("the plugin's contents did not travel: %q", body)
	}
	member, found := manifest.Member("plugins/textable/textable")
	if !found || !member.Restored {
		t.Fatalf("the manifest says %+v; a plugin has to be restored", member)
	}

	// Restore into a host that has a plugins directory of its own.
	target := t.TempDir()
	targetPlugins := filepath.Join(target, "plugins")
	if err := os.MkdirAll(targetPlugins, 0o700); err != nil {
		t.Fatal(err)
	}
	stage := stageOpts(t, target)
	stage.PluginsDir = targetPlugins

	if _, err := Stage(context.Background(), bytes.NewReader(archive), stage); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := ApplyPending(target, stage.DatabasePath, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	restored := filepath.Join(targetPlugins, "textable", "textable")
	info, err := os.Stat(restored)
	if err != nil {
		t.Fatalf("the plugin is not there after the restore: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the restored plugin is mode %v; internal/plugins/external "+
			"refuses one without an executable bit", info.Mode().Perm())
	}
	// The owner's bits and nothing else: the machine the archive came from does
	// not get to decide that a binary is group- or world-writable here.
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("the restored plugin is mode %v; only the owner's bits travel",
			info.Mode().Perm())
	}

	// A plugin's ordinary files are not made executable on the way.
	manifestInfo, err := os.Stat(filepath.Join(targetPlugins, "textable", "plugin.json"))
	if err != nil {
		t.Fatalf("the plugin's manifest is not there: %v", err)
	}
	if manifestInfo.Mode().Perm()&0o111 != 0 {
		t.Errorf("plugin.json came back as mode %v", manifestInfo.Mode().Perm())
	}
}

// A restore merges into the plugins directory per file rather than replacing
// it.
//
// A directory an operator manages by hand is not something a restore should be
// able to empty: a plugin this host has and the archive does not is one
// somebody installed since the backup was taken.
func TestRestoreMergesIntoThePluginsDirectory(t *testing.T) {
	source, opts := instance(t, []byte("SQLite format 3\x00 merged"))
	opts.PluginsDir = pluginsIn(t, source)
	archive := create(t, opts)

	target := t.TempDir()
	targetPlugins := filepath.Join(target, "plugins")
	if err := os.MkdirAll(filepath.Join(targetPlugins, "newer"), 0o700); err != nil {
		t.Fatal(err)
	}
	survivor := filepath.Join(targetPlugins, "newer", "newer")
	if err := os.WriteFile(survivor, []byte("installed since the backup"), 0o700); err != nil {
		t.Fatal(err)
	}

	stage := stageOpts(t, target)
	stage.PluginsDir = targetPlugins
	if _, err := Stage(context.Background(), bytes.NewReader(archive), stage); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := ApplyPending(target, stage.DatabasePath, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if _, err := os.Stat(survivor); err != nil {
		t.Errorf("a plugin the archive did not carry was removed by the restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetPlugins, "textable", "textable")); err != nil {
		t.Errorf("the archive's own plugin did not arrive: %v", err)
	}
}

// A symlink in the plugins directory is left out and named in the log.
//
// A symlink written into a tar is a write-outside primitive on the way back:
// safeName checks a member's own name and has nothing to say about where a link
// points. Skipping it silently would leave a plugin that did not travel and
// nothing saying so.
func TestASymlinkInThePluginsDirectoryIsLeftOutAndSaidSo(t *testing.T) {
	source, opts := instance(t, []byte("SQLite format 3\x00 links"))
	dir := pluginsIn(t, source)
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "sneaky")); err != nil {
		t.Skipf("this filesystem will not make a symlink: %v", err)
	}
	opts.PluginsDir = dir

	var logged bytes.Buffer
	opts.Log = slog.New(slog.NewTextHandler(&logged, nil))

	_, members := read(t, create(t, opts), passphrase)
	if _, found := members["plugins/sneaky"]; found {
		t.Error("a symlink travelled in the archive")
	}
	if !strings.Contains(logged.String(), "sneaky") {
		t.Errorf("nothing said the symlink was left out; the log holds %q", logged.String())
	}
}

// The plugins directory is a bind mount an operator writes into, and nothing
// stops a build artefact or a container image landing in it. The budget makes
// that a refusal naming the directory rather than an archive nobody can
// restore, since Open bounds what a restore will read.
func TestABigPluginsDirectoryIsRefusedWithASentenceNamingIt(t *testing.T) {
	source, opts := instance(t, []byte("SQLite format 3\x00 too big"))
	dir := pluginsIn(t, source)
	if err := os.WriteFile(filepath.Join(dir, "huge.bin"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.PluginsDir = dir
	opts.MaxPluginBytes = 1024

	var buf bytes.Buffer
	err := Create(context.Background(), &buf, opts)
	if err == nil {
		t.Fatal("an oversized plugins directory was carried")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the refusal does not name the directory: %v", err)
	}
}

// A host with no plugins directory at all is the ordinary state of one that has
// never had an external plugin, and is not an error.
func TestNoPluginsDirectoryIsNotAFailure(t *testing.T) {
	source, opts := instance(t, []byte("SQLite format 3\x00 none"))
	opts.PluginsDir = filepath.Join(source, "plugins")

	_, members := read(t, create(t, opts), passphrase)
	for name := range members {
		if strings.HasPrefix(name, "plugins/") {
			t.Errorf("the archive holds %s from a directory that does not exist", name)
		}
	}
}

// An archive carrying plugins landing on a host with nowhere to put them is
// refused rather than half-restored, which is the same rule every other member
// follows.
func TestAnArchiveWithPluginsIsRefusedWhenThereIsNowhereToPutThem(t *testing.T) {
	source, opts := instance(t, []byte("SQLite format 3\x00 homeless"))
	opts.PluginsDir = pluginsIn(t, source)
	archive := create(t, opts)

	target := t.TempDir()
	stage := stageOpts(t, target) // no PluginsDir
	_, err := Stage(context.Background(), bytes.NewReader(archive), stage)
	if err == nil {
		t.Fatal("an archive holding plugins was staged onto a host with nowhere for them")
	}
	if !strings.Contains(err.Error(), "plugins") {
		t.Errorf("the refusal does not say what could not be placed: %v", err)
	}
}
