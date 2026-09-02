package tunnel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tcadmin "github.com/openai/tunnel-client/pkg/controlplane/admin"
)

// Without an admin key the feature is unavailable, which is different from
// broken: the dashboard offers paste-an-id instead of reporting a failure.
func TestDirectoryIsUnavailableWithoutAKey(t *testing.T) {
	d := NewDirectory("  ", "org_test", "")
	if d.Available() {
		t.Fatal("whitespace is not a key")
	}
	if _, err := d.List(context.Background()); !errors.Is(err, ErrNoAdminKey) {
		t.Fatalf("List error = %v, want ErrNoAdminKey", err)
	}
}

func TestDeleteRejectsSomethingThatIsNotATunnelID(t *testing.T) {
	d := NewDirectory("sk-admin-test", "org_test", "")

	err := d.Delete(context.Background(), "../../v1/tunnels")
	if err == nil {
		t.Fatal("a malformed id must be refused before any request is made")
	}
	if strings.Contains(err.Error(), "sk-admin") {
		t.Fatal("the key must never appear in an error")
	}
}

func TestCreateNeedsAName(t *testing.T) {
	d := NewDirectory("sk-admin-test", "org_test", "")
	if _, err := d.Create(context.Background(), "   ", "", ""); err == nil {
		t.Fatal("a nameless tunnel is not identifiable in anyone's account")
	}
}

// A request id is hex, so one containing "403" must not be diagnosed as a
// permissions problem. Reading the typed error is what prevents that.
func TestDiagnosisReadsTheStatusNotTheText(t *testing.T) {
	d := NewDirectory("sk-admin-test", "org_test", "")

	// The reason is the contract, not the sentence beside it: the dashboard
	// branches on it to decide which explanation to lay out, and prose cannot
	// be branched on.
	for status, want := range map[int]string{
		401: ReasonAdminKeyRejected,
		403: ReasonTunnelsManageRequired,
	} {
		got := d.explain(&tcadmin.RequestError{StatusCode: status})
		if Reason(got) != want {
			t.Errorf("%d reason = %q, want %q", status, Reason(got), want)
		}
		// Short enough to read in a toast. The explanation lives in the page.
		if len(got.Error()) > 120 {
			t.Errorf("%d message is %d characters; it has to fit a toast: %q",
				status, len(got.Error()), got.Error())
		}
	}

	missingOrg := d.explain(&tcadmin.RequestError{
		StatusCode: 400,
		Message:    "Exactly one of organization_id, workspace_id, or tenant_id must be provided",
	})
	if Reason(missingOrg) != ReasonOrgIDRejected {
		t.Errorf("400 reason = %q, want %q", Reason(missingOrg), ReasonOrgIDRejected)
	}

	// The trap: a plain error whose text happens to contain a status-like
	// number must not be diagnosed at all.
	plain := d.explain(errors.New("dial tcp: request req_c403a99 failed"))
	if Reason(plain) != "" {
		t.Errorf("a request id containing 403 was misdiagnosed as %q", Reason(plain))
	}
}

// Errors from the transport quote request details freely.
func TestAnUnrecognisedFailureStillHidesTheKey(t *testing.T) {
	const key = "sk-admin-verysecret"
	d := NewDirectory(key, "org_test", "")

	got := d.explain(errors.New("dial failed for key " + key)).Error()
	if strings.Contains(got, key) {
		t.Fatalf("explain leaked the key: %q", got)
	}
}

// An admin key alone cannot list anything: every tunnel request is scoped to
// exactly one organisation and a request naming none is rejected.
func TestBothCredentialsAreNeeded(t *testing.T) {
	if NewDirectory("sk-admin-test", "", "").Available() {
		t.Error("an admin key without an organization is not usable")
	}
	if NewDirectory("", "org_test", "").Available() {
		t.Error("an organization without an admin key is not usable")
	}

	missing := NewDirectory("sk-admin-test", "", "").Missing()
	if !strings.Contains(missing, "organization") {
		t.Errorf("Missing = %q, want it to name what is absent", missing)
	}
}

// A 404 is the answer "no"; anything else is the question not being
// answered, and must not mark a tunnel missing because an admin key expired.
func TestDirectoryExists_TellsMissingFromUnanswered(t *testing.T) {
	const id = "tunnel_0123456789abcdef0123456789abcdef"
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"id":"` + id + `","name":"x"}`))
	}))
	defer srv.Close()
	d := NewDirectory("sk-admin-x", "org_1", srv.URL)

	if ok, err := d.Exists(t.Context(), id); err != nil || !ok {
		t.Fatalf("200 = %v, %v; want present", ok, err)
	}
	status = http.StatusNotFound
	if ok, err := d.Exists(t.Context(), id); err != nil || ok {
		t.Fatalf("404 = %v, %v; want missing", ok, err)
	}
	status = http.StatusUnauthorized
	if _, err := d.Exists(t.Context(), id); err == nil {
		t.Fatal("a 401 is not an answer about the tunnel")
	}
}

// Deleting a tunnel OpenAI no longer has is the outcome asked for, not a
// failure: refusing on the 404 left the assignment behind, and mcpd went on
// reporting the tunnel as stopped on every restart with no way to remove it.
func TestDirectoryDelete_TreatsGoneAsDeleted(t *testing.T) {
	const id = "tunnel_0123456789abcdef0123456789abcdef"
	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
	}))
	defer srv.Close()
	d := NewDirectory("sk-admin-x", "org_1", srv.URL)
	if err := d.Delete(t.Context(), id); err != nil {
		t.Fatalf("404 should count as deleted: %v", err)
	}
	status = http.StatusForbidden
	if err := d.Delete(t.Context(), id); err == nil {
		t.Fatal("a refusal is still a refusal")
	}
}
