// Package backup writes and reads a whole mcpd instance as one encrypted file.
//
// What is in it, and what is deliberately not:
//
// The database is everything mcpd decides -- accounts, groups, grants, API
// keys, settings, ChatGPT accounts, the approval history and the hash-chained
// audit trail. The TLS material is this host's own certificate authority, so a
// restored instance keeps the identity clients were told to trust rather than
// issuing a new one every operator has to accept again. Both are restored.
//
// config.yaml is carried but never installed. It holds where the database is
// and what to bind, which are facts about the machine rather than about the
// instance; a path from the machine the backup came from is at best wrong on
// the machine it lands on. It travels so a backup is a complete record of what
// the host looked like, and it is left for a person to read.
//
// The settings encryption key is not in here at all. That is what makes a
// stolen backup useless on its own, and it is the same reason the key is not
// in the database. A restore checks that the key it has is the key the archive
// was written under and refuses when it is not, because the alternative is an
// instance that starts and then cannot read a single credential it holds.
package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Magic identifies the file and its format version. First line, plain text, so
// `head -1` on a file somebody found in a bucket says what it is.
const Magic = "mcpd-backup/1"

// The name a downloaded archive is offered under. `.mcpdbak` rather than
// `.tar.gz` because it is not one: unpacking it takes the passphrase.
const Extension = ".mcpdbak"

// Names inside the archive. Fixed, because a restore looks for them by name.
const (
	manifestName = "manifest.json"
	databaseName = "mcpd.db"
	tlsPrefix    = "tls/"
	configName   = "config.yaml"
	// pluginsPrefix holds the out-of-process plugins. They are executables an
	// operator put on the host by hand, so an instance restored without them
	// comes up configured for integrations that are not there.
	pluginsPrefix = "plugins/"
)

// MaxPluginBytes is how much the plugins directory may contribute to an archive
// before Create refuses.
//
// A limit rather than none, because the directory is a bind mount an operator
// writes into and nothing stops a build artefact, a core dump or a copy of a
// container image landing in it. maxArchive already bounds what a restore will
// read; without this the first anybody would know is a backup that cannot be
// restored by the host that wrote it.
const MaxPluginBytes = 512 << 20 // 512 MiB

// maxArchive bounds what Open will read, so a hostile upload cannot ask this
// host to allocate its way out of memory. Generous for a control plane whose
// database holds decisions rather than data.
const maxArchive = 2 << 30 // 2 GiB

// Header is the plaintext preamble. It carries what is needed to derive the
// key and nothing else -- an archive should not name the host it came from to
// anybody who merely has the file.
type Header struct {
	Format    string    `json:"format"`
	CreatedAt time.Time `json:"created_at"`
	// Version is the mcpd that wrote it, so an archive from the future can be
	// refused with something better than a parse error.
	Version string `json:"mcpd_version"`
	KDF     string `json:"kdf"`
	// Iterations is recorded rather than assumed, so raising the constant does
	// not strand every archive written before the change.
	Iterations  int    `json:"iterations"`
	Salt        string `json:"salt"`
	NoncePrefix string `json:"nonce_prefix"`
}

// FileInfo is one member of the archive.
type FileInfo struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	// Restored is false for a member carried for reference. See the package
	// comment on config.yaml.
	Restored bool `json:"restored"`
}

// Manifest describes the instance, and lives inside the encrypted part.
type Manifest struct {
	CreatedAt time.Time `json:"created_at"`
	Version   string    `json:"mcpd_version"`
	// SchemaVersion is the migration the database was at. A restore refuses an
	// archive from a newer schema than this build knows: migrations are
	// forward-only, so there is no path back down and starting anyway would
	// mean a binary reading tables it does not have.
	SchemaVersion int `json:"schema_version"`
	// Instance is how this host was reached, for telling two backups apart.
	Instance string `json:"instance,omitempty"`
	// KeyFingerprint identifies the settings encryption key without carrying
	// it. Empty when the source had no key configured.
	KeyFingerprint string     `json:"key_fingerprint,omitempty"`
	Files          []FileInfo `json:"files"`
}

