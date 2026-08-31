package bandwidth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/plugins"
)

func newFor(t *testing.T, cfg Config, hc *http.Client) *Plugin {
	t.Helper()
	p, err := New(plugins.Deps{
		Instance: "bandwidth",
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTP:     hc,
		Now:      at(fixedNow),
	}, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// An instance nobody has finished configuring still mounts, so its settings
// form has somewhere to live and the health report can say what is missing.
func TestUnconfiguredMountsAndSaysWhatIsMissing(t *testing.T) {
	p := newFor(t, Config{}, nil)
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("an unconfigured instance failed to start: %v", err)
	}
	h := p.Check(context.Background())
	if h.State == plugins.HealthyState {
		t.Fatal("an unconfigured instance reported healthy")
	}
	if !strings.Contains(h.Message, "credential") {
		t.Errorf("the health message does not say what is missing: %q", h.Message)
	}
	// A tool call must say the same thing rather than fail as a connection
	// error, which is what sends a model on to try three more tools.
	if _, err := p.getStatistics(context.Background(), StatisticsInput{}); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Errorf("a tool call on an unconfigured instance said: %v", err)
	}
}

// The probe proves the credential without reading a row of anybody's estate,
// and catches the mistake that is otherwise a 404 on every call: a credential
// that is real but does not cover the account this instance was told to read.
func TestStartRefusesAnAccountTheCredentialDoesNotCover(t *testing.T) {
	_, srv := newTokenServer(t, 3600)
	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh",
		AccountID: "5010469", // real account, not one this credential covers
		APIURL:    srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	err := p.Start(context.Background())
	if err == nil {
		t.Fatal("an account outside the credential's scope was accepted")
	}
	// The message has to name both sides, or the operator is left comparing a
	// number they typed against nothing.
	for _, want := range []string{"5010469", "5009021", "5009041"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not name %s: %v", want, err)
		}
	}
	if h := p.Check(context.Background()); h.State == plugins.HealthyState {
		t.Error("a failed probe reported healthy")
	}
}

func TestStartAcceptsACoveredAccount(t *testing.T) {
	_, srv := newTokenServer(t, 3600)
	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh", AccountID: "5009041",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h := p.Check(context.Background()); h.State != plugins.HealthyState {
		t.Fatalf("a working instance is not healthy: %+v", h)
	}
}

// A credential whose claims name no accounts is still usable: the claim is the
// credential describing itself, and the first real read settles it either way.
func TestStartAcceptsACredentialThatNamesNoAccounts(t *testing.T) {
	ts, srv := newTokenServer(t, 3600)
	ts.accounts = "[]"
	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh", AccountID: "5009021",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("a credential with no accounts claim was refused: %v", err)
	}
}

// A 403 is Bandwidth's only signal that a role is missing, and it does not say
// which. Naming the likely one turns a guess among thirteen into one edit.
func TestForbiddenNamesTheLikelyRole(t *testing.T) {
	for path, want := range map[string]string{
		"/api/v2/users/5009021/messages":                                   "Messaging insights",
		"/api/v2/accounts/5009021/statistics":                              "Reporting",
		"/v2/accounts/5009021/phoneNumberLookup/bulk/r-1":                  "TN lookup",
		"/api/v2/accounts/5009021/phoneNumbers/+1800/tollFreeVerification": "Campaign management",
	} {
		err := explainRequestFailure(http.StatusForbidden, path, nil)
		if !strings.Contains(err.Error(), want) {
			t.Errorf("403 on %s does not suggest %q: %v", path, want, err)
		}
		if !strings.Contains(err.Error(), "cannot be edited after it is created") {
			t.Errorf("403 on %s does not say roles are fixed: %v", path, err)
		}
	}
}

// A listing that was cut has to say so. A caller handed the first page of nine
// with nothing to indicate it will reason about the estate as though that were
// all of it.
func TestATruncatedListingSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			newTokenServer(t, 3600)
			_, _ = io.WriteString(w, `{"access_token":"`+jwt(t, `{"accounts":["5009021"]}`)+
				`","expires_in":3600}`)
			return
		}
		_, _ = io.WriteString(w, `[{"callId":"a"},{"callId":"b"},{"callId":"c"}]`)
	}))
	t.Cleanup(srv.Close)

	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh", AccountID: "5009021",
		MaxItems: 2,
		APIURL:   srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	got, err := p.listCalls(context.Background(), CallsInput{})
	if err != nil {
		t.Fatalf("listCalls: %v", err)
	}
	if got.Returned != 2 || len(got.Items) != 2 {
		t.Fatalf("returned %d items, want 2", got.Returned)
	}
	if !strings.Contains(got.Note, "truncated") {
		t.Errorf("a truncated listing did not say so: %q", got.Note)
	}
}
