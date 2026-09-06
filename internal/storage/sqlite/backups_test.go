package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/backup"
)

func newBackupStore(t *testing.T, storageDir string) *BackupStore {
	t.Helper()
	return NewBackupStore(newTestDB(t), testCipher{}, nil, storageDir)
}

func localDest(t *testing.T, name string) backup.Destination {
	t.Helper()
	return backup.Destination{
		Name: name, Kind: backup.KindLocal, Enabled: true,
		Policy:   backup.DefaultPolicy,
		Settings: backup.Settings{Path: t.TempDir()},
	}
}

// A destination's credential is encrypted on the way in and readable on the way
// out, and the ciphertext is what is on disk.
//
// The same arrangement every other stored credential has. A row somebody dumps
// out of the database with sqlite3 must not hand them a NAS password.
func TestBackupDestinationCredentialIsEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())

	d := localDest(t, "nas")
	d.Secret = "hunter2"
	created, err := store.CreateDestination(ctx, "user:someone", d)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var stored string
	if err := store.db.Reader().QueryRowContext(ctx,
		`SELECT secret FROM backup_destinations WHERE id = ?`, created.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "hunter2" || stored == "" {
		t.Fatalf("the column holds %q; the credential must be sealed", stored)
	}

	read, ok, err := store.Destination(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("read back: %v", err)
	}
	if read.Secret != "hunter2" {
		t.Errorf("read back %q", read.Secret)
	}
}

// A destination is edited leaving unsent fields alone.
//
// The page never reads a credential back, so an edit that changes only the
// retention arrives with no secret at all. Reading that as an erasure would
// silently break the destination on the next run.
func TestUpdateDestinationWithNoSecretKeepsTheOneItHas(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())

	d := localDest(t, "nas")
	d.Secret = "hunter2"
	created, err := store.CreateDestination(ctx, "user:someone", d)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	policy := backup.Policy{KeepLast: 12}
	updated, err := store.UpdateDestination(ctx, "user:someone", created.ID,
		backup.DestinationUpdate{Policy: &policy})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Secret != "hunter2" {
		t.Errorf("the credential became %q after an edit that did not mention it", updated.Secret)
	}
	if updated.Policy.KeepLast != 12 {
		t.Errorf("the retention did not change: %+v", updated.Policy)
	}

	// An empty string is an erasure, which is a different instruction.
	empty := ""
	cleared, err := store.UpdateDestination(ctx, "user:someone", created.ID,
		backup.DestinationUpdate{Secret: &empty})
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.Secret != "" {
		t.Errorf("the credential survived being cleared: %q", cleared.Secret)
	}
}

// Editing or removing a destination that is not there is a refusal rather than
// a silent no-op, so a form left open on a page somebody else has since changed
// says so.
func TestActingOnADestinationThatIsGoneIsARefusal(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())

	name := "renamed"
	if _, err := store.UpdateDestination(ctx, "user:someone", "dst_missing",
		backup.DestinationUpdate{Name: &name}); !errors.Is(err, ErrNoSuchDestination) {
		t.Errorf("update: got %v, want a refusal", err)
	}
	if err := store.DeleteDestination(ctx, "user:someone", "dst_missing"); !errors.Is(err, ErrNoSuchDestination) {
		t.Errorf("delete: got %v, want a refusal", err)
	}
	if _, err := store.RecordHostKey(ctx, "user:someone", "dst_missing", "SHA256:x"); !errors.Is(err, ErrNoSuchDestination) {
		t.Errorf("record host key: got %v, want a refusal", err)
	}
}

