package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The runner's tests are all in-process and on a fake clock. CI runs every
// package under the race detector with ten minutes to do it in, and a scheduler
// test that waits for real time is how that budget gets spent.

// fakeStore is the database, remembered in a map.
type fakeStore struct {
	mu sync.Mutex

	dests []Destination
	runs  []Run
	// outcomes is what was recorded against each destination, in order.
	outcomes []DestinationOutcome
	// lastScheduled is what LastScheduledRun answers, and everScheduled
	// whether one has ever happened.
	lastScheduled time.Time
	everScheduled bool
	// settled is signalled by FinishRun, so a test can wait for the worker to
	// finish a run rather than sleeping until it probably has.
	settled chan string
	// failStart makes StartRun refuse, standing in for a row already running.
	failStart bool
	nextID    int
}

func (f *fakeStore) Destinations(context.Context) ([]Destination, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Destination(nil), f.dests...), nil
}

func (f *fakeStore) StartRun(_ context.Context, run Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failStart {
		return ErrRunInProgress
	}
	for _, existing := range f.runs {
		if existing.Status == StatusRunning {
			return ErrRunInProgress
		}
	}
	f.runs = append(f.runs, run)
	if run.Trigger == TriggerSchedule {
		f.lastScheduled, f.everScheduled = run.StartedAt, true
	}
	return nil
}

func (f *fakeStore) FinishRun(_ context.Context, run Run) error {
	f.mu.Lock()
	settle := f.settled
	found := false
	for i := range f.runs {
		if f.runs[i].ID == run.ID && f.runs[i].Status == StatusRunning {
			started, trigger := f.runs[i].StartedAt, f.runs[i].Trigger
			f.runs[i] = run
			f.runs[i].StartedAt, f.runs[i].Trigger = started, trigger
			found = true
			break
		}
	}
	f.mu.Unlock()

	if !found {
		return errors.New("no running row with that id")
	}
	if settle != nil {
		// Outside the lock: the test reads the run back through this store.
		settle <- run.ID
	}
	return nil
}

func (f *fakeStore) RecordDestination(_ context.Context, out DestinationOutcome) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.outcomes = append(f.outcomes, out)
	for i := range f.dests {
		if f.dests[i].ID == out.ID && out.OK {
			f.dests[i].LastSeen = out.Seen
		}
	}
	return nil
}

func (f *fakeStore) LastScheduledRun(context.Context) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastScheduled, f.everScheduled, nil
}

func (f *fakeStore) LatestRun(context.Context) (Run, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.runs) == 0 {
		return Run{}, false, nil
	}
	return f.runs[len(f.runs)-1], true, nil
}

func (f *fakeStore) MarkInterrupted(_ context.Context, before, at time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for i := range f.runs {
		if f.runs[i].Status == StatusRunning && f.runs[i].StartedAt.Before(before) {
			f.runs[i].Status = StatusInterrupted
			finished := at
			f.runs[i].FinishedAt = &finished
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) NewRunID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	return fmt.Sprintf("bkr_%03d", f.nextID)
}

func (f *fakeStore) run(id string) (Run, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.runs {
		if r.ID == id {
			return r, true
		}
	}
	return Run{}, false
}

// fakeTransport is a destination that does what the test told it to.
type fakeTransport struct {
	mu       sync.Mutex
	putErr   error
	listErr  error
	objects  []Object
	put      []string
	deleted  []string
	putBytes int64
}

func (f *fakeTransport) Put(_ context.Context, name string, r io.Reader, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return f.putErr
	}
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		return err
	}
	f.putBytes = n
	f.put = append(f.put, name)
	f.objects = append(f.objects, Object{Name: name, Size: n})
	return nil
}

func (f *fakeTransport) List(context.Context) ([]Object, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]Object(nil), f.objects...), nil
}

func (f *fakeTransport) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, name)
	kept := f.objects[:0]
	for _, o := range f.objects {
		if o.Name != name {
			kept = append(kept, o)
		}
	}
	f.objects = kept
	return nil
}

func (f *fakeTransport) Check(context.Context) error { return nil }
func (f *fakeTransport) Close() error                { return nil }

func (f *fakeTransport) snapshot() (put, deleted []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.put...), append([]string(nil), f.deleted...)
}

// harness wires a runner against fakes, with the transports it will open.
type harness struct {
	t          *testing.T
	store      *fakeStore
	runner     *Runner
	transports map[string]*fakeTransport
	// notified holds every failure notification the runner raised.
	notified []string
	now      time.Time
	storage  string
}

