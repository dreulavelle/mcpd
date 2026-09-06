package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/backup"
)

// Backup and restore, over HTTP.
//
// Neither act is written to the audit trail, and that is deliberate rather
// than an omission. The trail is appended inside the transaction that makes
// the change it describes, precisely so an entry cannot disagree with the
// database -- and neither of these is a database change. A backup alters
// nothing; a restore replaces the trail itself, so an entry recorded about it
// would be destroyed by the act it records. Both are logged instead, with the
// principal who asked, which is where a reader of the log will look.

// BackupService is the host's own view of its backups.
//
// An interface for the same reason Accounts and Groups are: it keeps this
// package's handlers testable without a database, and it says exactly what the
// dashboard is allowed to ask for. *backup.Service is the implementation.
type BackupService interface {
	// Status describes what an archive taken now would hold, and any restore
	// already staged.
	Status(ctx context.Context) backup.Status
	// Filename is what a download should be offered as.
	Filename() string
	// Create streams an encrypted archive of the instance.
	Create(ctx context.Context, w io.Writer, passphrase string) error
	// Stage validates an archive and lays it out for the next start. It must
	// not touch anything the running host is using.
	Stage(ctx context.Context, r io.Reader, passphrase, actor string) (*backup.Pending, error)
	// Cancel discards a staged restore.
	Cancel(ctx context.Context, actor string) error
}

// maxPassphrase bounds the field so a hostile request cannot make this host
// run PBKDF2 over a megabyte.
const maxPassphrase = 1 << 10

type backupRequest struct {
	Passphrase string `json:"passphrase"`
}

// handleBackupStatus reports what an archive taken now would hold, and whether
// a restore is already waiting for a restart.
func (s *Server) handleBackupStatus(w http.ResponseWriter, r *http.Request) {
	if s.opts.Backup == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host is not configured to write backups")
		return
	}
	status := s.opts.Backup.Status(r.Context())
	// The schedule on the same response as the contents, because they are two
	// halves of one question: what this host would send, and whether it is
	// going to send it without being asked.
	if s.opts.BackupRunner != nil {
		status.Schedule = s.opts.BackupRunner.Status(r.Context())
	}
	s.writeJSON(w, r, http.StatusOK, status)
}

// handleCreateBackup streams an encrypted archive of this instance.
func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	if s.opts.Backup == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host is not configured to write backups")
		return
	}

	var req backupRequest
	if !s.decode(w, r, &req) {
		return
	}
	if len(req.Passphrase) < backup.MinPassphrase {
		s.writeError(w, r, http.StatusBadRequest, fmt.Sprintf(
			"the passphrase must be at least %d characters. It is the only thing "+
				"protecting the archive", backup.MinPassphrase))
		return
	}
	if len(req.Passphrase) > maxPassphrase {
		s.writeError(w, r, http.StatusBadRequest, "that passphrase is too long")
		return
	}

	actor := auth.FromContext(r.Context()).ID
	s.opts.Log.WarnContext(r.Context(), "a backup of this instance was taken",
		"principal", actor)

	// The response headers are held back until the first byte, so a failure
	// while taking the snapshot -- which is everything before any output --
	// still answers with a JSON error the dashboard can show. Once bytes are
	// on the wire the status is spent, and a later failure can only be logged
	// and the connection dropped, which is what a truncated download looks
	// like to the browser. That is the honest signal: the archive would be
	// unopenable anyway, because its final chunk never arrives.
	lazy := &deferredHeaders{w: w, filename: s.opts.Backup.Filename()}
	if err := s.opts.Backup.Create(r.Context(), lazy, req.Passphrase); err != nil {
		if lazy.started {
			s.opts.Log.ErrorContext(r.Context(), "a backup failed after it had begun sending",
				"principal", actor, "error", err)
			return
		}
		s.opts.Log.ErrorContext(r.Context(), "a backup failed",
			"principal", actor, "error", err)
		s.writeError(w, r, http.StatusInternalServerError,
			"the backup could not be written: "+err.Error())
		return
	}
	if !lazy.started {
		// An archive is never empty, so this cannot happen; if it ever does,
		// an empty 200 would be a file the operator keeps and cannot restore.
		s.writeError(w, r, http.StatusInternalServerError, "the backup was empty")
	}
}

// deferredHeaders writes the download headers when the body starts, so that
// everything before the first byte can still fail as an ordinary error.
type deferredHeaders struct {
	w        http.ResponseWriter
	filename string
	started  bool
}

func (d *deferredHeaders) Write(p []byte) (int, error) {
	if !d.started {
		d.started = true
		d.w.Header().Set("Content-Type", "application/octet-stream")
		d.w.Header().Set("Cache-Control", "no-store")
		d.w.Header().Set("Content-Disposition",
			mime.FormatMediaType("attachment", map[string]string{"filename": d.filename}))
		d.w.WriteHeader(http.StatusOK)
	}
	return d.w.Write(p)
}

