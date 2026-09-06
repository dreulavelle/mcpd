package backup

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The part that happens when nobody is watching.
//
// One worker, one run at a time, and every decision it makes recorded in a row
// rather than in a log line -- because the question an operator asks is "did
// last night work", and the honest answer to that has to survive a restart.
//
// A run is: take one archive, then send that one archive to every enabled
// destination. Once, not once per destination. The snapshot is the expensive
// part and taking three of them would be three different instants pretending to
// be one backup.

// Trigger says what started a run.
const (
	TriggerSchedule = "schedule"
	TriggerManual   = "manual"
)

// Run status.
const (
	// StatusRunning is the row a run inserts before it does anything, so that a
	// second run cannot start and a crash leaves evidence.
	StatusRunning = "running"
	StatusOK      = "ok"
	// StatusPartial is some destinations and not others. It is not a failure:
	// there is a backup, it is just not everywhere it should be.
	StatusPartial = "partial"
	StatusFailed  = "failed"
	// StatusInterrupted is a run the process did not survive. Deliberately not
	// "failed": a write may have landed, and a reader told it failed would
	// conclude nothing was uploaded.
	StatusInterrupted = "interrupted"
)

// Run is one attempt to back this host up to its destinations.
type Run struct {
	ID          string     `json:"id"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Trigger     string     `json:"trigger"`
	ArchiveName string     `json:"archive_name,omitempty"`
	SizeBytes   int64      `json:"size_bytes"`
	Status      string     `json:"status"`
	// Error is the sentence and Detail the evidence behind it. Anything
	// rendering Detail in prose is a bug.
	Error        string           `json:"error,omitempty"`
	Detail       string           `json:"detail,omitempty"`
	Destinations []RunDestination `json:"destinations"`
}

// RunDestination is what happened at one destination during one run.
type RunDestination struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	OK   bool   `json:"ok"`
	// Error is the sentence, Detail the evidence.
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Removed is how many older archives retention took away here, and Held
	// says why it took none when it took none.
	Removed int    `json:"removed"`
	Held    string `json:"held,omitempty"`
}

// DestinationOutcome is what a run writes back onto the destination row.
type DestinationOutcome struct {
	ID     string
	At     time.Time
	OK     bool
	Error  string
	Detail string
	// Seen is how many of this instance's archives were listed here, and it is
	// UnknownSeen whenever this run has no trustworthy count -- a listing that
	// failed, or one retention held back as not the whole picture.
	//
	// The store leaves the stored baseline alone for UnknownSeen, and that is
	// the whole point of the value. Recording the small count a distrusted
	// listing produced would make it the number the *next* run measures itself
	// against, so the check that caught this one would never fire again and the
	// following truncated listing would delete real backups.
	Seen int
}

// RunStore is the database, seen from here.
//
// Narrow on purpose. This package deliberately knows nothing about SQLite, and
// the guarantees the runner depends on -- that two runs cannot both be running,
// that finishing a run only touches a row still marked running -- are the
// store's to keep in a WHERE clause.
type RunStore interface {
	// Destinations returns every stored destination, enabled or not, with its
	// credential in the clear. Only a run calls it.
	Destinations(ctx context.Context) ([]Destination, error)
	// ListDestinations is the same set without the credentials, for counting
	// them. The status is read on every page load and has no use for a secret,
	// and a host whose encryption key has been rotated without the credentials
	// being re-entered should still be able to draw the page that says so.
	ListDestinations(ctx context.Context) ([]Destination, error)
	// StartRun inserts a running row. It returns ErrRunInProgress when one is
	// already running, which is the only thing that decides whether a second
	// run may begin.
	StartRun(ctx context.Context, run Run) error
	// FinishRun settles a run that is still marked running.
	FinishRun(ctx context.Context, run Run) error
	// RecordDestination writes what happened at one destination.
	RecordDestination(ctx context.Context, out DestinationOutcome) error
	// LastScheduledRun reports when a scheduled run last started, and whether
	// one ever has.
	LastScheduledRun(ctx context.Context) (time.Time, bool, error)
	// LatestRun is the most recent run of any kind, for the summary.
	LatestRun(ctx context.Context) (Run, bool, error)
	// MarkInterrupted settles rows left running by a process that stopped.
	//
	// `before` is when this process's runner was built, and it is in the
	// statement's WHERE clause rather than assumed: the dashboard's listener
	// and this worker both start during App.Run, so a backup asked for over the
	// API in that window has a running row of its own -- and a sweep that took
	// every running row would settle the run it is about to carry out.
	MarkInterrupted(ctx context.Context, before, at time.Time) (int, error)
	// NewRunID mints an identifier, so the store keeps the one convention this
	// database uses for them.
	NewRunID() string
}

// ErrRunInProgress reports a backup that is already under way.
var ErrRunInProgress = errors.New("backup: a backup is already running")

// ErrNoDestination reports a run asked for with nowhere to send it.
var ErrNoDestination = errors.New(
	"backup: no destination is switched on, so there is nowhere to send a backup")

// ErrNoPassphrase reports a run asked for with nothing to seal the archive
// with.
var ErrNoPassphrase = fmt.Errorf(
	"backup: no passphrase is set, so a backup cannot be sealed. Set one on the "+
		"Backup page; it must be at least %d characters", MinPassphrase)

// RunnerConfig is what the runner needs from the host.
type RunnerConfig struct {
	Service *Service
	Store   RunStore

	// Schedule and Passphrase are read once per run rather than captured, so a
	// change on the page takes effect on the next fire instead of the next
	// restart.
	Schedule   func(ctx context.Context) Schedule
	Passphrase func(ctx context.Context) string

	// Pool is this host's trusted roots. A function because an operator can add
	// a certificate while mcpd runs, and a pool captured at startup would be
	// the set the process booted with.
	Pool func(ctx context.Context) *x509.CertPool

	// Failed is told when a run did not fully succeed. A function rather than
	// the notifier itself, so this package can report the event it knows about
	// without being able to invent others.
	Failed func(ctx context.Context, title, text string)

	Log *slog.Logger
	Now func() time.Time
	// Timer is time.After, taken as a field so a test can drive the schedule
	// without sleeping. CI runs every package under the race detector with ten
	// minutes to do it in, and a scheduler test that waits for real time is how
	// that budget is spent.
	Timer func(d time.Duration) <-chan time.Time
}

// Runner takes backups on a schedule and on demand.
type Runner struct {
	cfg RunnerConfig

	// manual carries a run id from the API to the worker. Buffered by one, and
	// sent to without blocking: the row inserted by StartRun is what actually
	// refuses a second run, so this channel never has to queue.
	manual chan string
	// wake re-arms the timer after the schedule changes. Also buffered by one
	// and sent to without blocking, because settings watchers run inside the
	// write path and a watcher that blocks holds the database's only writer.
	wake chan struct{}

	// openDestination builds a transport. A field rather than a direct call so
	// the runner's own tests can hand it a transport that does what the test
	// says, and stay about the runner's decisions rather than about SFTP.
	openDestination func(Destination, TransportOptions) (Transport, error)

	// builtAt is when this process's runner was constructed, which is what
	// divides a run left by a previous process from one this one has just been
	// asked for. Captured here rather than when the worker starts: the two are
	// not the same moment, and the API can be answering in between.
	builtAt time.Time
}

// NewRunner builds the runner. It does no I/O.
func NewRunner(cfg RunnerConfig) *Runner {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Timer == nil {
		cfg.Timer = time.After
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Runner{
		cfg:             cfg,
		manual:          make(chan string, 1),
		wake:            make(chan struct{}, 1),
		openDestination: OpenDestination,
		builtAt:         cfg.Now().UTC(),
	}
}

func (r *Runner) now() time.Time { return r.cfg.Now().UTC() }

// SettingsChanged re-arms the timer.
//
// Safe to call from a settings watcher, which runs synchronously inside the
// write path: the send never blocks, so a schedule saved while a backup is
// running does not hold the database's single writer open behind it.
func (r *Runner) SettingsChanged() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Trigger starts a run now and returns the row it inserted.
//
// The insert is the gate. Two administrators pressing the button together, or
// one pressing it as the schedule fires, produce one run and one refusal --
// decided by a guarded statement rather than by a lock this process holds and
// the database knows nothing about.
func (r *Runner) Trigger(ctx context.Context, trigger string) (Run, error) {
	// Counted without decrypting: this only decides whether a run has anywhere
	// to go, and the credentials are read by the run itself.
	dests, err := r.cfg.Store.ListDestinations(ctx)
	if err != nil {
		return Run{}, err
	}
	if countEnabled(dests) == 0 {
		return Run{}, ErrNoDestination
	}
	if len(r.cfg.Passphrase(ctx)) < MinPassphrase {
		return Run{}, ErrNoPassphrase
	}

	run := Run{
		ID:        r.cfg.Store.NewRunID(),
		StartedAt: r.now(),
		Trigger:   trigger,
		Status:    StatusRunning,
	}
	if err := r.cfg.Store.StartRun(ctx, run); err != nil {
		return Run{}, err
	}

	select {
	case r.manual <- run.ID:
		return run, nil
	default:
		// Unreachable while the row above is the gate: the worker drains this
		// channel before it runs, so a full buffer would mean a run row exists
		// that nothing is going to execute. Settle it here rather than leave a
		// row that stays running until the next restart calls it interrupted.
		run.Status = StatusFailed
		run.Error = "The backup could not be started."
		run.Detail = "the runner was not accepting work"
		finished := r.now()
		run.FinishedAt = &finished
		if err := r.cfg.Store.FinishRun(ctx, run); err != nil {
			r.cfg.Log.ErrorContext(ctx, "could not settle a backup run that was never started",
				"run_id", run.ID, "error", err)
		}
		return Run{}, ErrRunInProgress
	}
}

// Status describes what will happen without anybody pressing anything.
//
// It answers three questions in one place, because they are three ways of
// asking the same one: is this host actually backing itself up. A schedule that
// is on with no destination, or with no passphrase, is a page that looks
// configured and does nothing -- so NextRunAt is null in both cases and Reason
// says which.
func (r *Runner) Status(ctx context.Context) *ScheduleStatus {
	sched := r.cfg.Schedule(ctx)
	out := &ScheduleStatus{
		Enabled:  sched.Enabled,
		Cadence:  sched.Cadence,
		Weekday:  int(sched.Weekday),
		Time:     fmt.Sprintf("%02d:%02d", sched.Hour, sched.Minute),
		Timezone: sched.Location.String(),
		Reason:   sched.Warning,
	}
	out.PassphraseSet = len(r.cfg.Passphrase(ctx)) >= MinPassphrase

	dests, err := r.cfg.Store.ListDestinations(ctx)
	if err != nil {
		r.cfg.Log.ErrorContext(ctx, "could not read the backup destinations", "error", err)
	}
	out.Destinations = len(dests)
	out.EnabledDestinations = countEnabled(dests)

	if last, ok, err := r.cfg.Store.LatestRun(ctx); err != nil {
		r.cfg.Log.ErrorContext(ctx, "could not read the last backup run", "error", err)
	} else if ok {
		out.LastRun = &last
		out.Running = last.Status == StatusRunning
	}

	switch {
	case !sched.Enabled:
		out.Reason = "Scheduled backups are switched off."
	case out.EnabledDestinations == 0:
		out.Reason = "No destination is switched on, so a scheduled backup has nowhere to go."
	case !out.PassphraseSet:
		out.Reason = "No passphrase is set, so a scheduled backup cannot be sealed."
	default:
		next := sched.Next(r.now())
		if next.IsZero() {
			// A schedule whose instant this build cannot reach. Left as a
			// reason rather than a silent null, because the alternative is a
			// page that says a backup is scheduled and never takes one.
			if out.Reason == "" {
				out.Reason = "This schedule has no next run mcpd can work out."
			}
			return out
		}
		out.NextRunAt = &next
		// A warning from parsing survives into Reason beside a real next run:
		// the schedule is working, just not with the timezone that was asked
		// for.
	}
	return out
}

// Run is the worker. It returns when ctx is cancelled.
func (r *Runner) Run(ctx context.Context) error {
	// A run the process did not survive is settled first, so the history has no
	// row claiming to be running and the guarded insert is not blocked by one.
	//
	// Only rows older than this runner. A backup asked for over the API between
	// the dashboard's listener coming up and this worker reaching this line has
	// a running row of its own, and sweeping it would settle the very run this
	// worker is about to be handed.
	if n, err := r.cfg.Store.MarkInterrupted(ctx, r.builtAt, r.now()); err != nil {
		r.cfg.Log.ErrorContext(ctx, "could not settle backup runs left by a previous process",
			"error", err)
	} else if n > 0 {
		r.cfg.Log.WarnContext(ctx,
			"a backup was interrupted by mcpd stopping; some destinations may hold it",
			"runs", n)
	}

	r.catchUp(ctx)

	for {
		sched := r.cfg.Schedule(ctx)
		wait := time.Duration(0)
		var next time.Time
		if sched.Enabled {
			next = sched.Next(r.now())
			if next.IsZero() {
				// A schedule with no reachable instant. Wait to be woken by a
				// change rather than spinning.
				wait = 24 * time.Hour
			} else {
				wait = next.Sub(r.now())
			}
		} else {
			// Nothing to fire. The worker still exists so that switching the
			// schedule on takes effect without a restart.
			wait = 24 * time.Hour
		}

		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
			continue
		case id := <-r.manual:
			r.execute(ctx, id, TriggerManual)
		case <-r.cfg.Timer(wait):
			if !sched.Enabled || next.IsZero() || r.now().Before(next) {
				continue
			}
			if !next.IsZero() && next.Hour() != sched.Hour {
				// Daylight saving moved the clock over the time that was asked
				// for. time.Date normalises it forward rather than skipping the
				// run, which is the right answer, but it is worth saying once.
				r.cfg.Log.InfoContext(ctx,
					"the scheduled backup time does not exist on this date, so it runs at the next instant that does",
					"asked_for", fmt.Sprintf("%02d:%02d", sched.Hour, sched.Minute),
					"running_at", next.Format(time.RFC3339))
			}
			r.fire(ctx, TriggerSchedule)
		}
	}
}

// catchUp runs once at startup when a scheduled backup was missed.
//
// Only when a scheduled run has happened before. Enabling the schedule at
// three in the afternoon must not immediately take a backup because this
// morning's four o'clock is in the past -- the operator asked for tomorrow, and
// a run they did not ask for is a run they have to explain.
func (r *Runner) catchUp(ctx context.Context) {
	sched := r.cfg.Schedule(ctx)
	if !sched.Enabled {
		return
	}
	last, ever, err := r.cfg.Store.LastScheduledRun(ctx)
	if err != nil {
		r.cfg.Log.ErrorContext(ctx, "could not read when the last scheduled backup ran", "error", err)
		return
	}
	if !ever {
		return
	}
	due := sched.Previous(r.now())
	if due.IsZero() || !last.Before(due) {
		return
	}
	r.cfg.Log.InfoContext(ctx, "a scheduled backup was missed while mcpd was not running, taking one now",
		"was_due", due.Format(time.RFC3339), "last_ran", last.Format(time.RFC3339))
	r.fire(ctx, TriggerSchedule)
}

// fire starts a scheduled run, and says nothing when there is nothing to do.
//
// A schedule with no destination is not an error worth a row in the history
// every night; the status says so instead, on the page where it can be fixed.
func (r *Runner) fire(ctx context.Context, trigger string) {
	run, err := r.Trigger(ctx, trigger)
	switch {
	case err == nil:
		// Trigger posts to the channel this loop selects on. Drain it here
		// rather than going round the loop, so a scheduled run does not have to
		// wait for the next iteration to start.
		select {
		case <-r.manual:
		default:
		}
		r.execute(ctx, run.ID, trigger)
	case errors.Is(err, ErrNoDestination), errors.Is(err, ErrNoPassphrase):
		r.cfg.Log.WarnContext(ctx, "the schedule fired but there is nothing to do", "reason", err)
	case errors.Is(err, ErrRunInProgress):
		r.cfg.Log.WarnContext(ctx, "the schedule fired while a backup was still running; skipping it")
	default:
		r.cfg.Log.ErrorContext(ctx, "the scheduled backup could not be started", "error", err)
	}
}

// execute does the work of one run, and always settles the row.
func (r *Runner) execute(ctx context.Context, runID, trigger string) {
	run := Run{ID: runID, StartedAt: r.now(), Trigger: trigger, Status: StatusRunning}

	spool, name, size, err := r.writeArchive(ctx, &run)
	if spool != "" {
		defer os.RemoveAll(filepath.Dir(spool))
	}
	if err != nil {
		run.Status = StatusFailed
		run.Error = "The backup could not be written, so nothing was sent anywhere."
		run.Detail = err.Error()
		r.cfg.Log.ErrorContext(ctx, "a backup could not be written", "run_id", runID, "error", err)
		r.settle(ctx, run)
		return
	}
	run.ArchiveName, run.SizeBytes = name, size

	dests, err := r.cfg.Store.Destinations(ctx)
	if err != nil {
		run.Status = StatusFailed
		run.Error = "The backup was written but the list of destinations could not be read."
		run.Detail = err.Error()
		r.settle(ctx, run)
		return
	}

	pool := (*x509.CertPool)(nil)
	if r.cfg.Pool != nil {
		pool = r.cfg.Pool(ctx)
	}

	ok, failed := 0, 0
	for _, d := range dests {
		if !d.Enabled {
			continue
		}
		outcome := r.send(ctx, d, spool, name, size, pool)
		run.Destinations = append(run.Destinations, outcome)
		if outcome.OK {
			ok++
		} else {
			failed++
		}
	}

	switch {
	case ok > 0 && failed == 0:
		run.Status = StatusOK
	case ok > 0:
		run.Status = StatusPartial
		run.Error = fmt.Sprintf(
			"The backup reached %d of %d destinations.", ok, ok+failed)
	default:
		run.Status = StatusFailed
		run.Error = "The backup was written but did not reach any destination."
	}
	r.settle(ctx, run)
}

// writeArchive spools one archive to the data volume.
//
// To a file rather than straight to a destination, and this is the one place it
// matters: the same bytes go to every destination, so they are written once and
// read back once per destination from a fresh handle. Streaming into the first
// destination would mean taking a second snapshot for the second one, at a
// different instant, of a database that has moved.
//
// The directory is prefixed `backup-`, which is what SweepWorkDirs already
// looks for -- so a process that dies mid-run leaves something the next start
// collects rather than a copy of the database nothing owns.
func (r *Runner) writeArchive(ctx context.Context, run *Run) (spool, name string, size int64, err error) {
	svc := r.cfg.Service
	dir, err := os.MkdirTemp(svc.cfg.StorageDir, "backup-")
	if err != nil {
		return "", "", 0, fmt.Errorf("create a working directory: %w", err)
	}

	// The run's id goes on the end of the name. The timestamp is only to the
	// second, and two runs in one second -- a schedule firing as somebody
	// presses the button -- would otherwise write the same name twice, with the
	// second replacing the first while the history showed two successes.
	name = ArchiveName(svc.Instance(ctx), run.StartedAt, run.ID)
	spool = filepath.Join(dir, name)

	f, err := os.OpenFile(spool, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return spool, "", 0, fmt.Errorf("create %s: %w", spool, err)
	}
	if err := svc.Create(ctx, f, r.cfg.Passphrase(ctx)); err != nil {
		f.Close()
		return spool, "", 0, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return spool, "", 0, fmt.Errorf("flush %s: %w", spool, err)
	}
	if err := f.Close(); err != nil {
		return spool, "", 0, fmt.Errorf("close %s: %w", spool, err)
	}

	info, err := os.Stat(spool)
	if err != nil {
		return spool, "", 0, fmt.Errorf("measure %s: %w", spool, err)
	}
	return spool, name, info.Size(), nil
}

// send uploads to one destination and applies its retention.
func (r *Runner) send(
	ctx context.Context, d Destination, spool, name string, size int64, pool *x509.CertPool,
) RunDestination {
	out := RunDestination{ID: d.ID, Name: d.Name, Kind: d.Kind}

	transport, err := r.openDestination(d, TransportOptions{Pool: pool, Log: r.cfg.Log, Now: r.cfg.Now})
	if err != nil {
		return r.failed(ctx, d, &out, err)
	}
	defer transport.Close()

	// A fresh handle per destination. One reader shared between them would be
	// at end of file for the second.
	f, err := os.Open(spool)
	if err != nil {
		return r.failed(ctx, d, &out, err)
	}
	uploadErr := transport.Put(ctx, name, f, size)
	f.Close()
	if uploadErr != nil {
		return r.failed(ctx, d, &out, uploadErr)
	}

	// Retention only after a successful upload, and only from this
	// destination's own listing. Deleting on the strength of what some other
	// destination holds is how one broken NAS empties a bucket.
	listing, err := transport.List(ctx)
	if err != nil {
		out.OK = true
		out.Held = "The backup was uploaded. Old backups were not removed, because " +
			"the destination could not be listed."
		// UnknownSeen, not zero. A listing that failed says nothing about how
		// many archives are here, and writing a number over the baseline would
		// disarm the check that stops a truncated listing being acted on.
		r.record(ctx, d, out, UnknownSeen)
		return out
	}

	decision := Retain(listing, d.Policy, name, d.LastSeen, r.location(ctx))
	out.Held = decision.Held
	for _, obj := range decision.Remove {
		if err := transport.Delete(ctx, obj.Name); err != nil {
			// Recorded and carried on. A delete that failed leaves an extra
			// archive, which is the harmless direction to fail in, and the
			// destination is still a success: the backup got there.
			r.cfg.Log.WarnContext(ctx, "could not remove an old backup",
				"destination", d.Name, "archive", obj.Name, "error", err)
			continue
		}
		out.Removed++
	}

	out.OK = true
	r.record(ctx, d, out, decision.Seen)
	return out
}

// failed fills in a destination's outcome from an error, keeping the sentence
// and the evidence apart.
func (r *Runner) failed(ctx context.Context, d Destination, out *RunDestination, err error) RunDestination {
	out.OK = false
	out.Error, out.Detail = describe(err)
	r.cfg.Log.ErrorContext(ctx, "a backup could not be sent to a destination",
		"destination", d.Name, "kind", string(d.Kind), "error", err)
	r.record(ctx, d, *out, UnknownSeen)
	return *out
}

// Evidencer is an error that has already been written for a person, with its
// evidence kept separate. HostKeyMismatch and the WebDAV refusals are the two
// that implement it.
type Evidencer interface {
	error
	Evidence() string
}

// describe splits an error into the sentence somebody reads and the evidence
// behind it.
//
// An error that has already been written for a person says so by carrying its
// own evidence; anything else gets a sentence from here and keeps its text as
// the evidence, because a Go error is a developer's sentence and putting one on
// a page is what docs/dashboard-copy.md exists to stop.
func describe(err error) (sentence, evidence string) {
	var written Evidencer
	if errors.As(err, &written) {
		return written.Error(), written.Evidence()
	}
	switch {
	case errors.Is(err, ErrNoHostKey):
		return ErrNoHostKey.Error(), ""
	case errors.Is(err, context.Canceled):
		return "The backup was stopped before it finished.", err.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "The destination took too long to answer.", err.Error()
	}
	return "The backup could not be sent to this destination.", err.Error()
}

// record writes a destination's outcome onto its row.
//
// seen is UnknownSeen whenever this run has no trustworthy count of what is
// here, and the store leaves the stored baseline alone for exactly that value.
// See DestinationOutcome.Seen.
func (r *Runner) record(ctx context.Context, d Destination, out RunDestination, seen int) {
	err := r.cfg.Store.RecordDestination(ctx, DestinationOutcome{
		ID:     d.ID,
		At:     r.now(),
		OK:     out.OK,
		Error:  out.Error,
		Detail: out.Detail,
		Seen:   seen,
	})
	if err != nil {
		r.cfg.Log.ErrorContext(ctx, "could not record what happened at a backup destination",
			"destination", d.Name, "error", err)
	}
}

// settle finishes the run row and tells somebody when it did not work.
func (r *Runner) settle(ctx context.Context, run Run) {
	finished := r.now()
	run.FinishedAt = &finished
	if err := r.cfg.Store.FinishRun(ctx, run); err != nil {
		r.cfg.Log.ErrorContext(ctx, "could not record how a backup ended",
			"run_id", run.ID, "status", run.Status, "error", err)
	}

	switch run.Status {
	case StatusOK:
		r.cfg.Log.InfoContext(ctx, "backup sent",
			"run_id", run.ID, "archive", run.ArchiveName,
			"bytes", run.SizeBytes, "destinations", len(run.Destinations))
		return
	case StatusPartial, StatusFailed:
	default:
		return
	}
	if r.cfg.Failed == nil {
		return
	}

	var names []string
	for _, d := range run.Destinations {
		if !d.OK {
			names = append(names, d.Name)
		}
	}
	title := "A backup did not reach where it was going"
	text := run.Error
	if len(names) > 0 {
		text += " It did not reach " + strings.Join(names, ", ") + "."
	}
	// Nothing from Detail. A notification goes to a chat channel, and the
	// evidence belongs on the page beside the run it describes.
	r.cfg.Failed(ctx, title, text)
}

// location is where retention counts days, weeks and months. The schedule's
// timezone, because an operator who asked for a weekly backup on Sunday means
// their Sunday.
func (r *Runner) location(ctx context.Context) *time.Location {
	if r.cfg.Schedule == nil {
		return time.UTC
	}
	if loc := r.cfg.Schedule(ctx).Location; loc != nil {
		return loc
	}
	return time.UTC
}

func countEnabled(dests []Destination) int {
	n := 0
	for _, d := range dests {
		if d.Enabled {
			n++
		}
	}
	return n
}
