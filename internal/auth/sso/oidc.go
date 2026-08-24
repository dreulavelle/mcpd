package sso

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/cachestore"
)

// discovery is the part of an OpenID provider's metadata this host uses.
//
// Read rather than hardcoded, because the endpoints are the provider's to move
// and a build that pinned them would break on somebody else's schedule. The
// issuer is read for a different reason: it is what an ID token's `iss` is
// compared against, and taking it from the document the provider publishes at
// its own well-known address is what makes that comparison mean anything.
type discovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// discoveryTTL is how long a provider's metadata is reused. Endpoints move
// rarely; an hour keeps a busy host from re-fetching per sign-in and still
// notices a move within the working day.
const discoveryTTL = time.Hour

// jwksTTL is how long a signing key set is reused.
//
// Shorter than the metadata, because keys rotate and a stale set presents as
// every sign-in failing. An unknown `kid` also forces a refetch regardless,
// bounded so a token carrying a made-up key id cannot turn one sign-in into a
// stream of requests at the provider.
const jwksTTL = 15 * time.Minute

// jwksRefetchFloor bounds how often an unknown key id can send this host back
// to a provider.
//
// A token naming a key that does not exist is what a rotation looks like from
// here, and it is also what a made-up token looks like. Without a floor the
// second turns one refused sign-in into one request at the provider, at
// whatever rate somebody cares to send them -- from an endpoint that needs no
// credential. A minute is far below any real rotation interval and far above
// any useful amplification.
const jwksRefetchFloor = time.Minute

// issuerBase returns the address a provider publishes its metadata under.
func issuerBase(c Config) (string, error) {
	switch c.Provider {
	case users.ProviderGoogle:
		return "https://accounts.google.com", nil
	case users.ProviderEntra:
		if !validTenant(c.TenantID) {
			return "", fmt.Errorf(
				"%w: Entra needs a directory (tenant) id; `common`, `organizations` "+
					"and `consumers` name no directory and cannot be trusted for an issuer",
				ErrNotConfigured)
		}
		return "https://login.microsoftonline.com/" + url.PathEscape(c.TenantID) + "/v2.0", nil
	case users.ProviderOIDC:
		// Trailing slash removed rather than tolerated: discovery appends a
		// path to this and the result is also the cache key, so "…/realms/x"
		// and "…/realms/x/" would otherwise be two providers with one
		// configuration between them.
		if !validIssuer(c.IssuerURL) {
			return "", fmt.Errorf(
				"%w: the issuer must be an https address with no query or fragment, "+
					"like https://auth.example.com/application/o/mcpd",
				ErrNotConfigured)
		}
		return strings.TrimSuffix(strings.TrimSpace(c.IssuerURL), "/"), nil
	}
	return "", fmt.Errorf("%w: %s is not an OpenID provider", ErrNotConfigured, c.Provider)
}

// discover fetches and caches a provider's metadata.
func (s *Service) discover(ctx context.Context, c Config) (*discovery, error) {
	base, err := issuerBase(c)
	if err != nil {
		return nil, err
	}
	key := "discovery:" + base
	if e := s.cache.Get(key); e != nil && e.State(s.now()) == cachestore.Fresh {
		return e.Value.(*discovery), nil
	}

	value, _, err := s.group.Do(ctx, key, requestTimeout, func(ctx context.Context) (any, error) {
		var d discovery
		if err := getJSON(ctx, s.http, base+"/.well-known/openid-configuration", nil, &d); err != nil {
			return nil, err
		}
		if err := d.validate(base); err != nil {
			return nil, err
		}
		return &d, nil
	})
	if err != nil {
		return nil, err
	}
	d := value.(*discovery)
	s.cache.Put(key, &cachestore.Entry{Value: d, FetchedAt: s.now(), TTL: discoveryTTL})
	return d, nil
}

