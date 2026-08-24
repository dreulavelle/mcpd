package settings

import "testing"

// The address a client secret is sent to, and whose signatures are believed
// for an identity. Every refusal here is a mistake somebody makes with a
// self-hosted provider, and each one has to say which.
func TestValidateIssuerURL(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		ok    bool
	}{
		{"an https issuer", "https://auth.example.com/application/o/mcpd", true},
		{"a realm on https", "https://sso.corp.internal/realms/main", true},
		{"a provider on this machine", "http://localhost:9000/realms/main", true},
		{"loopback by address", "http://127.0.0.1:9000/realms/main", true},
		{"http across a network", "http://auth.example.com/realms/main", false},
		{"the discovery address", "https://auth.example.com/realms/main/.well-known/openid-configuration", false},
		{"a query the appended path would strand", "https://auth.example.com/realms/main?x=1", false},
		{"a fragment", "https://auth.example.com/realms/main#here", false},
		{"credentials in the address", "https://user:pass@auth.example.com/realms/main", false},
		{"no scheme", "auth.example.com/realms/main", false},
		{"not an address at all", "  ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateIssuerURL(tc.value)
			if tc.ok && err != nil {
				t.Errorf("ValidateIssuerURL(%q) = %v; want accepted", tc.value, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("ValidateIssuerURL(%q) was accepted; want refused", tc.value)
			}
		})
	}
}

// The form and the flow have to agree about what an issuer is, so the field
// is checked by the same rule rather than accepted here and refused later.
func TestValidate_RefusesAnIssuerTheFlowCouldNotUse(t *testing.T) {
	if err := Validate(KeyOIDCIssuer, "https://auth.example.com/realms/main"); err != nil {
		t.Fatalf("a good issuer was refused: %v", err)
	}
	if err := Validate(KeyOIDCIssuer,
		"https://auth.example.com/realms/main/.well-known/openid-configuration"); err == nil {
		t.Error("the discovery address was accepted as an issuer")
	}
}