func newHarness(t *testing.T, dests ...Destination) *harness {
	t.Helper()
	storage := t.TempDir()
	database := filepath.Join(storage, "mcpd.db")
	if err := os.WriteFile(database, []byte("a pretend database"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := &harness{
		t:          t,
		store:      &fakeStore{dests: dests},
		transports: map[string]*fakeTransport{},
		now:        time.Date(2026, 2, 8, 4, 0, 0, 0, time.UTC),
		storage:    storage,
	}
	for _, d := range dests {
		h.transports[d.ID] = &fakeTransport{}
	}

	service := NewService(ServiceConfig{
		Snapshot: func(_ context.Context, path string) error {
			return os.WriteFile(path, []byte("a pretend database"), 0o600)
		},
		StorageDir:   storage,
		DatabasePath: database,
		Version:      "test",
		Instance:     func(context.Context) string { return "https://nas.example.com" },
		Now:          func() time.Time { return h.now },
	})

	h.runner = NewRunner(RunnerConfig{
		Service: service,
		Store:   h.store,
		Schedule: func(context.Context) Schedule {
			return Schedule{
				Enabled: true, Cadence: CadenceWeekly, Weekday: time.Sunday,
				Hour: 4, Location: time.UTC,
			}
		},
		Passphrase: func(context.Context) string { return "a-long-enough-passphrase" },
		Failed: func(_ context.Context, _, text string) {
			h.notified = append(h.notified, text)
		},
		Now: func() time.Time { return h.now },
	})
	// The transports are handed over rather than dialled: what these tests are
	// about is the runner's decisions, and a real transport would be testing
	// the transport.
	h.runner.openDestination = func(d Destination, _ TransportOptions) (Transport, error) {
		transport, ok := h.transports[d.ID]
		if !ok {
			return nil, fmt.Errorf("no transport for %s", d.ID)
		}
		return transport, nil
	}
	return h
}

func dest(id, name string, enabled bool) Destination {
	return Destination{
		ID: id, Name: name, Kind: KindLocal, Enabled: enabled,
		Policy: Policy{KeepLast: 2}, Settings: Settings{Path: "/somewhere"},
	}
}

// One archive, sent to every destination.
//
// Once, not once per destination: the snapshot is the expensive part, and three
// of them would be three different instants pretending to be one backup.
func TestARunSendsOneArchiveToEveryEnabledDestination(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true), dest("dst_2", "bucket", true),
		dest("dst_3", "old nas", false))

	run, err := h.runner.Trigger(t.Context(), TriggerManual)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	h.runner.execute(t.Context(), run.ID, TriggerManual)

	settled, ok := h.store.run(run.ID)
	if !ok {
		t.Fatal("the run row is gone")
	}
	if settled.Status != StatusOK {
		t.Fatalf("status %q, error %q, detail %q", settled.Status, settled.Error, settled.Detail)
	}
	if len(settled.Destinations) != 2 {
		t.Fatalf("recorded %d destinations, want the 2 that are switched on", len(settled.Destinations))
	}

	first, _ := h.transports["dst_1"].snapshot()
	second, _ := h.transports["dst_2"].snapshot()
	if len(first) != 1 || len(second) != 1 || first[0] != second[0] {
		t.Fatalf("the two destinations got %v and %v, want one identical archive", first, second)
	}
	if first[0] != settled.ArchiveName {
		t.Errorf("uploaded %q but recorded %q", first[0], settled.ArchiveName)
	}
	if _, ours := TimeFromName(settled.ArchiveName); !ours {
		t.Errorf("the archive is called %q, which retention cannot read", settled.ArchiveName)
	}
	if settled.SizeBytes <= 0 {
		t.Error("the run recorded no size")
	}
	// The bytes really reached both, from a fresh handle each. One reader
	// shared between them would be at end of file for the second.
	if h.transports["dst_2"].putBytes != settled.SizeBytes {
		t.Errorf("the second destination received %d bytes, want %d",
			h.transports["dst_2"].putBytes, settled.SizeBytes)
	}
	if len(h.transports["dst_3"].put) != 0 {
		t.Error("a destination that is switched off received a backup")
	}

	// The spool is gone: a copy of the database left on the data volume every
	// night is how one fills up.
	entries, err := os.ReadDir(h.storage)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "backup-") {
			t.Errorf("the run left %s behind", e.Name())
		}
	}
}

