package admin

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/backup"
)

// Destinations, runs, and the schedule, over HTTP.
//
// Every route here takes the same right as the backup routes beside them, and
// for the same reason: a destination is where a complete copy of this instance
// goes -- every account, every group's reach, every stored credential -- so
// naming one is an administrator's decision rather than an operator's.
//
// A destination's credential never comes back out. The form needs to know
// whether one is set, not what it is; a page that could show one is a page that
// hands every credential to anybody who reaches it.

// BackupDestinations is the stored set of places archives are sent.
//
// An interface for the reason Accounts and Groups are: it keeps these handlers
// testable without a database, and it says exactly what the dashboard may ask
// for. *sqlite.BackupStore is the implementation.
type BackupDestinations interface {
	Destinations(ctx context.Context) ([]backup.Destination, error)
	Destination(ctx context.Context, id string) (backup.Destination, bool, error)
	CreateDestination(ctx context.Context, actor string, d backup.Destination) (backup.Destination, error)
	UpdateDestination(ctx context.Context, actor, id string, up backup.DestinationUpdate) (backup.Destination, error)
	DeleteDestination(ctx context.Context, actor, id string) error
	// RecordHostKey pins what an SFTP server presented. Only Test connection
	// calls it, and only when nothing is pinned yet.
	RecordHostKey(ctx context.Context, actor, id, fingerprint string) error
	Runs(ctx context.Context, limit int) ([]backup.Run, error)
	Run(ctx context.Context, id string) (backup.Run, bool, error)
}

// BackupRunner takes backups without anybody asking, and on demand.
type BackupRunner interface {
	// Trigger starts a run and returns the row it inserted, or
	// backup.ErrRunInProgress when one is already going.
	Trigger(ctx context.Context, trigger string) (backup.Run, error)
	// Status is the schedule summary the page opens with.
	Status(ctx context.Context) *backup.ScheduleStatus
}

// destinationView is one destination as the dashboard sees it.
//
// No credential, ever. HasSecret is the only thing said about it, because
// whether a destination has one is a fact the form has to render and an
// operator who has forgotten a password replaces it rather than reading it.
type destinationView struct {
	ID   string      `json:"id"`
	Name string      `json:"name"`
	Kind backup.Kind `json:"kind"`
	// Where is the address in one line, for the list.
	Where     string          `json:"where"`
	Settings  backup.Settings `json:"settings"`
	Enabled   bool            `json:"enabled"`
	Policy    backup.Policy   `json:"policy"`
	HostKey   string          `json:"host_key,omitempty"`
	HasSecret bool            `json:"has_secret"`
	CreatedAt time.Time       `json:"created_at"`
	LastRunAt *time.Time      `json:"last_run_at,omitempty"`
	LastOK    *bool           `json:"last_ok,omitempty"`
	// LastError is the sentence and LastDetail the evidence. Anything that
	// renders the second in prose is a bug.
	LastError  string `json:"last_error,omitempty"`
	LastDetail string `json:"last_detail,omitempty"`
}

func newDestinationView(d backup.Destination) destinationView {
	return destinationView{
		ID:         d.ID,
		Name:       d.Name,
		Kind:       d.Kind,
		Where:      d.Where(),
		Settings:   d.Settings,
		Enabled:    d.Enabled,
		Policy:     d.Policy,
		HostKey:    d.HostKey,
		HasSecret:  d.Secret != "",
		CreatedAt:  d.CreatedAt,
		LastRunAt:  d.LastRunAt,
		LastOK:     d.LastOK,
		LastError:  d.LastError,
		LastDetail: d.LastDetail,
	}
}

// destinationBody is what the form sends.
//
// Pointers on everything editable, so "not sent" and "set to empty" stay
// different instructions. It matters most for the credential: the page never
// reads one back, so an edit that changes only the retention arrives with no
// secret at all, and a plain string would read that as an erasure.
type destinationBody struct {
	Name     *string          `json:"name"`
	Kind     *string          `json:"kind"`
	Settings *backup.Settings `json:"settings"`
	Secret   *string          `json:"secret"`
	Enabled  *bool            `json:"enabled"`
	Policy   *backup.Policy   `json:"policy"`
	HostKey  *string          `json:"host_key"`
}

