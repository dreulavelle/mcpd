package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const passphrase = "a-long-enough-passphrase"

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// instance builds a data directory that looks like a running host's: a
// database file, TLS material beside it, and a config.
func instance(t *testing.T, database []byte) (dir string, opts Options) {
	t.Helper()
	dir = t.TempDir()

	source := filepath.Join(dir, "source.db")
	if err := os.WriteFile(source, database, 0o600); err != nil {
		t.Fatal(err)
	}
	tlsDir := filepath.Join(dir, "tls")
	if err := os.MkdirAll(tlsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"ca.pem":   "-- ca --",
		"cert.pem": "-- cert --",
		"key.pem":  "-- key --",
	} {
		if err := os.WriteFile(filepath.Join(tlsDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("storage:\n  path: /var/lib/mcpd/mcpd.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	return dir, Options{
		Snapshot: func(_ context.Context, path string) error {
			return copyFile(source, path)
		},
		WorkDir:        dir,
		TLSDir:         tlsDir,
		ConfigPath:     configPath,
		KeyFingerprint: Fingerprint("a-generated-settings-key-of-some-length"),
		Instance:       "https://mcp.example",
		Version:        "1.2.3",
		SchemaVersion:  19,
		Passphrase:     passphrase,
		Now:            func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
}

func copyFile(from, to string) error {
	body, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, body, 0o600)
}

func create(t *testing.T, opts Options) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Create(context.Background(), &buf, opts); err != nil {
		t.Fatalf("create: %v", err)
	}
	return buf.Bytes()
}

// read returns every member of an opened archive, by name.
func read(t *testing.T, archive []byte, pass string) (Manifest, map[string][]byte) {
	t.Helper()
	tr, err := Open(bytes.NewReader(archive), pass)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var manifest Manifest
	members := map[string][]byte{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", header.Name, err)
		}
		if header.Name == manifestName {
			if err := json.Unmarshal(body, &manifest); err != nil {
				t.Fatal(err)
			}
			continue
		}
		members[header.Name] = body
	}
	return manifest, members
}

func TestRoundTrip(t *testing.T) {
	_, opts := instance(t, []byte("SQLite format 3\x00 pretend database"))
	manifest, members := read(t, create(t, opts), passphrase)

	if got := string(members[databaseName]); !strings.Contains(got, "pretend database") {
		t.Errorf("database member is %q", got)
	}
	for _, name := range []string{"tls/ca.pem", "tls/cert.pem", "tls/key.pem"} {
		if _, ok := members[name]; !ok {
			t.Errorf("%s is missing from the archive", name)
		}
	}
	if manifest.SchemaVersion != 19 || manifest.Version != "1.2.3" {
		t.Errorf("manifest describes %+v", manifest)
	}
	if manifest.Instance != "https://mcp.example" {
		t.Errorf("instance is %q", manifest.Instance)
	}
}

// The archive is a complete record, so config.yaml travels; it is a record of
// the machine rather than of the instance, so it is not restored.
func TestConfigIsCarriedButNotRestored(t *testing.T) {
	_, opts := instance(t, []byte("db"))
	manifest, members := read(t, create(t, opts), passphrase)

	if _, ok := members[configName]; !ok {
		t.Fatal("config.yaml is not in the archive")
	}
	file, ok := manifest.Member(configName)
	if !ok {
		t.Fatal("the manifest does not list config.yaml")
	}
	if file.Restored {
		t.Error("config.yaml is marked as restored, which would put another machine's paths on this one")
	}
}

// The key is what makes a stolen archive useless. It must not be in one.
func TestArchiveDoesNotCarryTheEncryptionKey(t *testing.T) {
	key := "a-generated-settings-key-of-some-length"
	_, opts := instance(t, []byte("db"))
	opts.KeyFingerprint = Fingerprint(key)
	archive := create(t, opts)

	if bytes.Contains(archive, []byte(key)) {
		t.Fatal("the encryption key appears in the archive")
	}
	manifest, _ := read(t, archive, passphrase)
	if manifest.KeyFingerprint != Fingerprint(key) {
		t.Errorf("fingerprint is %q", manifest.KeyFingerprint)
	}
	// A fingerprint identifies the key without being it.
	if strings.Contains(manifest.KeyFingerprint, key) {
		t.Error("the fingerprint contains the key")
	}
}

func TestPassphraseFloor(t *testing.T) {
	_, opts := instance(t, []byte("db"))
	opts.Passphrase = "short"
	if err := Create(context.Background(), io.Discard, opts); err == nil {
		t.Fatal("a passphrase below the floor was accepted")
	}
}

func TestWrongPassphrase(t *testing.T) {
	_, opts := instance(t, []byte("db"))
	archive := create(t, opts)

	tr, err := Open(bytes.NewReader(archive), "a-different-passphrase")
	if err == nil {
		// The header is plaintext, so failure surfaces at the first read.
		_, err = tr.Next()
	}
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("got %v, want ErrPassphrase", err)
	}
}

