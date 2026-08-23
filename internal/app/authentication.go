package app

import (
	"context"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth/sso"
	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/settings"
)

// registrationPolicy reads what this host will accept from a stranger.
//
// Read per request rather than captured at startup, like every other policy
// here: an administrator who has just switched registration off should not
// have to restart to make that true, and the whole point of the setting living
// in the database is that the dashboard can change it.
//
// The fallbacks are the zero value of the feature. Off, and approving each one
// -- so a host upgrading into this migration accepts nothing, and a host whose
// operator turns registration on without thinking about approval still puts
// each new account in front of somebody.
func (a *App) registrationPolicy(ctx context.Context) users.RegistrationPolicy {
	if a.settings == nil {
		return users.RegistrationPolicy{}
	}
	return users.RegistrationPolicy{
		Enabled:         a.settings.Bool(ctx, settings.KeyRegistrationEnabled, false),
		RequireApproval: a.settings.Bool(ctx, settings.KeyRegistrationApproval, true),
		AllowedDomains:  a.settings.Strings(ctx, settings.KeyRegistrationDomains, nil),
	}
}

// ssoProviders reads the configured identity providers.
//
// A provider that is switched off contributes nothing, and one that is on but
// incomplete contributes a Config that reports itself not ready -- which is
// what keeps a half-filled form from putting a button on the sign-in page that
// leads to a refusal.
func (a *App) ssoProviders(ctx context.Context) []sso.Config {
	if a.settings == nil {
		return nil
	}
	var out []sso.Config
	if a.settings.Bool(ctx, settings.KeyGoogleEnabled, false) {
		out = append(out, sso.Config{
			Provider:     users.ProviderGoogle,
			ClientID:     a.settings.String(ctx, settings.KeyGoogleClientID, ""),
			ClientSecret: a.settings.Secret(ctx, settings.KeyGoogleSecret, ""),
		})
	}
	if a.settings.Bool(ctx, settings.KeyGitHubEnabled, false) {
		out = append(out, sso.Config{
			Provider:     users.ProviderGitHub,
			ClientID:     a.settings.String(ctx, settings.KeyGitHubClientID, ""),
			ClientSecret: a.settings.Secret(ctx, settings.KeyGitHubSecret, ""),
		})
	}
	if a.settings.Bool(ctx, settings.KeyEntraEnabled, false) {
		out = append(out, sso.Config{
			Provider:     users.ProviderEntra,
			ClientID:     a.settings.String(ctx, settings.KeyEntraClientID, ""),
			ClientSecret: a.settings.Secret(ctx, settings.KeyEntraSecret, ""),
			TenantID:     a.settings.String(ctx, settings.KeyEntraTenant, ""),
		})
	}
	return out
}

// buildSSO wires the provider flows.
//
// The redirect base is the dashboard's configured public URL and nothing else.
// Deriving it from a request would produce a URL that works when an operator
// tests it from the same machine and fails for everybody else, and the Host
// header it would have to read is set by whoever is talking to this process.
// An empty one is reported as an absence -- no buttons, and a line on the
// Authentication page saying why -- rather than being guessed at.
func (a *App) buildSSO() {
	a.ssoStates = sso.NewStateStore(a.db, time.Now)
	base := strings.TrimSpace(a.cfg.Server.FrontendPublicURL)
	a.sso = sso.NewService(sso.Options{
		Providers:    a.ssoProviders,
		RedirectBase: func() string { return base },
		States:       a.ssoStates,
		Log:          a.log.With("component", "sso"),
		Now:          time.Now,
	})
}

// purgeSSOStates removes half-finished flows past their expiry.
//
// Hygiene rather than correctness: expiry is a condition of the claim, so a row
// that outlives it stops being usable whether or not it is deleted. What this
// prevents is a table growing without bound on a host people sign in to often.
func (a *App) purgeSSOStates(ctx context.Context) error {
	if a.ssoStates == nil {
		return nil
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := a.ssoStates.Purge(ctx); err != nil {
				a.log.Warn("could not purge expired sign-in states", "error", err)
			}
		}
	}
}