// validate refuses a metadata document this host cannot rely on.
//
// The issuer check is the load-bearing one. A document fetched from a
// well-known address under one issuer must declare that issuer, or the
// comparison an ID token's `iss` is later held to means nothing. Entra's
// multi-tenant endpoints publish a templated issuer -- literally containing
// "{tenantid}" -- which is why this host insists on a directory id: that
// template can never equal a token's issuer, so accepting it would mean
// dropping the check.
func (d *discovery) validate(base string) error {
	switch {
	case d.Issuer == "":
		return fmt.Errorf("%w: metadata at %s declares no issuer", ErrProvider, base)
	case strings.Contains(d.Issuer, "{"):
		return fmt.Errorf(
			"%w: metadata at %s declares a templated issuer (%q); configure one directory",
			ErrNotConfigured, base, d.Issuer)
	case strings.TrimRight(d.Issuer, "/") != strings.TrimRight(base, "/"):
		return fmt.Errorf("%w: metadata at %s declares issuer %q", ErrProvider, base, d.Issuer)
	case !encrypted(d.AuthorizationEndpoint, base) ||
		!encrypted(d.TokenEndpoint, base) ||
		!encrypted(d.JWKSURI, base):
		// A provider that offers a plain-http endpoint is either
		// misconfigured or being impersonated; either way the client secret
		// is not going down it.
		return fmt.Errorf("%w: metadata at %s names an endpoint that is not https", ErrProvider, base)
	}
	return nil
}

// encrypted reports whether an endpoint may be used, given the issuer that
// named it.
//
// https always. Plain http only when the issuer is itself on this machine,
// which is what somebody developing against a local Keycloak or Authentik has
// -- there is no network for a secret to cross, and the settings form accepts
// exactly the same addresses. The two rules have to agree: a configuration the
// form takes and the flow then refuses is worse than one refused outright,
// because the operator has no way to tell what is wrong.
//
// The permission comes from the issuer rather than from the endpoint alone,
// and that is the whole point. Were it read off the endpoint, a provider
// reached over https could name an http endpoint on the operator's own
// loopback and this host would post the client secret to whatever answered
// there. An issuer that is already loopback can direct traffic to loopback and
// has learned nothing by doing it.
func encrypted(raw, issuer string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return u.Scheme == "http" && isLoopback(u) && isLoopbackIssuer(issuer)
}

func isLoopbackIssuer(issuer string) bool {
	u, err := url.Parse(issuer)
	return err == nil && u.Scheme == "http" && isLoopback(u)
}

