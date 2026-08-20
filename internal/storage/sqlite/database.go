// Package sqlite is the only package in mcpd that writes SQL. Everything above
// it works through the interfaces in internal/storage.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver; keeps the binary CGO-free
)

// DB holds two connection pools against the same database file.
//
// SQLite in WAL mode permits many concurrent readers alongside exactly one
// writer. Sharing a single database/sql pool between readers and writers means
// two goroutines can both begin write transactions and one gets SQLITE_BUSY.
// Serialising writers in Go instead — a pool capped at one connection — turns
// that contention into an orderly queue with no retry loop and no busy errors
// on the hot path.
//
// The split also serves latency directly: agent tool calls are overwhelmingly
// reads, and under WAL a reader never blocks behind the writer.
type DB struct {
	read  *sql.DB
	write *sql.DB
	path  string
}

// Options configures database startup.
type Options struct {
	// Path is the database file. It must be a real path; in-memory databases
	// cannot provide the durability this system depends on.
	Path string

	// ReadPoolSize bounds concurrent readers. Zero selects a value derived
	// from GOMAXPROCS.
	ReadPoolSize int

	// BusyTimeout bounds how long a statement waits for a lock before
	// erroring. Zero selects five seconds.
	BusyTimeout time.Duration

	// RelaxedDurability drops synchronous from FULL to NORMAL.
	//
	// Leave this false. Under WAL, NORMAL can lose the most recent
	// transactions on power loss or host failure, and those transactions are
	// approvals of infrastructure changes. The setting exists for test
	// fixtures and throwaway environments, not for production.
	RelaxedDurability bool
}

func (o *Options) withDefaults() {
	if o.ReadPoolSize <= 0 {
		o.ReadPoolSize = max(4, runtime.GOMAXPROCS(0))
	}
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = 5 * time.Second
	}
}

// Open prepares the database file and returns pools ready for use. It does not
// apply migrations; call Migrate separately so that startup can report schema
// changes distinctly from connection failures.
func Open(ctx context.Context, opts Options) (*DB, error) {
	opts.withDefaults()
	if strings.TrimSpace(opts.Path) == "" {
		return nil, fmt.Errorf("sqlite: database path is required")
	}
	if opts.Path == ":memory:" || strings.Contains(opts.Path, "mode=memory") {
		return nil, fmt.Errorf("sqlite: in-memory databases cannot provide durable approvals")
	}
	abs, err := filepath.Abs(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: resolve path: %w", err)
	}

	sync := "FULL"
	if opts.RelaxedDurability {
		sync = "NORMAL"
	}

	// The writer opens first so it creates the file and establishes WAL mode
	// before any reader attaches.
	//
	// txlock=immediate makes BeginTx issue BEGIN IMMEDIATE, taking the write
	// lock up front. SQLite's default deferred transaction acquires it lazily
	// on first write, so a read-then-write transaction can fail with
	// SQLITE_BUSY partway through after already doing work. Taking the lock at
	// the start turns that into an orderly wait instead.
	write, err := openPool(ctx, abs, sync, opts.BusyTimeout, 1, "immediate")
	if err != nil {
		return nil, fmt.Errorf("sqlite: open writer: %w", err)
	}
	read, err := openPool(ctx, abs, sync, opts.BusyTimeout, opts.ReadPoolSize, "")
	if err != nil {
		write.Close()
		return nil, fmt.Errorf("sqlite: open reader: %w", err)
	}

	db := &DB{read: read, write: write, path: abs}
	if err := db.verify(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openPool(ctx context.Context, path, sync string, busy time.Duration, conns int, txlock string) (*sql.DB, error) {
	q := url.Values{}
	// Pragmas are per-connection in SQLite, so they belong in the DSN where
	// the driver replays them on every new connection in the pool. Setting
	// them once via Exec would apply to one connection only.
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", fmt.Sprintf("synchronous(%s)", sync))
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	// Bound WAL growth on a process that runs for months.
	q.Add("_pragma", "wal_autocheckpoint(1000)")
	// 64 MiB page cache (negative value means KiB). The working set here is
	// small; this keeps the hot indexes resident.
	q.Add("_pragma", "cache_size(-65536)")
	// Keep temporary b-trees for ORDER BY off disk.
	q.Add("_pragma", "temp_store(MEMORY)")
	if txlock != "" {
		q.Set("_txlock", txlock)
	}

	dsn := "file:" + path + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(conns)
	db.SetMaxIdleConns(conns)
	// Connections are cheap to keep and expensive to re-establish (each one
	// replays every pragma), so they are retained for the process lifetime.
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// verify asserts that the settings this system depends on actually took
// effect. A silently ignored pragma would mean believing writes are durable
// when they are not, so a mismatch fails startup rather than being logged.
func (d *DB) verify(ctx context.Context) error {
	var journal string
	if err := d.write.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return fmt.Errorf("sqlite: read journal_mode: %w", err)
	}
	if !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("sqlite: journal_mode is %q, want wal", journal)
	}
	var fk int
	if err := d.write.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("sqlite: read foreign_keys: %w", err)
	}
	if fk != 1 {
		return fmt.Errorf("sqlite: foreign_keys is off")
	}
	return nil
}

// Reader returns the pool for queries. Never use it for writes: a write on a
// reader connection can deadlock against the writer pool.
func (d *DB) Reader() *sql.DB { return d.read }

// Writer returns the single-connection pool for mutations.
func (d *DB) Writer() *sql.DB { return d.write }

// Path returns the resolved database path.
func (d *DB) Path() string { return d.path }

// Close shuts both pools down and checkpoints the WAL so the database file is
// self-contained on exit.
func (d *DB) Close() error {
	var firstErr error
	if d.read != nil {
		if err := d.read.Close(); err != nil {
			firstErr = err
		}
	}
	if d.write != nil {
		// Fold the WAL back into the main file. Best effort: a failure here
		// costs startup time on the next boot, not correctness.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = d.write.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
		cancel()
		if err := d.write.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Stats reports pool utilisation for the health endpoint.
func (d *DB) Stats() (read, write sql.DBStats) {
	return d.read.Stats(), d.write.Stats()
}