// A run asked for over the API is carried out by the worker.
//
// Trigger inserts the row and posts the id; the worker picks it up and does the
// work. It is the one handoff in this package that crosses a goroutine, and
// everything else here tests the two halves separately.
func TestAManualRunIsCarriedOutByTheWorker(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true))
	h.store.settled = make(chan string, 1)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// A timer that never fires, so nothing but the manual run wakes the loop.
	// No clock, and therefore no sleeping: the test waits on the store.
	h.runner.cfg.Timer = func(time.Duration) <-chan time.Time { return nil }

	done := make(chan error, 1)
	go func() { done <- h.runner.Run(ctx) }()

	run, err := h.runner.Trigger(ctx, TriggerManual)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	select {
	case id := <-h.store.settled:
		if id != run.ID {
			t.Fatalf("the worker settled %s, want %s", id, run.ID)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the worker never ran the backup that was asked for")
	}

	settled, _ := h.store.run(run.ID)
	if settled.Status != StatusOK {
		t.Errorf("status %q: %s", settled.Status, settled.Error)
	}
	if put, _ := h.transports["dst_1"].snapshot(); len(put) != 1 {
		t.Errorf("the destination received %v", put)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("the worker stopped with %v", err)
	}
}

// Some destinations and not others is 'partial', not 'failed'. There is a
// backup; it is just not everywhere it should be.
func TestARunThatReachesSomeDestinationsIsPartial(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true), dest("dst_2", "bucket", true))
	h.transports["dst_2"].putErr = errors.New("the bucket said no")

	run, err := h.runner.Trigger(t.Context(), TriggerManual)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	h.runner.execute(t.Context(), run.ID, TriggerManual)

	settled, _ := h.store.run(run.ID)
	if settled.Status != StatusPartial {
		t.Fatalf("status %q, want partial", settled.Status)
	}
	if len(h.notified) != 1 {
		t.Fatalf("raised %d notifications, want one", len(h.notified))
	}
	if !strings.Contains(h.notified[0], "bucket") {
		t.Errorf("the notification does not name the destination that failed: %q", h.notified[0])
	}
	// The evidence stays out of the message that goes to a chat channel; it
	// belongs on the page beside the run it describes.
	if strings.Contains(h.notified[0], "said no") {
		t.Errorf("the notification carries the upstream's own words: %q", h.notified[0])
	}

	// The failing destination's row carries the sentence and the evidence
	// apart, and the working one is not marked down with it.
	var failed, worked DestinationOutcome
	for _, out := range h.store.outcomes {
		if out.ID == "dst_2" {
			failed = out
		} else {
			worked = out
		}
	}
	if failed.OK {
		t.Error("the destination that refused the upload was recorded as working")
	}
	if failed.Error == "" || !strings.Contains(failed.Detail, "the bucket said no") {
		t.Errorf("outcome sentence %q, evidence %q", failed.Error, failed.Detail)
	}
	if strings.Contains(failed.Error, "the bucket said no") {
		t.Errorf("the evidence is in the sentence: %q", failed.Error)
	}
	if !worked.OK {
		t.Error("the destination that worked was recorded as failed")
	}
}

// Every destination failing is 'failed', and nothing about it says the archive
// was not written -- it was, it just did not get anywhere.
func TestARunThatReachesNoDestinationIsFailed(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true))
	h.transports["dst_1"].putErr = errors.New("connection refused")

	run, _ := h.runner.Trigger(t.Context(), TriggerManual)
	h.runner.execute(t.Context(), run.ID, TriggerManual)

	settled, _ := h.store.run(run.ID)
	if settled.Status != StatusFailed {
		t.Fatalf("status %q, want failed", settled.Status)
	}
	if len(h.notified) != 1 {
		t.Errorf("raised %d notifications, want one", len(h.notified))
	}
}

// Retention runs per destination, from that destination's own listing. Deleting
// on the strength of what some other destination holds is how one broken NAS
// empties a bucket.
func TestRetentionRunsPerDestinationAfterTheUpload(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true), dest("dst_2", "bucket", true))
	old := []Object{
		{Name: "mcpd-nas-example-com-20260201T040000Z.mcpdbak"},
		{Name: "mcpd-nas-example-com-20260125T040000Z.mcpdbak"},
		{Name: "mcpd-nas-example-com-20260118T040000Z.mcpdbak"},
	}
	h.transports["dst_1"].objects = append([]Object(nil), old...)
	// The second destination is empty apart from what this run puts there, so
	// it has nothing to prune.

	run, _ := h.runner.Trigger(t.Context(), TriggerManual)
	h.runner.execute(t.Context(), run.ID, TriggerManual)

	settled, _ := h.store.run(run.ID)
	if settled.Status != StatusOK {
		t.Fatalf("status %q: %s", settled.Status, settled.Error)
	}

	_, deleted := h.transports["dst_1"].snapshot()
	// KeepLast is 2: the new archive and the newest old one stay.
	want := []string{
		"mcpd-nas-example-com-20260118T040000Z.mcpdbak",
		"mcpd-nas-example-com-20260125T040000Z.mcpdbak",
	}
	if len(deleted) != len(want) {
		t.Fatalf("deleted %v, want %v", deleted, want)
	}
	for i := range want {
		if deleted[i] != want[i] {
			t.Fatalf("deleted %v, want %v (oldest first)", deleted, want)
		}
	}
	if _, otherDeleted := h.transports["dst_2"].snapshot(); len(otherDeleted) != 0 {
		t.Errorf("the second destination lost %v on the strength of the first's listing", otherDeleted)
	}
	for _, out := range settled.Destinations {
		if out.ID == "dst_1" && out.Removed != 2 {
			t.Errorf("the run recorded %d removed, want 2", out.Removed)
		}
	}
}

