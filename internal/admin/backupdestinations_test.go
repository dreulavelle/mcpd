package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/auth/groups"
	"github.com/spoked/mcpd/internal/backup"
	"github.com/spoked/mcpd/internal/observability"
)

// These tests are about the HTTP layer: which requests are refused and with
// what, what comes back out, and above all what never does.

// fakeDestinations stands in for the store.
type fakeDestinations struct {
	dests []backup.Destination
	runs  []backup.Run

	createErr error
	updateErr error
	// recorded is the host key the test endpoint pinned, if any.
	recorded string
	// updates is what the last PATCH passed through.
	updates backup.DestinationUpdate
	deleted string
}

func (f *fakeDestinations) Destinations(context.Context) ([]backup.Destination, error) {
	return f.dests, nil
}

func (f *fakeDestinations) Destination(_ context.Context, id string) (backup.Destination, bool, error) {
	for _, d := range f.dests {
		if d.ID == id {
			return d, true, nil
		}
	}
	return backup.Destination{}, false, nil
}

func (f *fakeDestinations) CreateDestination(
	_ context.Context, _ string, d backup.Destination,
) (backup.Destination, error) {
	if f.createErr != nil {
		return backup.Destination{}, f.createErr
	}
	d.ID = "dst_new"
	f.dests = append(f.dests, d)
	return d, nil
}

func (f *fakeDestinations) UpdateDestination(
	_ context.Context, _, id string, up backup.DestinationUpdate,
) (backup.Destination, error) {
	f.updates = up
	if f.updateErr != nil {
		return backup.Destination{}, f.updateErr
	}
	for _, d := range f.dests {
		if d.ID == id {
			if up.Name != nil {
				d.Name = *up.Name
			}
			return d, nil
		}
	}
	return backup.Destination{}, errors.New("sqlite: no such backup destination")
}

func (f *fakeDestinations) DeleteDestination(_ context.Context, _, id string) error {
	f.deleted = id
	return nil
}

func (f *fakeDestinations) RecordHostKey(_ context.Context, _, _, fingerprint string) error {
	f.recorded = fingerprint
	return nil
}

func (f *fakeDestinations) Runs(_ context.Context, limit int) ([]backup.Run, error) {
	if limit < len(f.runs) {
		return f.runs[:limit], nil
	}
	return f.runs, nil
}

func (f *fakeDestinations) Run(_ context.Context, id string) (backup.Run, bool, error) {
	for _, r := range f.runs {
		if r.ID == id {
			return r, true, nil
		}
	}
	return backup.Run{}, false, nil
}

// fakeRunner stands in for the worker.
type fakeRunner struct {
	run        backup.Run
	triggerErr error
	triggered  string
	status     *backup.ScheduleStatus
}

func (f *fakeRunner) Trigger(_ context.Context, trigger string) (backup.Run, error) {
	f.triggered = trigger
	if f.triggerErr != nil {
		return backup.Run{}, f.triggerErr
	}
	return f.run, nil
}

func (f *fakeRunner) Status(context.Context) *backup.ScheduleStatus { return f.status }

func newDestinationServer(
	t *testing.T, accounts Accounts, store BackupDestinations, runner BackupRunner,
) *Server {
	t.Helper()
	return NewServer(Options{
		Log:                slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts:           accounts,
		Backup:             &fakeBackup{},
		BackupDestinations: store,
		BackupRunner:       runner,
		Version:            "test",
		Health:             observability.NewHealthRegistry(time.Second),
		KeyAccess: func(context.Context, string) (groups.Resolved, error) {
			return groups.Resolved{Permissions: auth.Permissions{}, Grants: auth.Grants{}}, nil
		},
	})
}

func sampleDestination() backup.Destination {
	return backup.Destination{
		ID: "dst_1", Name: "the nas", Kind: backup.KindSFTP, Enabled: true,
		Settings: backup.Settings{
			Host: "nas.example.com", Port: 22, Username: "ops", RemotePath: "/volume1/backups",
		},
		Secret:  "hunter2",
		HostKey: "SHA256:recorded",
		Policy:  backup.Policy{KeepLast: 6},
	}
}

