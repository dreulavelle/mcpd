package tunnel

import (
	"context"
	"errors"
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

	unauthorised := d.explain(&tcadmin.RequestError{StatusCode: 401}).Error()
	if !strings.Contains(unauthorised, "Admin keys") {
		t.Errorf("401 = %q, want it to name admin keys", unauthorised)
	}

	forbidden := d.explain(&tcadmin.RequestError{StatusCode: 403}).Error()
	if !strings.Contains(forbidden, "Tunnels: Manage") {
		t.Errorf("403 = %q, want it to name the missing permission", forbidden)
	}

	missingOrg := d.explain(&tcadmin.RequestError{
		StatusCode: 400,
		Message:    "Exactly one of organization_id, workspace_id, or tenant_id must be provided",
	}).Error()
	if !strings.Contains(missingOrg, "org_") {
		t.Errorf("400 = %q, want it to name the organization ID", missingOrg)
	}

	// The trap: a plain error whose text happens to contain a status-like
	// number must not be diagnosed at all.
	plain := d.explain(errors.New("dial tcp: request req_c403a99 failed")).Error()
	if strings.Contains(plain, "Tunnels: Manage") {
		t.Errorf("a request id containing 403 was misdiagnosed: %q", plain)
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
