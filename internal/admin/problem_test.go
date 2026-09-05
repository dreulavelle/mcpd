package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A 4xx body is read by whoever made the request, so it never carries an
// error written for whoever reads a stack trace.
//
// The bug this defends: a bad server.json paste answered with
// "mcpservers: this is not a JSON document: invalid character 'x'", a locked
// database answered a save with "sqlite: begin: database is locked", and both
// went straight into a toast. The error still has to reach somebody, so it
// goes to the log with the correlation id the caller was given.
func TestWriteProblem_HidesWhatOnlyALogCanCarry(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "a store failure",
			err:  errors.New("sqlite: begin: database is locked"),
			want: "That change could not be saved.",
		},
		{
			name: "a wrapped store failure",
			err:  fmt.Errorf("import: %w", errors.New("sqlite: import mcp server: constraint")),
			want: "That change could not be saved.",
		},
		{
			name: "a settings failure",
			err:  errors.New("settings: openai.api_key is a secret and no encryption key is configured"),
			want: "That change could not be saved.",
		},
		{
			name: "a document that will not parse",
			err:  errors.New("mcpservers: server.json is missing \"name\""),
			want: "That change could not be saved.",
		},
		{
			// Written for the person who made the request, so it is the
			// answer. Substituting a fallback here would lose the only thing
			// they could act on.
			name: "a refusal a person can act on",
			err:  errors.New("there is no system called \"graylog\""),
			want: "there is no system called \"graylog\"",
		},
	}

	s := newTestServer(t, newFakeAccounts())
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/api/plugins/echo", nil)
			s.writeProblem(w, r, http.StatusBadRequest, tc.err, "That change could not be saved.")

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", w.Code)
			}
			var body map[string]string
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["error"] != tc.want {
				t.Errorf("error = %q, want %q", body["error"], tc.want)
			}
		})
	}
}
