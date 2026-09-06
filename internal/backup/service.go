package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Service is one host's view of its own backups.
//
// It exists so the dashboard's handlers can stay handlers: everything they
// would otherwise have to know -- where the database is, what key its secrets
// are under, which migration this build stops at -- is settled once, here,
// where app wires it.
type Service struct {
	cfg ServiceConfig
	// staging serialises restores. Two uploads arriving together would each
	// clear and refill the one staging directory, and the marker written
	// second would describe a mixture of both archives. One at a time is not a
	// bottleneck for an act that happens once in the life of a host.
	staging sync.Mutex
}

// ServiceConfig is what a host has to tell this package about itself.
type ServiceConfig struct {
	// Snapshot writes a consistent copy of the live database. The database's
	// own VACUUM INTO, passed as a function so this package stays free of
	// SQLite.
	Snapshot func(ctx context.Context, path string) error
	// VerifyDatabase opens a staged database and reports whether it is sound.
	// Run before a restore is accepted, never after.
	VerifyDatabase func(ctx context.Context, path string) error

	StorageDir   string
	DatabasePath string
	TLSDir       string
	ConfigPath   string
	// PluginsDir holds the out-of-process plugins that travel in the archive
	// and are laid back down by a restore. Empty leaves them out.
	PluginsDir string

	// KeyFingerprint identifies this host's settings encryption key, or is
	// empty when none is configured.
	//
	// The fingerprint rather than the key, so the key itself never reaches
	// this package at all. Nothing here needs to decrypt anything -- an
	// archive carries the database exactly as it is on disk -- and a value
	// that is never held cannot be logged, serialised, or written into a file
	// by a later change that did not think about it.
	KeyFingerprint func() string

	// SchemaVersion is the migration the live database is at, and MaxSchema
	// the highest this build knows. They are usually equal; they differ on a
	// host that has been downgraded.
	SchemaVersion func(ctx context.Context) int
	MaxSchema     int

	// Instance is how this host is reached, recorded so two archives can be
	// told apart by something more useful than a timestamp.
	Instance func(ctx context.Context) string

	Version string
	Log     *slog.Logger
	Now     func() time.Time
}

// NewService builds the service. It does no I/O.
func NewService(cfg ServiceConfig) *Service { return &Service{cfg: cfg} }

func (s *Service) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

func (s *Service) fingerprint() string {
	if s.cfg.KeyFingerprint == nil {
		return ""
	}
	return s.cfg.KeyFingerprint()
}

// maxStatusPluginFiles bounds the walk Status does on every page load. Far
// more than any real plugins directory holds, and far fewer than a directory
// somebody has been keeping build output in.
const maxStatusPluginFiles = 5000

// Status is what the dashboard shows before anybody presses anything.
type Status struct {
	// DatabaseBytes is the live database's size on disk. The archive is
	// smaller -- it is compressed, and VACUUM INTO drops free pages -- so this
	// is an upper bound rather than a prediction.
	DatabaseBytes int64 `json:"database_bytes"`
	// TLSFiles is how many certificate files travel with it.
	TLSFiles int `json:"tls_files"`
	// ConfigIncluded reports whether config.yaml was found to carry. It is
	// never restored; see the package comment.
	ConfigIncluded bool `json:"config_included"`
	// KeyFingerprint identifies the encryption key an archive taken now would
	// be readable under. Empty when this host has no key, in which case it has
	// no stored credentials to lose either.
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	SchemaVersion  int    `json:"schema_version"`
	Version        string `json:"mcpd_version"`
	Instance       string `json:"instance,omitempty"`
	// Pending is a restore staged and waiting for a restart.
	Pending *Pending `json:"pending,omitempty"`
	// MinPassphrase is the floor the form should enforce, so the browser and
	// the host agree on it without the number being written down twice.
	MinPassphrase int `json:"min_passphrase"`
	// PluginFiles and PluginBytes describe the out-of-process plugins the
	// archive carries. Worth showing because they are the one part of an
	// archive whose size an operator controls, and the one part that can make
	// a backup refuse itself.
	//
	// PluginsTruncated says the count stopped early, so a page renders "at
	// least this many" rather than a number that is quietly wrong.
	PluginFiles      int   `json:"plugin_files"`
	PluginBytes      int64 `json:"plugin_bytes"`
	PluginsTruncated bool  `json:"plugins_truncated,omitempty"`
	// Schedule is what will happen without anybody pressing anything. Nil when
	// this host has no runner wired.
	Schedule *ScheduleStatus `json:"schedule,omitempty"`
}

