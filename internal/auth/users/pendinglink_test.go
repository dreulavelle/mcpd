package users

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// offer makes an account with a password and an offer to link Google to it,
// which is the state every test below starts from.
func offer(t *testing.T, s *Store, binding string) (*User, string) {
	t.Helper()
	ctx := context.Background()
	claim(t, s)
	u := mustCreate(t, s, "alice@example.com", "role_operator")
	token, err := s.OfferLink(ctx, PendingLink{
		Provider: ProviderGoogle,
		Subject:  "google-subject-1",
		Email:    u.Email,
		Name:     "Alice",
		UserID:   u.ID,
	}, binding)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	return u, token
}

// The offer is bound to the browser the flow began in, exactly as a state is.
// An offer nobody can bind to a browser is one anybody can hand to anybody,
// and the thing being handed over here is a screen that asks for a password.
func TestPendingLink_OnlyUsableWithTheBrowserItWasIssuedTo(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, token := offer(t, s, "the-right-binding")

	for _, tc := range []struct {
		name    string
		token   string
		binding string
	}{
		{"another browser's binding", token, "somebody-elses-binding"},
		{"no binding at all", token, ""},
		{"a token nobody was issued", "a-token-nobody-holds", "the-right-binding"},
		{"no token at all", "", "the-right-binding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.PendingLinkFor(ctx, tc.token, tc.binding); !errors.Is(err, ErrNotFound) {
				t.Errorf("PendingLinkFor = %v; want ErrNotFound", err)
			}
			_, err := s.ClaimPendingLink(ctx, tc.token, tc.binding, "a-sufficiently-long-passphrase")
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("ClaimPendingLink = %v; want ErrNotFound", err)
			}
		})
	}

	// The control: the right pair works, so the refusals above are the
	// binding and not the offer never having been written.
	if _, err := s.PendingLinkFor(ctx, token, "the-right-binding"); err != nil {
		t.Fatalf("the offer this browser holds was refused: %v", err)
	}
}

// Single use, enforced by the guard rather than by the caller remembering. A
// replayed request -- a double submit, a back button -- matches zero rows.
func TestPendingLink_ASecondClaimMatchesNothing(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, token := offer(t, s, "binding")

	if _, err := s.ClaimPendingLink(ctx, token, "binding", "a-sufficiently-long-passphrase"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	_, err := s.ClaimPendingLink(ctx, token, "binding", "a-sufficiently-long-passphrase")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("second claim = %v; want ErrNotFound", err)
	}
}

// The row names one account, so without a ceiling it is a password oracle
// with a ten-minute life. Two wrong tries leave it; the third retires it.
func TestPendingLink_ThreeWrongPasswordsRetireTheOffer(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, token := offer(t, s, "binding")

	for i := 1; i <= 2; i++ {
		if _, err := s.ClaimPendingLink(ctx, token, "binding", "not-the-password"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d = %v; want ErrInvalidCredentials", i, err)
		}
		if _, err := s.PendingLinkFor(ctx, token, "binding"); err != nil {
			t.Fatalf("the offer was retired after %d wrong passwords: %v", i, err)
		}
	}
	if _, err := s.ClaimPendingLink(ctx, token, "binding", "not-the-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("third attempt = %v; want ErrInvalidCredentials", err)
	}
	// And now there is nothing left, including for the right password.
	if _, err := s.PendingLinkFor(ctx, token, "binding"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the offer survived three wrong passwords: %v", err)
	}
	_, err := s.ClaimPendingLink(ctx, token, "binding", "a-sufficiently-long-passphrase")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("the right password still claimed a retired offer: %v", err)
	}
}

// Expiry is a condition of the claim, not a sweep the claim relies on having
// run.
func TestPendingLink_AnExpiredOfferIsRefused(t *testing.T) {
	s, setClock := newStore(t)
	ctx := context.Background()
	_, token := offer(t, s, "binding")

	setClock(testClock.Add(PendingLinkTTL + time.Second))
	if _, err := s.PendingLinkFor(ctx, token, "binding"); !errors.Is(err, ErrNotFound) {
		t.Errorf("PendingLinkFor = %v; want ErrNotFound for an expired offer", err)
	}
	_, err := s.ClaimPendingLink(ctx, token, "binding", "a-sufficiently-long-passphrase")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("ClaimPendingLink = %v; want ErrNotFound for an expired offer", err)
	}
}

// disabled is a decision an administrator made, and it has to reach every way
// in. An offer written before the account was switched off must not still
// work afterwards.
func TestPendingLink_ADisabledAccountCannotBeLinked(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	u, token := offer(t, s, "binding")

	off := true
	if _, err := s.Update(ctx, u.ID, UpdateRequest{Disabled: &off}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := s.PendingLinkFor(ctx, token, "binding"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a disabled account still had an offer: %v", err)
	}
	_, err := s.ClaimPendingLink(ctx, token, "binding", "a-sufficiently-long-passphrase")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("a disabled account was linked: %v", err)
	}
}

