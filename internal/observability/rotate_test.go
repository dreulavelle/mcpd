package observability

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a hand-wound time source, so an age-based rotation is testable
// without waiting for one.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func rotated(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "mcpd-*.log"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return matches
}

func TestRotatesWhenTheSizeLimitWouldBePassed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcpd.log")
	c := &clock{t: time.Now()}

	r, err := openRotatingFile(path, 64, time.Hour, 5, c.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	first := []byte(strings.Repeat("a", 40) + "\n")
	if _, err := r.Write(first); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Together these pass 64 bytes, so the second must land in a new file
	// rather than take the first over the limit.
	c.advance(time.Second)
	if _, err := r.Write([]byte(strings.Repeat("b", 40) + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	old := rotated(t, dir)
	if len(old) != 1 {
		t.Fatalf("want 1 rotated file, got %d: %v", len(old), old)
	}
	if got, err := os.ReadFile(old[0]); err != nil || string(got) != string(first) {
		t.Fatalf("rotated file should hold the first record, got %q (%v)", got, err)
	}
	active, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(active), "bbb") {
		t.Fatalf("active file should hold the second record, got %q (%v)", active, err)
	}
}

func TestRotatesOnAgeEvenWhenTheFileIsSmall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcpd.log")
	c := &clock{t: time.Now()}

	r, err := openRotatingFile(path, 1<<20, 7*24*time.Hour, 5, c.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	if _, err := r.Write([]byte("first\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if n := len(rotated(t, dir)); n != 0 {
		t.Fatalf("nothing should have rotated yet, got %d", n)
	}

	c.advance(7 * 24 * time.Hour)
	if _, err := r.Write([]byte("second\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if n := len(rotated(t, dir)); n != 1 {
		t.Fatalf("a week-old file should rotate however small it is, got %d", n)
	}
}

// An empty file must not rotate, or a record larger than the whole size limit
// rotates on every write and leaves a directory of empty files behind it.
func TestASingleOversizedRecordDoesNotRotateAnEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcpd.log")
	c := &clock{t: time.Now()}

	r, err := openRotatingFile(path, 16, time.Hour, 5, c.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	for i := range 3 {
		if _, err := r.Write([]byte(strings.Repeat("x", 100) + "\n")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		c.advance(time.Second)
	}

	// Three writes, each over the limit: the first fills the empty file, and
	// each one after it rotates exactly once.
	if n := len(rotated(t, dir)); n != 2 {
		t.Fatalf("want 2 rotated files, got %d", n)
	}
	for _, old := range rotated(t, dir) {
		info, err := os.Stat(old)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Size() == 0 {
			t.Fatalf("%s is empty; an empty file was rotated", old)
		}
	}
}

func TestKeepsOnlyTheRetainedNumberOfRotatedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcpd.log")
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	const keep = 3
	r, err := openRotatingFile(path, 32, time.Hour, keep, c.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	for i := range 8 {
		if _, err := r.Write([]byte(fmt.Sprintf("%02d ", i) + strings.Repeat("y", 30) + "\n")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		// A distinct second per rotation, so the names are distinct and sort.
		c.advance(time.Second)
	}

	old := rotated(t, dir)
	if len(old) != keep {
		t.Fatalf("want %d retained files, got %d: %v", keep, len(old), old)
	}
	// The ones kept must be the newest, not whichever the filesystem listed
	// first.
	got, err := os.ReadFile(old[0])
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.HasPrefix(string(got), "04 ") {
		t.Fatalf("oldest retained file should be record 04, got %q", got)
	}
}

// The age limit is measured from the first record in the file, so a restart
// does not reset the clock and let a file collect for ever.
func TestAgeSurvivesAReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcpd.log")
	start := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	c := &clock{t: start}

	r, err := openRotatingFile(path, 1<<20, 7*24*time.Hour, 5, c.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	line := fmt.Sprintf(`{"time":"%s","level":"INFO","msg":"first"}`+"\n", start.Format(time.RFC3339Nano))
	if _, err := r.Write([]byte(line)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Restarted eight days later. The file is small, so only its age can
	// rotate it -- and its age is only knowable from the record inside it.
	c.advance(8 * 24 * time.Hour)
	r2, err := openRotatingFile(path, 1<<20, 7*24*time.Hour, 5, c.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer r2.Close()

	if n := len(rotated(t, dir)); n != 1 {
		t.Fatalf("an eight-day-old file should rotate on the next start, got %d", n)
	}
}

func TestReopeningAppendsRatherThanTruncating(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcpd.log")
	c := &clock{t: time.Now()}

	r, err := openRotatingFile(path, 1<<20, time.Hour, 5, c.now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := r.Write([]byte("before restart\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	r2, err := openRotatingFile(path, 1<<20, time.Hour, 5, c.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer r2.Close()
	if _, err := r2.Write([]byte("after restart\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if want := "before restart\nafter restart\n"; string(got) != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestWriteAfterCloseReportsRatherThanPanics(t *testing.T) {
	dir := t.TempDir()
	r, err := openRotatingFile(filepath.Join(dir, "mcpd.log"), 1<<20, time.Hour, 5, time.Now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := r.Write([]byte("late\n")); err == nil {
		t.Fatal("writing to a closed file should report an error")
	}
	// Closing twice is what a deferred Close does after an explicit one.
	if err := r.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestFirstRecordTimeReadsBothFormats(t *testing.T) {
	want := time.Date(2026, 8, 31, 14, 50, 1, 523434695, time.UTC)
	// The JSON case carries a bare time= inside the message, which is what the
	// tunnel client's own output looks like. The JSON field must win.
	cases := map[string]string{
		"json": fmt.Sprintf(`{"time":"%s","level":"INFO","msg":"tunnel-client","line":"time=2020-01-01T00:00:00Z level=INFO"}`,
			want.Format(time.RFC3339Nano)),
		"text": fmt.Sprintf("time=%s level=INFO msg=started", want.Format(time.RFC3339Nano)),
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mcpd.log")
			if err := os.WriteFile(path, []byte(line+"\n"), 0o640); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, ok := firstRecordTime(path)
			if !ok {
				t.Fatal("the timestamp should have been read")
			}
			if !got.Equal(want) {
				t.Fatalf("want %s, got %s", want, got)
			}
		})
	}
}

func TestFirstRecordTimeFallsBackWhenTheLineIsNotARecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcpd.log")
	if err := os.WriteFile(path, []byte("not a log record at all\n"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := firstRecordTime(path); ok {
		t.Fatal("an unparseable line should not yield a time")
	}
}

// The destination is written by two handlers at once, so concurrent writes
// must not tear a record or lose one.
func TestConcurrentWritesAreSerialised(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcpd.log")

	r, err := openRotatingFile(path, 1<<20, time.Hour, 5, time.Now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	const writers, each = 8, 50
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			line := []byte(strings.Repeat(string(rune('a'+i)), 32) + "\n")
			for range each {
				if _, err := r.Write(line); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
	if len(lines) != writers*each {
		t.Fatalf("want %d lines, got %d", writers*each, len(lines))
	}
	for _, line := range lines {
		if len(line) != 32 {
			t.Fatalf("torn record: %q", line)
		}
	}
}
