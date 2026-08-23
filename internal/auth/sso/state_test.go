package sso

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/storage/sqlite"
)

var testClock = time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)

func newStates(t *testing.T) (*StateStore, func(time.Time)) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, sqlite.Options{
		Path:              filepath.Join(t.TempDir(), "sso.db"),
		RelaxedDurability: true,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := sqlite.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	clock := testClock
	// A link flow's state carries a foreign key into users, so the tests that
	// exercise one need an account to link to. Made here rather than per test:
	// what those tests are about is the state, not how the account got there.
	if _, err := users.NewStore(db, func() time.Time { return clock }).
		CreateFirst(ctx, "owner@example.com", "a-sufficiently-long-passphrase", ""); err != nil {
		t.Fatalf("claim: %v", err)
	}
	return NewStateStore(db, func() time.Time { return clock }),
		func(at time.Time) { clock = at }
}

// linkedAccount is the id of the account newStates created.
func linkedAccount(t *testing.T, s *StateStore) string {
	t.Helper()
	u, err := users.NewStore(s.db, s.now).ByEmail(context.Background(), "owner@example.com")
	if err != nil {
		t.Fatalf("read the seeded account: %v", err)
	}
	return u.ID
}

func issueSignIn(t *testing.T, s *StateStore) (state, binding string) {
	t.Helper()
	state, binding, err := s.Issue(context.Background(), State{
		Provider:    users.ProviderGoogle,
		Purpose:     PurposeSignIn,
		Nonce:       "a-nonce",
		RedirectURI: "https://mcpd.example.com/api/auth/sso/google/callback",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return state, binding
}

// A callback carrying a state this host never issued is refused. There is no
// row, and there is nothing about a made-up value that could produce one.
func TestClaim_RefusesAStateThisHostNeverIssued(t *testing.T) {
	s, _ := newStates(t)
	_, binding := issueSignIn(t, s)

	for _, forged := range []string{"", "not-a-state", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if _, err := s.Claim(context.Background(), users.ProviderGoogle, forged, binding); !errors.Is(err, ErrState) {
			t.Errorf("claiming forged state %q = %v; want ErrState", forged, err)
		}
	}
}

// Single use. The guard is a condition in the WHERE clause, so the second
// attempt matches zero rows -- which is what makes a captured callback URL
// worth nothing after the browser that started the flow has used it.
func TestClaim_RefusesAReplayedState(t *testing.T) {
	ctx := context.Background()
	s, _ := newStates(t)
	state, binding := issueSignIn(t, s)

	if _, err := s.Claim(ctx, users.ProviderGoogle, state, binding); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := s.Claim(ctx, users.ProviderGoogle, state, binding); !errors.Is(err, ErrState) {
		t.Fatalf("replay = %v; want ErrState", err)
	}
}

// The state is bound to the browser that started the flow. Without that, a
// state obtained from a referer header, a shared screen or a proxy log could
// be completed in somebody else's browser -- signing them in as an account the
// attacker controls, which is login CSRF and is exactly as bad as it sounds.
func TestClaim_RefusesAStateFromADifferentBrowser(t *testing.T) {
	ctx := context.Background()
	s, _ := newStates(t)
	state, _ := issueSignIn(t, s)
	_, otherBinding := issueSignIn(t, s)

	if _, err := s.Claim(ctx, users.ProviderGoogle, state, otherBinding); !errors.Is(err, ErrState) {
		t.Fatalf("claiming with another browser's cookie = %v; want ErrState", err)
	}
	if _, err := s.Claim(ctx, users.ProviderGoogle, state, ""); !errors.Is(err, ErrState) {
		t.Fatalf("claiming with no cookie = %v; want ErrState", err)
	}
}

// A flow begun for one provider cannot be completed at another's callback. The
// two return different things and only the route knows which parsing applies.
func TestClaim_RefusesAStateAtTheWrongProvider(t *testing.T) {
	ctx := context.Background()
	s, _ := newStates(t)
	state, binding := issueSignIn(t, s)

	if _, err := s.Claim(ctx, users.ProviderGitHub, state, binding); !errors.Is(err, ErrState) {
		t.Fatalf("claiming at the wrong provider = %v; want ErrState", err)
	}
	// And the state survives, because the wrong-provider attempt matched
	// nothing rather than consuming it.
	if _, err := s.Claim(ctx, users.ProviderGoogle, state, binding); err != nil {
		t.Fatalf("the real callback was refused after a wrong-provider attempt: %v", err)
	}
}

func TestClaim_RefusesAnExpiredState(t *testing.T) {
	ctx := context.Background()
	s, advance := newStates(t)
	state, binding := issueSignIn(t, s)

	advance(testClock.Add(StateTTL + time.Second))
	if _, err := s.Claim(ctx, users.ProviderGoogle, state, binding); !errors.Is(err, ErrState) {
		t.Fatalf("claiming an expired state = %v; want ErrState", err)
	}
}

// What was bound to the flow comes back with it, so the exchange can send the
// same redirect_uri and the id token can be held to the same nonce.
func TestClaim_ReturnsWhatWasBoundToTheFlow(t *testing.T) {
	ctx := context.Background()
	s, _ := newStates(t)
	account := linkedAccount(t, s)
	state, binding, err := s.Issue(ctx, State{
		Provider:     users.ProviderGoogle,
		Purpose:      PurposeLink,
		UserID:       account,
		Nonce:        "a-nonce",
		CodeVerifier: "a-verifier",
		RedirectURI:  "https://mcpd.example.com/api/auth/sso/google/callback",
		ReturnTo:     "/profile",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := s.Claim(ctx, users.ProviderGoogle, state, binding)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got.Purpose != PurposeLink || got.UserID != account {
		t.Errorf("purpose/user = %q/%q; want link/%s", got.Purpose, got.UserID, account)
	}
	if got.Nonce != "a-nonce" || got.CodeVerifier != "a-verifier" {
		t.Errorf("nonce/verifier = %q/%q", got.Nonce, got.CodeVerifier)
	}
	if got.ReturnTo != "/profile" {
		t.Errorf("return_to = %q; want /profile", got.ReturnTo)
	}
}

// Where a finished flow may send the browser is bounded to a path on this
// dashboard. A redirect target taken from a request is an open redirector, and
// this one is written into a row a later request acts on with nobody looking.
func TestReturnTo(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "/"},
		{"/profile", "/profile"},
		{"https://attacker.example/", "/"},
		{"//attacker.example/", "/"},
		{"/\\attacker.example", "/"},
		{"/settings/authentication", "/settings/authentication"},
		{"/x\r\nSet-Cookie: a=b", "/"},
		{"profile", "/"},
	} {
		if got := returnTo(tc.in); got != tc.want {
			t.Errorf("returnTo(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestIssue_RefusesAFlowItCannotComplete(t *testing.T) {
	ctx := context.Background()
	s, _ := newStates(t)

	if _, _, err := s.Issue(ctx, State{Provider: "okta", Purpose: PurposeSignIn}); err == nil {
		t.Error("a provider this build does not know was accepted")
	}
	if _, _, err := s.Issue(ctx, State{Provider: users.ProviderGoogle, Purpose: PurposeLink}); err == nil {
		t.Error("a link flow with no account to link to was accepted")
	}
}

func TestPurge_RemovesExpiredStatesAndKeepsLiveOnes(t *testing.T) {
	ctx := context.Background()
	s, advance := newStates(t)
	stale, staleBinding := issueSignIn(t, s)

	advance(testClock.Add(StateTTL + time.Minute))
	live, liveBinding := issueSignIn(t, s)

	if err := s.Purge(ctx); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := s.Claim(ctx, users.ProviderGoogle, stale, staleBinding); !errors.Is(err, ErrState) {
		t.Errorf("an expired state survived the purge: %v", err)
	}
	if _, err := s.Claim(ctx, users.ProviderGoogle, live, liveBinding); err != nil {
		t.Errorf("the purge took a live state: %v", err)
	}
}