// Every chunk is sealed against the header, so an archive whose header was
// edited does not open even with the right passphrase.
func TestTamperedHeaderIsRefused(t *testing.T) {
	_, opts := instance(t, []byte("db"))
	archive := create(t, opts)

	edited := bytes.Replace(archive, []byte(`"mcpd_version":"1.2.3"`), []byte(`"mcpd_version":"9.9.9"`), 1)
	if bytes.Equal(edited, archive) {
		t.Fatal("the header was not edited, so this proves nothing")
	}

	tr, err := Open(bytes.NewReader(edited), passphrase)
	if err == nil {
		_, err = tr.Next()
	}
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("got %v, want ErrPassphrase", err)
	}
}

func TestTamperedBodyIsRefused(t *testing.T) {
	_, opts := instance(t, []byte("db"))
	archive := create(t, opts)

	// A byte well inside the ciphertext.
	edited := append([]byte(nil), archive...)
	edited[len(edited)-20] ^= 0xff

	tr, err := Open(bytes.NewReader(edited), passphrase)
	if err == nil {
		for {
			if _, err = tr.Next(); err != nil {
				break
			}
			if _, err = io.Copy(io.Discard, tr); err != nil {
				break
			}
		}
	}
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("got %v, want ErrPassphrase", err)
	}
}

// The bug the final-chunk flag exists for. Without it, an archive with its
// tail cut off opens cleanly as a shorter, complete-looking one.
func TestTruncatedArchiveIsRefused(t *testing.T) {
	// Large enough to be several chunks, so a truncation lands on a boundary
	// that would otherwise look like a legitimate end.
	_, opts := instance(t, bytes.Repeat([]byte("mcpd"), 900_000))
	archive := create(t, opts)

	tr, err := Open(bytes.NewReader(archive[:len(archive)/2]), passphrase)
	if err == nil {
		for {
			if _, err = tr.Next(); err != nil {
				break
			}
			if _, err = io.Copy(io.Discard, tr); err != nil {
				break
			}
		}
	}
	if errors.Is(err, io.EOF) || err == nil {
		t.Fatal("a truncated archive read to a clean end")
	}
	if !errors.Is(err, ErrPassphrase) {
		t.Fatalf("got %v, want ErrPassphrase", err)
	}
}

// Anything past one chunk exercises the counter in the nonce and the framing.
func TestMultipleChunks(t *testing.T) {
	body := bytes.Repeat([]byte("not very compressible: "), 200_000)
	for i := range body {
		body[i] ^= byte(i * 7)
	}
	_, opts := instance(t, body)

	_, members := read(t, create(t, opts), passphrase)
	if !bytes.Equal(members[databaseName], body) {
		t.Fatalf("database came back as %d bytes, want %d", len(members[databaseName]), len(body))
	}
}

// --- staging and applying ---

func stageOpts(t *testing.T, dir string) StageOptions {
	t.Helper()
	return StageOptions{
		Passphrase:     passphrase,
		StorageDir:     dir,
		DatabasePath:   filepath.Join(dir, "mcpd.db"),
		TLSDir:         filepath.Join(dir, "tls"),
		KeyFingerprint: Fingerprint("a-generated-settings-key-of-some-length"),
		MaxSchema:      19,
		Actor:          "user:someone",
		Now:            func() time.Time { return time.Unix(1700000100, 0).UTC() },
	}
}

