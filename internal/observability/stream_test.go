package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// The backlog is what somebody opening the page during an incident is there
// to read.
func TestLogStream_KeepsWhatWasLoggedBeforeAnybodyWatched(t *testing.T) {
	log, _, stream := NewStreamingLogger(&bytes.Buffer{}, slog.LevelInfo, "json", true)

	log.Info("first")
	log.Info("second")

	lines := stream.Recent()
	if len(lines) != 2 {
		t.Fatalf("kept %d lines; want 2", len(lines))
	}
	if !bytes.Contains(lines[0], []byte(`"first"`)) ||
		!bytes.Contains(lines[1], []byte(`"second"`)) {
		t.Errorf("the backlog is not in the order it happened: %s", lines)
	}
}

// Past capacity the oldest go, and what is left still reads oldest first --
// a ring that reports itself from wherever the write head happens to be would
// hand somebody the middle of the log as its beginning.
func TestLogStream_KeepsTheMostRecentInOrder(t *testing.T) {
	log, _, stream := NewStreamingLogger(&bytes.Buffer{}, slog.LevelInfo, "json", true)

	for i := 0; i < StreamCapacity+10; i++ {
		log.Info("line", "n", i)
	}

	lines := stream.Recent()
	if len(lines) != StreamCapacity {
		t.Fatalf("kept %d lines; want %d", len(lines), StreamCapacity)
	}
	if !bytes.Contains(lines[0], []byte(`"n":10`)) {
		t.Errorf("the oldest line kept is not the one expected: %s", lines[0])
	}
	if !bytes.Contains(lines[len(lines)-1], []byte(`"n":509`)) {
		t.Errorf("the newest line kept is not the one expected: %s", lines[len(lines)-1])
	}
}

// The gap this exists to close: reading the backlog and then subscribing loses
// whatever was logged in between, which is the moment somebody watching a
// restart most wants to see.
func TestLogStream_NothingFallsBetweenTheBacklogAndTheStream(t *testing.T) {
	log, _, stream := NewStreamingLogger(&bytes.Buffer{}, slog.LevelInfo, "json", true)
	log.Info("before")

	watch := stream.Watch()
	defer watch.Close()
	log.Info("after")

	if len(watch.Backlog) != 1 || !bytes.Contains(watch.Backlog[0], []byte(`"before"`)) {
		t.Fatalf("backlog was %s", watch.Backlog)
	}
	select {
	case line := <-watch.Lines:
		if !bytes.Contains(line, []byte(`"after"`)) {
			t.Errorf("the line that followed was %s", line)
		}
	default:
		t.Error("what was logged after the watch began never arrived")
	}
}

// A browser must never be able to stall the process that is logging. The
// alternative to dropping is a goroutine blocked inside Handle holding the
// writer's lock, with everything else that wants to log queued behind it.
func TestLogStream_ASlowWatcherIsDroppedRatherThanWaitedFor(t *testing.T) {
	log, _, stream := NewStreamingLogger(&bytes.Buffer{}, slog.LevelInfo, "json", true)

	watch := stream.Watch()
	defer watch.Close()

	// Nothing reads watch.Lines, so it fills and stays full. If this blocks,
	// the test hangs -- which is the failure, stated as plainly as the test
	// can state it.
	for i := 0; i < subscriberBuffer*3; i++ {
		log.Info("busy", "n", i)
	}

	if dropped := watch.Dropped(); dropped == 0 {
		t.Error("a watcher that read nothing was never reported as having missed anything")
	}
	// Counted once and forgotten, or the same gap is reported for ever.
	if again := watch.Dropped(); again != 0 {
		t.Errorf("the same %d dropped lines were reported twice", again)
	}
}

// The whole point of taking the copy through a handler rather than off the
// destination's bytes. Redaction is a property of the options both share, so
// a value withheld from the file is not available to a browser either.
func TestLogStream_RedactionAppliesToWhatTheDashboardSees(t *testing.T) {
	log, _, stream := NewStreamingLogger(&bytes.Buffer{}, slog.LevelInfo, "json", true)

	log.Info("dialling", "api_key", "sk-should-never-appear", "url", "https://example.com")

	lines := stream.Recent()
	if len(lines) != 1 {
		t.Fatalf("kept %d lines; want 1", len(lines))
	}
	if bytes.Contains(lines[0], []byte("sk-should-never-appear")) {
		t.Errorf("a credential reached the dashboard's copy: %s", lines[0])
	}
	if !bytes.Contains(lines[0], []byte(Redacted)) {
		t.Errorf("the value was dropped rather than marked redacted: %s", lines[0])
	}
}

// The dashboard parses these, so they have to be JSON whatever an operator has
// chosen for the file. Those are different audiences and only one of them can
// be asked to cope with the change.
func TestLogStream_StaysJSONWhenTheFileTurnsToText(t *testing.T) {
	var file bytes.Buffer
	log, ctl, stream := NewStreamingLogger(&file, slog.LevelInfo, "json", true)

	ctl.Set(slog.LevelInfo, "text")
	log.Info("switched", "n", 1)

	if strings.HasPrefix(file.String(), "{") {
		t.Fatal("the file did not switch to text, so this proves nothing")
	}
	lines := stream.Recent()
	var record map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &record); err != nil {
		t.Fatalf("the dashboard's copy stopped being JSON: %v", err)
	}
	if record["msg"] != "switched" {
		t.Errorf("record read back as %v", record)
	}
}

// Nobody watching is the ordinary case, and it must not be the one that costs
// anything: the tap is nil, so a line is rendered once.
func TestStreamingLogger_NotAskedForMeansNotKept(t *testing.T) {
	log, _, stream := NewStreamingLogger(&bytes.Buffer{}, slog.LevelInfo, "json", false)
	log.Info("nothing is watching")
	if stream != nil {
		t.Error("a host that asked for no copy was given one anyway")
	}
}

// Watching is a thing several people may do at once, and closing one view must
// not disturb another.
func TestLogStream_IsSafeUnderConcurrentUse(t *testing.T) {
	log, _, stream := NewStreamingLogger(&bytes.Buffer{}, slog.LevelInfo, "json", true)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				log.Info("working", "n", j)
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := stream.Watch()
			for j := 0; j < 50; j++ {
				select {
				case <-w.Lines:
				default:
				}
			}
			w.Close()
			// Twice, because a handler that returns early and then runs its
			// deferred close would otherwise panic on a closed channel.
			w.Close()
		}()
	}
	wg.Wait()
}
