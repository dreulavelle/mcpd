package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func supersede(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(dir, supersededDir, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, databaseName), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func remaining(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, supersededDir))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// Each copy is the size of a database. Working out which archive is the right
// one takes a few tries, and without a bound those tries fill the volume.
func TestPruneKeepsTheMostRecent(t *testing.T) {
	dir := t.TempDir()
	supersede(t, dir,
		"20260101T000000Z", "20260102T000000Z", "20260103T000000Z",
		"20260104T000000Z", "20260105T000000Z")

	PruneSuperseded(dir, 3, discard())

	got := remaining(t, dir)
	want := []string{"20260103T000000Z", "20260104T000000Z", "20260105T000000Z"}
	if len(got) != len(want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("kept %v, want %v", got, want)
			break
		}
	}
}

// The newest is the instance a restore replaced moments ago. Whatever else
// this removes, it must never be that one.
func TestPruneKeepsAtLeastTheLatest(t *testing.T) {
	dir := t.TempDir()
	supersede(t, dir, "20260101T000000Z", "20260105T000000Z")

	PruneSuperseded(dir, 1, discard())

	got := remaining(t, dir)
	if len(got) != 1 || got[0] != "20260105T000000Z" {
		t.Fatalf("kept %v, want only the newest", got)
	}
}

func TestPruneLeavesFewerThanTheLimitAlone(t *testing.T) {
	dir := t.TempDir()
	supersede(t, dir, "20260101T000000Z", "20260102T000000Z")

	PruneSuperseded(dir, 3, discard())

	if got := remaining(t, dir); len(got) != 2 {
		t.Fatalf("kept %v, want both", got)
	}
}

// This removes directories, so it removes only ones it recognises as its own.
// Anything an operator put there by hand stays.
func TestPruneIgnoresWhatItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	supersede(t, dir, "20260101T000000Z", "20260102T000000Z",
		"20260103T000000Z", "20260104T000000Z")
	mine := filepath.Join(dir, supersededDir, "before-the-migration")
	if err := os.MkdirAll(mine, 0o700); err != nil {
		t.Fatal(err)
	}

	PruneSuperseded(dir, 2, discard())

	if _, err := os.Stat(mine); err != nil {
		t.Errorf("a directory this did not write was removed: %v", err)
	}
}

func TestPruneOnAHostThatHasNeverRestored(t *testing.T) {
	// Must not create the directory, or complain that it is missing.
	dir := t.TempDir()
	PruneSuperseded(dir, 3, discard())

	if _, err := os.Stat(filepath.Join(dir, supersededDir)); !os.IsNotExist(err) {
		t.Error("pruning created a directory on a host with nothing to prune")
	}
}
