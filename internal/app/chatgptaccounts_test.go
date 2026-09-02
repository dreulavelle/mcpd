package app

import (
	"context"
	"testing"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/settings"
	"github.com/spoked/mcpd/internal/tunnel"
)

// The upgrade this exists for: the single set of OpenAI credentials stopped
// being read to run anything, so a deployment that had them and got no account
// would have every connector go offline with nothing said.
func TestTheOldCredentialsAreCarriedIntoAnAccount(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelAPIKey, Value: "sk-runtime", Secret: true},
		{Key: settings.KeyTunnelAdminKey, Value: "sk-admin", Secret: true},
		{Key: settings.KeyTunnelOrgID, Value: `"org_123"`},
		{Key: settings.KeyTunnelPlugins, Value: `["echo"]`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.seedChatGPTAccount(ctx); err != nil {
		t.Fatal(err)
	}

	accounts, err := a.chatgpt.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("got %d accounts, want the one carried across", len(accounts))
	}
	got := accounts[0]
	if got.APIKey != "sk-runtime" {
		t.Errorf("API key = %q, want the one that was already stored", got.APIKey)
	}
	if got.AdminKey != "sk-admin" || got.OrgID != "org_123" {
		t.Errorf("admin credentials = %q/%q, want the stored pair", got.AdminKey, got.OrgID)
	}
	if len(got.Plugins) != 1 || got.Plugins[0] != "echo" {
		t.Errorf("grant = %v, want the stored one; widening it on upgrade would "+
			"hand a connector systems nobody granted it", got.Plugins)
	}
	// The identity has to be the one the audit trail has been recording, or
	// every entry written before the upgrade refers to a principal that
	// nothing else mentions.
	if got.Principal != "svc:chatgpt" {
		t.Errorf("principal = %q, want the one the trail already holds", got.Principal)
	}
}

// The upgrade has to leave the tunnels it found pointed at the account it
// made, explicitly.
//
// They would run either way, because an unassigned tunnel resolves to the only
// account. They would also stop the day a second account was added, since with
// two an unassigned tunnel is ambiguous -- so somebody adding an account for a
// new workspace would take every connector they already had offline, having
// changed nothing about them.
func TestSeedingPinsTheTunnelsItFound(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	const id = "tunnel_6a87964313a88191b1cf9d9bf28dde48"
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelAPIKey, Value: "sk-runtime", Secret: true},
		{Key: settings.TunnelPluginKey(id), Value: `"*"`},
		{Key: settings.TunnelPluginKey("tunnel_1123456789abcdef0123456789abcdef"),
			Value: `"echo"`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.seedChatGPTAccount(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.pinTunnelsToTheOnlyAccount(ctx); err != nil {
		t.Fatal(err)
	}

	accounts, err := a.chatgpt.List(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts = %d, %v", len(accounts), err)
	}
	seeded := accounts[0].ID

	if got := a.settings.String(ctx, settings.TunnelAccountKey(id), ""); got != seeded {
		t.Errorf("the main tunnel names %q, want the seeded account", got)
	}
	if got := a.settings.String(ctx,
		settings.TunnelAccountKey("tunnel_1123456789abcdef0123456789abcdef"), ""); got != seeded {
		t.Errorf("the echo tunnel names %q, want the seeded account", got)
	}

	// And the consequence that matters: a second account does not disturb them.
	if _, err := a.chatgpt.Create(ctx, "user:test", tunnel.Account{
		Name: "Second", APIKey: "sk-other", Role: auth.RoleUser,
		Plugins: []string{auth.Wildcard}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := a.tunnelConfigs(ctx); len(got) != 2 {
		t.Fatalf("got %d tunnels after a second account was added, want both still running", len(got))
	}
}

// Seeding is the one turn the old keys get. Running it again would put a
// second copy of the same credentials beside the first, and an operator who
// had since edited the account would find the edit undone.
func TestTheOldCredentialsAreCarriedOnlyOnce(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelAPIKey, Value: "sk-runtime", Secret: true},
	}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := a.seedChatGPTAccount(ctx); err != nil {
			t.Fatal(err)
		}
	}

	accounts, err := a.chatgpt.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("got %d accounts after seeding three times, want one", len(accounts))
	}
}

// A deployment that never had the old keys gets no account invented for it.
// An account with no credential looks like a working connector and is not.
func TestNothingIsSeededWithoutTheOldCredentials(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	if err := a.seedChatGPTAccount(ctx); err != nil {
		t.Fatal(err)
	}
	accounts, err := a.chatgpt.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 0 {
		t.Fatalf("got %d accounts on a host that never had credentials", len(accounts))
	}
}

// Removing an account has to take its tunnel assignments with it. Left behind,
// they point at an account that no longer exists -- which reads on the Tunnels
// page exactly like a tunnel that is merely failing to connect.
func TestRemovingAnAccountUnassignsItsTunnels(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	acct := addAccount(t, a, "Work", nil)
	const id = "tunnel_6a87964313a88191b1cf9d9bf28dde48"
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.TunnelPluginKey(id), Value: `"*"`},
		{Key: settings.TunnelAccountKey(id), Value: `"` + acct.ID + `"`},
	}); err != nil {
		t.Fatal(err)
	}

	if err := a.RemoveChatGPTAccount(ctx, "user:test", acct.ID); err != nil {
		t.Fatal(err)
	}
	if got := a.settings.String(ctx, settings.TunnelAccountKey(id), ""); got != "" {
		t.Fatalf("the tunnel still names the removed account (%q)", got)
	}
}