func isLoopback(u *url.URL) bool {
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// --- signing keys ----------------------------------------------------------

type jwks struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	Kty string   `json:"kty"`
	Kid string   `json:"kid"`
	Alg string   `json:"alg"`
	Use string   `json:"use"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	X5c []string `json:"x5c"`
}

// key returns the RSA public key this entry describes.
func (k jwk) key() (*rsa.PublicKey, error) {
	if k.Kty != "RSA" {
		return nil, fmt.Errorf("%w: signing key %q is %q, not RSA", ErrProvider, k.Kid, k.Kty)
	}
	if k.N != "" && k.E != "" {
		n, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, fmt.Errorf("%w: signing key %q has an unreadable modulus", ErrProvider, k.Kid)
		}
		e, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, fmt.Errorf("%w: signing key %q has an unreadable exponent", ErrProvider, k.Kid)
		}
		// Four bytes, because that is what the destination is: the exponent
		// goes into an int, and anything wider is either a key this host
		// cannot represent or a number chosen to see what happens when it is
		// truncated into one. Every real RSA exponent is 65537.
		if len(e) == 0 || len(e) > 4 {
			return nil, fmt.Errorf("%w: signing key %q has an unusable exponent", ErrProvider, k.Kid)
		}
		return &rsa.PublicKey{
			N: new(big.Int).SetBytes(n),
			E: int(new(big.Int).SetBytes(e).Int64()),
		}, nil
	}
	// Entra publishes x5c alongside n/e; the certificate is the fallback for a
	// key set that omits the raw parameters rather than a second code path
	// anybody chooses.
	if len(k.X5c) > 0 {
		der, err := base64.StdEncoding.DecodeString(k.X5c[0])
		if err != nil {
			return nil, fmt.Errorf("%w: signing key %q has an unreadable certificate", ErrProvider, k.Kid)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("%w: signing key %q has an unparseable certificate", ErrProvider, k.Kid)
		}
		pub, ok := cert.PublicKey.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%w: signing key %q is not RSA", ErrProvider, k.Kid)
		}
		return pub, nil
	}
	return nil, fmt.Errorf("%w: signing key %q carries no key material", ErrProvider, k.Kid)
}

// signingKey resolves a key id against a provider's key set.
//
// A miss refetches, because that is what key rotation looks like from here: a
// token signed with a key published after the set was cached. It is also what
// a token carrying an invented key id looks like, and the two are
// indistinguishable at this point -- so the refetch is floored at
// jwksRefetchFloor rather than being taken on every miss. A set fetched within
// the floor is used as it stands and the sign-in is refused, which is what
// keeps a stream of made-up tokens from becoming a stream of requests at the
// provider.
func (s *Service) signingKey(ctx context.Context, d *discovery, kid string) (*rsa.PublicKey, error) {
	set, err := s.keySet(ctx, d, false)
	if err != nil {
		return nil, err
	}
	if k, ok := find(set, kid); ok {
		return k.key()
	}
	if e := s.cache.Get("jwks:" + d.JWKSURI); e != nil &&
		s.now().Sub(e.FetchedAt) < jwksRefetchFloor {
		return nil, fmt.Errorf("%w: %s does not publish a signing key %q",
			ErrProvider, d.Issuer, kid)
	}
	// Worth a line: a key set that has fallen behind presents as every sign-in
	// failing, and this is the only place that says why.
	s.log.Info("refetching an identity provider's signing keys",
		"issuer", d.Issuer, "unknown_kid", kid)
	if set, err = s.keySet(ctx, d, true); err != nil {
		return nil, err
	}
	if k, ok := find(set, kid); ok {
		return k.key()
	}
	return nil, fmt.Errorf("%w: %s does not publish a signing key %q", ErrProvider, d.Issuer, kid)
}

func find(set *jwks, kid string) (jwk, bool) {
	// `use` says what a key is published for, and a set may carry encryption
	// keys beside the signing ones. Verifying a signature against a key its
	// owner said was not for signing is using it for something they did not
	// publish it for; an entry that says nothing is treated as a signing key,
	// which is what the field's absence has always meant.
	usable := make([]jwk, 0, len(set.Keys))
	for _, k := range set.Keys {
		if k.Use == "" || k.Use == "sig" {
			usable = append(usable, k)
		}
	}
	for _, k := range usable {
		if k.Kid == kid {
			return k, true
		}
	}
	// A provider publishing exactly one key need not name it, and a token from
	// such a provider carries no kid to match. One key and no kid is not
	// ambiguous; anything else is, and falls through to a refusal rather than
	// to a guess.
	if kid == "" && len(usable) == 1 {
		return usable[0], true
	}
	return jwk{}, false
}

func (s *Service) keySet(ctx context.Context, d *discovery, refresh bool) (*jwks, error) {
	key := "jwks:" + d.JWKSURI
	if e := s.cache.Get(key); e != nil && e.State(s.now()) == cachestore.Fresh && !refresh {
		return e.Value.(*jwks), nil
	}
	value, _, err := s.group.Do(ctx, key, requestTimeout, func(ctx context.Context) (any, error) {
		var set jwks
		if err := getJSON(ctx, s.http, d.JWKSURI, nil, &set); err != nil {
			return nil, err
		}
		if len(set.Keys) == 0 {
			return nil, fmt.Errorf("%w: %s publishes no signing keys", ErrProvider, d.Issuer)
		}
		return &set, nil
	})
	if err != nil {
		return nil, err
	}
	set := value.(*jwks)
	s.cache.Put(key, &cachestore.Entry{Value: set, FetchedAt: s.now(), TTL: jwksTTL})
	return set, nil
}

// --- id tokens -------------------------------------------------------------

// idClaims is what an ID token has to say for this host to act on it.
type idClaims struct {
	Issuer    string          `json:"iss"`
	Subject   string          `json:"sub"`
	Audience  audienceClaim   `json:"aud"`
	Expiry    int64           `json:"exp"`
	IssuedAt  int64           `json:"iat"`
	NotBefore int64           `json:"nbf"`
	Nonce     string          `json:"nonce"`
	Email     string          `json:"email"`
	Verified  verifiableClaim `json:"email_verified"`
	Name      string          `json:"name"`
	// PreferredUsername is Entra's usual home for a work address. It is read
	// only as a fallback, and only when it is an address.
	PreferredUsername string `json:"preferred_username"`
}

// audienceClaim reads `aud`, which the specification allows to be a string or
// an array of strings.
type audienceClaim []string

func (a *audienceClaim) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audienceClaim{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("aud is neither a string nor an array of strings")
	}
	*a = many
	return nil
}

func (a audienceClaim) contains(want string) bool {
	for _, v := range a {
		if v == want {
			return true
		}
	}
	return false
}

// verifiableClaim reads `email_verified`, which providers spell as a boolean
// and, in at least one case historically, as the string "true".
type verifiableClaim bool

func (v *verifiableClaim) UnmarshalJSON(b []byte) error {
	var asBool bool
	if err := json.Unmarshal(b, &asBool); err == nil {
		*v = verifiableClaim(asBool)
		return nil
	}
	var asString string
	if err := json.Unmarshal(b, &asString); err == nil {
		*v = verifiableClaim(asString == "true")
		return nil
	}
	return fmt.Errorf("email_verified is neither a boolean nor a string")
}

// clockSkew is what this host tolerates between its clock and a provider's.
const clockSkew = 2 * time.Minute

// verifyIDToken checks an ID token and returns its claims.
//
// The signature is verified against the provider's published keys even though
// the token arrived over TLS from the token endpoint, where the specification
// permits skipping it. The permission is real and the reasoning behind it is
// sound, and it is still not what this host does: the check costs one cached
// key set, it is the difference between trusting the transport and trusting
// the issuer, and a flow that has never verified a signature is one nobody can
// safely move to a front-channel response later.
//
// Five things are checked, and each has a way of being wrong that matters:
//
//   - The algorithm is RS256. Reading it from the header and using it to pick
//     a verifier is how "alg: none" and HMAC-with-the-public-key work; the
//     algorithm is decided here and the header is only compared against it.
//   - The signature verifies against a key the issuer publishes.
//   - The issuer equals the one in the metadata this host fetched.
//   - The audience contains this host's client id, so a token minted for a
//     different application at the same provider is not accepted here.
//   - The nonce equals the one bound to this flow, which is what ties the
//     token to the request that asked for it.
func (s *Service) verifyIDToken(ctx context.Context, c Config, d *discovery, raw, nonce string) (*idClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: the id token is not a JWT", ErrProvider)
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: the id token header is not base64url", ErrProvider)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("%w: the id token header is not JSON", ErrProvider)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("%w: the id token is signed with %q; this host verifies RS256",
			ErrProvider, header.Alg)
	}

	key, err := s.signingKey(ctx, d, header.Kid)
	if err != nil {
		return nil, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: the id token signature is not base64url", ErrProvider)
	}
	signed := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, signed[:], signature); err != nil {
		return nil, fmt.Errorf("%w: the id token signature does not verify", ErrProvider)
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: the id token payload is not base64url", ErrProvider)
	}
	var claims idClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("%w: the id token payload is not JSON: %v", ErrProvider, err)
	}

	now := s.now()
	switch {
	case strings.TrimRight(claims.Issuer, "/") != strings.TrimRight(d.Issuer, "/"):
		return nil, fmt.Errorf("%w: the id token was issued by %q, not %q",
			ErrProvider, claims.Issuer, d.Issuer)
	case !claims.Audience.contains(c.ClientID):
		return nil, fmt.Errorf("%w: the id token was not issued for this application", ErrProvider)
	case claims.Subject == "":
		return nil, fmt.Errorf("%w: the id token names no subject", ErrProvider)
	case claims.Expiry == 0 || now.After(time.Unix(claims.Expiry, 0).Add(clockSkew)):
		return nil, fmt.Errorf("%w: the id token has expired", ErrProvider)
	case claims.NotBefore != 0 && now.Add(clockSkew).Before(time.Unix(claims.NotBefore, 0)):
		return nil, fmt.Errorf("%w: the id token is not valid yet", ErrProvider)
	case claims.Nonce != nonce:
		// The nonce ties this token to the authorization request this host
		// made. A token that verifies but carries somebody else's nonce is a
		// replayed token, which is the case a signature check alone permits.
		return nil, fmt.Errorf("%w: the id token does not carry this request's nonce", ErrProvider)
	}
	return &claims, nil
}
