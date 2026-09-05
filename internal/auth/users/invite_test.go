package users

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// invite is what an administrator does on the Users page: an account with no
// password, waiting for a first sign-in through one named provider.
func invite(t *testing.T, s *Store, email string, provider Provider) *User {
	t.Helper()
	u, err := s.Create(context.Background(), CreateRequest{
		Email:          email,
		RoleID:         "role_operator",
		InviteProvider: provider,
		Actor:          "user:owner@example.com",
	})
	if err != nil {
		t.Fatalf("invite %s: %v", email, err)
	}
	return u
}

// The whole of the feature. An administrator writes the address down, the
// person signs in at the provider, and the account they were meant to have is
// the one they get -- with no password anybody had to invent and hand over.
func TestInvite_FirstVerifiedSignInClaimsTheAccountAndClearsTheMarker(t *testing.T) {
	s, db := newStoreWithDB(t)
	ctx := context.Background()
	claim(t, s)
	invited := invite(t, s, "alice@example.com", ProviderGoogle)

	if invited.HasPassword() {
		t.Error("an invited account was given a password")
	}
	if !invited.Invited() || invited.InviteProvider != ProviderGoogle {
		t.Fatalf("invite_provider = %q; want google", invited.InviteProvider)
	}

	got, err := s.ClaimInvite(ctx, Identity{
		Provider: ProviderGoogle, Subject: "google-subject-1", Email: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got.ID != invited.ID {
		t.Errorf("claimed %q, want the invited account %q", got.ID, invited.ID)
	}
	if got.Invited() {
		t.Error("the marker survived the claim, so the invitation is still open")
	}
	if got.InviteExpiresAt != nil {
		t.Error("the expiry survived the claim")
	}

	// It really is an identity now, which is the only thing that ever turns a
	// subject into an account.
	linked, err := s.IdentitiesFor(ctx, got.ID)
	if err != nil || len(linked) != 1 || linked[0].Subject != "google-subject-1" {
		t.Fatalf("identities = %v, %v; want the claimed subject", linked, err)
	}

	entry := auditEntry(t, db, "account.invitation_claimed")
	if entry.actor != "self:alice@example.com" {
		t.Errorf("actor = %q; nobody else performed this", entry.actor)
	}
	if !strings.Contains(entry.detail, `"provider":"google"`) {
		t.Errorf("detail = %s; want the provider it was claimed through", entry.detail)
	}
}

// The claim is one guarded statement, so a second callback finds the marker
// already cleared and refuses rather than attaching a second identity.
func TestInvite_ASecondCallbackFindsNothingToClaim(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	claim(t, s)
	invite(t, s, "alice@example.com", ProviderGoogle)

	first := Identity{Provider: ProviderGoogle, Subject: "google-subject-1", Email: "alice@example.com"}
	if _, err := s.ClaimInvite(ctx, first); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// A different subject at the same provider, which is what a second person
	// arriving at a reassigned address looks like.
	second := Identity{Provider: ProviderGoogle, Subject: "google-subject-2", Email: "alice@example.com"}
	if _, err := s.ClaimInvite(ctx, second); !errors.Is(err, ErrNotFound) {
		t.Errorf("second claim = %v; want ErrNotFound", err)
	}
}

// Two callbacks racing. Exactly one may win, and the loser must not leave a
// second identity behind. Run under -race.
func TestInvite_TwoCallbacksAtOnceClaimItOnce(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	claim(t, s)
	invited := invite(t, s, "alice@example.com", ProviderGoogle)

	var wg sync.WaitGroup
	results := make([]error, 2)
	wg.Add(2)
	for i := range results {
		go func() {
			defer wg.Done()
			_, results[i] = s.ClaimInvite(ctx, Identity{
				Provider: ProviderGoogle,
				Subject:  "google-subject-" + string(rune('1'+i)),
				Email:    "alice@example.com",
			})
		}()
	}
	wg.Wait()

	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrIdentityLinked):
		default:
			t.Errorf("unexpected refusal: %v", err)
		}
	}
	if won != 1 {
		t.Errorf("%d of 2 concurrent claims succeeded; want exactly 1", won)
	}
	linked, err := s.IdentitiesFor(ctx, invited.ID)
	if err != nil || len(linked) != 1 {
		t.Fatalf("identities = %v, %v; want exactly one", linked, err)
	}
}

