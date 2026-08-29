package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// A restore happens between two processes, not inside one.
//
// The database cannot be swapped under a running mcpd. Connections are open,
// the WAL is live, and every component from the settings store to the audit
// trail is holding state read from the file that would be replaced. So a
// restore is two acts: this process validates the archive and lays the new
// files out beside the old ones, and the *next* process moves them into place
// before it opens anything.
//
// The marker file is what joins them. It is written last, once the staged copy
// is complete and verified, so a crash while unpacking leaves nothing for the
// next start to find. It is removed last on the way back, so a crash while
// swapping leaves a restore that finishes on the following start rather than
// one that half happened.
const (
	stagingDir = "restore.staging"
	markerName = "restore.pending"
	// Where the files being replaced are kept. Not deleted: a restore is the
	// one operation here that destroys the current instance, and an operator
	// who restored the wrong archive needs the old database to still exist.
	supersededDir = "superseded"
)

// Pending describes a restore that has been staged and is waiting for a
// restart.
type Pending struct {
	StagedAt time.Time `json:"staged_at"`
	// Actor is who uploaded it, for the log line the next start writes.
	Actor    string   `json:"actor"`
	Manifest Manifest `json:"manifest"`
	// Files are the staged members, relative to the staging directory, paired
	// with where each one belongs.
	Files []Placement `json:"files"`
}

// Placement is one staged file and its destination.
type Placement struct {
	Staged string `json:"staged"`
	Target string `json:"target"`
}

// StageOptions is what staging needs to know about this host.
type StageOptions struct {
	Passphrase string
	// StorageDir holds the database, and is where staging and the superseded
	// copies go. Everything stays on one filesystem so the swap is a rename.
	StorageDir string
	// DatabasePath is where the restored database belongs.
	DatabasePath string
	// TLSDir is where restored TLS material belongs.
	TLSDir string
	// KeyFingerprint is this host's settings encryption key. An archive
	// written under a different one is refused; see Stage.
	KeyFingerprint string
	// MaxSchema is the highest migration this build knows.
	MaxSchema int
	// Verify is given the staged database before the marker is written, so a
	// corrupt or unreadable file is refused now rather than discovered by a
	// host that has already replaced its own. A function because this package
	// deliberately knows nothing about SQLite.
	Verify func(ctx context.Context, path string) error
	Actor  string
	Now    func() time.Time
}

func (o StageOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// ErrKeyMismatch reports an archive written by an instance using a different
// settings encryption key.
//
// Refused rather than restored, because the failure it prevents is silent and
// total: the host would start, and every stored credential in it -- every
// plugin's API key, every ChatGPT account -- would be ciphertext nothing on
// the machine can open. Better to say so while the operator still has the
// archive in their hand.
var ErrKeyMismatch = errors.New(
	"backup: this archive was written by an instance using a different " +
		"settings encryption key, so its stored credentials cannot be read " +
		"here. Set this host's key to the one that instance used, restart, " +
		"and restore again")

// Stage validates an archive and lays it out for the next start to apply.
//
// Nothing the running host uses is touched. If this returns an error, the
// instance is exactly as it was.
func Stage(ctx context.Context, r io.Reader, opts StageOptions) (Pending, error) {
	if opts.Passphrase == "" {
		return Pending{}, errors.New("backup: a passphrase is needed to open the archive")
	}
	if opts.StorageDir == "" || opts.DatabasePath == "" {
		return Pending{}, errors.New("backup: nowhere to stage a restore")
	}

	staging := filepath.Join(opts.StorageDir, stagingDir)
	// A staging directory left by an abandoned attempt is replaced, not
	// merged. Half of one archive over half of another is not an instance.
	if err := os.RemoveAll(staging); err != nil {
		return Pending{}, fmt.Errorf("backup: clear the staging directory: %w", err)
	}
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return Pending{}, fmt.Errorf("backup: create the staging directory: %w", err)
	}

	archive, err := Open(r, opts.Passphrase)
	if err != nil {
		os.RemoveAll(staging)
		return Pending{}, err
	}

	manifest, hashes, err := unpack(archive, staging)
	if err != nil {
		os.RemoveAll(staging)
		return Pending{}, err
	}

	pending, err := validate(ctx, manifest, hashes, staging, opts)
	if err != nil {
		os.RemoveAll(staging)
		return Pending{}, err
	}

	// Written last. Until this file exists there is no restore, only a
	// directory of files nothing reads.
	if err := writeMarker(opts.StorageDir, pending); err != nil {
		os.RemoveAll(staging)
		return Pending{}, err
	}
	return pending, nil
}

