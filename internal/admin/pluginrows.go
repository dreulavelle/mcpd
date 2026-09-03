package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

// PluginRows is the store behind a plugin's collection settings.
//
// An interface of the four verbs the dashboard needs, satisfied by
// *sqlite.PluginRowStore, so a test can hand the server a store over a
// temporary database and so nothing here reaches past those four.
type PluginRows interface {
	List(ctx context.Context, instance, field string) ([]sqlite.PluginRow, error)
	Create(ctx context.Context, actor, instance, field, identity string,
		data map[string]any, secrets map[string]string) (sqlite.PluginRow, error)
	Update(ctx context.Context, actor, id, identity string,
		data map[string]any, setSecrets map[string]string, clearSecrets []string) (sqlite.PluginRow, error)
	Delete(ctx context.Context, id string) error
}

// rowView is one row as the dashboard sees it: the non-secret columns as
// values, and for each secret column only whether it holds something. A stored
// credential is never sent back, for the same reason a secret setting is not.
type rowView struct {
	ID         string         `json:"id"`
	Values     map[string]any `json:"values"`
	SecretsSet []string       `json:"secrets_set"`
	UpdatedAt  time.Time      `json:"updated_at"`
	UpdatedBy  string         `json:"updated_by"`
}

func viewRow(r sqlite.PluginRow) rowView {
	set := make([]string, 0, len(r.Secrets))
	for k, v := range r.Secrets {
		if v != "" {
			set = append(set, k)
		}
	}
	sort.Strings(set)
	values := r.Data
	if values == nil {
		values = map[string]any{}
	}
	return rowView{ID: r.ID, Values: values, SecretsSet: set, UpdatedAt: r.UpdatedAt, UpdatedBy: r.UpdatedBy}
}

// rowRequest is what the row form submits: every column as a string, the way
// the settings form does, plus the secret columns to remove.
type rowRequest struct {
	Values       map[string]string `json:"values"`
	ClearSecrets []string          `json:"clear_secrets,omitempty"`
}

// collectionFor resolves a setting key to the instance, field and declaration
// of the collection it names, refusing anything that is not one.
func (s *Server) collectionFor(key string) (instance, field string, decl settings.Field, err error) {
	instance, field = settings.PluginFromSettingKey(key)
	if instance == "" {
		return "", "", settings.Field{}, fmt.Errorf("%q is not a plugin setting", key)
	}
	decl, ok := s.catalog().FieldFor(key)
	if !ok {
		return "", "", settings.Field{}, fmt.Errorf("%q is not an editable setting", key)
	}
	if decl.Kind != settings.KindCollection {
		return "", "", settings.Field{}, fmt.Errorf("%q is not a table", key)
	}
	return instance, field, decl, nil
}

func (s *Server) handleListPluginRows(w http.ResponseWriter, r *http.Request) {
	if s.opts.PluginRows == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "collection settings are unavailable")
		return
	}
	instance, field, decl, err := s.collectionFor(r.PathValue("key"))
	if err != nil {
		s.writeError(w, r, http.StatusNotFound, err.Error())
		return
	}
	rows, err := s.opts.PluginRows.List(r.Context(), instance, field)
	if err != nil {
		s.opts.Log.ErrorContext(r.Context(), "could not list collection rows", "key", r.PathValue("key"), "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "could not read the rows")
		return
	}
	views := make([]rowView, 0, len(rows))
	for _, row := range rows {
		views = append(views, viewRow(row))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"field": decl, "rows": views, "count": len(views),
	})
}

// rowInput is a validated, typed row ready to store.
type rowInput struct {
	identity string
	data     map[string]any
	secrets  map[string]string
}

// readRow validates a submitted row against the collection's columns.
//
// Everything is checked before anything is written, so a row with one bad
// column changes nothing. existing is the row being edited, or nil for a new
// one: a required secret may be left blank on an edit because a blank means
// "keep what is there", and that is only an answer when something is.
func readRow(decl settings.Field, req rowRequest, existing *sqlite.PluginRow) (rowInput, []string) {
	in := rowInput{data: map[string]any{}, secrets: map[string]string{}}
	var problems []string
	columns := map[string]bool{}
	clearing := map[string]bool{}
	for _, k := range req.ClearSecrets {
		clearing[k] = true
	}

	for i, col := range decl.Columns {
		columns[col.Key] = true
		value := strings.TrimSpace(req.Values[col.Key])

		if col.Kind == settings.KindSecret {
			switch {
			case value != "":
				in.secrets[col.Key] = value
			case clearing[col.Key] || existing == nil || existing.Secrets[col.Key] == "":
				if col.Required {
					problems = append(problems, fmt.Sprintf("settings: %s is required", col.Label))
				}
			}
			continue
		}

		if value == "" && !col.Required {
			// An empty optional column is stored as its empty value rather than
			// omitted, so a plugin reading the row sees the column exist.
			in.data[col.Key] = emptyValue(col.Kind)
			continue
		}
		if err := settings.ValidateValue(col, value); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		encoded, err := settings.Encode(col.Kind, value)
		if err != nil {
			problems = append(problems, fmt.Sprintf("settings: %s: %v", col.Label, err))
			continue
		}
		var typed any
		if err := json.Unmarshal([]byte(encoded), &typed); err != nil {
			problems = append(problems, fmt.Sprintf("settings: %s could not be read", col.Label))
			continue
		}
		in.data[col.Key] = typed
		if i == 0 {
			in.identity = value
		}
	}

	for key := range req.Values {
		if !columns[key] {
			problems = append(problems, fmt.Sprintf("settings: %q is not a column of %s", key, decl.Label))
		}
	}
	for key := range clearing {
		if !columns[key] {
			problems = append(problems, fmt.Sprintf("settings: %q is not a column of %s", key, decl.Label))
		}
	}
	sort.Strings(problems)
	return in, problems
}

