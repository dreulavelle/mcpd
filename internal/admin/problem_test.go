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
			name: "a settings failure",
			err:  errors.New("settings: openai.api_key is a secret and no encryption key is configured"),
			want: "That change could not be saved.",
		},
		{
			// The denylist this replaced named neither, and both reach a 400.
			name: "an archive that will not read",
			err:  errors.New("backup: read the manifest: unexpected EOF"),
			want: "That change could not be saved.",
		},
		{
			name: "an account that will not validate",
			err:  errors.New("chatgpt account: the rate limit cannot be negative"),
			want: "That change could not be saved.",
		},
		{
			name: "Go's own decoder",
			err:  errors.New(`json: unknown field "enabld"`),
			want: "That change could not be saved.",
		},
		{
			// Every error this package returns is a statement about the
			// document somebody just pasted, so the text is the answer and
			// only the prefix was in the way.
			name: "a document that will not parse",
			err:  errors.New(`mcpservers: server.json is missing "name"`),
			want: `server.json is missing "name"`,
		},
		{
			name: "a header name the far end could not be sent",
			err:  fmt.Errorf("%w", errors.New(`mcpservers: "X Foo" is not a usable HTTP header name`)),
			want: `"X Foo" is not a usable HTTP header name`,
		},
		{
			// Written for the person who made the request, so it is the
			// answer. Substituting a fallback here would lose the only thing
			// they could act on.
			name: "a refusal a person can act on",
			err:  errors.New(`there is no system called "graylog"`),
			want: `there is no system called "graylog"`,
		},
		{
			// A colon after a lowercase word is what a wrapped error looks
			// like, and a URL is not one. Matching "app:" anywhere in the
			// text turned this into the fallback.
			name: "a sentence carrying a URL",
			err:  errors.New("http://app:8080 refused the connection"),
			want: "http://app:8080 refused the connection",
		},
		{
			name: "a sentence carrying an OAuth word",
			err:  errors.New("oauth: the far end did not answer"),
			want: "That change could not be saved.",
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
