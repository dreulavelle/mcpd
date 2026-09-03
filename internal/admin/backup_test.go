package admin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
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

// fakeBackup stands in for the service. These tests are about the HTTP layer:
// which requests are refused and with what, how an upload is read, and whether
// a failure before the first byte can still be reported as one.
type fakeBackup struct {
	status backup.Status
	// body is what Create writes. writeErr fails it, after writing prefix.
	body     string
	prefix   string
	writeErr error

	stageErr error
	// staged records what the handler passed through.
	staged     string
	passphrase string
	actor      string

	cancelled string
}

func (f *fakeBackup) Status(context.Context) backup.Status { return f.status }
func (f *fakeBackup) Filename() string                     { return "mcpd-test.mcpdbak" }

func (f *fakeBackup) Create(_ context.Context, w io.Writer, passphrase string) error {
	f.passphrase = passphrase
	if f.prefix != "" {
		if _, err := io.WriteString(w, f.prefix); err != nil {
			return err
		}
	}
	if f.writeErr != nil {
		return f.writeErr
	}
	_, err := io.WriteString(w, f.body)
	return err
}

func (f *fakeBackup) Stage(_ context.Context, r io.Reader, passphrase, actor string) (*backup.Pending, error) {
	body, _ := io.ReadAll(r)
	f.staged = string(body)
	f.passphrase = passphrase
	f.actor = actor
	if f.stageErr != nil {
		return nil, f.stageErr
	}
	return &backup.Pending{StagedAt: time.Unix(1700000000, 0).UTC(), Actor: actor}, nil
}

func (f *fakeBackup) Cancel(_ context.Context, actor string) error {
	f.cancelled = actor
	return nil
}

func newBackupServer(t *testing.T, accounts Accounts, service BackupService, supervised bool) *Server {
	t.Helper()
	opts := Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Backup:   service,
		Version:  "test",
		Health:   observability.NewHealthRegistry(time.Second),
		KeyAccess: func(context.Context, string) (groups.Resolved, error) {
			return groups.Resolved{Permissions: auth.Permissions{}, Grants: auth.Grants{}}, nil
		},
	}
	if supervised {
		opts.Restart = func(string) error { return nil }
	}
	return NewServer(opts)
}

