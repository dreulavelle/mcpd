package sso

import (
	"context"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/auth/users"
)

// GET /user carries an `email` that is the person's *public profile* address:
// frequently null, frequently not the one they sign in with, and never
// asserted to be verified. The address comes from /user/emails instead, and
// only from the entry that is both primary and verified -- because this host
// decides a registration against it, and an unverified address is one somebody
// added minutes ago and has not confirmed.
func TestPrimaryVerified(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []githubEmail
		want      string
		wantErr   bool
	}{
		{
			name: "the primary verified one, not the first",
			addresses: []githubEmail{
				{Email: "added-just-now@corp.com", Primary: false, Verified: false},
				{Email: "real@example.com", Primary: true, Verified: true},
			},
			want: "real@example.com",
		},
		{
			name: "primary but unverified is an address somebody typed",
			addresses: []githubEmail{
				{Email: "claimed@corp.com", Primary: true, Verified: false},
				{Email: "verified@example.com", Primary: false, Verified: true},
			},
			wantErr: true,
		},
		{
			name: "verified but not primary is not the identity either",
			addresses: []githubEmail{
				{Email: "verified@example.com", Primary: false, Verified: true},
			},
			wantErr: true,
		},
		{
			name:      "nothing at all",
			addresses: nil,
			wantErr:   true,
		},
		{
			name:      "normalised",
			addresses: []githubEmail{{Email: " Real@Example.COM ", Primary: true, Verified: true}},
			want:      "real@example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := primaryVerified(tc.addresses)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("primaryVerified = %q; want a refusal", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("primaryVerified: %v", err)
			}
			if got != tc.want {
				t.Errorf("primaryVerified = %q; want %q", got, tc.want)
			}
		})
	}
}

// The redirect URI is derived from what an operator wrote down about how this
// host is reached, and from nothing else. An empty base is an error rather
// than a guess: a URI assembled from a request works when it is tested from
// the same machine and fails for everybody else.
func TestRedirectURI(t *testing.T) {
	for _, tc := range []struct {
		name    string
		base    string
		want    string
		wantErr bool
	}{
		{
			name: "the ordinary case",
			base: "https://mcpd.example.com",
			want: "https://mcpd.example.com/api/auth/sso/google/callback",
		},
		{
			name: "a trailing slash changes nothing",
			base: "https://mcpd.example.com/",
			want: "https://mcpd.example.com/api/auth/sso/google/callback",
		},
		{
			name: "a path prefix is kept",
			base: "https://example.com/mcpd",
			want: "https://example.com/mcpd/api/auth/sso/google/callback",
		},
		{
			name: "a query on the base is dropped",
			base: "https://mcpd.example.com/?a=b",
			want: "https://mcpd.example.com/api/auth/sso/google/callback",
		},
		{
			name:    "unset",
			base:    "  ",
			wantErr: true,
		},
		{
			name:    "not absolute",
			base:    "mcpd.example.com",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RedirectURI(tc.base, users.ProviderGoogle)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("RedirectURI = %q; want a refusal", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RedirectURI: %v", err)
			}
			if got != tc.want {
				t.Errorf("RedirectURI = %q; want %q", got, tc.want)
			}
		})
	}
}

// A provider missing anything it needs is not offered. A button that leads to
// a refusal reads as this host being broken rather than as it not having been
// set up.
func TestConfigReady(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    Config
		want bool
	}{
		{
			name: "google, complete",
			c:    Config{Provider: users.ProviderGoogle, ClientID: "id", ClientSecret: "secret"},
			want: true,
		},
		{
			name: "google, no secret",
			c:    Config{Provider: users.ProviderGoogle, ClientID: "id"},
		},
		{
			name: "google, whitespace for a client id",
			c:    Config{Provider: users.ProviderGoogle, ClientID: "   ", ClientSecret: "secret"},
		},
		{
			name: "entra, no tenant",
			c:    Config{Provider: users.ProviderEntra, ClientID: "id", ClientSecret: "secret"},
		},
		{
			name: "entra, a tenant naming every directory",
			c: Config{
				Provider: users.ProviderEntra, ClientID: "id",
				ClientSecret: "secret", TenantID: "common",
			},
		},
		{
			name: "entra, one directory",
			c: Config{
				Provider: users.ProviderEntra, ClientID: "id",
				ClientSecret: "secret", TenantID: "72f988bf-86f1-41af-91ab-2d7cd011db47",
			},
			want: true,
		},
		{
			name: "a provider this build does not know",
			c:    Config{Provider: "okta", ClientID: "id", ClientSecret: "secret"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.Ready(); got != tc.want {
				t.Errorf("Ready() = %v; want %v", got, tc.want)
			}
		})
	}
}

