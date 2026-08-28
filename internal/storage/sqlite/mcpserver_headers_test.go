package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/spoked/mcpd/internal/mcpservers"
)

// TestHeaders_SurviveAReadBack defends the store half of the operator's answer
// to a document that declares no credential.
func TestHeaders_SurviveAReadBack(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	h := mcpservers.KeyValueInput{
		Name: "X-Syncro-API-Key",
		Input: mcpservers.Input{
			Description: "Admin > API Tokens.",
			IsSecret:    true,
			IsRequired:  true,
		},
	}
	if err := s.AddHeader(ctx, "admin@example.test", "weather", h); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Get carries them, because that is what builds a client.
	srv, ok, err := s.Get(ctx, "weather")
	if err != nil || !ok {
		t.Fatalf("get: %v (found %v)", err, ok)
	}
	if len(srv.ExtraHeaders) != 1 || srv.ExtraHeaders[0].Name != h.Name {
		t.Fatalf("ExtraHeaders = %+v", srv.ExtraHeaders)
	}
	if !srv.ExtraHeaders[0].Input.IsSecret {
		t.Error("a header stored as secret must read back secret")
	}

	// List carries them too: it is the boot path that mounts every server, and
	// one that dropped them would come up sending no credential.
	all, err := s.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 || len(all[0].ExtraHeaders) != 1 {
		t.Fatalf("List dropped the headers: %+v", all)
	}

	// The stored document is untouched -- an operator's re-export is theirs.
	if remote, err := srv.Parsed.Remote(); err != nil || len(remote.Headers) != 0 {
		t.Errorf("the published document was rewritten: %d headers, %v",
			len(remote.Headers), err)
	}
}

// TestAddHeader_RefusesADuplicate keeps one header name from deriving one
// settings key twice, where the second silently decides what the first asked.
func TestAddHeader_RefusesADuplicate(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)

	h := mcpservers.KeyValueInput{Name: "X-Key", Input: mcpservers.Input{IsSecret: true}}
	if err := s.AddHeader(ctx, "admin@example.test", "weather", h); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.AddHeader(ctx, "admin@example.test", "weather", h); !errors.Is(err, ErrHeaderExists) {
		t.Errorf("second add = %v, want ErrHeaderExists", err)
	}
}

// TestAddHeader_RefusesAnUnimportedServer stops a header being declared
// against a name nothing is mounted under.
func TestAddHeader_RefusesAnUnimportedServer(t *testing.T) {
	s, _ := newMCPStore(t)
	err := s.AddHeader(context.Background(), "admin@example.test", "nope",
		mcpservers.KeyValueInput{Name: "X-Key"})
	if !errors.Is(err, ErrNoSuchServer) {
		t.Errorf("add = %v, want ErrNoSuchServer", err)
	}
}

// TestRemoveHeader_ReportsOneThatWasNeverThere keeps a no-op from reading as a
// withdrawal in the audit trail.
func TestRemoveHeader_ReportsOneThatWasNeverThere(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)
	if err := s.RemoveHeader(ctx, "admin@example.test", "weather", "X-Absent"); !errors.Is(err, ErrNoSuchHeader) {
		t.Errorf("remove = %v, want ErrNoSuchHeader", err)
	}
}

// TestRemoveServer_TakesItsHeaders defends the cascade: a name reused later
// must not inherit the last server's credentials.
func TestRemoveServer_TakesItsHeaders(t *testing.T) {
	ctx := context.Background()
	s, _ := newMCPStore(t)
	importFixture(t, s)
	if err := s.AddHeader(ctx, "admin@example.test", "weather",
		mcpservers.KeyValueInput{Name: "X-Key", Input: mcpservers.Input{IsSecret: true}}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := s.Remove(ctx, "admin@example.test", "weather"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	importFixture(t, s)
	got, err := s.Headers(ctx, "weather")
	if err != nil {
		t.Fatalf("headers: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a reimported name inherited %d headers", len(got))
	}
}
