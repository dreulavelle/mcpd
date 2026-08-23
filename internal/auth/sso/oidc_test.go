package sso

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/spoked/mcpd/internal/auth/users"
	"github.com/spoked/mcpd/internal/cachestore"
)

// The verification tests run against a cache primed with a discovery document
// and a key set, and reach no network at all. What is under test is the
// checking, and a fake provider over HTTP would only add a transport between
// the claim and the assertion.

const (
	testIssuer   = "https://accounts.google.com"
	testClientID = "mcpd.apps.googleusercontent.com"
	testKid      = "test-key"
)

func newVerifier(t *testing.T) (*Service, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s := NewService(Options{
		Now:   func() time.Time { return testClock },
		Cache: cachestore.New(8),
	})
	d := &discovery{
		Issuer:                testIssuer,
		AuthorizationEndpoint: testIssuer + "/o/oauth2/v2/auth",
		TokenEndpoint:         "https://oauth2.googleapis.com/token",
		JWKSURI:               "https://www.googleapis.com/oauth2/v3/certs",
	}
	s.cache.Put("discovery:"+testIssuer, &cachestore.Entry{
		Value: d, FetchedAt: testClock, TTL: discoveryTTL,
	})
	s.cache.Put("jwks:"+d.JWKSURI, &cachestore.Entry{
		Value: &jwks{Keys: []jwk{{
			Kty: "RSA",
			Kid: testKid,
			Alg: "RS256",
			N:   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}},
		FetchedAt: testClock, TTL: jwksTTL,
	})
	return s, key
}

func testDiscovery(t *testing.T, s *Service) *discovery {
	t.Helper()
	e := s.cache.Get("discovery:" + testIssuer)
	if e == nil {
		t.Fatal("the primed discovery document is gone")
	}
	return e.Value.(*discovery)
}

// mintToken builds a signed JWT. header lets a test change what the token
// claims about itself without changing how it is signed, which is how the
// algorithm-confusion cases are expressed.
func mintToken(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := encode(header) + "." + encode(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func goodHeader() map[string]any {
	return map[string]any{"alg": "RS256", "kid": testKid, "typ": "JWT"}
}

func goodClaims() map[string]any {
	return map[string]any{
		"iss":            testIssuer,
		"sub":            "google-subject-1",
		"aud":            testClientID,
		"exp":            testClock.Add(time.Hour).Unix(),
		"iat":            testClock.Unix(),
		"nonce":          "the-nonce",
		"email":          "alice@example.com",
		"email_verified": true,
		"name":           "Alice",
	}
}

func TestVerifyIDToken_AcceptsAWellFormedToken(t *testing.T) {
	s, key := newVerifier(t)
	c := Config{Provider: users.ProviderGoogle, ClientID: testClientID, ClientSecret: "s"}

	claims, err := s.verifyIDToken(context.Background(), c, testDiscovery(t, s),
		mintToken(t, key, goodHeader(), goodClaims()), "the-nonce")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "google-subject-1" {
		t.Errorf("subject = %q", claims.Subject)
	}
	email, err := addressFrom(c, claims)
	if err != nil {
		t.Fatalf("address: %v", err)
	}
	if email != "alice@example.com" {
		t.Errorf("email = %q", email)
	}
}

// Each of these is a way a token can be wrong that matters, and each is
// refused. The algorithm cases are first because reading `alg` and using it to
// pick a verifier is the classic JWT hole: `none` accepts anything, and HS256
// against a public key accepts anything an attacker who has read the key set
// cares to sign.
func TestVerifyIDToken_Refusals(t *testing.T) {
	c := Config{Provider: users.ProviderGoogle, ClientID: testClientID, ClientSecret: "s"}

	for _, tc := range []struct {
		name   string
		header func(map[string]any)
		claims func(map[string]any)
		tamper func(string) string
		nonce  string
		want   string
	}{
		{
			name:   "alg none",
			header: func(h map[string]any) { h["alg"] = "none" },
			want:   "verifies RS256",
		},
		{
			name:   "alg HS256",
			header: func(h map[string]any) { h["alg"] = "HS256" },
			want:   "verifies RS256",
		},
		{
			name:   "an unknown signing key",
			header: func(h map[string]any) { h["kid"] = "a-key-nobody-published" },
			want:   "does not publish a signing key",
		},
		{
			name:   "a tampered payload",
			tamper: func(tok string) string { return swapSubject(tok) },
			want:   "signature does not verify",
		},
		{
			name:   "a different issuer",
			claims: func(cl map[string]any) { cl["iss"] = "https://accounts.evil.example" },
			want:   "was issued by",
		},
		{
			name:   "a token for another application",
			claims: func(cl map[string]any) { cl["aud"] = "somebody-elses-client-id" },
			want:   "not issued for this application",
		},
		{
			name:   "an expired token",
			claims: func(cl map[string]any) { cl["exp"] = testClock.Add(-time.Hour).Unix() },
			want:   "has expired",
		},
		{
			name:   "a token with no expiry",
			claims: func(cl map[string]any) { delete(cl, "exp") },
			want:   "has expired",
		},
		{
			name:   "a token not valid yet",
			claims: func(cl map[string]any) { cl["nbf"] = testClock.Add(time.Hour).Unix() },
			want:   "not valid yet",
		},
		{
			name:   "somebody else's nonce",
			claims: func(cl map[string]any) { cl["nonce"] = "a-nonce-from-another-request" },
			want:   "does not carry this request's nonce",
		},
		{
			name:   "no nonce at all",
			claims: func(cl map[string]any) { delete(cl, "nonce") },
			want:   "does not carry this request's nonce",
		},
		{
			name:   "no subject",
			claims: func(cl map[string]any) { delete(cl, "sub") },
			want:   "names no subject",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, key := newVerifier(t)
			header, claims := goodHeader(), goodClaims()
			if tc.header != nil {
				tc.header(header)
			}
			if tc.claims != nil {
				tc.claims(claims)
			}
			token := mintToken(t, key, header, claims)
			if tc.tamper != nil {
				token = tc.tamper(token)
			}
			nonce := tc.nonce
			if nonce == "" {
				nonce = "the-nonce"
			}
			_, err := s.verifyIDToken(context.Background(), c, testDiscovery(t, s), token, nonce)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}

// swapSubject rewrites the payload of a signed token without re-signing it,
// which is what an attacker editing a token they intercepted would produce.
func swapSubject(token string) string {
	parts := strings.Split(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return token
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return token
	}
	claims["sub"] = "somebody-else"
	edited, err := json.Marshal(claims)
	if err != nil {
		return token
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(edited)
	return strings.Join(parts, ".")
}

// Google says whether it has verified an address, and an unverified one is not
// something to decide a registration against: Google hands one out for an
// account that merely typed it in.
func TestAddressFrom_GoogleRequiresAVerifiedAddress(t *testing.T) {
	c := Config{Provider: users.ProviderGoogle, ClientID: testClientID}
	for _, tc := range []struct {
		name    string
		claims  idClaims
		want    string
		wantErr bool
	}{
		{
			name:   "verified",
			claims: idClaims{Email: "alice@example.com", Verified: true},
			want:   "alice@example.com",
		},
		{
			name:    "unverified",
			claims:  idClaims{Email: "alice@example.com"},
			wantErr: true,
		},
		{
			name:    "absent",
			claims:  idClaims{Verified: true},
			wantErr: true,
		},
		{
			name:    "not an address",
			claims:  idClaims{Email: "not-an-address", Verified: true},
			wantErr: true,
		},
		{
			name:   "normalised",
			claims: idClaims{Email: "  Alice@Example.COM ", Verified: true},
			want:   "alice@example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := addressFrom(c, &tc.claims)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("addressFrom = %q; want a refusal", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("addressFrom: %v", err)
			}
			if got != tc.want {
				t.Errorf("addressFrom = %q; want %q", got, tc.want)
			}
		})
	}
}

// Entra issues no email_verified, and commonly no email claim either. What
// stands in its place is the tenant: mcpd will not run Entra without a
// directory id, so the address in an accepted token was assigned by that
// directory rather than asserted by its holder.
func TestAddressFrom_EntraFallsBackToThePreferredUsername(t *testing.T) {
	c := Config{Provider: users.ProviderEntra, ClientID: testClientID, TenantID: "a-directory-id"}

	got, err := addressFrom(c, &idClaims{PreferredUsername: "alice@corp.com"})
	if err != nil {
		t.Fatalf("addressFrom: %v", err)
	}
	if got != "alice@corp.com" {
		t.Errorf("addressFrom = %q; want alice@corp.com", got)
	}
	// The email claim wins where it is present.
	got, err = addressFrom(c, &idClaims{Email: "alice@corp.com", PreferredUsername: "alice"})
	if err != nil || got != "alice@corp.com" {
		t.Errorf("addressFrom = %q, %v; want the email claim", got, err)
	}
	// preferred_username is not required to be an address, and is unusable
	// here when it is not one.
	if _, err := addressFrom(c, &idClaims{PreferredUsername: "alice"}); err == nil {
		t.Error("a preferred_username that is not an address was accepted as one")
	}
}

// A tenant that names every directory cannot be trusted for an issuer, and
// Entra's discovery document for one carries a templated issuer that no
// token's `iss` can equal. Refusing at configuration time is what keeps the
// issuer check from having to be dropped.
func TestValidTenant(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", false},
		{"common", false},
		{"COMMON", false},
		{"organizations", false},
		{"consumers", false},
		{"72f988bf-86f1-41af-91ab-2d7cd011db47", true},
		{"corp.onmicrosoft.com", true},
		{"a/b", false},
		{"a b", false},
	} {
		if got := validTenant(tc.in); got != tc.want {
			t.Errorf("validTenant(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

func TestDiscoveryValidate(t *testing.T) {
	base := "https://login.microsoftonline.com/tenant/v2.0"
	good := discovery{
		Issuer:                base,
		AuthorizationEndpoint: "https://login.microsoftonline.com/tenant/oauth2/v2.0/authorize",
		TokenEndpoint:         "https://login.microsoftonline.com/tenant/oauth2/v2.0/token",
		JWKSURI:               "https://login.microsoftonline.com/tenant/discovery/v2.0/keys",
	}
	if err := good.validate(base); err != nil {
		t.Fatalf("a well-formed document was refused: %v", err)
	}

	templated := good
	templated.Issuer = "https://login.microsoftonline.com/{tenantid}/v2.0"
	if err := templated.validate(base); err == nil {
		t.Error("a templated issuer was accepted")
	}

	mismatched := good
	mismatched.Issuer = "https://accounts.evil.example"
	if err := mismatched.validate(base); err == nil {
		t.Error("a document declaring somebody else's issuer was accepted")
	}

	plain := good
	plain.TokenEndpoint = "http://login.microsoftonline.com/tenant/oauth2/v2.0/token"
	if err := plain.validate(base); err == nil {
		t.Error("a plain-http token endpoint was accepted; the client secret goes down it")
	}
}
