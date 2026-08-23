package app

import (
	"context"
	"testing"

	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/settings"
)

// Off unless somebody turned it on, and turning it on is a deliberate act in
// the dashboard.
//
// This is the one default that must survive an upgrade unchanged: a host that
// had no sign-ups before this shipped must not have them afterwards because a
// zero value said so. The app is built over a database the migrations have
// just walked from nothing to current, which is the same path an upgrading
// deployment takes.
func TestRegistrationPolicy_ClosedUntilSomebodyOpensIt(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	policy := a.registrationPolicy(ctx)
	if policy.Enabled {
		t.Fatal("registration is open on a host nobody opened it on")
	}
	if !policy.RequireApproval {
		t.Error("approval is off by default; an operator who opens registration " +
			"without thinking about approval should still be asked")
	}
	if len(policy.AllowedDomains) != 0 {
		t.Errorf("allowed domains = %v; want none", policy.AllowedDomains)
	}
	if err := policy.Allows("anyone@example.com"); err == nil {
		t.Error("the default policy accepted an address")
	}

	// And it follows the store, which is the other half: a form that writes
	// where nothing reads reports success and changes nothing.
	if err := a.settings.Apply(ctx, "test", []settings.Change{
		{Key: settings.KeyRegistrationEnabled, Value: "true"},
		{Key: settings.KeyRegistrationApproval, Value: "false"},
		{Key: settings.KeyRegistrationDomains, Value: `["corp.com"]`},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	policy = a.registrationPolicy(ctx)
	if !policy.Enabled || policy.RequireApproval {
		t.Errorf("policy = %+v; want open, with no approval step", policy)
	}
	if err := policy.Allows("someone@corp.com"); err != nil {
		t.Errorf("an allowed domain was refused: %v", err)
	}
	if err := policy.Allows("someone@elsewhere.example"); err == nil {
		t.Error("an address outside the allow-list was accepted")
	}
}

// A provider nobody configured is not offered, and neither is one that is
// switched on with half a form filled in. A button that leads to a refusal
// reads as this host being broken rather than as it not having been set up.
func TestSSOProviders_OfferedOnlyWhenTheyWouldWork(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if got := a.ssoProviders(ctx); len(got) != 0 {
		t.Fatalf("providers = %+v; want none on a host nobody configured", got)
	}

	// Switched on, and missing the secret. Still not offered.
	if err := a.settings.Apply(ctx, "test", []settings.Change{
		{Key: settings.KeyGoogleEnabled, Value: "true"},
		{Key: settings.KeyGoogleClientID, Value: `"an-id.apps.googleusercontent.com"`},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := a.ssoProviders(ctx)
	if len(got) != 1 {
		t.Fatalf("providers = %+v; want the one that is switched on", got)
	}
	if got[0].Provider != users.ProviderGoogle {
		t.Errorf("provider = %q; want google", got[0].Provider)
	}
	if got[0].Ready() {
		t.Error("a provider with no client secret reports itself ready")
	}
	if offered := a.sso.Available(ctx); len(offered) != 0 {
		t.Errorf("offered = %+v; want none until it would work", offered)
	}

	// Complete, and still not offered: this host has no address for a
	// provider to send anybody back to.
	if err := a.settings.Apply(ctx, "test", []settings.Change{
		{Key: settings.KeyGoogleSecret, Value: "a-client-secret", Secret: true},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := a.ssoProviders(ctx); !got[0].Ready() {
		t.Error("a complete provider does not report itself ready")
	}
	if offered := a.sso.Available(ctx); len(offered) != 0 {
		t.Errorf("offered = %+v; want none without a public URL to come back to",
			offered)
	}
}

// Entra is the one provider that needs a fourth thing, and it needs it badly
// enough that a tenant naming every directory is refused rather than accepted:
// the discovery document for one carries a templated issuer that no token's
// `iss` can equal, so accepting it would mean dropping the issuer check.
func TestSSOProviders_EntraNeedsOneDirectory(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if err := a.settings.Apply(ctx, "test", []settings.Change{
		{Key: settings.KeyEntraEnabled, Value: "true"},
		{Key: settings.KeyEntraClientID, Value: `"an-application-id"`},
		{Key: settings.KeyEntraSecret, Value: "a-client-secret", Secret: true},
		{Key: settings.KeyEntraTenant, Value: `"common"`},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := a.ssoProviders(ctx)
	if len(got) != 1 || got[0].Ready() {
		t.Fatalf("providers = %+v; `common` names no directory and is not usable", got)
	}

	if err := a.settings.Apply(ctx, "test", []settings.Change{
		{Key: settings.KeyEntraTenant, Value: `"72f988bf-86f1-41af-91ab-2d7cd011db47"`},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := a.ssoProviders(ctx); !got[0].Ready() {
		t.Error("a directory id was refused")
	}
}
