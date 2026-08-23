package sso

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spoked/mcpd/internal/auth/users"
)

// GitHub is plain OAuth 2.0, and the difference from the two OpenID providers
// is not cosmetic.
//
// There is no id token, so there is nothing signed to verify and no nonce to
// bind: what the exchange returns is an access token, and who it belongs to is
// a question answered by calling GitHub's API with it. There is no standard
// claim for a verified address either, and the obvious field is a trap --
// `GET /user` carries an `email` that is the person's *public profile* address,
// which is frequently null, frequently not the one they sign in with, and is
// not asserted to be verified. So the address comes from `GET /user/emails`,
// and only the entry that is both primary and verified.
//
// That last rule is worth stating rather than leaving to the code. Taking the
// first entry the API returns would take whichever address happens to sort
// first in GitHub's response -- including one somebody added minutes ago and
// has not confirmed -- and this host uses the address to decide whether a
// stranger may register under an allowed domain. An unverified address in that
// position is an invitation to add `someone@corp.com` to a GitHub account and
// walk in.

const (
	githubTokenEndpoint = "https://github.com/login/oauth/access_token"
	githubAPI           = "https://api.github.com"
	// githubAPIVersion pins the API's behaviour. GitHub dates its REST
	// versions and honours the header; without it this host follows whatever
	// they make current.
	githubAPIVersion = "2022-11-28"
)

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func (s *Service) completeGitHub(ctx context.Context, c Config, st *State, code string) (*Identity, error) {
	var tok tokenResponse
	err := postForm(ctx, s.http, githubTokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {st.RedirectURI},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
	}, &tok)
	if err != nil {
		return nil, err
	}
	// GitHub answers a refused exchange with 200 and an error body, so the
	// status code is not what decides.
	if err := tok.err("github.com"); err != nil {
		return nil, err
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("%w: github.com returned no access token", ErrProvider)
	}

	headers := map[string]string{
		"Authorization":        "Bearer " + tok.AccessToken,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": githubAPIVersion,
	}

	var who githubUser
	if err := getJSON(ctx, s.http, githubAPI+"/user", headers, &who); err != nil {
		return nil, err
	}
	if who.ID == 0 {
		return nil, fmt.Errorf("%w: github.com named no account", ErrProvider)
	}

	var addresses []githubEmail
	if err := getJSON(ctx, s.http, githubAPI+"/user/emails", headers, &addresses); err != nil {
		return nil, err
	}
	email, err := primaryVerified(addresses)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(who.Name)
	if name == "" {
		name = who.Login
	}
	return &Identity{
		Provider: users.ProviderGitHub,
		// The numeric id, never the login. A login can be changed and, once
		// released, taken by somebody else -- which would make the account
		// key a value another person can come to own.
		Subject: strconv.FormatInt(who.ID, 10),
		Email:   email,
		Name:    name,
	}, nil
}

// primaryVerified picks the one address GitHub vouches for.
//
// Both flags, and no fallback. "Primary but unverified" is an address somebody
// typed; "verified but not primary" is an address they own but did not choose
// to be reached at, and using it would sign somebody in under a different
// identity than the one they and this host would both expect.
func primaryVerified(addresses []githubEmail) (string, error) {
	for _, a := range addresses {
		if a.Primary && a.Verified {
			email, err := users.NormalizeEmail(a.Email)
			if err != nil {
				return "", ErrNoVerifiedEmail
			}
			return email, nil
		}
	}
	return "", ErrNoVerifiedEmail
}