// A destination is where a complete copy of this instance goes -- every
// account, every grant, every stored credential -- so naming one is an
// administrator's decision rather than an operator's.
func TestBackupDestinationsNeedAdmin(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.RoleID = auth.RoleOperator
	accounts.user.Grants = auth.GrantsAt([]string{"echo"}, auth.LevelWrite)
	s := newDestinationServer(t, accounts, &fakeDestinations{}, &fakeRunner{})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/backup/destinations"},
		{http.MethodPost, "/api/backup/destinations"},
		{http.MethodGet, "/api/backup/destinations/dst_1"},
		{http.MethodPatch, "/api/backup/destinations/dst_1"},
		{http.MethodDelete, "/api/backup/destinations/dst_1"},
		{http.MethodPost, "/api/backup/destinations/dst_1/test"},
		{http.MethodPost, "/api/backup/run"},
		{http.MethodGet, "/api/backup/runs"},
		{http.MethodGet, "/api/backup/runs/bkr_1"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
		r.Header.Set(csrfHeader, accounts.session.CSRFToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)

		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", tc.method, tc.path, w.Code)
		}
	}
}

// A destination's credential never comes back out.
//
// The form needs to know whether one is set, not what it is: a page that could
// show one hands every credential to anybody who reaches the dashboard.
func TestListingDestinationsNeverReturnsACredential(t *testing.T) {
	accounts := newFakeAccounts()
	store := &fakeDestinations{dests: []backup.Destination{sampleDestination()}}
	s := newDestinationServer(t, accounts, store, &fakeRunner{})

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/backup/destinations", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatalf("the response carries the credential: %s", w.Body.String())
	}

	var body struct {
		Destinations []struct {
			Name      string `json:"name"`
			Where     string `json:"where"`
			HasSecret bool   `json:"has_secret"`
			HostKey   string `json:"host_key"`
		} `json:"destinations"`
		Kinds []string `json:"kinds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Destinations) != 1 {
		t.Fatalf("listed %d destinations", len(body.Destinations))
	}
	got := body.Destinations[0]
	if !got.HasSecret {
		t.Error("the page is not told that a credential is set")
	}
	if got.Where == "" {
		t.Error("nothing says where this destination points")
	}
	// The host key is a public fingerprint, and showing it is the point: an
	// operator compares it with what the server says about itself.
	if got.HostKey != "SHA256:recorded" {
		t.Errorf("host key %q", got.HostKey)
	}
	if len(body.Kinds) == 0 {
		t.Error("the form is not told which kinds this build has")
	}
}

// An edit that does not mention the credential keeps the one there is.
//
// The page never reads one back, so an edit that changes only the retention
// arrives with no secret at all, and reading that as an erasure would silently
// break the destination on the next run.
func TestPatchingWithoutASecretLeavesItAlone(t *testing.T) {
	accounts := newFakeAccounts()
	store := &fakeDestinations{dests: []backup.Destination{sampleDestination()}}
	s := newDestinationServer(t, accounts, store, &fakeRunner{})

	w := asAdmin(t, s, accounts, http.MethodPatch, "/api/backup/destinations/dst_1",
		`{"name":"the other nas"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", w.Code, w.Body.String())
	}
	if store.updates.Secret != nil {
		t.Errorf("the store was told to set the credential to %q", *store.updates.Secret)
	}
	if store.updates.Name == nil || *store.updates.Name != "the other nas" {
		t.Errorf("the name was not passed through: %+v", store.updates.Name)
	}

	// An empty string is a different instruction, and it does reach the store.
	w = asAdmin(t, s, accounts, http.MethodPatch, "/api/backup/destinations/dst_1",
		`{"secret":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", w.Code, w.Body.String())
	}
	if store.updates.Secret == nil || *store.updates.Secret != "" {
		t.Errorf("clearing the credential did not reach the store: %+v", store.updates.Secret)
	}
}

// A destination's kind cannot be changed in place.
//
// It would keep a name, a retention and a credential that belonged to a
// different sort of thing entirely. Removing it and adding another is one
// honest act instead of one confusing one.
func TestADestinationsKindCannotBeChanged(t *testing.T) {
	accounts := newFakeAccounts()
	store := &fakeDestinations{dests: []backup.Destination{sampleDestination()}}
	s := newDestinationServer(t, accounts, store, &fakeRunner{})

	w := asAdmin(t, s, accounts, http.MethodPatch, "/api/backup/destinations/dst_1",
		`{"kind":"s3"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PATCH = %d, want 400: %s", w.Code, w.Body.String())
	}
}

// Test connection is the one path that records a host key, and it says what it
// recorded so an operator can compare it before switching the destination on.
func TestTestConnectionRecordsAHostKeyAndReportsIt(t *testing.T) {
	accounts := newFakeAccounts()
	d := sampleDestination()
	// Nothing pinned yet, which is what a destination looks like the moment
	// after it is added.
	d.HostKey = ""
	d.Enabled = false
	// A port nothing is listening on, so the handshake fails after the address
	// is resolved rather than hanging.
	d.Settings.Host = "127.0.0.1"
	d.Settings.Port = 1
	store := &fakeDestinations{dests: []backup.Destination{d}}
	s := newDestinationServer(t, accounts, store, &fakeRunner{})

	w := asAdmin(t, s, accounts, http.MethodPost, "/api/backup/destinations/dst_1/test", "")
	// Always a 200: the question is "does this work", and both answers are
	// answers. An HTTP error would leave the page deciding whether the request
	// failed or the destination did.
	if w.Code != http.StatusOK {
		t.Fatalf("POST test = %d: %s", w.Code, w.Body.String())
	}

	var result testResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Fatal("a port nothing is listening on passed the connection test")
	}
	if result.Message == "" {
		t.Error("the failure has no sentence")
	}
	if strings.Contains(result.Message, "127.0.0.1") {
		t.Errorf("the sentence carries evidence: %q", result.Message)
	}
	if result.Detail == "" {
		t.Error("the failure has no evidence")
	}
	// Nothing was presented, so nothing was pinned. A destination that cannot
	// be reached must not end up with a recorded key.
	if store.recorded != "" {
		t.Errorf("a host key was recorded from a connection that failed: %q", store.recorded)
	}
}