// An invitation names one provider, and only that one may take it up. The
// administrator said "this person signs in with Google", and a Microsoft
// account presenting the same address is somebody making a claim nobody
// authorised.
func TestInvite_AProviderOtherThanTheOneInvitedIsRefused(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	claim(t, s)
	invited := invite(t, s, "alice@example.com", ProviderGoogle)

	_, err := s.ClaimInvite(ctx, Identity{
		Provider: ProviderEntra, Subject: "entra-subject-1", Email: "alice@example.com",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("a different provider claimed the invitation: %v", err)
	}
	after, err := s.ByID(ctx, invited.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Invited() {
		t.Error("the refusal cleared the marker anyway")
	}
}

// An address can be reassigned -- a mailbox at a company somebody left, a
// Workspace account recycled -- so an invitation that never lapses is an
// account handed to whoever holds the address next.
func TestInvite_AnExpiredInvitationIsRefusedAndSaysSo(t *testing.T) {
	s, setClock := newStore(t)
	ctx := context.Background()
	claim(t, s)
	invite(t, s, "alice@example.com", ProviderGoogle)

	setClock(testClock.Add(InviteTTL + time.Hour))
	_, err := s.ClaimInvite(ctx, Identity{
		Provider: ProviderGoogle, Subject: "google-subject-1", Email: "alice@example.com",
	})
	// Its own error, because it is the one refusal here an administrator can
	// do something about.
	if !errors.Is(err, ErrInviteExpired) {
		t.Errorf("claim = %v; want ErrInviteExpired", err)
	}
}

// An account that was never invited is not claimable, whatever the provider
// says the address is. This is the rule the whole feature is shaped around,
// and the invitation is an exception to who decides rather than to the rule.
func TestInvite_AnOrdinaryAccountIsNotClaimable(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	claim(t, s)
	u := mustCreate(t, s, "alice@example.com", "role_operator")

	_, err := s.ClaimInvite(ctx, Identity{
		Provider: ProviderGoogle, Subject: "google-subject-1", Email: u.Email,
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("a password account was claimed by a matching address: %v", err)
	}
	if _, err := s.ClaimInvite(ctx, Identity{
		Provider: ProviderGoogle, Subject: "google-subject-1", Email: "nobody@example.com",
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an address nobody holds was claimed: %v", err)
	}
}

// Giving an invited account a password answers "who holds this address" a
// different way, so leaving the invitation live would let whoever holds the
// address at the provider claim an account somebody is already using.
func TestInvite_SettingAPasswordClearsTheInvitation(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	claim(t, s)
	invited := invite(t, s, "alice@example.com", ProviderGoogle)

	if err := s.SetPassword(ctx, invited.ID, "a-sufficiently-long-passphrase"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	after, err := s.ByID(ctx, invited.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Invited() {
		t.Error("the invitation survived a password being set")
	}
	if !after.HasPassword() {
		t.Error("the account has no password after one was set")
	}
	if _, err := s.ClaimInvite(ctx, Identity{
		Provider: ProviderGoogle, Subject: "google-subject-1", Email: invited.Email,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("the invitation was still claimable: %v", err)
	}
}

// An invited account is one an administrator decided about, so it is active
// and it is not in the queue of people waiting for a decision. Landing it
// there would ask somebody to approve a person they already approved.
func TestInvite_DoesNotAppearInThePendingQueue(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	claim(t, s)
	invited := invite(t, s, "alice@example.com", ProviderGoogle)

	if invited.Pending() {
		t.Errorf("status = %q; an invited account was already decided about", invited.Status)
	}
	queue, err := s.PendingRegistrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range queue {
		if u.ID == invited.ID {
			t.Error("an invited account is waiting for an approval nobody has to give")
		}
	}
}

// An invitation is a credential or a password is, never both: an account
// holding both is one whose invitation can still be claimed by whoever holds
// the address after somebody was given a password for it.
func TestInvite_CannotBeCreatedWithAPasswordAsWell(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	claim(t, s)

	_, err := s.Create(ctx, CreateRequest{
		Email:          "alice@example.com",
		Password:       "a-sufficiently-long-passphrase",
		RoleID:         "role_operator",
		InviteProvider: ProviderGoogle,
	})
	if err == nil {
		t.Fatal("an account was created with both an invitation and a password")
	}
	if _, err := s.Create(ctx, CreateRequest{
		Email:          "bob@example.com",
		RoleID:         "role_operator",
		InviteProvider: Provider("nothing-this-build-knows"),
	}); err == nil {
		t.Error("an invitation named a provider this build does not know")
	}
}

// The policy is what a host will accept from a stranger, and somebody an
// administrator invited is not one. A host with registration off must still be
// able to invite people, or the setting stops meaning "no strangers" and
// starts meaning "nobody new".
func TestInvite_IsNotSubjectToClosedRegistrationOrTheDomainList(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	claim(t, s)
	invited := invite(t, s, "alice@elsewhere.example", ProviderGoogle)

	// The control: the same address, through the door that does check, on a
	// host that accepts nothing.
	if _, err := s.Register(ctx, RegisterRequest{
		Email: "someone@elsewhere.example",
		Identity: &Identity{
			Provider: ProviderGoogle, Subject: "google-subject-9",
			Email: "someone@elsewhere.example",
		},
		Policy: RegistrationPolicy{},
	}); !errors.Is(err, ErrRegistrationClosed) {
		t.Fatalf("Register = %v; the control did not refuse, so nothing below is proved", err)
	}

	got, err := s.ClaimInvite(ctx, Identity{
		Provider: ProviderGoogle, Subject: "google-subject-1",
		Email: "alice@elsewhere.example",
	})
	if err != nil {
		t.Fatalf("an invitation was refused by a policy about strangers: %v", err)
	}
	if got.ID != invited.ID || got.Status != StatusActive {
		t.Errorf("claimed account = %+v; want the invited one, active", got)
	}
}
