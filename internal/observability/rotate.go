package observability

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The rotation policy.
//
// Constants rather than settings, and the reason is the same one that shapes
// the rest of this package: logging has to be running before the database
// that would hold a setting is open. A policy that cannot be read at the
// moment it is needed is not a policy.
const (
	// MaxLogSize is the size at which the active file is rotated.
	MaxLogSize = 20 << 20

	// MaxLogAge is how long one file collects before it is rotated, however
	// small it still is. A file per week, so the file covering an incident is
	// found by its name rather than by opening it.
	MaxLogAge = 7 * 24 * time.Hour

	// RetainedLogs is how many rotated files are kept behind the active one.
	//
	// Five weeks of history, and never more than (RetainedLogs+1)*MaxLogSize
	// on disk, so the ceiling is arithmetic rather than a hope. mcpd runs on
	// somebody else's hardware; a log that can fill their disk is a fault.
	RetainedLogs = 5
)

// RotatingFile is an append-only log file that rotates on size or on age and
// keeps a bounded number of predecessors.
//
// Rotation is done here rather than left to logrotate because mcpd ships as a
// container and the thing that would run logrotate is not in the image.
//
// Nothing is buffered. A record is written to the file as it is logged, so a
// process that is killed loses no line it had already logged -- which is the
// case the file exists for.
type RotatingFile struct {
	mu sync.Mutex

	path    string
	maxSize int64
	maxAge  time.Duration
	keep    int

	// now is a field so a test can rotate a week-old file without waiting a
	// week.
	now func() time.Time

	f    *os.File
	size int64
	// started is when the active file began collecting, which is what the age
	// limit is measured from.
	started time.Time
	// reported stops a directory that cannot be written filling stderr with
	// one complaint per log line. Cleared by a rotation that works.
	reported bool
}

// OpenRotatingFile opens path for appending under the standard policy,
// creating the directory if it is not there.
func OpenRotatingFile(path string) (*RotatingFile, error) {
	return openRotatingFile(path, MaxLogSize, MaxLogAge, RetainedLogs, time.Now)
}

func openRotatingFile(path string, maxSize int64, maxAge time.Duration, keep int, now func() time.Time) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("log directory: %w", err)
	}
	r := &RotatingFile{
		path:    path,
		maxSize: maxSize,
		maxAge:  maxAge,
		keep:    keep,
		now:     now,
	}
	if err := r.open(); err != nil {
		return nil, err
	}
	// What is already on disk may be over a limit: mcpd was down for a
	// fortnight, or the file was left just under the cap. Checked once here so
	// the first line of a new run does not land in a file that should have
	// been rotated before it started.
	if r.overLimit(0) {
		if err := r.rotate(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Path is where the active file is, so a caller can say so at startup.
func (r *RotatingFile) Path() string { return r.path }

// Write appends one record, rotating first if this one would take the file
// over a limit.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.f == nil {
		return 0, errors.New("observability: log file is closed")
	}
	if r.overLimit(len(p)) {
		if err := r.rotate(); err != nil {
			// A failed rotation must not cost the line. The file is still open
			// and still writable; it is only larger than it should be, and the
			// next write tries again.
			//
			// Reported to stderr rather than logged, because logging here is
			// how this function was reached.
			if !r.reported {
				fmt.Fprintln(os.Stderr, "mcpd: log rotation failed:", err)
				r.reported = true
			}
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Close closes the active file. There is nothing to flush.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// open attaches to the file at r.path, creating it if it is not there. The
// caller holds mu, except in openRotatingFile where nothing else has it yet.
func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat log file: %w", err)
	}
	r.f = f
	r.size = info.Size()
	r.started = r.startOf(info)
	return nil
}

// overLimit reports whether writing n more bytes should rotate first. The
// caller holds mu.
//
// An empty file is never rotated. A single record larger than the whole size
// limit would otherwise rotate on every write and leave a directory of empty
// files behind it.
func (r *RotatingFile) overLimit(n int) bool {
	if r.size == 0 {
		return false
	}
	if r.size+int64(n) > r.maxSize {
		return true
	}
	return r.now().Sub(r.started) >= r.maxAge
}

// rotate retires the active file and opens a fresh one. The caller holds mu.
func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(r.path, r.rotatedName()); err != nil {
		// Reopen, so a failure to rename does not also lose the destination.
		if openErr := r.open(); openErr != nil {
			return errors.Join(err, openErr)
		}
		return err
	}
	if err := r.open(); err != nil {
		return err
	}
	r.reported = false
	r.prune()
	return nil
}

// rotatedName is where the active file goes when it is retired.
//
// Named for the moment it was retired and zero-padded throughout, so the
// directory sorts into the order things happened. Colons are left out because
// Windows and more than one archive tool refuse them.
func (r *RotatingFile) rotatedName() string {
	ext := filepath.Ext(r.path)
	base := strings.TrimSuffix(r.path, ext)
	stamp := r.now().UTC().Format("2006-01-02T15-04-05Z")

	name := base + "-" + stamp + ext
	// Two rotations in the same second would otherwise have the second
	// silently overwrite the first.
	for i := 1; i < 100; i++ {
		if _, err := os.Stat(name); errors.Is(err, fs.ErrNotExist) {
			return name
		}
		name = fmt.Sprintf("%s-%s.%d%s", base, stamp, i, ext)
	}
	return name
}

// prune deletes the oldest rotated files beyond the retention count. The
// caller holds mu.
//
// Best effort: a file that will not delete is not worth failing a write for,
// and the next rotation tries again.
func (r *RotatingFile) prune() {
	ext := filepath.Ext(r.path)
	base := strings.TrimSuffix(r.path, ext)

	matches, err := filepath.Glob(base + "-*" + ext)
	if err != nil || len(matches) <= r.keep {
		return
	}
	// The stamp is fixed width, so lexical order is chronological order.
	sort.Strings(matches)
	for _, old := range matches[:len(matches)-r.keep] {
		os.Remove(old)
	}
}

// startOf decides when the active file began collecting.
//
// The filesystem cannot answer this portably -- Go exposes a modification
// time, and that is the last write rather than the first, so a week-old file
// written to a second ago would look new and never rotate on age. So the
// first record is read and its own timestamp used, which is exact and costs
// one small read per start. An empty file starts now; one whose first line
// will not parse falls back to the modification time, which is the best of
// the answers left.
func (r *RotatingFile) startOf(info fs.FileInfo) time.Time {
	if info.Size() == 0 {
		return r.now()
	}
	if t, ok := firstRecordTime(r.path); ok {
		return t
	}
	return info.ModTime()
}

// firstRecordTime reads the timestamp of the first record in the file.
//
// Both handler formats are understood, because an operator may have switched
// between them while this file was collecting. The JSON form is tried first:
// a JSON line can carry a bare time= inside a message -- the tunnel client's
// own output does exactly that -- and matching it would date the file from
// whatever an upstream happened to log.
func firstRecordTime(path string) (time.Time, bool) {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()

	buf := make([]byte, 8<<10)
	n, err := f.Read(buf)
	if n == 0 || (err != nil && !errors.Is(err, io.EOF)) {
		return time.Time{}, false
	}
	line := buf[:n]
	if i := bytes.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}

	for _, f := range []struct{ prefix, terminator string }{
		{`"time":"`, `"`},
		{"time=", " "},
	} {
		i := bytes.Index(line, []byte(f.prefix))
		if i < 0 {
			continue
		}
		value := line[i+len(f.prefix):]
		if j := bytes.Index(value, []byte(f.terminator)); j >= 0 {
			value = value[:j]
		}
		if t, err := time.Parse(time.RFC3339Nano, string(value)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