// Fingerprint identifies an encryption key by a hash of it.
//
// Truncated to eight bytes because it is shown to a person comparing two
// hosts, and it is a label rather than a secret: the key it is taken from is
// generated with full entropy, so the hash offers nothing to work back from.
func Fingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// Options describes the instance being written.
type Options struct {
	// Snapshot writes a consistent copy of the database to the path given.
	// A function rather than a database handle, so this package needs to know
	// nothing about SQLite and a test can hand it a file.
	Snapshot func(ctx context.Context, path string) error
	// WorkDir is where the snapshot is staged. It must be on the data volume:
	// the container's /tmp is a small tmpfs, and a database of any size
	// written there fills it.
	WorkDir string
	// TLSDir holds this host's certificate authority and certificate. Missing
	// is not an error -- a host behind a proxy that terminates TLS has none.
	TLSDir string
	// ConfigPath is carried for reference. Missing is not an error.
	ConfigPath string
	// KeyFingerprint identifies the key the database's secrets are under.
	KeyFingerprint string
	Instance       string
	Version        string
	SchemaVersion  int
	Passphrase     string
	Now            func() time.Time
	// PluginsDir holds out-of-process plugins. Empty leaves them out, which is
	// the right answer for a host that has none.
	PluginsDir string
	// MaxPluginBytes bounds what that directory may add. Zero takes the
	// package's own MaxPluginBytes.
	MaxPluginBytes int64
	// Log records what was skipped. Optional: nothing here fails for want of a
	// logger.
	Log *slog.Logger
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Create writes an encrypted archive of the instance to w.
//
// Streamed rather than assembled: the tarball is gzipped into the sealer as it
// is built, so the largest thing in memory at any moment is one chunk.
func Create(ctx context.Context, w io.Writer, opts Options) error {
	if len(opts.Passphrase) < MinPassphrase {
		return fmt.Errorf("backup: the passphrase must be at least %d characters", MinPassphrase)
	}
	if opts.Snapshot == nil {
		return errors.New("backup: no snapshot function was supplied")
	}
	if opts.WorkDir == "" {
		return errors.New("backup: no working directory was supplied")
	}

	// The snapshot is taken to a file rather than streamed, because VACUUM
	// INTO writes a file and there is no version of it that writes to a pipe.
	staging, err := os.MkdirTemp(opts.WorkDir, "backup-")
	if err != nil {
		return fmt.Errorf("backup: create working directory: %w", err)
	}
	defer os.RemoveAll(staging)

	snapshot := filepath.Join(staging, databaseName)
	if err := opts.Snapshot(ctx, snapshot); err != nil {
		return fmt.Errorf("backup: snapshot the database: %w", err)
	}

	members, err := collect(opts, snapshot)
	if err != nil {
		return err
	}

	salt, err := randomBytes(saltSize)
	if err != nil {
		return err
	}
	prefix, err := randomBytes(noncePrefix)
	if err != nil {
		return err
	}

	header := Header{
		Format:      Magic,
		CreatedAt:   opts.now().UTC(),
		Version:     opts.Version,
		KDF:         "pbkdf2-sha256",
		Iterations:  iterations,
		Salt:        base64.StdEncoding.EncodeToString(salt),
		NoncePrefix: base64.StdEncoding.EncodeToString(prefix),
	}
	encoded, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("backup: encode header: %w", err)
	}

	if _, err := fmt.Fprintf(w, "%s\n%s\n", Magic, encoded); err != nil {
		return fmt.Errorf("backup: write header: %w", err)
	}

	key, err := deriveKey(opts.Passphrase, salt, iterations)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(encoded)
	sealed, err := newSealer(w, key, prefix, sum[:])
	if err != nil {
		return err
	}

	if err := writeBody(sealed, opts, members); err != nil {
		return err
	}
	return sealed.Close()
}

// member is a file on its way into the archive.
type member struct {
	name     string
	source   string
	restored bool
	// mode is the permission bits to record. Zero means 0600, which is what
	// everything but a plugin gets; only a plugin has to be executable on the
	// way back out. See writeMember.
	mode int64
}

