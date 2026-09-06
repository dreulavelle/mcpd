package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localDestination(t *testing.T, dir string) Transport {
	t.Helper()
	transport, err := OpenDestination(
		Destination{Kind: KindLocal, Settings: Settings{Path: dir}}, TransportOptions{})
	if err != nil {
		t.Fatalf("open the destination: %v", err)
	}
	t.Cleanup(func() { transport.Close() })
	return transport
}

// A backup written into the directory being backed up is in the next backup,
// which doubles in size every run until the volume is full -- and a destination
// pointed at the data directory itself would have retention deleting files
// beside the live database.
//
// Anything containing the data directory is refused for the same reason: the
// archive lands one level up from the database it is a copy of, and is lost
// with the disk it exists to survive.
func TestLocalDestinationRefusesTheDataDirectory(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	inside := filepath.Join(data, "backups")
	elsewhere := filepath.Join(root, "elsewhere")
	for _, dir := range []string{data, inside, elsewhere} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name    string
		path    string
		refused bool
	}{
		{name: "the data directory itself", path: data, refused: true},
		{name: "a directory inside it", path: inside, refused: true},
		{name: "a directory holding it", path: root, refused: true},
		{name: "somewhere else entirely", path: elsewhere},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Destination{
				Name: "local", Kind: KindLocal, Policy: DefaultPolicy,
				Settings: Settings{Path: tc.path},
			}
			err := d.Validate(data)
			if tc.refused {
				if err == nil {
					t.Fatalf("%s was accepted as a backup destination", tc.path)
				}
				if !strings.Contains(err.Error(), data) {
					t.Errorf("the refusal does not name the data directory: %v", err)
				}
				return
			}
			if err != nil {
				t.Errorf("%s was refused: %v", tc.path, err)
			}
		})
	}
}

// A symlink into the data directory is how the check above gets passed by
// accident, so the path is resolved before it is compared.
func TestLocalDestinationRefusesASymlinkIntoTheDataDirectory(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(data, "backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "somewhere-else")
	if err := os.Symlink(filepath.Join(data, "backups"), link); err != nil {
		t.Skipf("this filesystem will not make a symlink: %v", err)
	}

	d := Destination{
		Name: "local", Kind: KindLocal, Policy: DefaultPolicy,
		Settings: Settings{Path: link},
	}
	if err := d.Validate(data); err == nil {
		t.Error("a link into the data directory was accepted as a backup destination")
	}
}

// mcpd never creates an operator's directory. A typo would otherwise produce an
// empty directory somewhere nobody meant, and the backups would appear to be
// working.
func TestLocalDestinationRefusesADirectoryThatIsNotThere(t *testing.T) {
	root := t.TempDir()
	d := Destination{
		Name: "local", Kind: KindLocal, Policy: DefaultPolicy,
		Settings: Settings{Path: filepath.Join(root, "not-there")},
	}
	err := d.Validate(filepath.Join(root, "data"))
	if err == nil {
		t.Fatal("a directory that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "no directory") {
		t.Errorf("the refusal does not say the directory is missing: %v", err)
	}
}

func TestLocalDestinationRoundTrip(t *testing.T) {
	dir := t.TempDir()
	transport := localDestination(t, dir)
	ctx := t.Context()

	const name = "mcpd-nas-20260208T040000Z.mcpdbak"
	body := "an archive, pretend"
	if err := transport.Put(ctx, name, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Written under its final name and nothing else: a `.part` left behind
	// would be counted by the next run's retention and offered to somebody
	// restoring.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != name {
		got := make([]string, 0, len(entries))
		for _, e := range entries {
			got = append(got, e.Name())
		}
		t.Fatalf("the directory holds %v, want just %s", got, name)
	}

	objects, err := transport.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(objects) != 1 || objects[0].Name != name || objects[0].Size != int64(len(body)) {
		t.Fatalf("list returned %+v", objects)
	}

	if err := transport.Delete(ctx, name); err != nil {
		t.Fatalf("delete: %v", err)
	}
	objects, err = transport.List(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(objects) != 0 {
		t.Errorf("list still returns %+v", objects)
	}
}

// A destination is very often a shared folder, so a listing must never carry a
// file somebody else put there -- retention reads it.
func TestLocalDestinationListsOnlyOurOwnArchives(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"mcpd-nas-20260208T040000Z.mcpdbak",
		"mcpd-20260101T000000Z.mcpdbak",
		"family-photos.zip",
		"mcpd-nas-20260208T040000Z.mcpdbak.part",
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "mcpd-nas-20260101T000000Z.mcpdbak.d"), 0o755); err != nil {
		t.Fatal(err)
	}

	objects, err := localDestination(t, dir).List(t.Context())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(objects) != 2 {
		names := make([]string, 0, len(objects))
		for _, o := range objects {
			names = append(names, o.Name)
		}
		t.Errorf("list returned %v, want only the two archives mcpd wrote", names)
	}
}

// Delete only ever takes a name this package produced. A caller passing a path
// would otherwise be able to remove something outside the directory.
func TestLocalDestinationRefusesToDeleteAnythingThatIsNotOurs(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport := localDestination(t, dir)

	for _, name := range []string{"notes.txt", "../notes.txt", "/etc/passwd"} {
		if err := transport.Delete(t.Context(), name); err == nil {
			t.Errorf("delete accepted %q", name)
		}
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("a file that is not ours was removed: %v", err)
	}
}

// Listing proves the directory is readable, which is not the question a Test
// connection asks. A share mounted read-only lists perfectly and fails at four
// in the morning.
func TestLocalDestinationCheckWritesAsWellAsReads(t *testing.T) {
	dir := t.TempDir()
	transport := localDestination(t, dir)
	if err := transport.Check(t.Context()); err != nil {
		t.Fatalf("check on a writable directory: %v", err)
	}
	// And nothing is left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the check left %d files behind", len(entries))
	}

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("this filesystem will not make a directory read-only: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write to a read-only directory")
	}
	if err := transport.Check(t.Context()); err == nil {
		t.Error("a directory mcpd cannot write to passed the connection test")
	}
}
