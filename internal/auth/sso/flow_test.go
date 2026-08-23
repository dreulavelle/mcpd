package sso

import (
	"context"
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
