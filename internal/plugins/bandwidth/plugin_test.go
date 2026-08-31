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

// A default account that the credential cannot reach is a misconfiguration
// worth refusing at startup: every unqualified call would otherwise fail later
// with a 404 that says nothing about why.
func TestStartRefusesADefaultAccountTheCredentialDoesNotCover(t *testing.T) {
	_, srv := newTokenServer(t, 3600)
	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh",
		DefaultAccountID: "5010469", // real account, not one this credential covers
		APIURL:           srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	err := p.Start(context.Background())
	if err == nil {
		t.Fatal("a default outside the credential's scope was accepted")
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
		ClientID: "client", ClientSecret: "shh", DefaultAccountID: "5009041",
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
		ClientID: "client", ClientSecret: "shh", DefaultAccountID: "5009021",
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
		// A role guess reads as a diagnosis, so the message must also carry
		// the case where the credential already has every role -- otherwise
		// it sends somebody hunting for a role that does not exist.
		if !strings.Contains(err.Error(), "not enabled for the product") {
			t.Errorf("403 on %s offers only the role explanation: %v", path, err)
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
		ClientID: "client", ClientSecret: "shh", DefaultAccountID: "5009021",
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

// One instance answers for every account the credential reaches, so a caller
// names the account and the estate is readable from one place.
func TestACallMayNameAnyAccountTheCredentialCovers(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			_, _ = io.WriteString(w, `{"access_token":"`+
				jwt(t, `{"accounts":["5009021","5009041"]}`)+`","expires_in":3600}`)
			return
		}
		asked = append(asked, r.URL.Path)
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(srv.Close)

	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	for _, account := range []string{"5009021", "5009041"} {
		if _, err := p.listCalls(context.Background(), CallsInput{Account: account}); err != nil {
			t.Fatalf("listCalls on %s: %v", account, err)
		}
	}
	if len(asked) != 2 ||
		!strings.Contains(asked[0], "/accounts/5009021/") ||
		!strings.Contains(asked[1], "/accounts/5009041/") {
		t.Fatalf("the account did not reach the path: %v", asked)
	}
}

// An account the credential cannot reach is refused here, with both sides
// named, rather than sent upstream to come back as a 404 that explains nothing.
func TestACallNamingAnUncoveredAccountIsRefused(t *testing.T) {
	_, srv := newTokenServer(t, 3600)
	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	_, err := p.listCalls(context.Background(), CallsInput{Account: "5010469"})
	if err == nil {
		t.Fatal("an account outside the credential's scope was accepted")
	}
	for _, want := range []string{"5010469", "5009021", "5009041"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not name %s: %v", want, err)
		}
	}
}

// With several accounts in scope and nothing to choose between them, an
// unqualified call is refused. Answering about whichever account happened to be
// first is worse: "no port-ins are stuck" reads the same either way, and the
// operator has no way to tell it was about the wrong account.
func TestAnUnqualifiedCallIsRefusedRatherThanGuessed(t *testing.T) {
	_, srv := newTokenServer(t, 3600) // covers two accounts
	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	_, err := p.listCalls(context.Background(), CallsInput{})
	if err == nil {
		t.Fatal("an unqualified call was answered about an arbitrary account")
	}
	if !strings.Contains(err.Error(), "Name which one") {
		t.Errorf("the message does not ask for an account: %v", err)
	}
}

// One account in scope settles it on its own: there is nothing to choose
// between, so making somebody say it would be ceremony.
func TestOneAccountInScopeNeedsNoNaming(t *testing.T) {
	ts, srv := newTokenServer(t, 3600)
	ts.accounts = `["5009021"]`
	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	got, err := p.listAccounts(context.Background(), AccountsInput{})
	if err != nil {
		t.Fatalf("listAccounts: %v", err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0] != "5009021" {
		t.Fatalf("accounts = %v", got.Accounts)
	}
	if got.Note != "" {
		t.Errorf("a single account needs no caveat: %q", got.Note)
	}
}

// The default decides what an unqualified question means.
func TestTheDefaultAnswersAnUnqualifiedCall(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			_, _ = io.WriteString(w, `{"access_token":"`+
				jwt(t, `{"accounts":["5009021","5009041"]}`)+`","expires_in":3600}`)
			return
		}
		asked = append(asked, r.URL.Path)
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(srv.Close)

	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh", DefaultAccountID: "5009041",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	if _, err := p.listCalls(context.Background(), CallsInput{}); err != nil {
		t.Fatalf("listCalls: %v", err)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "/accounts/5009041/") {
		t.Fatalf("the default did not decide the account: %v", asked)
	}
}

// list_accounts is how an agent finds out what it may ask about, so it has to
// report both the options and what happens if nobody picks.
func TestListAccountsReportsTheChoiceAndTheDefault(t *testing.T) {
	_, srv := newTokenServer(t, 3600)
	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())

	got, err := p.listAccounts(context.Background(), AccountsInput{})
	if err != nil {
		t.Fatalf("listAccounts: %v", err)
	}
	if len(got.Accounts) != 2 {
		t.Fatalf("accounts = %v", got.Accounts)
	}
	if got.Default != "" {
		t.Errorf("no default was configured, got %q", got.Default)
	}
	if !strings.Contains(got.Note, "refused") {
		t.Errorf("the note does not say what an unqualified call does: %q", got.Note)
	}
}