// collect decides what goes in, and hashes each member so the manifest can say
// what was written.
func collect(opts Options, snapshot string) ([]member, error) {
	members := []member{{name: databaseName, source: snapshot, restored: true}}

	if opts.TLSDir != "" {
		entries, err := os.ReadDir(opts.TLSDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("backup: read %s: %w", opts.TLSDir, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			members = append(members, member{
				name:     tlsPrefix + entry.Name(),
				source:   filepath.Join(opts.TLSDir, entry.Name()),
				restored: true,
			})
		}
	}

	if opts.ConfigPath != "" {
		if _, err := os.Stat(opts.ConfigPath); err == nil {
			members = append(members, member{
				name: configName, source: opts.ConfigPath, restored: false,
			})
		}
	}

	plugins, err := collectPlugins(opts)
	if err != nil {
		return nil, err
	}
	return append(members, plugins...), nil
}

// collectPlugins gathers the out-of-process plugins.
//
// Recursive and operator-managed, which is why it is the one part of collect
// that walks rather than reading a flat directory -- and why it is the one part
// that has to be careful about what it finds. A symlink written into a tar is a
// write-outside primitive on the way back: safeName checks a member's own name
// and has nothing to say about where a link points. So anything that is not an
// ordinary file is skipped and named in the log, rather than silently not
// travelling.
func collectPlugins(opts Options) ([]member, error) {
	if opts.PluginsDir == "" {
		return nil, nil
	}
	budget := opts.MaxPluginBytes
	if budget <= 0 {
		budget = MaxPluginBytes
	}

	var (
		out   []member
		total int64
	)
	err := filepath.WalkDir(opts.PluginsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A host with no plugins directory at all is not an error; it is
			// the ordinary state of one that has never had an external plugin.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("backup: read %s: %w", path, err)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			if opts.Log != nil {
				opts.Log.Warn("a backup left out something in the plugins directory "+
					"that is not an ordinary file",
					"path", path, "type", d.Type().String())
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("backup: read %s: %w", path, err)
		}
		total += info.Size()
		if total > budget {
			return fmt.Errorf(
				"backup: the plugins directory %s holds more than %d MiB, which is "+
					"more than a backup will carry. Move whatever does not belong to "+
					"a plugin out of it and take the backup again",
				opts.PluginsDir, budget>>20)
		}

		rel, err := filepath.Rel(opts.PluginsDir, path)
		if err != nil {
			return fmt.Errorf("backup: read %s: %w", path, err)
		}
		out = append(out, member{
			name:   pluginsPrefix + filepath.ToSlash(rel),
			source: path,
			// Restored: an instance without its plugin binaries comes up
			// configured for integrations that are not on the host.
			restored: true,
			// The owner's bits, and only those. A plugin has to keep its
			// executable bit -- the external runner refuses one without it --
			// and must not gain setuid, setgid, or anything for group or other
			// because the machine it came from was careless.
			mode: int64(info.Mode().Perm() & 0o700),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// writeBody builds the tarball inside the encryption.
func writeBody(w io.Writer, opts Options, members []member) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	// The manifest is written last, because a member's hash is only known once
	// it has been read. It is found by name on the way back out, so its
	// position in the stream does not matter.
	files := make([]FileInfo, 0, len(members))
	for _, m := range members {
		info, err := writeMember(tw, m)
		if err != nil {
			return err
		}
		files = append(files, info)
	}

	manifest := Manifest{
		CreatedAt:      opts.now().UTC(),
		Version:        opts.Version,
		SchemaVersion:  opts.SchemaVersion,
		Instance:       opts.Instance,
		KeyFingerprint: opts.KeyFingerprint,
		Files:          files,
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("backup: encode manifest: %w", err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: manifestName, Mode: 0o600, Size: int64(len(encoded)),
		ModTime: manifest.CreatedAt, Typeflag: tar.TypeReg,
	}); err != nil {
		return fmt.Errorf("backup: write manifest header: %w", err)
	}
	if _, err := tw.Write(encoded); err != nil {
		return fmt.Errorf("backup: write manifest: %w", err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("backup: close archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("backup: close compression: %w", err)
	}
	return nil
}

func writeMember(tw *tar.Writer, m member) (FileInfo, error) {
	f, err := os.Open(m.source)
	if err != nil {
		return FileInfo{}, fmt.Errorf("backup: open %s: %w", m.source, err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return FileInfo{}, fmt.Errorf("backup: stat %s: %w", m.source, err)
	}

	// 0600 unless the member said otherwise, and never the mode on disk by
	// default: a restored private key must not become world-readable because
	// the source host was careless. A plugin is the exception and says so, with
	// its owner bits already masked; see collectPlugins.
	mode := m.mode
	if mode == 0 {
		mode = 0o600
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: m.name, Mode: mode, Size: stat.Size(),
		ModTime: stat.ModTime().UTC(), Typeflag: tar.TypeReg,
	}); err != nil {
		return FileInfo{}, fmt.Errorf("backup: write header for %s: %w", m.name, err)
	}

	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(tw, digest), f)
	if err != nil {
		return FileInfo{}, fmt.Errorf("backup: write %s: %w", m.name, err)
	}
	if written != stat.Size() {
		// tar records the size in the header, so a file that changed under us
		// produces an archive that cannot be read back. Better to fail here.
		return FileInfo{}, fmt.Errorf(
			"backup: %s changed while it was being read", m.name)
	}

	return FileInfo{
		Name: m.name, Size: written,
		SHA256:   hex.EncodeToString(digest.Sum(nil)),
		Restored: m.restored,
	}, nil
}

// ReadHeader reads the plaintext preamble and leaves r positioned at the
// ciphertext.
func ReadHeader(r *bufio.Reader) (Header, []byte, error) {
	magic, err := r.ReadString('\n')
	if err != nil || strings.TrimRight(magic, "\r\n") != Magic {
		return Header{}, nil, errors.New(
			"backup: this is not an mcpd backup file")
	}

	line, err := r.ReadString('\n')
	if err != nil {
		return Header{}, nil, errors.New("backup: the file ends before its header does")
	}
	raw := []byte(strings.TrimRight(line, "\r\n"))

	var header Header
	if err := json.Unmarshal(raw, &header); err != nil {
		return Header{}, nil, fmt.Errorf("backup: read header: %w", err)
	}
	if header.KDF != "pbkdf2-sha256" {
		return Header{}, nil, fmt.Errorf(
			"backup: this archive uses %q, which this build cannot open", header.KDF)
	}
	if header.Iterations <= 0 {
		return Header{}, nil, errors.New("backup: the header names no work factor")
	}
	return header, raw, nil
}

// Open decrypts an archive and returns a reader over the tarball inside.
//
// The manifest is not returned here because it is written last: it names every
// member's hash, and a hash is only known once the member has been read. A
// caller unpacks first and reads the manifest at the end, which is what Unpack
// does.
func Open(r io.Reader, passphrase string) (*tar.Reader, error) {
	buffered := bufio.NewReader(io.LimitReader(r, maxArchive))
	header, raw, err := ReadHeader(buffered)
	if err != nil {
		return nil, err
	}

	salt, err := base64.StdEncoding.DecodeString(header.Salt)
	if err != nil {
		return nil, fmt.Errorf("backup: read salt: %w", err)
	}
	prefix, err := base64.StdEncoding.DecodeString(header.NoncePrefix)
	if err != nil {
		return nil, fmt.Errorf("backup: read nonce: %w", err)
	}
	if len(prefix) != noncePrefix {
		return nil, errors.New("backup: the header's nonce is the wrong length")
	}

	// The header's own iteration count, so an archive written before the
	// constant changed still opens.
	key, err := deriveKey(passphrase, salt, header.Iterations)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	plain, err := newOpener(buffered, key, prefix, sum[:])
	if err != nil {
		return nil, err
	}

	gz, err := gzip.NewReader(plain)
	if err != nil {
		// The first read is where a wrong passphrase surfaces: nothing before
		// this point is authenticated.
		if errors.Is(err, ErrPassphrase) {
			return nil, ErrPassphrase
		}
		return nil, fmt.Errorf("backup: decompress: %w", err)
	}
	return tar.NewReader(gz), nil
}

// VerifyKey reports whether a key is the one an archive's database is
// encrypted under.
//
// Compared in constant time out of habit rather than need -- a fingerprint is
// not a secret -- because a comparison that leaks is a bad pattern to leave
// lying in a package about credentials.
func (m Manifest) VerifyKey(fingerprint string) bool {
	return subtle.ConstantTimeCompare([]byte(m.KeyFingerprint), []byte(fingerprint)) == 1
}

// Member reports what the manifest says about one file, by name.
func (m Manifest) Member(name string) (FileInfo, bool) {
	for _, f := range m.Files {
		if f.Name == name {
			return f, true
		}
	}
	return FileInfo{}, false
}

// safeName rejects an archive member whose name would write outside the
// directory it is being unpacked into.
//
// An archive is uploaded by an administrator, which is not a reason to skip
// this: the point of a restore is that it runs before anybody can look at what
// arrived, and "the operator meant well" is not a property of a file.
func safeName(name string) (string, error) {
	clean := path.Clean(name)
	if clean != name || path.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("backup: the archive holds an unsafe path %q", name)
	}
	return clean, nil
}