// A destination with a key already pinned is never asked to learn another one.
func TestTestConnectionDoesNotRelearnAPinnedHostKey(t *testing.T) {
	accounts := newFakeAccounts()
	d := sampleDestination()
	d.Settings.Host = "127.0.0.1"
	d.Settings.Port = 1
	store := &fakeDestinations{dests: []backup.Destination{d}}
	s := newDestinationServer(t, accounts, store, &fakeRunner{})

	asAdmin(t, s, accounts, http.MethodPost, "/api/backup/destinations/dst_1/test", "")
	if store.recorded != "" {
		t.Errorf("a pinned destination was given a new key: %q", store.recorded)
	}
}

// A manual run answers 202 with the row it inserted, so the page can follow it
// in the history rather than waiting on a request that takes minutes.
func TestRunningABackupAnswersWithTheRun(t *testing.T) {
	accounts := newFakeAccounts()
	runner := &fakeRunner{run: backup.Run{
		ID: "bkr_1", Status: backup.StatusRunning, Trigger: backup.TriggerManual,
		StartedAt: time.Unix(1700000000, 0).UTC(),
	}}
	s := newDestinationServer(t, accounts, &fakeDestinations{}, runner)

	w := asAdmin(t, s, accounts, http.MethodPost, "/api/backup/run", "")
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST run = %d: %s", w.Code, w.Body.String())
	}
	if runner.triggered != backup.TriggerManual {
		t.Errorf("the run was recorded as %q", runner.triggered)
	}

	var run backup.Run
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.ID != "bkr_1" || run.Status != backup.StatusRunning {
		t.Errorf("answered with %+v", run)
	}
}

