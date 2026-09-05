package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spoked/mcpd/internal/tunnel"
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

// A start or restart that fails answers with the tunnel's own sentence.
//
// The bug: the handler answered with what Start returned, which is the
// wrapped error the manager built for the log -- so pressing Restart on a
// tunnel with no configuration produced "tunnel: no tunnel is configured" in
// a toast, and a dial failure produced an address.
func TestTunnelFailure_AnswersWithTheSentenceNotTheError(t *testing.T) {
	s := newTestServer(t, newFakeAccounts())
	s.opts.Tunnel = stubTunnels{list: []tunnel.Status{
		{TunnelID: "tunnel_a", State: tunnel.StateConnected},
		{
			TunnelID: "tunnel_b",
			State:    tunnel.StateFailed,
			Message:  "OpenAI no longer accepts this account's key.",
			Detail:   "tunnel: OpenAI refused this account's key (token_invalidated)",
			Code:     "token_invalidated",
		},
	}}

	if got := s.tunnelFailure("tunnel_b", "no"); got != "OpenAI no longer accepts this account's key." {
		t.Errorf("named tunnel = %q", got)
	}
	// No id: the aggregate start, which has no one tunnel to ask about.
	if got := s.tunnelFailure("", "no"); got != "OpenAI no longer accepts this account's key." {
		t.Errorf("any tunnel = %q", got)
	}
	// A tunnel that did not fail says nothing, so the handler's own sentence
	// stands rather than a blank detail.
	if got := s.tunnelFailure("tunnel_a", "That connector could not be started."); got != "That connector could not be started." {
		t.Errorf("healthy tunnel = %q", got)
	}
	if got := s.tunnelFailure("tunnel_z", "That connector could not be started."); got != "That connector could not be started." {
		t.Errorf("unknown tunnel = %q", got)
	}
}

type stubTunnels struct{ list []tunnel.Status }

func (s stubTunnels) Status() []tunnel.Status             { return s.list }
func (stubTunnels) Start(context.Context) error           { return nil }
func (stubTunnels) Stop(context.Context) error            { return nil }
func (stubTunnels) Enabled() bool                         { return true }
func (stubTunnels) Restart(context.Context, string) error { return nil }