// An edit that moves nothing writes nothing, so an operator who opened a form
// and closed it does not leave an entry in the trail.
func TestAnEditToADestinationThatChangesNothingIsNotRecorded(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	created, err := store.CreateDestination(ctx, "user:someone", localDest(t, "nas"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	same := created.Name
	if _, err := store.UpdateDestination(ctx, "user:someone", created.ID,
		backup.DestinationUpdate{Name: &same}); err != nil {
		t.Fatalf("update: %v", err)
	}

	var n int
	if err := store.db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE kind = 'backup.destination.updated'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d entries were recorded for an edit that changed nothing", n)
	}
}

// An SFTP destination cannot be switched on with no host key recorded.
//
// No trust on first use: a run that learned an identity would be trusting
// whatever answered that night, and anything that can put itself on that
// address would get a complete copy of this instance.
func TestAnSFTPDestinationCannotBeEnabledWithNoHostKey(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())

	d := backup.Destination{
		Name: "nas", Kind: backup.KindSFTP, Policy: backup.DefaultPolicy,
		Settings: backup.Settings{Host: "nas.example.com", Username: "ops"},
		Secret:   "hunter2", Enabled: true,
	}
	if _, err := store.CreateDestination(ctx, "user:someone", d); !errors.Is(err, backup.ErrNoHostKey) {
		t.Fatalf("got %v, want a refusal naming the missing host key", err)
	}

	d.Enabled = false
	created, err := store.CreateDestination(ctx, "user:someone", d)
	if err != nil {
		t.Fatalf("create it switched off: %v", err)
	}

	on := true
	if _, err := store.UpdateDestination(ctx, "user:someone", created.ID,
		backup.DestinationUpdate{Enabled: &on}); !errors.Is(err, backup.ErrNoHostKey) {
		t.Fatalf("got %v, want a refusal when switching it on", err)
	}

	// Recording the key is what makes it usable, and only Test connection does
	// that.
	if _, err := store.RecordHostKey(ctx, "user:someone", created.ID, "SHA256:abcdef"); err != nil {
		t.Fatalf("record: %v", err)
	}
	enabled, err := store.UpdateDestination(ctx, "user:someone", created.ID,
		backup.DestinationUpdate{Enabled: &on})
	if err != nil {
		t.Fatalf("switch on after recording a key: %v", err)
	}
	if !enabled.Enabled || enabled.HostKey != "SHA256:abcdef" {
		t.Errorf("destination is %+v", enabled)
	}
}