// handleStageRestore takes an uploaded archive and lays it out for the next
// start. Nothing the running host uses is touched here.
func (s *Server) handleStageRestore(w http.ResponseWriter, r *http.Request) {
	if s.opts.Backup == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host is not configured to restore backups")
		return
	}
	if s.opts.Restart == nil {
		// Staging a restore this host cannot then apply would leave an
		// operator waiting for a restart that never comes.
		s.writeError(w, r, http.StatusNotImplemented,
			"nothing is supervising this host, so it cannot restart to apply a "+
				"restore. Restore it the way it was started: stop mcpd, put the "+
				"archive's database in place, and start it again")
		return
	}

	// Streamed rather than parsed into memory or a temporary file. The
	// container's /tmp is a small tmpfs, and ParseMultipartForm spills there:
	// an archive of any size would fail with a message about temporary space
	// rather than about the restore.
	parts, err := r.MultipartReader()
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest,
			"send the archive as a file upload")
		return
	}

	var passphrase string
	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.writeError(w, r, http.StatusBadRequest, "the upload could not be read")
			return
		}

		switch part.FormName() {
		case "passphrase":
			value, err := io.ReadAll(io.LimitReader(part, maxPassphrase+1))
			part.Close()
			if err != nil || len(value) > maxPassphrase {
				s.writeError(w, r, http.StatusBadRequest, "that passphrase could not be read")
				return
			}
			passphrase = strings.TrimRight(string(value), "\r\n")

		case "archive":
			// The passphrase has to arrive first, because the file is streamed
			// straight into the decryption rather than being held anywhere.
			if passphrase == "" {
				part.Close()
				s.writeError(w, r, http.StatusBadRequest,
					"the passphrase has to be sent before the file")
				return
			}
			s.stageFrom(w, r, part, passphrase)
			part.Close()
			return

		default:
			part.Close()
		}
	}

	s.writeError(w, r, http.StatusBadRequest, "there is no archive in this upload")
}

// stageFrom runs the staging and answers, translating the package's refusals
// into the status codes that say what kind of refusal each one is.
//
// A successful stage restarts the host, rather than leaving the restore for
// somebody to apply. Two reasons, and the second is the important one.
//
// An archive that has been uploaded and accepted is an operator who has said
// what they want; a restore that then sits there is the request half done.
//
// And a staged restore applies on the next start of *any* kind -- a reboot, a
// `docker compose up`, a crash loop -- not only on one somebody meant as the
// second half of this. Left waiting, it is a change that fires later for
// reasons unconnected to it, at a moment nobody has associated with a restore.
// Applying it now is what keeps the act and its effect in the same place.
func (s *Server) stageFrom(w http.ResponseWriter, r *http.Request, body io.Reader, passphrase string) {
	actor := auth.FromContext(r.Context()).ID

	pending, err := s.opts.Backup.Stage(r.Context(), body, passphrase, actor)
	switch {
	case err == nil:
	case errors.Is(err, backup.ErrPassphrase):
		// 400 rather than 403: nothing here is about the caller's rights.
		//
		// The sentence is written here rather than carried by the sentinel,
		// which stays an ordinary Go error. This is the one place that knows
		// both what failed and that somebody is reading the answer.
		s.writeError(w, r, http.StatusBadRequest,
			"The archive did not open. Check the passphrase, and that the file "+
				"is the one that was downloaded.")
		return
	case errors.Is(err, backup.ErrKeyMismatch):
		s.writeError(w, r, http.StatusConflict,
			"This archive was made by a host using a different settings encryption "+
				"key, so the credentials in it cannot be read here. Set this host's "+
				"key to the one that host used, restart, and restore again.")
		return
	default:
		// writeProblem logs it with the correlation id the caller was given;
		// a second line here says the same thing without the id.
		s.writeProblem(w, r, http.StatusBadRequest, err,
			"That archive could not be read. Check it is a backup made by mcpd "+
				"and that the download finished.")
		return
	}

	s.opts.Log.WarnContext(r.Context(), "restoring this instance from an archive",
		"principal", actor,
		"taken_at", pending.Manifest.CreatedAt,
		"from_instance", pending.Manifest.Instance)

	// The staged copy is kept when the restart cannot be asked for, rather
	// than discarded. The archive was checked and is sound; what failed is the
	// restart, and telling somebody to upload it again would be answering the
	// wrong problem. The next start applies it, and the page says so.
	if err := s.opts.Restart("restore requested by " + actor); err != nil {
		s.writeJSON(w, r, http.StatusAccepted, map[string]any{
			"status":  "staged",
			"pending": pending,
			"note": "The archive is checked and ready, but this host could not " +
				"restart itself: " + err.Error() + ". It will be applied the next " +
				"time mcpd starts.",
		})
		return
	}

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{
		"status":  "restoring",
		"pending": pending,
		"note": "The archive has been checked. mcpd is restarting to apply it, " +
			"and this page will reconnect on its own.",
	})
}

// handleCancelRestore discards a staged restore.
func (s *Server) handleCancelRestore(w http.ResponseWriter, r *http.Request) {
	if s.opts.Backup == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host is not configured to restore backups")
		return
	}
	if err := s.opts.Backup.Cancel(r.Context(), auth.FromContext(r.Context()).ID); err != nil {
		s.writeError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