// The refusals a run can meet each get the status that describes them, so the
// page can tell "wait" apart from "you have not finished setting this up".
func TestRunningABackupReportsWhyItCannot(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "one is already running", err: backup.ErrRunInProgress, want: http.StatusConflict},
		{name: "nowhere to send it", err: backup.ErrNoDestination, want: http.StatusBadRequest},
		{name: "nothing to seal it with", err: backup.ErrNoPassphrase, want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accounts := newFakeAccounts()
			s := newDestinationServer(t, accounts, &fakeDestinations{},
				&fakeRunner{triggerErr: tc.err})

			w := asAdmin(t, s, accounts, http.MethodPost, "/api/backup/run", "")
			if w.Code != tc.want {
				t.Fatalf("POST run = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			// Whatever it says, it is a sentence rather than a Go error.
			if strings.Contains(w.Body.String(), "backup:") {
				t.Errorf("the answer carries a package prefix: %s", w.Body.String())
			}
		})
	}
}

// The history is read newest first and bounded, and an empty one is an empty
// list rather than a null the page would blank on.
func TestReadingTheBackupHistory(t *testing.T) {
	accounts := newFakeAccounts()
	store := &fakeDestinations{}
	s := newDestinationServer(t, accounts, store, &fakeRunner{})

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/backup/runs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET runs = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"runs":[]`) {
		t.Errorf("an empty history came back as %s", w.Body.String())
	}

	store.runs = []backup.Run{{ID: "bkr_1", Status: backup.StatusOK}}
	w = asAdmin(t, s, accounts, http.MethodGet, "/api/backup/runs/bkr_1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET run = %d: %s", w.Code, w.Body.String())
	}
	w = asAdmin(t, s, accounts, http.MethodGet, "/api/backup/runs/bkr_missing", "")
	if w.Code != http.StatusNotFound {
		t.Errorf("a run that is not there = %d, want 404", w.Code)
	}
}

// The status page asks one question -- is this host backing itself up -- so the
// schedule comes back on the same response as what an archive would hold.
func TestBackupStatusCarriesTheSchedule(t *testing.T) {
	accounts := newFakeAccounts()
	next := time.Unix(1700000000, 0).UTC()
	runner := &fakeRunner{status: &backup.ScheduleStatus{
		Enabled: true, Cadence: backup.CadenceWeekly, Time: "04:00",
		Timezone: "America/Chicago", NextRunAt: &next,
		Destinations: 2, EnabledDestinations: 1, PassphraseSet: true,
	}}
	s := newDestinationServer(t, accounts, &fakeDestinations{}, runner)

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/backup", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", w.Code, w.Body.String())
	}

	var status backup.Status
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Schedule == nil {
		t.Fatal("the status says nothing about the schedule")
	}
	if !status.Schedule.Enabled || status.Schedule.NextRunAt == nil {
		t.Errorf("schedule %+v", status.Schedule)
	}
	if !status.Schedule.PassphraseSet || status.Schedule.EnabledDestinations != 1 {
		t.Errorf("schedule %+v", status.Schedule)
	}
}

// A host with no encryption key cannot hold a destination's credential, and
// says so rather than offering a page whose every button fails.
func TestDestinationRoutesSayWhenThisHostCannotHoldOne(t *testing.T) {
	accounts := newFakeAccounts()
	s := newDestinationServer(t, accounts, nil, nil)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/backup/destinations"},
		{http.MethodPost, "/api/backup/destinations"},
		{http.MethodPost, "/api/backup/run"},
		{http.MethodGet, "/api/backup/runs"},
	} {
		w := asAdmin(t, s, accounts, tc.method, tc.path, "{}")
		if w.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", tc.method, tc.path, w.Code)
		}
	}
}

// A refusal already written for a person reaches them as their sentence, with
// no package prefix in front of it.
func TestDestinationRefusalsReadAsSentences(t *testing.T) {
	accounts := newFakeAccounts()
	store := &fakeDestinations{
		createErr: errors.New("backup destination: give the directory to write into"),
	}
	s := newDestinationServer(t, accounts, store, &fakeRunner{})

	w := asAdmin(t, s, accounts, http.MethodPost, "/api/backup/destinations",
		`{"name":"nowhere","kind":"local"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST = %d, want 400: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "backup destination:") {
		t.Errorf("the answer carries a package prefix: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "directory to write into") {
		t.Errorf("the answer lost the sentence: %s", w.Body.String())
	}
}