// unpack writes the archive's members into the staging directory and returns
// what it found, along with each member's hash as written.
func unpack(archive *tar.Reader, staging string) (Manifest, map[string]string, error) {
	var (
		manifest Manifest
		found    bool
		hashes   = map[string]string{}
	)

	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if errors.Is(err, ErrPassphrase) {
				return Manifest{}, nil, ErrPassphrase
			}
			return Manifest{}, nil, fmt.Errorf("backup: read the archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name, err := safeName(header.Name)
		if err != nil {
			return Manifest{}, nil, err
		}

		if name == manifestName {
			if err := json.NewDecoder(archive).Decode(&manifest); err != nil {
				return Manifest{}, nil, fmt.Errorf("backup: read the manifest: %w", err)
			}
			found = true
			continue
		}

		digest, err := writeStaged(archive, filepath.Join(staging, filepath.FromSlash(name)))
		if err != nil {
			return Manifest{}, nil, err
		}
		hashes[name] = digest
	}

	if !found {
		return Manifest{}, nil, errors.New(
			"backup: the archive has no manifest, so there is no way to say what it holds")
	}
	return manifest, hashes, nil
}

func writeStaged(r io.Reader, dest string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", fmt.Errorf("backup: create %s: %w", filepath.Dir(dest), err)
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("backup: write %s: %w", dest, err)
	}
	defer f.Close()

	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, digest), r); err != nil {
		if errors.Is(err, ErrPassphrase) {
			return "", ErrPassphrase
		}
		return "", fmt.Errorf("backup: write %s: %w", dest, err)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("backup: flush %s: %w", dest, err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// validate decides whether the staged copy may replace this instance.
func validate(
	ctx context.Context,
	manifest Manifest,
	hashes map[string]string,
	staging string,
	opts StageOptions,
) (Pending, error) {
	if opts.MaxSchema > 0 && manifest.SchemaVersion > opts.MaxSchema {
		return Pending{}, fmt.Errorf(
			"backup: this archive is from a newer mcpd (%s, database schema %d); "+
				"this build knows schema %d, and migrations only go forward. "+
				"Upgrade this host first",
			manifest.Version, manifest.SchemaVersion, opts.MaxSchema)
	}
	if !manifest.VerifyKey(opts.KeyFingerprint) {
		return Pending{}, ErrKeyMismatch
	}

	// Every member the manifest claims must be present and byte-for-byte what
	// it says. A hash that does not match means the archive is damaged, and a
	// damaged database is not something to find out about after the swap.
	var files []Placement
	for _, want := range manifest.Files {
		if !want.Restored {
			continue
		}
		got, ok := hashes[want.Name]
		if !ok {
			return Pending{}, fmt.Errorf(
				"backup: the manifest lists %s, but the archive does not hold it", want.Name)
		}
		if got != want.SHA256 {
			return Pending{}, fmt.Errorf(
				"backup: %s does not match the hash the manifest records; "+
					"the archive is damaged", want.Name)
		}
		target, err := targetFor(want.Name, opts)
		if err != nil {
			return Pending{}, err
		}
		files = append(files, Placement{Staged: want.Name, Target: target})
	}

	// The placements, not the manifest's list: a database the manifest carries
	// but marks as not restored would satisfy a check on the list alone, and
	// leave a restore that swaps the TLS material and nothing else.
	if !slices.ContainsFunc(files, func(p Placement) bool { return p.Staged == databaseName }) {
		return Pending{}, errors.New("backup: the archive holds no database to restore")
	}
	if opts.Verify != nil {
		staged := filepath.Join(staging, databaseName)
		if err := opts.Verify(ctx, staged); err != nil {
			return Pending{}, fmt.Errorf("backup: the archive's database will not open: %w", err)
		}
	}

	return Pending{
		StagedAt: opts.now().UTC(),
		Actor:    opts.Actor,
		Manifest: manifest,
		Files:    files,
	}, nil
}

// targetFor maps an archive member to where it belongs on this host.
//
// A closed set. An unrecognised name is refused rather than ignored, so a
// future format that adds a member is not silently half-restored by an older
// build that did not know to place it.
func targetFor(name string, opts StageOptions) (string, error) {
	switch {
	case name == databaseName:
		return opts.DatabasePath, nil
	case strings.HasPrefix(name, tlsPrefix):
		if opts.TLSDir == "" {
			return "", errors.New("backup: the archive holds TLS material, but this host has nowhere to put it")
		}
		return filepath.Join(opts.TLSDir, filepath.FromSlash(strings.TrimPrefix(name, tlsPrefix))), nil
	default:
		return "", fmt.Errorf(
			"backup: this build does not know where %s belongs, so it will not "+
				"restore an archive holding one", name)
	}
}

func markerPath(storageDir string) string { return filepath.Join(storageDir, markerName) }

func writeMarker(storageDir string, p Pending) error {
	encoded, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("backup: encode the restore marker: %w", err)
	}
	path := markerPath(storageDir)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return fmt.Errorf("backup: write %s: %w", path, err)
	}
	return nil
}