// A listing that could not be read leaves the destination a success. The backup
// got there, which is the thing that mattered; not pruning is worth saying and
// not worth failing over.
func TestADestinationThatCannotBeListedStillCountsAsUploaded(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true))
	run, _ := h.runner.Trigger(t.Context(), TriggerManual)
	h.transports["dst_1"].listErr = errors.New("permission denied")
	h.runner.execute(t.Context(), run.ID, TriggerManual)

	settled, _ := h.store.run(run.ID)
	if settled.Status != StatusOK {
		t.Fatalf("status %q, want ok: the archive was uploaded", settled.Status)
	}
	if settled.Destinations[0].Held == "" {
		t.Error("nothing was pruned and nothing said why")
	}
	if len(h.notified) != 0 {
		t.Errorf("a successful upload raised a failure notification: %v", h.notified)
	}
}

// A second run cannot start while one is going. The row is the gate, so this
// holds across two processes and not only inside one.
func TestASecondRunIsRefusedWhileOneIsRunning(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true))
	if _, err := h.runner.Trigger(t.Context(), TriggerManual); err != nil {
		t.Fatalf("first trigger: %v", err)
	}
	_, err := h.runner.Trigger(t.Context(), TriggerManual)
	if !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("got %v, want a refusal naming the run already going", err)
	}
}

// A run asked for with nowhere to send it, or nothing to seal it with, is
// refused before a row is written. A history full of rows recording that
// somebody had not finished configuring the page is not history.
func TestARunIsRefusedBeforeItStartsWhenThereIsNothingToDo(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", false))
	if _, err := h.runner.Trigger(t.Context(), TriggerManual); !errors.Is(err, ErrNoDestination) {
		t.Errorf("got %v, want a refusal about there being no destination", err)
	}

	h = newHarness(t, dest("dst_1", "nas", true))
	h.runner.cfg.Passphrase = func(context.Context) string { return "short" }
	if _, err := h.runner.Trigger(t.Context(), TriggerManual); !errors.Is(err, ErrNoPassphrase) {
		t.Errorf("got %v, want a refusal about the passphrase", err)
	}
	if len(h.store.runs) != 0 {
		t.Errorf("%d rows were written for runs that never started", len(h.store.runs))
	}
}

// A run the process did not survive is 'interrupted', never 'failed'.
//
// Indeterminate is not terminal: the run may have uploaded to some destinations
// and not others, and a reader told it failed would conclude nothing was
// written and go looking for a backup that is actually there.
func TestARunInterruptedByACrashIsNotRecordedAsFailed(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true))

	// A row left running by a previous process: it started before this runner
	// was built, which is what tells the two apart.
	stale := Run{
		ID: "bkr_stale", StartedAt: h.now.Add(-time.Hour),
		Trigger: TriggerSchedule, Status: StatusRunning,
	}
	h.store.runs = append(h.store.runs, stale)

	// The worker starts, settles it, and stops when its context is cancelled.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.runner.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	settled, _ := h.store.run(stale.ID)
	if settled.Status != StatusInterrupted {
		t.Fatalf("status %q, want interrupted", settled.Status)
	}
	if settled.FinishedAt == nil {
		t.Error("an interrupted run says nothing about when it ended")
	}
	if settled.Status == StatusFailed {
		t.Error("an interrupted run was recorded as a failure")
	}
}

// A backup asked for over the API while the worker is still starting is not
// swept away by the worker's own startup sweep.
//
// The dashboard's listener and this worker both come up during App.Run, so
// there is a window in which a run exists and nothing is watching the channel
// yet. A sweep that took every running row would settle the run it is about to
// be handed, and the operator who pressed the button would see it recorded as
// interrupted by a process that had only just started.
func TestTheStartupSweepLeavesARunThisProcessJustStarted(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true))

	// Asked for after the runner was built, which is the window in question.
	h.now = h.now.Add(time.Second)
	run, err := h.runner.Trigger(t.Context(), TriggerManual)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := h.runner.Run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Still running, or already carried out -- the worker may pick the run out
	// of the channel before it notices the cancelled context, and both of those
	// are right. What must not happen is the sweep calling it interrupted.
	settled, _ := h.store.run(run.ID)
	if settled.Status == StatusInterrupted {
		t.Errorf("the startup sweep settled a run this process had just been asked for")
	}
}