func (s *Server) backupDestinations() (BackupDestinations, bool) {
	if s.opts.BackupDestinations == nil {
		return nil, false
	}
	return s.opts.BackupDestinations, true
}

// handleListBackupDestinations lists where backups go.
func (s *Server) handleListBackupDestinations(w http.ResponseWriter, r *http.Request) {
	store, ok := s.backupDestinations()
	if !ok {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host cannot store backup destinations; no encryption key is configured")
		return
	}
	dests, err := store.Destinations(r.Context())
	if err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, err,
			"The list of backup destinations could not be read.")
		return
	}
	// Empty rather than nil: the page maps over these, and a null would blank
	// it on the ordinary state of a new install.
	views := []destinationView{}
	for _, d := range dests {
		views = append(views, newDestinationView(d))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"destinations": views,
		// What this build can talk to, so the form offers the kinds that exist
		// rather than a list written down twice.
		"kinds": backup.Kinds,
	})
}

// handleGetBackupDestination returns one.
func (s *Server) handleGetBackupDestination(w http.ResponseWriter, r *http.Request) {
	store, ok := s.backupDestinations()
	if !ok {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host cannot store backup destinations; no encryption key is configured")
		return
	}
	d, found, err := store.Destination(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, err,
			"That backup destination could not be read.")
		return
	}
	if !found {
		s.writeError(w, r, http.StatusNotFound, "There is no backup destination with that id.")
		return
	}
	s.writeJSON(w, r, http.StatusOK, newDestinationView(d))
}

// handleAddBackupDestination stores a new one.
func (s *Server) handleAddBackupDestination(w http.ResponseWriter, r *http.Request) {
	store, ok := s.backupDestinations()
	if !ok {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host cannot store backup destinations; no encryption key is configured")
		return
	}
	var body destinationBody
	if !s.decode(w, r, &body) {
		return
	}

	d := backup.Destination{Policy: backup.DefaultPolicy}
	if body.Name != nil {
		d.Name = *body.Name
	}
	if body.Kind != nil {
		d.Kind = backup.Kind(strings.TrimSpace(*body.Kind))
	}
	if body.Settings != nil {
		d.Settings = *body.Settings
	}
	if body.Secret != nil {
		d.Secret = *body.Secret
	}
	if body.Enabled != nil {
		d.Enabled = *body.Enabled
	}
	if body.Policy != nil {
		d.Policy = *body.Policy
	}
	if body.HostKey != nil {
		d.HostKey = *body.HostKey
	}

	created, err := store.CreateDestination(r.Context(), auth.FromContext(r.Context()).ID, d)
	if err != nil {
		s.writeError(w, r, destinationStatus(err), destinationMessage(err))
		return
	}
	s.writeJSON(w, r, http.StatusCreated, newDestinationView(created))
}

// handleUpdateBackupDestination edits one, leaving unsent fields alone.
func (s *Server) handleUpdateBackupDestination(w http.ResponseWriter, r *http.Request) {
	store, ok := s.backupDestinations()
	if !ok {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host cannot store backup destinations; no encryption key is configured")
		return
	}
	var body destinationBody
	if !s.decode(w, r, &body) {
		return
	}
	if body.Kind != nil {
		// Changing the kind would keep a name, a retention and a credential
		// that belonged to a different sort of thing entirely. Removing it and
		// adding another is one honest act instead of one confusing one.
		s.writeError(w, r, http.StatusBadRequest,
			"A destination's kind cannot be changed. Remove it and add the new one.")
		return
	}

	updated, err := store.UpdateDestination(r.Context(), auth.FromContext(r.Context()).ID,
		r.PathValue("id"), backup.DestinationUpdate{
			Name:     body.Name,
			Settings: body.Settings,
			Secret:   body.Secret,
			Enabled:  body.Enabled,
			Policy:   body.Policy,
			HostKey:  body.HostKey,
		})
	if err != nil {
		s.writeError(w, r, destinationStatus(err), destinationMessage(err))
		return
	}
	s.writeJSON(w, r, http.StatusOK, newDestinationView(updated))
}