// One limiter per account, shared across every tunnel it owns. A workspace
// given three connectors must not get three times the allowance by using all
// of them.
func TestAnAccountsRateIsSharedAcrossItsTunnels(t *testing.T) {
	l := newAccountLimiters()
	l.set([]tunnel.Account{{ID: "acct_a", RatePerSec: 1}})

	// Burst is one, so the first call takes the only turn available.
	if err := l.allow("acct_a"); err != nil {
		t.Fatalf("the first call was refused: %v", err)
	}
	if err := l.allow("acct_a"); err == nil {
		t.Fatal("a second immediate call was allowed; the allowance is per account")
	}
	// And another account is unaffected, or one workspace's traffic would
	// throttle everybody.
	if err := l.allow("acct_b"); err != nil {
		t.Fatalf("an unlimited account was refused: %v", err)
	}
}

// Zero is unlimited and is the default. The traffic runs inward, so the limit
// is a guard this host may want rather than a quota it owes anybody.
func TestAnAccountWithNoRateIsNotLimited(t *testing.T) {
	l := newAccountLimiters()
	l.set([]tunnel.Account{{ID: "acct_a", RatePerSec: 0}})

	for i := range 50 {
		if err := l.allow("acct_a"); err != nil {
			t.Fatalf("call %d was refused on an unlimited account: %v", i, err)
		}
	}
}

// Rebuilding a limiter resets its bucket, so an edit that did not change the
// rate must not build a new one -- otherwise saving the form is a way to hand
// yourself a fresh allowance.
func TestAnUnchangedRateKeepsItsBucket(t *testing.T) {
	l := newAccountLimiters()
	accounts := []tunnel.Account{{ID: "acct_a", Name: "Work", RatePerSec: 1}}
	l.set(accounts)

	if err := l.allow("acct_a"); err != nil {
		t.Fatalf("the first call was refused: %v", err)
	}
	// The same rate, a different name: the sort of edit a rename produces.
	accounts[0].Name = "Work (renamed)"
	l.set(accounts)

	if err := l.allow("acct_a"); err == nil {
		t.Fatal("an edit that did not change the rate reset the allowance")
	}
}

// A removed account's limiter goes with it, or the map grows for the life of
// the process and an id reused later inherits somebody else's bucket.
func TestARemovedAccountLosesItsLimiter(t *testing.T) {
	l := newAccountLimiters()
	l.set([]tunnel.Account{{ID: "acct_a", RatePerSec: 1}})
	if err := l.allow("acct_a"); err != nil {
		t.Fatal(err)
	}

	l.set(nil)
	if len(l.byID) != 0 {
		t.Fatalf("%d limiters survived the account being removed", len(l.byID))
	}
	if err := l.allow("acct_a"); err != nil {
		t.Fatalf("a removed account is still limited: %v", err)
	}
}

// The name seeds the identity, and an identity is what the audit trail
// records. Two accounts whose names differ only in punctuation must not
// collapse onto one principal.
func TestPrincipalsAreDerivedFromTheName(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"Work", "svc:chatgpt:work"},
		{"Work Space", "svc:chatgpt:work-space"},
		{"Work  Space", "svc:chatgpt:work-space"},
		{"work-space", "svc:chatgpt:work-space"},
		{"  Trimmed  ", "svc:chatgpt:trimmed"},
		// Nothing a slug can keep. The store's unique index is what stops two
		// of these existing at once.
		{"日本語", "svc:chatgpt"},
	} {
		if got := tunnel.PrincipalFor(tc.name); got != tc.want {
			t.Errorf("PrincipalFor(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// An account's grant is a bound, not a suggestion: intersecting it with the
// tunnel's own means assigning a tunnel to an account can only ever reduce
// what that tunnel reaches.
func TestAnAccountGrantNarrowsButNeverWidens(t *testing.T) {
	a := newSettingsApp(t)
	ctx := context.Background()

	// Granted everything, but the tunnel is for echo alone.
	addAccount(t, a, "Wide", []string{auth.Wildcard})
	if err := a.settings.Apply(ctx, "user:test", []settings.Change{
		{Key: settings.KeyTunnelEnabled, Value: "true"},
		{Key: settings.TunnelPluginKey("tunnel_1123456789abcdef0123456789abcdef"),
			Value: `"echo"`},
	}); err != nil {
		t.Fatal(err)
	}

	configs := a.tunnelConfigs(ctx)
	if len(configs) != 1 {
		t.Fatalf("got %d tunnels, want the one for echo", len(configs))
	}
	got := configs[0].Principal.Plugins
	if len(got) != 1 || got[0] != "echo" {
		t.Fatalf("grant = %v, want echo alone despite the account's wildcard", got)
	}
}