// With no address to be redirected back to there is no flow to start, so
// nothing is offered and the Authentication page says why.
func TestAvailable_SaysNothingWithoutAnAddressToComeBackTo(t *testing.T) {
	ready := []Config{{Provider: users.ProviderGoogle, ClientID: "id", ClientSecret: "secret"}}

	s := NewService(Options{
		Providers:    func(context.Context) []Config { return ready },
		RedirectBase: func() string { return "" },
	})
	if got := s.Available(t.Context()); len(got) != 0 {
		t.Errorf("Available() = %v; want nothing without a redirect base", got)
	}

	s = NewService(Options{
		Providers:    func(context.Context) []Config { return ready },
		RedirectBase: func() string { return "https://mcpd.example.com" },
	})
	got := s.Available(t.Context())
	if len(got) != 1 || got[0].Provider != "google" || got[0].Label != "Google" {
		t.Errorf("Available() = %+v; want one Google entry", got)
	}
}

// A provider the operator runs is configured by its issuer, so the issuer is
// what decides whether there is anything to offer. The cases here are the
// mistakes an operator actually makes: pasting the discovery URL, leaving the
// scheme off, and pointing at plain http across a network.
func TestConfigReady_YourOwnProvider(t *testing.T) {
	base := Config{Provider: users.ProviderOIDC, ClientID: "id", ClientSecret: "secret"}
	with := func(issuer string) Config { c := base; c.IssuerURL = issuer; return c }

	for _, tc := range []struct {
		name string
		c    Config
		want bool
	}{
		{name: "no issuer", c: base},
		{name: "https", c: with("https://auth.example.com/application/o/mcpd"), want: true},
		{name: "trailing slash", c: with("https://auth.example.com/realms/mcpd/"), want: true},
		{name: "loopback over http", c: with("http://localhost:9000/realms/mcpd"), want: true},
		{name: "http across a network", c: with("http://auth.example.com/realms/mcpd")},
		{name: "the discovery address", c: with(
			"https://auth.example.com/realms/mcpd/.well-known/openid-configuration")},
		{name: "a query the appended path would strand", c: with(
			"https://auth.example.com/realms/mcpd?realm=x")},
		{name: "credentials in the address", c: with("https://u:p@auth.example.com/realms/mcpd")},
		{name: "no scheme", c: with("auth.example.com/realms/mcpd")},
		{name: "issuer but no secret", c: Config{
			Provider: users.ProviderOIDC, ClientID: "id",
			IssuerURL: "https://auth.example.com/realms/mcpd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.Ready(); got != tc.want {
				t.Errorf("Ready() = %v; want %v", got, tc.want)
			}
		})
	}
}

// "Continue with oidc" is a button nobody recognises, so the operator names
// their own provider and the page says what they said.
func TestLabelFor(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    Config
		want string
	}{
		{"a name the operator chose", Config{Provider: users.ProviderOIDC, Label: "Authentik"}, "Authentik"},
		{"whitespace is not a name", Config{Provider: users.ProviderOIDC, Label: "  "}, "Single sign-on"},
		{"no name at all", Config{Provider: users.ProviderOIDC}, "Single sign-on"},
		{"a provider this build knows", Config{Provider: users.ProviderGoogle, Label: "ignored"}, "Google"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LabelFor(tc.c); got != tc.want {
				t.Errorf("LabelFor() = %q; want %q", got, tc.want)
			}
		})
	}
}