// handleRemoveBackupDestination forgets one.
//
// Nothing already written there is touched. A backup on a NAS is a file
// somebody may still need, and removing a row is mcpd being told to stop
// sending rather than being told to delete what it sent.
func (s *Server) handleRemoveBackupDestination(w http.ResponseWriter, r *http.Request) {
	store, ok := s.backupDestinations()
	if !ok {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host cannot store backup destinations; no encryption key is configured")
		return
	}
	err := store.DeleteDestination(r.Context(), auth.FromContext(r.Context()).ID, r.PathValue("id"))
	if err != nil {
		s.writeError(w, r, destinationStatus(err), destinationMessage(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testResult is what the Test connection button gets back.
//
// Always a 200. The question it asks is "does this work", and both answers are
// answers -- an HTTP error would leave the page deciding whether the request
// failed or the destination did.
type testResult struct {
	OK bool `json:"ok"`
	// Message is the sentence, Detail the evidence behind it.
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	// HostKey is what an SFTP server presented, so an operator can compare it
	// with what the server says about itself before trusting it.
	HostKey string `json:"host_key,omitempty"`
	// HostKeyRecorded says the fingerprint above is now pinned, which is the
	// difference between a page that shows a key and one that has stored it.
	HostKeyRecorded bool `json:"host_key_recorded"`
}

// testTimeout bounds a Test connection. Long enough for a NAS to wake a disk,
// short enough that somebody watching the button gets an answer.
const testTimeout = 45 * time.Second

// handleTestBackupDestination reaches the destination and says what happened.
//
// For SFTP with nothing pinned yet, this is the one path that records a host
// key -- and it is the only one. A run that learned an identity would be
// trusting whatever answered on the night; a person pressing this button is
// looking at the fingerprint while it is recorded, and can compare it with what
// `ssh-keyscan` says before they enable the destination.
func (s *Server) handleTestBackupDestination(w http.ResponseWriter, r *http.Request) {
	store, ok := s.backupDestinations()
	if !ok {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host cannot store backup destinations; no encryption key is configured")
		return
	}
	id := r.PathValue("id")
	d, found, err := store.Destination(r.Context(), id)
	if err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, err,
			"That backup destination could not be read.")
		return
	}
	if !found {
		s.writeError(w, r, http.StatusNotFound, "There is no backup destination with that id.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), testTimeout)
	defer cancel()

	learn := d.Kind == backup.KindSFTP && strings.TrimSpace(d.HostKey) == ""
	pool := (*x509.CertPool)(nil)
	if s.opts.TrustPool != nil {
		pool = s.opts.TrustPool()
	}
	transport, err := backup.OpenDestination(d, backup.TransportOptions{
		Pool:         pool,
		LearnHostKey: learn,
		Log:          s.opts.Log,
	})
	if err != nil {
		sentence, detail := backupProblem(err)
		s.writeJSON(w, r, http.StatusOK, testResult{Message: sentence, Detail: detail})
		return
	}
	defer transport.Close()

	checkErr := transport.Check(ctx)

	result := testResult{OK: checkErr == nil}
	if reporter, hasKey := transport.(backup.HostKeyReporter); hasKey {
		result.HostKey = reporter.PresentedHostKey()
	}
	// Recorded even when the probe afterwards failed: the identity was
	// established by the handshake, and a user who can sign in but not write is
	// a separate problem with its own sentence. RecordHostKey is guarded on the
	// key still being empty, so this can never overwrite a pin.
	if learn && result.HostKey != "" {
		if err := store.RecordHostKey(ctx, auth.FromContext(r.Context()).ID, id, result.HostKey); err != nil {
			s.opts.Log.ErrorContext(r.Context(), "could not record a backup destination's host key",
				"destination", d.Name, "error", err)
		} else {
			result.HostKeyRecorded = true
		}
	}

	if checkErr != nil {
		result.Message, result.Detail = backupProblem(checkErr)
		s.writeJSON(w, r, http.StatusOK, result)
		return
	}
	result.Message = "mcpd reached this destination, and wrote and removed a test file."
	if result.HostKeyRecorded {
		result.Message += " Its host key has been recorded, and a backup will not be " +
			"sent if the server ever presents a different one."
	}
	s.writeJSON(w, r, http.StatusOK, result)
}

// handleRunBackup starts a backup to every enabled destination.
func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	if s.opts.BackupRunner == nil {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host is not configured to send backups anywhere")
		return
	}
	run, err := s.opts.BackupRunner.Trigger(r.Context(), backup.TriggerManual)
	switch {
	case err == nil:
	case errors.Is(err, backup.ErrRunInProgress):
		s.writeError(w, r, http.StatusConflict,
			"A backup is already running. Wait for it to finish, then try again.")
		return
	case errors.Is(err, backup.ErrNoDestination):
		s.writeError(w, r, http.StatusBadRequest,
			"There is nowhere to send a backup. Add a destination and switch it on.")
		return
	case errors.Is(err, backup.ErrNoPassphrase):
		s.writeError(w, r, http.StatusBadRequest,
			"No passphrase is set, so a backup cannot be sealed. Set one on this page.")
		return
	default:
		s.writeProblem(w, r, http.StatusInternalServerError, err,
			"The backup could not be started.")
		return
	}

	s.opts.Log.WarnContext(r.Context(), "a backup to this host's destinations was started",
		"principal", auth.FromContext(r.Context()).ID, "run_id", run.ID)
	// 202: the run is under way and the page follows it in the history.
	s.writeJSON(w, r, http.StatusAccepted, run)
}

// handleListBackupRuns returns the history, newest first.
func (s *Server) handleListBackupRuns(w http.ResponseWriter, r *http.Request) {
	store, ok := s.backupDestinations()
	if !ok {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host is not keeping a record of backups")
		return
	}
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	runs, err := store.Runs(r.Context(), limit)
	if err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, err,
			"The record of past backups could not be read.")
		return
	}
	if runs == nil {
		runs = []backup.Run{}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"runs": runs})
}

