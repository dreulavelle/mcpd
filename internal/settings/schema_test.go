package settings

import (
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/backup"
)

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

// This package's copies of two backup rules still agree with the package that
// enforces them.
//
// The passphrase minimum and the shape of a time of day used to be imported
// from internal/backup. That package carries the S3 and SSH clients, and
// importing it put both in the dependency graph of everything that reads a
// setting -- which is nearly all of mcpd. So they are restated here, and this
// test is what stops the two drifting: a value the form accepts and the archive
// writer refuses is a scheduled backup that fails every night at four.
//
// The import lives in this test file, so it does not reach a built binary
// through this package.
func TestTheBackupRulesRestatedHereStillAgree(t *testing.T) {
	if minBackupPassphrase != backup.MinPassphrase {
		t.Errorf("this package requires %d characters and internal/backup requires %d",
			minBackupPassphrase, backup.MinPassphrase)
	}

	// Both parsers over one table: what either accepts, the other must.
	for _, value := range []string{
		"04:00", "00:00", "23:59", "09:05",
		"4:00", "24:00", "04:60", "0400", "", "aa:bb", "04:00:00", " 04:00 ",
	} {
		_, _, err := backup.ParseClock(value)
		mine := clockPattern.MatchString(strings.TrimSpace(value))
		if mine != (err == nil) {
			t.Errorf("%q: this package says %v, internal/backup says %v",
				value, mine, err == nil)
		}
	}
}