// ScheduleStatus is the scheduled-backup half of the page.
type ScheduleStatus struct {
	Enabled  bool   `json:"enabled"`
	Cadence  string `json:"cadence"`
	Weekday  int    `json:"weekday"`
	Time     string `json:"time"`
	Timezone string `json:"timezone"`
	// NextRunAt is null whenever no run is going to happen, whether because
	// the schedule is off or because something is missing. Reason says which,
	// in one sentence.
	NextRunAt *time.Time `json:"next_run_at"`
	Reason    string     `json:"reason,omitempty"`
	// LastRun is the most recent attempt, scheduled or manual.
	LastRun *Run `json:"last_run,omitempty"`
	// Destinations counts what is stored; Enabled counts what a run would
	// actually reach.
	Destinations        int  `json:"destinations"`
	EnabledDestinations int  `json:"enabled_destinations"`
	PassphraseSet       bool `json:"passphrase_set"`
	// Running is a backup happening right now.
	Running bool `json:"running"`
}

// Status describes what a backup taken now would hold.
func (s *Service) Status(ctx context.Context) Status {
	status := Status{
		Version:        s.cfg.Version,
		KeyFingerprint: s.fingerprint(),
		MinPassphrase:  MinPassphrase,
	}
	if info, err := os.Stat(s.cfg.DatabasePath); err == nil {
		status.DatabaseBytes = info.Size()
	}
	if s.cfg.TLSDir != "" {
		entries, err := os.ReadDir(s.cfg.TLSDir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					status.TLSFiles++
				}
			}
		}
	}
	if s.cfg.ConfigPath != "" {
		if _, err := os.Stat(s.cfg.ConfigPath); err == nil {
			status.ConfigIncluded = true
		}
	}
	if s.cfg.SchemaVersion != nil {
		status.SchemaVersion = s.cfg.SchemaVersion(ctx)
	}
	if s.cfg.Instance != nil {
		status.Instance = s.cfg.Instance(ctx)
	}
	if s.cfg.PluginsDir != "" {
		// Walked rather than measured from a listing, because the plugins
		// directory is the one place in an archive with subdirectories in it.
		// A failure is not reported: the page is describing what a backup would
		// hold, and Create is where an unreadable directory becomes a refusal.
		//
		// Bounded, because this runs on a page load and the directory is a bind
		// mount an operator writes into. A tree with a hundred thousand files
		// in it should make the page say "a lot" rather than make the page slow;
		// Create is the one that has to walk all of it, and it does that once
		// per run rather than once per request.
		_ = filepath.WalkDir(s.cfg.PluginsDir, func(_ string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return nil //nolint:nilerr // a listing, not a check
			}
			if info, err := d.Info(); err == nil {
				status.PluginFiles++
				status.PluginBytes += info.Size()
			}
			if status.PluginFiles >= maxStatusPluginFiles {
				status.PluginsTruncated = true
				return fs.SkipAll
			}
			return nil
		})
	}
	if pending, err := ReadPending(s.cfg.StorageDir); err == nil {
		status.Pending = pending
	}
	return status
}

// Filename is what a downloaded archive is offered as. Timestamped, because
// the common thing to do with a backup is take another one next month.
func (s *Service) Filename() string {
	return fmt.Sprintf("mcpd-%s%s", s.now().UTC().Format("20060102T150405Z"), Extension)
}

// Create streams an archive of this instance to w.
func (s *Service) Create(ctx context.Context, w io.Writer, passphrase string) error {
	schema := 0
	if s.cfg.SchemaVersion != nil {
		schema = s.cfg.SchemaVersion(ctx)
	}
	instance := ""
	if s.cfg.Instance != nil {
		instance = s.cfg.Instance(ctx)
	}

	return Create(ctx, w, Options{
		Snapshot: s.cfg.Snapshot,
		// The data volume, not /tmp: the container's /tmp is a small tmpfs and
		// a snapshot of any size fills it.
		WorkDir:        s.cfg.StorageDir,
		TLSDir:         s.cfg.TLSDir,
		ConfigPath:     s.cfg.ConfigPath,
		PluginsDir:     s.cfg.PluginsDir,
		KeyFingerprint: s.fingerprint(),
		Instance:       instance,
		Version:        s.cfg.Version,
		SchemaVersion:  schema,
		Passphrase:     passphrase,
		Now:            s.cfg.Now,
		Log:            s.cfg.Log,
	})
}

// Instance reports how this host is reached, for the slug in an archive's name.
func (s *Service) Instance(ctx context.Context) string {
	if s.cfg.Instance == nil {
		return ""
	}
	return s.cfg.Instance(ctx)
}

