package extremecloudiq

import (
	"strings"
	"testing"
)

// The address has a default that is right for every tenant, so unlike a
// self-hosted integration a token alone is a complete configuration. Requiring
// an address as well would be asking an operator for a value they have no way
// to be told.
func TestConfig_NeedsOnlyAToken(t *testing.T) {
	var c Config
	c.withDefaults()
	if c.BaseURL != defaultBaseURL {
		t.Errorf("BaseURL = %q, want the regionless endpoint %q", c.BaseURL, defaultBaseURL)
	}
	if c.Configured() {
		t.Error("a config with no token reports itself configured")
	}
	c.APIToken = "tok"
	if !c.Configured() {
		t.Error("a config with a token and the default address is not configured")
	}
}

func TestConfig_RefusesWhatCannotWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{
			"an address that is not a URL",
			Config{BaseURL: "not a url", MaxItems: 1, DefaultRangeSeconds: 1, RequestsPerSecond: 1},
			"http or https",
		},
		{
			// A credential in a URL ends up in logs, in errors, and in
			// whatever a support bundle collects.
			"a credential in the address",
			Config{BaseURL: "https://tok@api.extremecloudiq.com", MaxItems: 1, DefaultRangeSeconds: 1, RequestsPerSecond: 1},
			"not in the address",
		},
		{
			// A ceiling below the default refuses every read nobody narrowed,
			// which is a configuration that looks fine and answers nothing.
			"a ceiling below the default window",
			Config{MaxItems: 1, DefaultRangeSeconds: 3600, MaxRangeSeconds: 60, RequestsPerSecond: 1},
			"below",
		},
		{
			"a negative cache",
			Config{MaxItems: 1, DefaultRangeSeconds: 1, RequestsPerSecond: 1, CacheSeconds: -1},
			"cannot be negative",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if err == nil {
				t.Fatal("accepted a configuration that cannot work")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the message does not say %q: %v", tc.want, err)
			}
		})
	}
}

// Zero seconds of caching is a real choice -- never reuse an answer -- and has
// to survive withDefaults, which is where a "fill in what was left alone" pass
// would quietly overwrite it. Only a config that never mentioned it gets the
// default.
func TestConfig_CacheOffStaysOff(t *testing.T) {
	c := Config{CacheSeconds: 0}
	c.withDefaults()
	if c.CacheSeconds != int(defaultCacheTTL.Seconds()) {
		t.Fatalf("an unset cache did not get the default: %d", c.CacheSeconds)
	}
	// Deliberately off is expressed as a value below zero being refused and
	// zero being the only "off"; what must not happen is a Validate that
	// refuses the zero withDefaults would have replaced.
	off := Config{CacheSeconds: 0, MaxItems: 1, DefaultRangeSeconds: 1, RequestsPerSecond: 1}
	if err := off.Validate(); err != nil {
		t.Errorf("caching switched off was refused: %v", err)
	}
}

// A page is asked for at the size the answer actually needs. Asking the API
// for a hundred rows to satisfy a limit of five is ninety-five rows decoded
// and thrown away, on an API that meters requests by the account.
func TestPageSize_AsksForNoMoreThanIsWanted(t *testing.T) {
	for _, tc := range []struct{ want, endpointMax, expect int }{
		{0, 100, 100},   // no limit named: the endpoint's page
		{5, 100, 5},     // a small ask: exactly that
		{500, 100, 100}, // more than a page: a page
		{500, 500, 500}, // an endpoint that permits more
		{0, 0, 100},     // an endpoint with no stated maximum
	} {
		if got := pageSize(tc.want, tc.endpointMax); got != tc.expect {
			t.Errorf("pageSize(%d, %d) = %d, want %d",
				tc.want, tc.endpointMax, got, tc.expect)
		}
	}
}