// upload builds a multipart body with the parts in the order given, so a test
// can send the file before the passphrase.
func upload(t *testing.T, first, second [2]string) (string, io.Reader) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, part := range [][2]string{first, second} {
		if part[0] == "" {
			continue
		}
		var (
			field io.Writer
			err   error
		)
		if part[0] == "archive" {
			field, err = w.CreateFormFile("archive", "backup.mcpdbak")
		} else {
			field, err = w.CreateFormField(part[0])
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(field, part[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return w.FormDataContentType(), &buf
}

func postUpload(t *testing.T, s *Server, accounts *fakeAccounts, path, contentType string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, body)
	r.Header.Set("Content-Type", contentType)
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: accounts.token})
	r.Header.Set(csrfHeader, accounts.session.CSRFToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// An archive is the whole instance in one file, so every route here is an
// administrator's. An operator who may read the dashboard must not be able to
// take one, nor to learn this host's shape from what one would hold.
func TestBackupNeedsAdmin(t *testing.T) {
	accounts := newFakeAccounts()
	accounts.user.RoleID = auth.RoleOperator
	accounts.user.Grants = auth.GrantsAt([]string{"echo"}, auth.LevelWrite)
	s := newBackupServer(t, accounts, &fakeBackup{}, true)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/backup"},
		{http.MethodPost, "/api/backup"},
		{http.MethodPost, "/api/backup/restore"},
		{http.MethodDelete, "/api/backup/restore"},
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

func TestBackupDownload(t *testing.T) {
	accounts := newFakeAccounts()
	service := &fakeBackup{body: "mcpd-backup/1\nsealed bytes"}
	s := newBackupServer(t, accounts, service, true)

	w := asAdmin(t, s, accounts, http.MethodPost, "/api/backup",
		`{"passphrase":"a-long-enough-passphrase"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/backup = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != service.body {
		t.Errorf("body = %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "mcpd-test.mcpdbak") {
		t.Errorf("Content-Disposition = %q", got)
	}
	// A backup must never be cached by anything between here and the browser.
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if service.passphrase != "a-long-enough-passphrase" {
		t.Errorf("passphrase reached the service as %q", service.passphrase)
	}
}

// The passphrase is the only thing protecting the file, so the floor is
// enforced here as well as in the package that writes it.
func TestBackupRefusesAShortPassphrase(t *testing.T) {
	accounts := newFakeAccounts()
	service := &fakeBackup{body: "archive"}
	s := newBackupServer(t, accounts, service, true)

	w := asAdmin(t, s, accounts, http.MethodPost, "/api/backup", `{"passphrase":"short"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/backup = %d, want 400: %s", w.Code, w.Body.String())
	}
	if service.passphrase != "" {
		t.Error("a short passphrase still reached the service")
	}
}

// The bug the deferred headers exist for. A snapshot that fails happens before
// any output, so it must answer as an error the page can show rather than as a
// 200 holding an error message the browser would save as the backup.
func TestBackupFailureBeforeTheFirstByteIsAnError(t *testing.T) {
	accounts := newFakeAccounts()
	service := &fakeBackup{writeErr: errors.New("the disk is full")}
	s := newBackupServer(t, accounts, service, true)

	w := asAdmin(t, s, accounts, http.MethodPost, "/api/backup",
		`{"passphrase":"a-long-enough-passphrase"}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("POST /api/backup = %d, want 500: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("Content-Type = %q; a failure is not a download", got)
	}
	if strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Error("a failed backup was offered as a file to save")
	}
}

// A checked archive restarts the host itself. Left waiting, the restore would
// apply on the next start of any kind -- a reboot, a compose up -- which is a
// change firing at a moment nobody connected to a restore.
func TestStageRestoreRestartsTheHost(t *testing.T) {
	accounts := newFakeAccounts()
	service := &fakeBackup{}

	var restartedFor string
	s := NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Backup:   service,
		Version:  "test",
		Health:   observability.NewHealthRegistry(time.Second),
		Restart:  func(reason string) error { restartedFor = reason; return nil },
		KeyAccess: func(context.Context, string) (groups.Resolved, error) {
			return groups.Resolved{Permissions: auth.Permissions{}, Grants: auth.Grants{}}, nil
		},
	})

	contentType, body := upload(t,
		[2]string{"passphrase", "a-long-enough-passphrase"},
		[2]string{"archive", "the archive bytes"})

	w := postUpload(t, s, accounts, "/api/backup/restore", contentType, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("POST /api/backup/restore = %d: %s", w.Code, w.Body.String())
	}
	if service.staged != "the archive bytes" {
		t.Errorf("the service was given %q", service.staged)
	}
	if service.passphrase != "a-long-enough-passphrase" {
		t.Errorf("passphrase = %q", service.passphrase)
	}
	if service.actor == "" {
		t.Error("the staged restore records no actor")
	}
	if restartedFor == "" {
		t.Fatal("a checked archive did not restart the host")
	}
	if !strings.Contains(restartedFor, service.actor) {
		t.Errorf("the restart does not name who asked: %q", restartedFor)
	}
	if !strings.Contains(w.Body.String(), `"status":"restoring"`) {
		t.Errorf("the reply does not say it is restoring: %s", w.Body.String())
	}
}

// A restart that cannot be asked for must not throw the archive away. It was
// checked and is sound; what failed is the restart, and telling somebody to
// upload it again would be answering the wrong problem.
func TestStageKeepsTheArchiveWhenTheRestartFails(t *testing.T) {
	accounts := newFakeAccounts()
	service := &fakeBackup{}
	s := NewServer(Options{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Accounts: accounts,
		Backup:   service,
		Version:  "test",
		Health:   observability.NewHealthRegistry(time.Second),
		Restart: func(string) error {
			return errors.New("a restart is already under way")
		},
		KeyAccess: func(context.Context, string) (groups.Resolved, error) {
			return groups.Resolved{Permissions: auth.Permissions{}, Grants: auth.Grants{}}, nil
		},
	})

	contentType, body := upload(t,
		[2]string{"passphrase", "a-long-enough-passphrase"},
		[2]string{"archive", "the archive bytes"})

	w := postUpload(t, s, accounts, "/api/backup/restore", contentType, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("= %d, want 202: %s", w.Code, w.Body.String())
	}
	if service.cancelled != "" {
		t.Error("a sound archive was discarded because the restart failed")
	}
	// The page must not report a reconnect that is not coming.
	if !strings.Contains(w.Body.String(), `"status":"staged"`) {
		t.Errorf("the reply claims more than happened: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "next time mcpd starts") {
		t.Errorf("the reply does not say when it will apply: %s", w.Body.String())
	}
}

// The upload is streamed straight into the decryption rather than buffered, so
// the passphrase has to have arrived by the time the file does.
func TestStageRefusesAFileBeforeThePassphrase(t *testing.T) {
	accounts := newFakeAccounts()
	service := &fakeBackup{}
	s := newBackupServer(t, accounts, service, true)

	contentType, body := upload(t,
		[2]string{"archive", "the archive bytes"},
		[2]string{"passphrase", "a-long-enough-passphrase"})

	w := postUpload(t, s, accounts, "/api/backup/restore", contentType, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("= %d, want 400: %s", w.Code, w.Body.String())
	}
	if service.staged != "" {
		t.Error("the archive was staged without a passphrase")
	}
}

// A wrong passphrase is not an authorisation failure. 403 would send an
// operator looking at their own account.
func TestStageTranslatesRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"a wrong passphrase", backup.ErrPassphrase, http.StatusBadRequest},
		{"a different encryption key", backup.ErrKeyMismatch, http.StatusConflict},
		{"anything else", errors.New("the archive holds no database"), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := newFakeAccounts()
			s := newBackupServer(t, accounts, &fakeBackup{stageErr: tc.err}, true)

			contentType, body := upload(t,
				[2]string{"passphrase", "a-long-enough-passphrase"},
				[2]string{"archive", "bytes"})

			w := postUpload(t, s, accounts, "/api/backup/restore", contentType, body)
			if w.Code != tc.want {
				t.Fatalf("= %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "error") {
				t.Errorf("the reply carries no message: %s", w.Body.String())
			}
		})
	}
}

// Staging a restore on a host that cannot restart would leave an operator
// waiting for something that never happens.
func TestStageRefusedWithoutASupervisor(t *testing.T) {
	accounts := newFakeAccounts()
	service := &fakeBackup{}
	s := newBackupServer(t, accounts, service, false)

	contentType, body := upload(t,
		[2]string{"passphrase", "a-long-enough-passphrase"},
		[2]string{"archive", "bytes"})

	w := postUpload(t, s, accounts, "/api/backup/restore", contentType, body)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("= %d, want 501: %s", w.Code, w.Body.String())
	}
	if service.staged != "" {
		t.Error("a restore was staged on a host that cannot apply it")
	}
}

func TestBackupNotConfigured(t *testing.T) {
	accounts := newFakeAccounts()
	s := newBackupServer(t, accounts, nil, true)

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/backup", "")
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("= %d, want 501: %s", w.Code, w.Body.String())
	}
}

func TestCancelRestore(t *testing.T) {
	accounts := newFakeAccounts()
	service := &fakeBackup{}
	s := newBackupServer(t, accounts, service, true)

	w := asAdmin(t, s, accounts, http.MethodDelete, "/api/backup/restore", "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("= %d, want 204: %s", w.Code, w.Body.String())
	}
	if service.cancelled == "" {
		t.Error("the cancel records no actor")
	}
}

// The status is what the page renders before anybody types anything, so it has
// to carry the floor the form enforces rather than the browser inventing one.
func TestBackupStatus(t *testing.T) {
	accounts := newFakeAccounts()
	service := &fakeBackup{status: backup.Status{
		DatabaseBytes: 401408, TLSFiles: 3, ConfigIncluded: true,
		KeyFingerprint: "0badcafe0badcafe", SchemaVersion: 19,
		MinPassphrase: backup.MinPassphrase,
	}}
	s := newBackupServer(t, accounts, service, true)

	w := asAdmin(t, s, accounts, http.MethodGet, "/api/backup", "")
	if w.Code != http.StatusOK {
		t.Fatalf("= %d: %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"database_bytes", "key_fingerprint", "min_passphrase"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("the status does not carry %s: %s", want, w.Body.String())
		}
	}
}