// Stage validates an uploaded archive and lays it out for the next start.
//
// Nothing the running host uses is touched, so a failure here leaves the
// instance exactly as it was.
func (s *Service) Stage(ctx context.Context, r io.Reader, passphrase, actor string) (*Pending, error) {
	s.staging.Lock()
	defer s.staging.Unlock()

	pending, err := Stage(ctx, r, StageOptions{
		Passphrase:     passphrase,
		StorageDir:     s.cfg.StorageDir,
		DatabasePath:   s.cfg.DatabasePath,
		TLSDir:         s.cfg.TLSDir,
		PluginsDir:     s.cfg.PluginsDir,
		KeyFingerprint: s.fingerprint(),
		MaxSchema:      s.cfg.MaxSchema,
		Verify:         s.cfg.VerifyDatabase,
		Actor:          actor,
		Now:            s.cfg.Now,
	})
	if err != nil {
		return nil, err
	}
	if s.cfg.Log != nil {
		s.cfg.Log.WarnContext(ctx, "a restore has been staged and will be applied on the next start",
			"actor", actor,
			"taken_at", pending.Manifest.CreatedAt,
			"from_version", pending.Manifest.Version,
			"from_instance", pending.Manifest.Instance)
	}
	return &pending, nil
}

// Pending reports a restore waiting for a restart, or nil.
func (s *Service) Pending() (*Pending, error) { return ReadPending(s.cfg.StorageDir) }

// Cancel discards a staged restore.
func (s *Service) Cancel(ctx context.Context, actor string) error {
	s.staging.Lock()
	defer s.staging.Unlock()

	if err := Cancel(s.cfg.StorageDir); err != nil {
		return err
	}
	if s.cfg.Log != nil {
		s.cfg.Log.WarnContext(ctx, "a staged restore was cancelled", "actor", actor)
	}
	return nil
}

// KeepSuperseded is how many replaced instances are kept.
//
// Not one, and not unbounded. A restore is the only operation here that
// destroys the current instance, and since it applies immediately there is no
// moment in which to change your mind -- so the database it replaced has to
// still exist. But each copy is the size of a database, and a run of restores
// while somebody works out which archive is the right one would otherwise
// leave a data volume full of them.
//
// Three, because the case this is for is exactly that run: restore, wrong one,
// restore again, and now the instance you actually wanted is two back.
const KeepSuperseded = 3

// PruneSuperseded removes all but the most recent few replaced instances.
//
// At startup rather than after a restore, and the difference matters: the
// newest copy is the one made moments ago by a restore that has only just
// applied, and pruning at the end of that restore would be deleting on the
// same pass that created. A start is the first moment the operator could have
// looked at the result.
//
// Names are timestamps in a fixed format, so sorting them as strings sorts
// them by time. Anything that does not look like one is left alone -- this
// removes directories, and it should only ever remove ones it wrote.
func PruneSuperseded(storageDir string, keep int, log *slog.Logger) {
	root := filepath.Join(storageDir, supersededDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && log != nil {
			log.Warn("could not check the superseded instances", "error", err)
		}
		return
	}

	var kept []string
	for _, entry := range entries {
		if entry.IsDir() && isTimestamp(entry.Name()) {
			kept = append(kept, entry.Name())
		}
	}
	if len(kept) <= keep {
		return
	}

	sort.Strings(kept)
	for _, name := range kept[:len(kept)-keep] {
		path := filepath.Join(root, name)
		if err := os.RemoveAll(path); err != nil {
			if log != nil {
				log.Warn("could not remove a superseded instance", "path", path, "error", err)
			}
			continue
		}
		if log != nil {
			log.Info("removed a superseded instance past the last "+
				strconv.Itoa(keep), "path", path)
		}
	}
}

// isTimestamp matches the name ApplyPending gives a superseded directory.
func isTimestamp(name string) bool {
	_, err := time.Parse("20060102T150405Z", name)
	return err == nil
}

// SweepWorkDirs removes staging directories left by a backup that was
// interrupted -- a download the browser abandoned, or a process that was
// stopped mid-archive.
//
// Called at startup rather than on a timer: they are only ever created while
// an archive is being written, so anything found at a cold start is by
// definition finished with. Without this a data volume slowly accumulates a
// copy of the database per abandoned download.
func SweepWorkDirs(storageDir string, log *slog.Logger) {
	entries, err := os.ReadDir(storageDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) && log != nil {
			log.Warn("could not check for abandoned backup working directories", "error", err)
		}
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !isWorkDir(entry.Name()) {
			continue
		}
		path := filepath.Join(storageDir, entry.Name())
		if err := os.RemoveAll(path); err != nil && log != nil {
			log.Warn("could not remove an abandoned backup working directory",
				"path", path, "error", err)
			continue
		}
		if log != nil {
			log.Info("removed an abandoned backup working directory", "path", path)
		}
	}
}

// isWorkDir matches what os.MkdirTemp produces from the prefix Create uses.
func isWorkDir(name string) bool {
	return len(name) > len("backup-") && name[:len("backup-")] == "backup-"
}
