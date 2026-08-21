package tunnel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Without an admin key the feature is unavailable, which is different from
// broken: the dashboard offers paste-an-id instead of reporting a failure.
func TestDirectoryIsUnavailableWithoutAKey(t *testing.T) {
	d := NewDirectory("  ", "")
	if d.Available() {
		t.Fatal("whitespace is not a key")
	}
	if _, err := d.List(context.Background()); !errors.Is(err, ErrNoAdminKey) {
		t.Fatalf("List error = %v, want ErrNoAdminKey", err)
	}
}

func TestDeleteRejectsSomethingThatIsNotATunnelID(t *testing.T) {
	d := NewDirectory("sk-admin-test", "")

	err := d.Delete(context.Background(), "../../v1/tunnels")
	if err == nil {
		t.Fatal("a malformed id must be refused before any request is made")
	}
	if strings.Contains(err.Error(), "sk-admin") {
		t.Fatal("the key must never appear in an error")
	}
}

func TestCreateNeedsAName(t *testing.T) {
	d := NewDirectory("sk-admin-test", "")
	if _, err := d.Create(context.Background(), "   ", ""); err == nil {
		t.Fatal("a nameless tunnel is not identifiable in anyone's account")
	}
}

// The two keys look alike and are made on adjacent pages, so "401" alone sends
// people to check the wrong one.
func TestRejectionNamesTheRightKind(t *testing.T) {
	d := NewDirectory("sk-proj-runtime-key", "")

	got := d.explain(errors.New("unexpected status 401: invalid_api_key")).Error()
	if !strings.Contains(got, "Admin keys") {
		t.Fatalf("explain = %q, want it to name admin keys", got)
	}
	if strings.Contains(got, "sk-proj-runtime-key") {
		t.Fatal("the key must never appear in an error")
	}

	forbidden := d.explain(errors.New("unexpected status 403: forbidden")).Error()
	if !strings.Contains(forbidden, "Tunnels: Manage") {
		t.Fatalf("explain = %q, want it to name the missing permission", forbidden)
	}
}

// Errors from the transport quote request details freely.
func TestAnUnrecognisedFailureStillHidesTheKey(t *testing.T) {
	const key = "sk-admin-verysecret"
	d := NewDirectory(key, "")

	got := d.explain(errors.New("dial failed for key " + key)).Error()
	if strings.Contains(got, key) {
		t.Fatalf("explain leaked the key: %q", got)
	}
}