// The password required is the one the account holds now, not the one it held
// when the offer was made. An administrator resetting a password between the
// two is the case: the old one must stop working the moment it is replaced.
func TestPendingLink_ThePasswordRequiredIsTheCurrentOne(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	u, token := offer(t, s, "binding")

	if err := s.SetPassword(ctx, u.ID, "an-entirely-different-passphrase"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if _, err := s.ClaimPendingLink(ctx, token, "binding", "a-sufficiently-long-passphrase"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("the old password still worked: %v", err)
	}
	got, err := s.ClaimPendingLink(ctx, token, "binding", "an-entirely-different-passphrase")
	if err != nil {
		t.Fatalf("the current password was refused: %v", err)
	}
	if got.ID != u.ID {
		t.Errorf("linked %q, want %q", got.ID, u.ID)
	}
}

// The trail says which proof was given. A link confirmed by a password and one
// made from a live session are the same fact reached two ways, and an audit
// that cannot tell them apart cannot answer how an identity came to be
// attached.
func TestPendingLink_TheTrailSaysThePasswordConfirmedIt(t *testing.T) {
	s, db := newStoreWithDB(t)
	ctx := context.Background()
	claim(t, s)
	u := mustCreate(t, s, "alice@example.com", "role_operator")
	token, err := s.OfferLink(ctx, PendingLink{
		Provider: ProviderGoogle, Subject: "google-subject-1",
		Email: u.Email, UserID: u.ID,
	}, "binding")
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	if _, err := s.ClaimPendingLink(ctx, token, "binding", "a-sufficiently-long-passphrase"); err != nil {
		t.Fatalf("claim: %v", err)
	}

	entry := auditEntry(t, db, "account.identity_linked")
	if entry.actor != "user:"+u.Email {
		t.Errorf("actor = %q; want the account that gave the password", entry.actor)
	}
	if !strings.Contains(entry.detail, `"confirmed":"password"`) {
		t.Errorf("detail = %s; want confirmed: password", entry.detail)
	}

	// And the identity is really there, which is what the entry claims.
	list, err := s.IdentitiesFor(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].Provider != ProviderGoogle {
		t.Fatalf("identities = %v, %v; want one Google identity", list, err)
	}
}

// Two claims arriving together must produce one identity. The delete is the
// guard, so the loser finds nothing rather than inserting a second row.
func TestPendingLink_TwoClaimsAtOnceProduceOneIdentity(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	u, token := offer(t, s, "binding")

	var wg sync.WaitGroup
	results := make([]error, 2)
	wg.Add(2)
	for i := range results {
		go func() {
			defer wg.Done()
			_, results[i] = s.ClaimPendingLink(ctx, token, "binding", "a-sufficiently-long-passphrase")
		}()
	}
	wg.Wait()

	won := 0
	for _, err := range results {
		if err == nil {
			won++
		} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrIdentityLinked) {
			t.Errorf("unexpected refusal: %v", err)
		}
	}
	if won != 1 {
		t.Errorf("%d of 2 concurrent claims succeeded; want exactly 1", won)
	}
	list, err := s.IdentitiesFor(ctx, u.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("identities = %v, %v; want exactly one", list, err)
	}
}

// "Not now" retires the row rather than merely navigating away from it. An
// offer left live is one the next person at that browser is holding.
func TestPendingLink_DiscardingRetiresIt(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	_, token := offer(t, s, "binding")

	// Somebody else's binding retires nothing, for the same reason it cannot
	// claim anything.
	if err := s.DiscardPendingLink(ctx, token, "somebody-elses-binding"); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := s.PendingLinkFor(ctx, token, "binding"); err != nil {
		t.Fatalf("another browser's discard retired the offer: %v", err)
	}

	if err := s.DiscardPendingLink(ctx, token, "binding"); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if _, err := s.PendingLinkFor(ctx, token, "binding"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the offer survived being put away: %v", err)
	}
}

// Deleting an account takes its offers with it. A row left behind names an
// account that no longer exists, and the foreign key would take it anyway --
// saying so keeps the behaviour true of a database restored without foreign
// keys on, which is why Delete already spells out the identities.
func TestPendingLink_DeletingTheAccountTakesTheOffer(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	u, token := offer(t, s, "binding")

	if err := s.Delete(ctx, u.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.PendingLinkFor(ctx, token, "binding"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an offer outlived its account: %v", err)
	}
}
