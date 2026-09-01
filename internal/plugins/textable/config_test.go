package textable

import (
	"strings"
	"testing"
)

// Pasting a *user* token here is the mistake most likely to be made, because
// this integration used to take one and because Textable issues both kinds from
// the same settings area. The two are not interchangeable: a user token
// authenticates as one account and the v2 endpoints read here do not accept it.
//
// It has to be caught on the shape, because the failure otherwise is a 401
// saying "Invalid API Credentials" -- which is also what a revoked service
// token says, with nothing distinguishing them.
func TestValidate_RefusesAUserTokenPastedInPlaceOfAServiceToken(t *testing.T) {
	cases := []struct {
		name string
		key  string
		ok   bool
	}{
		{"a service account token", "svc-tok-abcdef0123456789", true},
		{"a long opaque token", strings.Repeat("a", 128), true},
		{"a user token", "acct123:secret456", false},
		{"a user token with a colon in the secret", "acct123:sec:ret", false},

		// Not a user token: one half is empty, so it is some other malformed
		// value. Nothing here knows what a service token looks like, so
		// guessing further would refuse a valid credential the day Textable
		// changes how it mints them.
		{"a trailing colon", "acct123:", true},
		{"a leading colon", ":secret456", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{BaseURL: "https://x.textable.app", APIKey: tc.key}
			cfg.withDefaults()
			err := cfg.Validate()
			if tc.ok && err != nil {
				t.Fatalf("%q should be accepted: %v", tc.key, err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("%q should be refused", tc.key)
				}
				if !strings.Contains(err.Error(), "service account") {
					t.Errorf("the refusal should say which kind of credential "+
						"is wanted, got: %v", err)
				}
			}
		})
	}
}

func TestValidate_RefusesAnAddressThatWouldBuildAWrongURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		want string
	}{
		// Paths here carry their own /api, so an address ending in one builds
		// /api/api/contacts and 404s from a path nobody meant to construct.
		{"the API path rather than the root", "https://x.textable.app/api", "drop the /api"},
		{"no scheme", "x.textable.app", "http or https"},
		{"a credential in the address", "https://u:p@x.textable.app", "ends up in logs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{BaseURL: tc.base, APIKey: testKey}
			cfg.withDefaults()
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("%q should be refused", tc.base)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("expected the refusal to mention %q, got: %v", tc.want, err)
			}
		})
	}
}

// A deployment served under a sub-path whose name merely contains the letters
// is a legitimate address, so the check is on segments rather than substrings.
func TestValidate_AcceptsAPathThatMerelyContainsTheLetters(t *testing.T) {
	cfg := Config{BaseURL: "https://x.example.com/apiary", APIKey: testKey}
	cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("/apiary is not the API path: %v", err)
	}
}

// An unconfigured instance is not an error. It mounts so its settings form has
// somewhere to live, and Check is what says the key is missing.
func TestValidate_AllowsAnInstanceNobodyHasConfiguredYet(t *testing.T) {
	var cfg Config
	cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an empty configuration should mount: %v", err)
	}
	if cfg.Configured() {
		t.Error("an empty configuration should not report itself configured")
	}
}

// Zero means "never reuse an answer" and is a real choice, so the default only
// applies to a configuration that never mentioned caching. Getting this wrong
// would silently switch caching back on for an operator who turned it off.
func TestWithDefaults_TreatsAnExplicitZeroCacheAsAChoice(t *testing.T) {
	cfg := Config{CacheSeconds: -1}
	cfg.withDefaults()
	if cfg.CacheSeconds != -1 {
		t.Errorf("a negative cache setting should reach Validate to be refused, got %d",
			cfg.CacheSeconds)
	}
	if err := cfg.Validate(); err == nil {
		t.Error("a negative cache_seconds should be refused")
	}
}

// The cache is an allow-list: an endpoint it does not recognise is fetched every
// time, so a tool added later cannot quietly start being served from memory.
//
// The tenant report is the one that must be held -- it is the directory behind
// three of the seven tools and the most expensive read here. A contact is the
// one that must not be: whether somebody has opted out of messages is acted on,
// and a stale answer to that has consequences outside the conversation.
func TestCacheTTL_HoldsTheDirectoryAndNeverAContact(t *testing.T) {
	cfg := Config{CacheSeconds: 60}
	held := []string{
		tenantsPath,
		tenantReportPath,
		tenantReportPath + "/t1",
		"/api/v2/organizations",
		"/api/v2/organizations/o1",
	}
	fetched := []string{
		"/api/v2/contacts/c1",
		"/health",
		"/api/v2/organizations/o1/move-warnings",
	}

	for _, path := range held {
		if cfg.cacheTTL(path) <= 0 {
			t.Errorf("%s describes how the instance is arranged and may be reused", path)
		}
	}
	for _, path := range fetched {
		if cfg.cacheTTL(path) != 0 {
			t.Errorf("%s must be fetched every time", path)
		}
	}
}