// The whole point: a restore staged by one process is applied by the next one,
// and nothing the running host is using moves until then.
func TestStageThenApply(t *testing.T) {
	_, opts := instance(t, []byte("SQLite format 3\x00 restored"))
	archive := create(t, opts)

	target := t.TempDir()
	live := filepath.Join(target, "mcpd.db")
	if err := os.WriteFile(live, []byte("SQLite format 3\x00 current"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A live WAL, which must not survive the swap.
	if err := os.WriteFile(live+"-wal", []byte("stale log"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Stage(context.Background(), bytes.NewReader(archive), stageOpts(t, target)); err != nil {
		t.Fatalf("stage: %v", err)
	}

	// Staging touches nothing the running host reads.
	if body, _ := os.ReadFile(live); !strings.Contains(string(body), "current") {
		t.Fatal("staging replaced the live database")
	}
	pending, err := ReadPending(target)
	if err != nil || pending == nil {
		t.Fatalf("no restore is pending: %v", err)
	}
	if pending.Actor != "user:someone" {
		t.Errorf("actor is %q", pending.Actor)
	}

	if err := ApplyPending(target, live, discard()); err != nil {
		t.Fatalf("apply: %v", err)
	}

	body, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "restored") {
		t.Errorf("the database was not replaced: %q", body)
	}
	// A write-ahead log belonging to the replaced database is corruption
	// waiting to be recovered against a file it was never written for.
	if _, err := os.Stat(live + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Error("the old write-ahead log is still beside the restored database")
	}
	if ca, err := os.ReadFile(filepath.Join(target, "tls", "ca.pem")); err != nil || string(ca) != "-- ca --" {
		t.Errorf("TLS material was not restored: %q %v", ca, err)
	}
	if p, _ := ReadPending(target); p != nil {
		t.Error("the marker survived a completed restore")
	}
}

// The database being replaced is kept. An operator who restored the wrong
// archive has one way back, and this is it.
func TestSupersededDatabaseIsKept(t *testing.T) {
	_, opts := instance(t, []byte("restored"))
	archive := create(t, opts)

	target := t.TempDir()
	live := filepath.Join(target, "mcpd.db")
	if err := os.WriteFile(live, []byte("the one that was there"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(context.Background(), bytes.NewReader(archive), stageOpts(t, target)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPending(target, live, discard()); err != nil {
		t.Fatal(err)
	}

	var found bool
	err := filepath.Walk(filepath.Join(target, supersededDir),
		func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			body, readErr := os.ReadFile(path)
			if readErr == nil && string(body) == "the one that was there" {
				found = true
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("the replaced database was not kept")
	}
}

// A crash between any two steps must leave a restore that finishes, not one
// that half happened. The marker is removed last, so running it again is the
// recovery path.
func TestApplyIsRepeatable(t *testing.T) {
	_, opts := instance(t, []byte("restored"))
	archive := create(t, opts)

	target := t.TempDir()
	live := filepath.Join(target, "mcpd.db")
	if err := os.WriteFile(live, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(context.Background(), bytes.NewReader(archive), stageOpts(t, target)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPending(target, live, discard()); err != nil {
		t.Fatal(err)
	}
	// A second run has no marker and must be a no-op rather than an error.
	if err := ApplyPending(target, live, discard()); err != nil {
		t.Fatalf("a second apply failed: %v", err)
	}
	if body, _ := os.ReadFile(live); string(body) != "restored" {
		t.Errorf("database is %q", body)
	}
}

// Interrupted after the database moved but before the marker went: the next
// start finds a marker, a staging directory missing that file, and finishes.
func TestApplyResumesAfterAnInterruption(t *testing.T) {
	_, opts := instance(t, []byte("restored"))
	archive := create(t, opts)

	target := t.TempDir()
	live := filepath.Join(target, "mcpd.db")
	if err := os.WriteFile(live, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(context.Background(), bytes.NewReader(archive), stageOpts(t, target)); err != nil {
		t.Fatal(err)
	}

	// Simulate the database half of the swap having already happened.
	staged := filepath.Join(target, stagingDir, databaseName)
	if err := os.Rename(staged, live); err != nil {
		t.Fatal(err)
	}

	if err := ApplyPending(target, live, discard()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if body, _ := os.ReadFile(live); string(body) != "restored" {
		t.Errorf("database is %q", body)
	}
	if ca, _ := os.ReadFile(filepath.Join(target, "tls", "ca.pem")); string(ca) != "-- ca --" {
		t.Error("the TLS material was not finished")
	}
	if p, _ := ReadPending(target); p != nil {
		t.Error("the marker survived")
	}
}

// The dangerous case: files gone and no database. Starting would create an
// empty one and the host would come up with no accounts and no history.
func TestApplyRefusesToStartWithNothingToRestore(t *testing.T) {
	_, opts := instance(t, []byte("restored"))
	archive := create(t, opts)

	target := t.TempDir()
	live := filepath.Join(target, "mcpd.db")
	if err := os.WriteFile(live, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Stage(context.Background(), bytes.NewReader(archive), stageOpts(t, target)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(target, stagingDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}

	err := ApplyPending(target, live, discard())
	if err == nil {
		t.Fatal("started with no database and no staged replacement")
	}
	if !strings.Contains(err.Error(), supersededDir) {
		t.Errorf("the error does not say where to look: %v", err)
	}
}

// Staged files gone but the database is there: nothing happened, so drop the
// marker and start normally rather than refusing forever.
func TestApplyIgnoresAMarkerWithNothingBehindIt(t *testing.T) {
	target := t.TempDir()
	live := filepath.Join(target, "mcpd.db")
	if err := os.WriteFile(live, []byte("current"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeMarker(target, Pending{StagedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	if err := ApplyPending(target, live, discard()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if p, _ := ReadPending(target); p != nil {
		t.Error("the marker survived")
	}
	if body, _ := os.ReadFile(live); string(body) != "current" {
		t.Errorf("the database changed: %q", body)
	}
}

// Restoring under the wrong key would start a host whose every stored
// credential is ciphertext it cannot open. Refused while the operator still
// has the archive in hand.
func TestKeyMismatchIsRefused(t *testing.T) {
	_, opts := instance(t, []byte("db"))
	archive := create(t, opts)

	target := t.TempDir()
	stage := stageOpts(t, target)
	stage.KeyFingerprint = Fingerprint("some-other-key-entirely-long-enough")

	_, err := Stage(context.Background(), bytes.NewReader(archive), stage)
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("got %v, want ErrKeyMismatch", err)
	}
	if p, _ := ReadPending(target); p != nil {
		t.Error("a refused restore left a marker")
	}
	if _, err := os.Stat(filepath.Join(target, stagingDir)); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused restore left its staging directory")
	}
}

// Migrations are forward-only, so a database from a newer build has tables
// this one does not know and no way down.
func TestNewerSchemaIsRefused(t *testing.T) {
	_, opts := instance(t, []byte("db"))
	opts.SchemaVersion = 42
	archive := create(t, opts)

	stage := stageOpts(t, t.TempDir())
	stage.MaxSchema = 19

	_, err := Stage(context.Background(), bytes.NewReader(archive), stage)
	if err == nil || !strings.Contains(err.Error(), "newer mcpd") {
		t.Fatalf("got %v, want a refusal naming the newer build", err)
	}
}

// Verify is the host's own check on the staged database. A failure has to stop
// the restore here, not after the file has replaced a working instance.
func TestVerifyFailureRefusesTheRestore(t *testing.T) {
	_, opts := instance(t, []byte("db"))
	archive := create(t, opts)

	target := t.TempDir()
	stage := stageOpts(t, target)
	stage.Verify = func(context.Context, string) error {
		return errors.New("database disk image is malformed")
	}

	_, err := Stage(context.Background(), bytes.NewReader(archive), stage)
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("got %v, want the verification failure", err)
	}
	if p, _ := ReadPending(target); p != nil {
		t.Error("a database that would not open was still staged")
	}
}

func TestCancelClearsAStagedRestore(t *testing.T) {
	_, opts := instance(t, []byte("db"))
	archive := create(t, opts)

	target := t.TempDir()
	if _, err := Stage(context.Background(), bytes.NewReader(archive), stageOpts(t, target)); err != nil {
		t.Fatal(err)
	}
	if err := Cancel(target); err != nil {
		t.Fatal(err)
	}
	if p, _ := ReadPending(target); p != nil {
		t.Error("the marker survived a cancel")
	}
	if _, err := os.Stat(filepath.Join(target, stagingDir)); !errors.Is(err, os.ErrNotExist) {
		t.Error("the staging directory survived a cancel")
	}
}

// --- hostile archives ---

// forge builds an archive holding exactly the members given, so a test can
// write one no honest Create would produce.
func forge(t *testing.T, members map[string][]byte, manifest Manifest) []byte {
	t.Helper()
	return forgeMembers(t, members, &manifest)
}

// forgeMembers is forge with the manifest optional, for the archive that has
// none at all.
func forgeMembers(t *testing.T, members map[string][]byte, manifest *Manifest) []byte {
	t.Helper()

	salt, err := randomBytes(saltSize)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := randomBytes(noncePrefix)
	if err != nil {
		t.Fatal(err)
	}
	header := Header{
		Format: Magic, CreatedAt: time.Now().UTC(), Version: "1.2.3",
		KDF: "pbkdf2-sha256", Iterations: iterations,
		Salt:        base64.StdEncoding.EncodeToString(salt),
		NoncePrefix: base64.StdEncoding.EncodeToString(prefix),
	}
	encoded, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "%s\n%s\n", Magic, encoded)

	key, err := deriveKey(passphrase, salt, iterations)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	sealed, err := newSealer(&out, key, prefix, sum[:])
	if err != nil {
		t.Fatal(err)
	}

	gz := gzip.NewWriter(sealed)
	tw := tar.NewWriter(gz)
	write := func(name string, body []byte) {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range members {
		write(name, body)
	}
	if manifest != nil {
		encodedManifest, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		write(manifestName, encodedManifest)
	}

	for _, closer := range []func() error{tw.Close, gz.Close, sealed.Close} {
		if err := closer(); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

func digestOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// An archive is uploaded by an administrator, which is not a reason to let it
// name where its contents land.
func TestArchiveCannotWriteOutsideStaging(t *testing.T) {
	body := []byte("owned")
	archive := forge(t, map[string][]byte{"../../escaped": body}, Manifest{
		SchemaVersion:  19,
		KeyFingerprint: Fingerprint("a-generated-settings-key-of-some-length"),
		Files:          []FileInfo{{Name: "../../escaped", Size: int64(len(body)), SHA256: digestOf(body), Restored: true}},
	})

	target := t.TempDir()
	_, err := Stage(context.Background(), bytes.NewReader(archive), stageOpts(t, target))
	if err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("got %v, want a refusal of the path", err)
	}
}

// A member this build has no home for is refused rather than dropped, so an
// older binary cannot half-restore an archive from a newer format.
//
// The example used to be plugins/, which this build now knows where to put.
// Any name outside the closed set does; what the test defends is the closed
// set, not the example.
func TestUnknownMemberIsRefused(t *testing.T) {
	body := []byte("something new")
	database := []byte("db")
	archive := forge(t, map[string][]byte{
		databaseName:         database,
		"secrets/thing.json": body,
	}, Manifest{
		SchemaVersion:  19,
		KeyFingerprint: Fingerprint("a-generated-settings-key-of-some-length"),
		Files: []FileInfo{
			{Name: databaseName, Size: int64(len(database)), SHA256: digestOf(database), Restored: true},
			{Name: "secrets/thing.json", Size: int64(len(body)), SHA256: digestOf(body), Restored: true},
		},
	})

	_, err := Stage(context.Background(), bytes.NewReader(archive), stageOpts(t, t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "does not know where") {
		t.Fatalf("got %v, want a refusal naming the member", err)
	}
}

// A hash that does not match means a damaged archive, and a damaged database
// is not something to discover after the swap.
func TestDamagedMemberIsRefused(t *testing.T) {
	archive := forge(t, map[string][]byte{databaseName: []byte("actual contents")}, Manifest{
		SchemaVersion:  19,
		KeyFingerprint: Fingerprint("a-generated-settings-key-of-some-length"),
		Files: []FileInfo{
			{Name: databaseName, Size: 15, SHA256: digestOf([]byte("different contents")), Restored: true},
		},
	})

	_, err := Stage(context.Background(), bytes.NewReader(archive), stageOpts(t, t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("got %v, want a refusal naming the damage", err)
	}
}

// Without a manifest there is no record of what the archive holds, so there is
// nothing to check the contents against.
func TestArchiveWithNoManifestIsRefused(t *testing.T) {
	archive := forgeMembers(t, map[string][]byte{databaseName: []byte("db")}, nil)

	_, err := Stage(context.Background(), bytes.NewReader(archive), stageOpts(t, t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "no manifest") {
		t.Fatalf("got %v, want a refusal naming the missing manifest", err)
	}
}

func TestNotAnArchive(t *testing.T) {
	_, err := Stage(context.Background(),
		strings.NewReader("just some file"), stageOpts(t, t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "not an mcpd backup") {
		t.Fatalf("got %v, want a refusal saying what the file is not", err)
	}
}