// Discovery appends a path to the issuer and the issuer is also the cache key,
// so a trailing slash has to be gone before either happens -- otherwise one
// provider is two, with one configuration between them.
func TestIssuerBase_YourOwnProvider(t *testing.T) {
	got, err := issuerBase(Config{
		Provider:  users.ProviderOIDC,
		IssuerURL: "  https://auth.example.com/realms/mcpd/  ",
	})
	if err != nil {
		t.Fatalf("issuerBase: %v", err)
	}
	if want := "https://auth.example.com/realms/mcpd"; got != want {
		t.Errorf("issuerBase = %q; want %q", got, want)
	}

	if _, err := issuerBase(Config{Provider: users.ProviderOIDC}); err == nil {
		t.Error("an issuer nobody set should not resolve to an address")
	}
}

// The redirect address is registered by hand at the provider, so a rule broken
// here surfaces as a refusal in somebody else's words, long after the operator
// has left the dashboard believing they were finished.
func TestRedirectRefusal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider users.Provider
		redirect string
		refused  bool
	}{
		{"google over https", users.ProviderGoogle,
			"https://mcpd.example.net/api/auth/sso/google/callback", false},
		{"google on a LAN address", users.ProviderGoogle,
			"http://192.168.50.125:9090/api/auth/sso/google/callback", true},
		{"google on an https LAN address is still an address", users.ProviderGoogle,
			"https://192.168.50.125:9090/api/auth/sso/google/callback", true},
		{"google on localhost", users.ProviderGoogle,
			"http://localhost:9090/api/auth/sso/google/callback", false},
		{"google on the loopback address", users.ProviderGoogle,
			"http://127.0.0.1:9090/api/auth/sso/google/callback", false},

		{"entra over https", users.ProviderEntra,
			"https://mcpd.example.net/api/auth/sso/entra/callback", false},
		{"entra over plain http", users.ProviderEntra,
			"http://mcpd.example.net/api/auth/sso/entra/callback", true},
		// Microsoft has no objection to an address, only to the scheme.
		{"entra on an https LAN address", users.ProviderEntra,
			"https://192.168.50.125:9090/api/auth/sso/entra/callback", false},

		// GitHub takes plain http and raw addresses, so there is nothing to
		// warn about and a warning would be wrong.
		{"github on a LAN address", users.ProviderGitHub,
			"http://192.168.50.125:9090/api/auth/sso/github/callback", false},
		// A provider the operator runs is theirs; this host has no business
		// guessing at its policy.
		{"the operator's own provider", users.ProviderOIDC,
			"http://192.168.50.125:9090/api/auth/sso/oidc/callback", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			why := RedirectRefusal(tc.provider, tc.redirect)
			if tc.refused && why == "" {
				t.Error("an address the provider will refuse was passed without a word")
			}
			if !tc.refused && why != "" {
				t.Errorf("a usable address was flagged: %s", why)
			}
		})
	}
}

// An address on plain http breaks both of Google's rules, and which one is
// reported decides what somebody does next. Naming the scheme sends them to
// arrange a certificate for a host Google will refuse anyway -- a wasted
// afternoon that ends at the same error.
func TestRedirectRefusal_NamesTheBlockerThatNeedsTheBiggerChange(t *testing.T) {
	why := RedirectRefusal(users.ProviderGoogle,
		"http://192.168.50.125:9090/api/auth/sso/google/callback")

	if !strings.Contains(why, "host name") {
		t.Errorf("the address, which is the blocker a certificate does not fix, went unmentioned: %s", why)
	}
	// The scheme is still wrong and still has to be said, or somebody fixes
	// the name and comes back for the other half.
	if !strings.Contains(why, "https") {
		t.Errorf("https went unmentioned, so fixing the name would not be enough: %s", why)
	}
	// Recognisable as the same problem when they meet it at Google.
	if !strings.Contains(why, "device_id") {
		t.Errorf("the error Google actually answers with went unmentioned: %s", why)
	}

	// With a name, only the scheme is left, and saying more would be noise.
	onlyScheme := RedirectRefusal(users.ProviderGoogle,
		"http://mcpd.example.net/api/auth/sso/google/callback")
	if strings.Contains(onlyScheme, "host name") {
		t.Errorf("a perfectly good host name was reported as a problem: %s", onlyScheme)
	}
}