// emptyValue is what an optional column holds when nothing was typed.
func emptyValue(kind settings.Kind) any {
	switch kind {
	case settings.KindBool:
		return false
	case settings.KindInt, settings.KindDuration:
		return 0
	case settings.KindList:
		return []string{}
	}
	return ""
}

func (s *Server) handleAddPluginRow(w http.ResponseWriter, r *http.Request) {
	if s.opts.PluginRows == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "collection settings are unavailable")
		return
	}
	instance, field, decl, err := s.collectionFor(r.PathValue("key"))
	if err != nil {
		s.writeError(w, r, http.StatusNotFound, err.Error())
		return
	}
	var req rowRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "the request could not be read")
		return
	}
	in, problems := readRow(decl, req, nil)
	if len(problems) > 0 {
		s.writeJSON(w, r, http.StatusBadRequest, map[string]any{"error": "invalid_row", "problems": problems})
		return
	}
	actor := auth.FromContext(r.Context()).ID
	row, err := s.opts.PluginRows.Create(r.Context(), actor, instance, field, in.identity, in.data, in.secrets)
	if err != nil {
		s.writeRowError(w, r, err)
		return
	}
	s.opts.Log.InfoContext(r.Context(), "collection row added",
		"actor", actor, "plugin", instance, "field", field, "row", row.Identity)
	s.rowsChanged(instance)
	s.writeJSON(w, r, http.StatusCreated, viewRow(row))
}

func (s *Server) handleUpdatePluginRow(w http.ResponseWriter, r *http.Request) {
	if s.opts.PluginRows == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "collection settings are unavailable")
		return
	}
	instance, field, decl, err := s.collectionFor(r.PathValue("key"))
	if err != nil {
		s.writeError(w, r, http.StatusNotFound, err.Error())
		return
	}
	var req rowRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "the request could not be read")
		return
	}
	id := r.PathValue("id")
	existing, ok := s.findRow(r.Context(), instance, field, id)
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "no such row")
		return
	}
	in, problems := readRow(decl, req, &existing)
	if len(problems) > 0 {
		s.writeJSON(w, r, http.StatusBadRequest, map[string]any{"error": "invalid_row", "problems": problems})
		return
	}
	actor := auth.FromContext(r.Context()).ID
	row, err := s.opts.PluginRows.Update(r.Context(), actor, id, in.identity, in.data, in.secrets, req.ClearSecrets)
	if err != nil {
		s.writeRowError(w, r, err)
		return
	}
	s.opts.Log.InfoContext(r.Context(), "collection row changed",
		"actor", actor, "plugin", instance, "field", field, "row", row.Identity)
	s.rowsChanged(instance)
	s.writeJSON(w, r, http.StatusOK, viewRow(row))
}

func (s *Server) handleRemovePluginRow(w http.ResponseWriter, r *http.Request) {
	if s.opts.PluginRows == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "collection settings are unavailable")
		return
	}
	instance, field, _, err := s.collectionFor(r.PathValue("key"))
	if err != nil {
		s.writeError(w, r, http.StatusNotFound, err.Error())
		return
	}
	id := r.PathValue("id")
	// Scoped to the collection in the path, so a row id from one instance
	// cannot be removed through another's endpoint.
	existing, ok := s.findRow(r.Context(), instance, field, id)
	if !ok {
		s.writeError(w, r, http.StatusNotFound, "no such row")
		return
	}
	if err := s.opts.PluginRows.Delete(r.Context(), id); err != nil {
		s.writeRowError(w, r, err)
		return
	}
	actor := auth.FromContext(r.Context()).ID
	s.opts.Log.InfoContext(r.Context(), "collection row removed",
		"actor", actor, "plugin", instance, "field", field, "row", existing.Identity)
	s.rowsChanged(instance)
	w.WriteHeader(http.StatusNoContent)
}

// findRow returns a row only if it belongs to the named collection.
func (s *Server) findRow(ctx context.Context, instance, field, id string) (sqlite.PluginRow, bool) {
	rows, err := s.opts.PluginRows.List(ctx, instance, field)
	if err != nil {
		return sqlite.PluginRow{}, false
	}
	for _, row := range rows {
		if row.ID == id {
			return row, true
		}
	}
	return sqlite.PluginRow{}, false
}

// rowsChanged tells the host an instance's configuration moved, so the plugin
// is rebuilt with its new rows the way it is when a setting changes.
func (s *Server) rowsChanged(instance string) {
	if s.opts.PluginRowsChanged != nil {
		s.opts.PluginRowsChanged(instance)
	}
}

func (s *Server) writeRowError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sqlite.ErrRowExists):
		s.writeJSON(w, r, http.StatusConflict, map[string]string{
			"error": "row_exists", "detail": "a row with that name already exists; names are unique within the table",
		})
	case errors.Is(err, sqlite.ErrNoSuchRow):
		s.writeError(w, r, http.StatusNotFound, "no such row")
	case errors.Is(err, sqlite.ErrRowNoCipher):
		s.writeJSON(w, r, http.StatusConflict, map[string]string{
			"error": "no_encryption", "detail": err.Error(),
		})
	default:
		s.opts.Log.ErrorContext(r.Context(), "could not write a collection row", "error", err)
		s.writeError(w, r, http.StatusInternalServerError, "the row could not be saved")
	}
}
