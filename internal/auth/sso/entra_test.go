package sso

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/spoked/mcpd/internal/auth/users"
)

// A directory can hold a member whose address is at a domain nobody verified,
// so the tenant does not establish the domain on its own. `xms_edov` is Entra
// saying whether it does, and the three states are three different answers:
// absent is an app registration made before the optional claim existed and has
// to keep working, present and true is the directory vouching, and present and
// false is the directory saying it cannot.
func TestAddressFrom_EntraXmsEdovFalseIsTreatedAsUnverified(t *testing.T) {
	c := Config{Provider: users.ProviderEntra, ClientID: testClientID, TenantID: "a-directory-id"}
	yes, no := verifiableClaim(true), verifiableClaim(false)

	for _, tc := range []struct {
		name    string
		claims  idClaims
		want    string
		wantErr bool
	}{
		{
			name:   "absent leaves the tenant standing on its own",
			claims: idClaims{Email: "alice@corp.com"},
			want:   "alice@corp.com",
		},
		{
			name:   "present and true",
			claims: idClaims{Email: "alice@corp.com", EmailDomainOwnerVerified: &yes},
			want:   "alice@corp.com",
		},
		{
			name:    "present and false",
			claims:  idClaims{Email: "alice@corp.com", EmailDomainOwnerVerified: &no},
			wantErr: true,
		},
		{
			// The fallback gets no second chance at it: the claim is about the
			// address the token carries, whichever claim that address came out
			// of.
			name:    "present and false, with the address in preferred_username",
			claims:  idClaims{PreferredUsername: "alice@corp.com", EmailDomainOwnerVerified: &no},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := addressFrom(c, &tc.claims)
			if tc.wantErr {
				if !errors.Is(err, ErrNoVerifiedEmail) {
					t.Fatalf("addressFrom = %q, %v; want ErrNoVerifiedEmail", got, err)
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

	// Google is untouched: the claim is Entra's and nothing else reads it.
	google := Config{Provider: users.ProviderGoogle, ClientID: testClientID}
	got, err := addressFrom(google, &idClaims{
		Email: "alice@example.com", Verified: true, EmailDomainOwnerVerified: &no,
	})
	if err != nil || got != "alice@example.com" {
		t.Errorf("addressFrom = %q, %v; xms_edov must not decide anything for Google", got, err)
	}
}

// The claim arrives as JSON, and providers have historically spelled booleans
// as strings. What matters most is that absent stays absent: a plain bool
// would read as "the directory said no" for every token that does not carry
// the claim, which is every token from an app registration made before it
// existed.
func TestIDClaims_XmsEdovDistinguishesAbsentFromFalse(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name    string
		payload string
		want    *bool
	}{
		{"absent", `{"sub":"s"}`, nil},
		{"false", `{"sub":"s","xms_edov":false}`, &no},
		{"true", `{"sub":"s","xms_edov":true}`, &yes},
		{"the string false", `{"sub":"s","xms_edov":"false"}`, &no},
		{"the string true", `{"sub":"s","xms_edov":"true"}`, &yes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var claims idClaims
			if err := json.Unmarshal([]byte(tc.payload), &claims); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			switch {
			case tc.want == nil && claims.EmailDomainOwnerVerified != nil:
				t.Errorf("xms_edov = %v; want absent", *claims.EmailDomainOwnerVerified)
			case tc.want != nil && claims.EmailDomainOwnerVerified == nil:
				t.Error("xms_edov is absent; want a value")
			case tc.want != nil && bool(*claims.EmailDomainOwnerVerified) != *tc.want:
				t.Errorf("xms_edov = %v; want %v", *claims.EmailDomainOwnerVerified, *tc.want)
			}
		})
	}
}