// ReadPending reports a staged restore waiting for a restart, or nil.
func ReadPending(storageDir string) (*Pending, error) {
	raw, err := os.ReadFile(markerPath(storageDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: read the restore marker: %w", err)
	}
	var p Pending
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("backup: read the restore marker: %w", err)
	}
	return &p, nil
}

// Cancel discards a staged restore.
func Cancel(storageDir string) error {
	if err := os.Remove(markerPath(storageDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("backup: remove the restore marker: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(storageDir, stagingDir)); err != nil {
		return fmt.Errorf("backup: clear the staging directory: %w", err)
	}
	return nil
}

// ApplyPending moves a staged restore into place.
//
// Called before the database is opened and before anything reads a certificate,
// which is the only moment this is safe. It is written to be run twice: every
// step tolerates having already happened, because the marker is removed last
// and a crash between any two steps means the next start does the rest.
func ApplyPending(storageDir, databasePath string, log *slog.Logger) error {
	pending, err := ReadPending(storageDir)
	if err != nil || pending == nil {
		return err
	}

	staging := filepath.Join(storageDir, stagingDir)
	if _, err := os.Stat(staging); errors.Is(err, fs.ErrNotExist) {
		// Nothing staged. Either somebody removed it, or the swap finished and
		// the process died before the marker went. Which one it was is
		// answered by whether the database is there.
		if _, dbErr := os.Stat(databasePath); dbErr == nil {
			log.Warn("a restore was staged but its files are gone; ignoring it",
				"staged_at", pending.StagedAt)
			return os.Remove(markerPath(storageDir))
		}
		// The database was moved aside and the replacement is missing. Do not
		// start: opening a path with no file there creates an empty database,
		// and mcpd would come up as a host with no history and no accounts.
		return fmt.Errorf(
			"backup: a restore was staged, its files are gone, and there is no "+
				"database at %s. The instance that was replaced is under %s; "+
				"put it back before starting",
			databasePath, filepath.Join(storageDir, supersededDir))
	}

	keep := filepath.Join(storageDir, supersededDir,
		pending.StagedAt.UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(keep, 0o700); err != nil {
		return fmt.Errorf("backup: create %s: %w", keep, err)
	}

	log.Warn("applying a staged restore",
		"staged_at", pending.StagedAt, "actor", pending.Actor,
		"from_version", pending.Manifest.Version,
		"taken_at", pending.Manifest.CreatedAt,
		"superseded_files_kept_in", keep)

	// The write-ahead log and shared-memory file belong to the database about
	// to be replaced. They go first, and not afterwards: a new database file
	// sitting beside the old database's log is exactly the state SQLite would
	// try to recover from, and recovering a log against a file it was never
	// written for is corruption rather than recovery. Doing this first means
	// that state never exists, even for the moment between two renames.
	for _, suffix := range []string{"-wal", "-shm"} {
		sidecar := databasePath + suffix
		if err := moveAside(sidecar, filepath.Join(keep, databaseName+suffix)); err != nil {
			return err
		}
	}

	for _, file := range pending.Files {
		staged := filepath.Join(staging, filepath.FromSlash(file.Staged))
		if _, err := os.Stat(staged); errors.Is(err, fs.ErrNotExist) {
			// Already moved on an earlier attempt.
			continue
		}
		aside := filepath.Join(keep, filepath.FromSlash(file.Staged))
		if err := moveAside(file.Target, aside); err != nil {
			return err
		}
		if err := movePath(staged, file.Target); err != nil {
			return err
		}
	}

	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("backup: clear the staging directory: %w", err)
	}
	// Last. Everything above is idempotent precisely so that this can be the
	// single point at which the restore is finished.
	if err := os.Remove(markerPath(storageDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("backup: remove the restore marker: %w", err)
	}

	log.Warn("restore applied", "database", databasePath, "superseded_files_kept_in", keep)
	return nil
}

// moveAside preserves a file that is about to be replaced. A file that is not
// there is not an error: this runs on a host being restored onto, which may
// never have had one.
func moveAside(path, dest string) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return movePath(path, dest)
}

// movePath renames, falling back to a copy when the two are on different
// filesystems. The staging directory is deliberately inside the storage
// directory so the ordinary case is a rename, but a configured TLS directory
// can be anywhere.
func movePath(from, to string) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return fmt.Errorf("backup: create %s: %w", filepath.Dir(to), err)
	}
	if err := os.Rename(from, to); err == nil {
		return nil
	}

	src, err := os.Open(from)
	if err != nil {
		return fmt.Errorf("backup: open %s: %w", from, err)
	}
	defer src.Close()

	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("backup: create %s: %w", to, err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("backup: copy %s to %s: %w", from, to, err)
	}
	if err := dst.Sync(); err != nil {
		dst.Close()
		return fmt.Errorf("backup: flush %s: %w", to, err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("backup: close %s: %w", to, err)
	}
	if err := os.Remove(from); err != nil {
		return fmt.Errorf("backup: remove %s: %w", from, err)
	}
	return nil
}