// handleGetBackupRun returns one run.
func (s *Server) handleGetBackupRun(w http.ResponseWriter, r *http.Request) {
	store, ok := s.backupDestinations()
	if !ok {
		s.writeError(w, r, http.StatusNotImplemented,
			"this host is not keeping a record of backups")
		return
	}
	run, found, err := store.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, err,
			"That backup could not be read.")
		return
	}
	if !found {
		s.writeError(w, r, http.StatusNotFound, "There is no backup with that id.")
		return
	}
	s.writeJSON(w, r, http.StatusOK, run)
}

// backupProblem splits an error into the sentence somebody reads and the
// evidence behind it, the same way the runner does when it records one.
func backupProblem(err error) (sentence, detail string) {
	var written backup.Evidencer
	if errors.As(err, &written) {
		return written.Error(), written.Evidence()
	}
	if errors.Is(err, backup.ErrNoHostKey) {
		return backup.ErrNoHostKey.Error(), ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The destination did not answer in time.", err.Error()
	}
	return "mcpd could not reach this destination.", err.Error()
}

// destinationStatus maps a store refusal onto the status that describes it.
func destinationStatus(err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "already exists"):
		return http.StatusConflict
	case strings.Contains(msg, "no such backup destination"):
		return http.StatusNotFound
	case strings.Contains(msg, "backup destination:"), errors.Is(err, backup.ErrNoHostKey):
		return http.StatusBadRequest
	case strings.Contains(msg, "no encryption key"):
		return http.StatusNotImplemented
	}
	return http.StatusInternalServerError
}

// destinationMessage strips the package prefix off a refusal already written
// for a person, and leaves anything else to say what it says.
func destinationMessage(err error) string {
	msg := err.Error()
	for _, prefix := range []string{"backup destination: ", "backup: ", "sqlite: "} {
		msg = strings.TrimPrefix(msg, prefix)
	}
	return msg
}