// Enabling the schedule at three in the afternoon must not immediately take a
// backup because this morning's four o'clock is in the past. The operator asked
// for tomorrow, and a run they did not ask for is a run they have to explain.
func TestCatchUpDoesNotFireWhenTheScheduleWasJustEnabled(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true))
	// A Sunday afternoon, hours after the 04:00 the schedule names, with no
	// scheduled run ever having happened.
	h.now = time.Date(2026, 2, 8, 15, 0, 0, 0, time.UTC)

	h.runner.catchUp(t.Context())

	if len(h.store.runs) != 0 {
		t.Errorf("switching the schedule on took %d backups straight away", len(h.store.runs))
	}
}

// A month of downtime catches up exactly once, not once per missed week.
func TestCatchUpFiresOnceAfterAMonthDown(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true))
	h.store.lastScheduled = time.Date(2026, 1, 4, 4, 0, 0, 0, time.UTC)
	h.store.everScheduled = true
	h.now = time.Date(2026, 2, 9, 9, 0, 0, 0, time.UTC)

	h.runner.catchUp(t.Context())

	if len(h.store.runs) != 1 {
		t.Fatalf("took %d backups after a month down, want one", len(h.store.runs))
	}
	if h.store.runs[0].Trigger != TriggerSchedule {
		t.Errorf("the catch-up was recorded as %q", h.store.runs[0].Trigger)
	}

	// And running again does nothing: the row it just wrote is now the last
	// scheduled run, and it is not before the due instant.
	h.runner.catchUp(t.Context())
	if len(h.store.runs) != 1 {
		t.Errorf("catching up twice took %d backups", len(h.store.runs))
	}
}

// A schedule that is up to date has nothing to catch up on.
func TestCatchUpDoesNothingWhenTheLastRunIsRecentEnough(t *testing.T) {
	h := newHarness(t, dest("dst_1", "nas", true))
	h.store.lastScheduled = time.Date(2026, 2, 8, 4, 0, 0, 0, time.UTC)
	h.store.everScheduled = true
	h.now = time.Date(2026, 2, 8, 9, 0, 0, 0, time.UTC)

	h.runner.catchUp(t.Context())
	if len(h.store.runs) != 0 {
		t.Errorf("took %d backups when the last one had already happened", len(h.store.runs))
	}
}

// The status is what the page opens with, and the question it answers is
// whether this host is actually backing itself up. A schedule that is on with
// no destination is a page that looks configured and does nothing, so it has no
// next run and says which of the two is missing.
func TestStatusSaysWhyNothingIsGoingToHappen(t *testing.T) {
	cases := []struct {
		name string
		set  func(h *harness)
		want string
	}{
		{
			name: "no destination is switched on",
			set: func(h *harness) {
				h.store.dests = []Destination{dest("dst_1", "nas", false)}
			},
			want: "no destination",
		},
		{
			name: "no passphrase",
			set: func(h *harness) {
				h.runner.cfg.Passphrase = func(context.Context) string { return "" }
			},
			want: "passphrase",
		},
		{
			name: "the schedule is off",
			set: func(h *harness) {
				h.runner.cfg.Schedule = func(context.Context) Schedule {
					return Schedule{Location: time.UTC}
				}
			},
			want: "switched off",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, dest("dst_1", "nas", true))
			tc.set(h)
			status := h.runner.Status(t.Context())
			if status.NextRunAt != nil {
				t.Errorf("a next run at %s was named when none is going to happen", status.NextRunAt)
			}
			if !strings.Contains(strings.ToLower(status.Reason), tc.want) {
				t.Errorf("reason %q, want it to mention %q", status.Reason, tc.want)
			}
		})
	}

	h := newHarness(t, dest("dst_1", "nas", true))
	status := h.runner.Status(t.Context())
	if status.NextRunAt == nil {
		t.Fatalf("no next run named on a working schedule: %q", status.Reason)
	}
	if status.Reason != "" {
		t.Errorf("a working schedule gave a reason not to run: %q", status.Reason)
	}
	if !status.PassphraseSet || status.EnabledDestinations != 1 || status.Destinations != 1 {
		t.Errorf("summary %+v", status)
	}
}