// Recording a host key is a first contact, never an overwrite.
//
// If it could overwrite, a server presenting a different key would be one
// button press away from being accepted -- which is the whole thing pinning it
// exists to prevent.
func TestRecordHostKeyNeverOverwritesAPin(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())

	created, err := store.CreateDestination(ctx, "user:someone", backup.Destination{
		Name: "nas", Kind: backup.KindSFTP, Policy: backup.DefaultPolicy,
		Settings: backup.Settings{Host: "nas.example.com", Username: "ops"},
		Secret:   "hunter2", HostKey: "SHA256:original",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// It reports that it recorded nothing rather than failing, and the caller
	// has to read that: a Test connection answering "recorded" on a destination
	// that already has a different key pinned would tell an operator the
	// fingerprint on their screen is the one mcpd checks against.
	recorded, err := store.RecordHostKey(ctx, "user:someone", created.ID, "SHA256:different")
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if recorded {
		t.Error("recording over a pinned key was reported as having worked")
	}
	read, _, err := store.Destination(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.HostKey != "SHA256:original" {
		t.Errorf("the pinned key became %q; a mismatch must not be one press from being accepted", read.HostKey)
	}

	// Clearing it is the deliberate act, and it switches the destination off in
	// the same statement.
	if err := store.ClearHostKey(ctx, "user:someone", created.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	read, _, err = store.Destination(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.HostKey != "" || read.Enabled {
		t.Errorf("after clearing the key the destination is %+v", read)
	}
}

// A local destination may not be mcpd's own data directory, and the store is
// where that is decided rather than the handler that happened to receive it.
func TestCreateDestinationRefusesTheDataDirectory(t *testing.T) {
	ctx := context.Background()
	storage := t.TempDir()
	store := newBackupStore(t, storage)

	d := backup.Destination{
		Name: "here", Kind: backup.KindLocal, Policy: backup.DefaultPolicy,
		Settings: backup.Settings{Path: storage},
	}
	_, err := store.CreateDestination(ctx, "user:someone", d)
	if err == nil {
		t.Fatal("the data directory was accepted as a backup destination")
	}
	if !strings.Contains(err.Error(), storage) {
		t.Errorf("the refusal does not name the directory: %v", err)
	}
}

// Adding, changing and removing a destination is written to the audit trail
// inside the transaction that makes the change, and no credential goes with it.
//
// The trail rather than settings_history, for the reason a ChatGPT account goes
// there: a destination is where a complete copy of this instance is sent.
func TestDestinationChangesAreAudited(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())

	d := localDest(t, "nas")
	d.Secret = "hunter2"
	created, err := store.CreateDestination(ctx, "user:someone", d)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	name := "the nas"
	if _, err := store.UpdateDestination(ctx, "user:someone", created.ID,
		backup.DestinationUpdate{Name: &name}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := store.DeleteDestination(ctx, "user:someone", created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	rows, err := store.db.Reader().QueryContext(ctx,
		`SELECT kind, actor, detail_json FROM audit_events WHERE kind LIKE 'backup.%' ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var kinds []string
	for rows.Next() {
		var kind, actor, detail string
		if err := rows.Scan(&kind, &actor, &detail); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, kind)
		if actor != "user:someone" {
			t.Errorf("%s was recorded against %q", kind, actor)
		}
		if strings.Contains(detail, "hunter2") {
			t.Errorf("%s put the credential in the trail: %s", kind, detail)
		}
	}
	want := []string{
		"backup.destination.added",
		"backup.destination.updated",
		"backup.destination.removed",
	}
	if len(kinds) != len(want) {
		t.Fatalf("recorded %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("recorded %v, want %v", kinds, want)
		}
	}

	// A run is deliberately not audited. backup_runs is the record, written by
	// the act itself; a second copy in the trail would be a second authority
	// for the same fact.
	var runs int
	if err := store.db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_events WHERE kind = 'backup.run'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Errorf("%d runs were written to the audit trail", runs)
	}
}

// Two administrators pressing the button together produce one run and one
// refusal, decided by the insert rather than by a lock this process holds.
func TestStartRunRefusesASecondRun(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	now := time.Unix(1700000000, 0).UTC()

	first := backup.Run{ID: store.NewRunID(), StartedAt: now, Trigger: backup.TriggerManual}
	if err := store.StartRun(ctx, first); err != nil {
		t.Fatalf("first run: %v", err)
	}
	second := backup.Run{ID: store.NewRunID(), StartedAt: now, Trigger: backup.TriggerSchedule}
	if err := store.StartRun(ctx, second); !errors.Is(err, backup.ErrRunInProgress) {
		t.Fatalf("got %v, want a refusal naming the run already going", err)
	}

	finished := now.Add(time.Minute)
	first.Status, first.FinishedAt = backup.StatusOK, &finished
	if err := store.FinishRun(ctx, first); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := store.StartRun(ctx, second); err != nil {
		t.Errorf("a run was refused after the previous one finished: %v", err)
	}
}

// Finishing a run only touches one still marked running, so a worker that came
// back does not rewrite a row a previous process already settled.
func TestFinishRunOnlyTouchesARunThatIsStillRunning(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	now := time.Unix(1700000000, 0).UTC()

	run := backup.Run{ID: store.NewRunID(), StartedAt: now, Trigger: backup.TriggerSchedule}
	if err := store.StartRun(ctx, run); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := store.MarkInterrupted(ctx, now.Add(time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	finished := now.Add(2 * time.Minute)
	run.Status, run.FinishedAt = backup.StatusOK, &finished
	if err := store.FinishRun(ctx, run); !errors.Is(err, ErrNoRowsAffected) {
		t.Fatalf("got %v, want the guard to match nothing", err)
	}

	settled, ok, err := store.Run(ctx, run.ID)
	if err != nil || !ok {
		t.Fatalf("read back: %v", err)
	}
	if settled.Status != backup.StatusInterrupted {
		t.Errorf("status %q; an interrupted run must not be rewritten as ok", settled.Status)
	}
}

// A process that stopped mid-run leaves rows saying 'interrupted', not
// 'failed'. Indeterminate is not terminal: a write may have landed.
func TestMarkInterruptedSettlesRunsLeftByAStoppedProcess(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	now := time.Unix(1700000000, 0).UTC()

	run := backup.Run{ID: store.NewRunID(), StartedAt: now, Trigger: backup.TriggerSchedule}
	if err := store.StartRun(ctx, run); err != nil {
		t.Fatalf("start: %v", err)
	}

	n, err := store.MarkInterrupted(ctx, now.Add(time.Minute), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if n != 1 {
		t.Fatalf("settled %d rows, want 1", n)
	}

	settled, _, err := store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != backup.StatusInterrupted {
		t.Errorf("status %q, want interrupted", settled.Status)
	}
	if settled.FinishedAt == nil {
		t.Error("an interrupted run says nothing about when it ended")
	}
	if !strings.Contains(settled.Error, "may hold it") {
		t.Errorf("the sentence does not say the outcome is unknown: %q", settled.Error)
	}

	// And running it again is a no-op rather than a second settlement.
	if n, err := store.MarkInterrupted(ctx, now.Add(2*time.Minute), now.Add(2*time.Minute)); err != nil || n != 0 {
		t.Errorf("settled %d more rows (%v)", n, err)
	}
}

// A run that started after this process's runner was built is left alone.
//
// The dashboard's listener and the backup worker both come up during App.Run,
// so a backup asked for over the API in that window has a running row of its
// own. A sweep that took every running row would settle the run it is about to
// carry out, and the operator who pressed the button would watch it be
// recorded as interrupted by a process that had only just started.
func TestMarkInterruptedLeavesARunThisProcessStarted(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	built := time.Unix(1700000000, 0).UTC()

	run := backup.Run{
		ID: store.NewRunID(), StartedAt: built.Add(time.Second),
		Trigger: backup.TriggerManual,
	}
	if err := store.StartRun(ctx, run); err != nil {
		t.Fatalf("start: %v", err)
	}

	n, err := store.MarkInterrupted(ctx, built, built.Add(time.Minute))
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if n != 0 {
		t.Fatalf("settled %d rows, want none", n)
	}

	still, _, err := store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if still.Status != backup.StatusRunning {
		t.Errorf("status %q; the sweep took a run this process had just started", still.Status)
	}
}

// The history is capped inside the transaction that adds to it, so the table
// cannot grow past the bound even briefly.
func TestTheRunHistoryIsCappedInsideTheInsert(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	base := time.Unix(1700000000, 0).UTC()

	for i := 0; i < keepRuns+10; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		run := backup.Run{ID: store.NewRunID(), StartedAt: at, Trigger: backup.TriggerSchedule}
		if err := store.StartRun(ctx, run); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		finished := at.Add(time.Second)
		run.Status, run.FinishedAt = backup.StatusOK, &finished
		if err := store.FinishRun(ctx, run); err != nil {
			t.Fatalf("finish %d: %v", i, err)
		}
	}

	var n int
	if err := store.db.Reader().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM backup_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n > keepRuns {
		t.Errorf("the history holds %d rows, want at most %d", n, keepRuns)
	}

	// And what survived is the newest.
	runs, err := store.Runs(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 5 {
		t.Fatalf("read back %d runs", len(runs))
	}
	newest := base.Add(time.Duration(keepRuns+9) * time.Minute)
	if !runs[0].StartedAt.Equal(newest) {
		t.Errorf("the newest run started %s, want %s", runs[0].StartedAt, newest)
	}
}

// last_seen only moves on a successful run. A failed run's listing is not a
// number the next run's retention should measure itself against.
func TestRecordDestinationOnlyMovesTheSeenCountOnSuccess(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	created, err := store.CreateDestination(ctx, "user:someone", localDest(t, "nas"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	now := time.Unix(1700000000, 0).UTC()

	if err := store.RecordDestination(ctx, backup.DestinationOutcome{
		ID: created.ID, At: now, OK: true, Seen: 6,
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := store.RecordDestination(ctx, backup.DestinationOutcome{
		ID: created.ID, At: now.Add(time.Hour), OK: false, Seen: 0,
		Error: "The server refused it.", Detail: "nas.example.com answered 403",
	}); err != nil {
		t.Fatalf("record a failure: %v", err)
	}

	// A run that succeeded but came away with no count it trusts -- a listing
	// that failed, or one retention held back -- also leaves it alone. This is
	// the condition that stops a truncated listing being made the standard the
	// next run measures itself against, and with it gone the check never fires
	// again and the following short listing deletes real backups.
	if err := store.RecordDestination(ctx, backup.DestinationOutcome{
		ID: created.ID, At: now.Add(2 * time.Hour), OK: true, Seen: backup.UnknownSeen,
	}); err != nil {
		t.Fatalf("record a held listing: %v", err)
	}

	read, _, err := store.Destination(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if read.LastSeen != 6 {
		t.Errorf("last_seen is %d, want the 6 the last run with a trustworthy "+
			"listing saw", read.LastSeen)
	}
	// The last write was a success with an unknown count, so that is the state
	// the row reports.
	if read.LastOK == nil || !*read.LastOK {
		t.Errorf("last_ok is %v, want true", read.LastOK)
	}
}

// The guarded update tells "gone" and "changed" apart by asking the database
// inside the same transaction, rather than guessing from zero rows.
func TestTheGuardedUpdateTellsGoneApartFromChanged(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	created, err := store.CreateDestination(ctx, "user:someone", localDest(t, "nas"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// The row is still there, and the version this write was built from is not
	// the one stored: exactly what a second administrator's save looks like.
	stale := created
	stale.UpdatedAt = created.UpdatedAt.Add(-time.Second)
	name := "renamed"

	_, err = store.applyUpdate(ctx, "user:someone", stale,
		backup.DestinationUpdate{Name: &name})
	if !errors.Is(err, ErrDestinationChanged) {
		t.Errorf("got %v, want a refusal saying somebody else changed it", err)
	}

	// And with the row gone, the same call says so instead.
	if err := store.DeleteDestination(ctx, "user:someone", created.ID); err != nil {
		t.Fatal(err)
	}
	_, err = store.applyUpdate(ctx, "user:someone", stale,
		backup.DestinationUpdate{Name: &name})
	if !errors.Is(err, ErrNoSuchDestination) {
		t.Errorf("got %v, want a refusal saying it is gone", err)
	}
}

// A destination that has never run says so, rather than saying it failed.
// Null and false are different facts and the page renders them differently.
func TestADestinationThatHasNeverRunSaysNothingAboutHowItWent(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	created, err := store.CreateDestination(ctx, "user:someone", localDest(t, "nas"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.LastOK != nil {
		t.Errorf("last_ok is %v on a destination that has never run", *created.LastOK)
	}
	if created.LastRunAt != nil {
		t.Errorf("last_run_at is %v on a destination that has never run", created.LastRunAt)
	}
}

// Two destinations cannot share a name, however it is capitalised, and the
// refusal comes from the index rather than from a read beforehand.
func TestDestinationNamesAreUnique(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	if _, err := store.CreateDestination(ctx, "user:someone", localDest(t, "Archive Box")); err != nil {
		t.Fatalf("create: %v", err)
	}
	_, err := store.CreateDestination(ctx, "user:someone", localDest(t, "archive box"))
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("got %v, want a refusal naming the clash", err)
	}
}

// The last scheduled run is what catch-up compares against, and "one has never
// happened" is a different answer from "one happened at the zero time".
func TestLastScheduledRunSaysWhetherOneEverHappened(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())
	now := time.Unix(1700000000, 0).UTC()

	if _, ever, err := store.LastScheduledRun(ctx); err != nil || ever {
		t.Fatalf("ever = %v (%v) on a host that has never run one", ever, err)
	}

	// A manual run is not a scheduled one.
	manual := backup.Run{ID: store.NewRunID(), StartedAt: now, Trigger: backup.TriggerManual}
	if err := store.StartRun(ctx, manual); err != nil {
		t.Fatal(err)
	}
	finished := now.Add(time.Minute)
	manual.Status, manual.FinishedAt = backup.StatusOK, &finished
	if err := store.FinishRun(ctx, manual); err != nil {
		t.Fatal(err)
	}
	if _, ever, err := store.LastScheduledRun(ctx); err != nil || ever {
		t.Errorf("a manual run counted as a scheduled one (%v, %v)", ever, err)
	}

	scheduled := backup.Run{
		ID: store.NewRunID(), StartedAt: now.Add(time.Hour), Trigger: backup.TriggerSchedule,
	}
	if err := store.StartRun(ctx, scheduled); err != nil {
		t.Fatal(err)
	}
	at, ever, err := store.LastScheduledRun(ctx)
	if err != nil || !ever {
		t.Fatalf("ever = %v (%v)", ever, err)
	}
	if !at.Equal(now.Add(time.Hour)) {
		t.Errorf("last scheduled run %s, want %s", at, now.Add(time.Hour))
	}
}

// A host with no encryption key can hold a destination that needs no
// credential, and refuses one that does rather than storing it in the clear.
func TestADestinationWithACredentialNeedsAnEncryptionKey(t *testing.T) {
	ctx := context.Background()
	store := NewBackupStore(newTestDB(t), nil, nil, t.TempDir())

	if _, err := store.CreateDestination(ctx, "user:someone", localDest(t, "no secret")); err != nil {
		t.Errorf("a destination with no credential was refused: %v", err)
	}

	d := localDest(t, "with secret")
	d.Secret = "hunter2"
	if _, err := store.CreateDestination(ctx, "user:someone", d); !errors.Is(err, ErrNoBackupCipher) {
		t.Errorf("got %v, want a refusal about the missing key", err)
	}
}

// The settings a destination was configured with survive a round trip, which is
// what makes the config_json column readable rather than merely valid.
func TestDestinationSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newBackupStore(t, t.TempDir())

	d := backup.Destination{
		Name: "bucket", Kind: backup.KindS3, Enabled: true,
		Policy: backup.Policy{KeepLast: 4, KeepMonthly: 6},
		Settings: backup.Settings{
			Endpoint: "s3.eu-central-003.backblazeb2.com", Region: "eu-central-003",
			Bucket: "mcpd-backups", Prefix: "hosts/one", AccessKey: "ak",
			PathStyle: true,
		},
		Secret: "sk",
	}
	created, err := store.CreateDestination(ctx, "user:someone", d)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	read, ok, err := store.Destination(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("read back: %v", err)
	}
	if read.Settings != d.Settings {
		t.Errorf("settings came back as %+v, want %+v", read.Settings, d.Settings)
	}
	if read.Policy != d.Policy {
		t.Errorf("retention came back as %+v, want %+v", read.Policy, d.Policy)
	}
	// The path a local destination is validated against is absolute on the way
	// out, so a relative one typed into the form cannot mean two things.
	local := localDest(t, "relative")
	local.Settings.Path = filepath.Join(local.Settings.Path, ".")
	if _, err := store.CreateDestination(ctx, "user:someone", local); err != nil {
		t.Errorf("a tidy-able path was refused: %v", err)
	}
}
