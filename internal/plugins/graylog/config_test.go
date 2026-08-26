package graylog

import (
	"strings"
	"testing"
)

// What is refused here is a configuration that is present and wrong, because
// that fails later, further away, and with a worse message. An empty one is
// not an error: the plugin mounts so its settings form has somewhere to live.
func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   Config
		wants string // a substring the message must carry, "" for accepted
	}{
		{"empty", Config{}, ""},
		{"a plain address", Config{BaseURL: "https://graylog.example:9000"}, ""},

		{"not a URL", Config{BaseURL: "graylog.example"}, "http or https"},
		{"no host", Config{BaseURL: "https://"}, "names no host"},
		{"the API path", Config{BaseURL: "https://graylog.example/api"}, "web root"},

		// A Graylog behind a reverse proxy at /graylog-api is a legitimate
		// address whose path merely contains the letters, so the check is on
		// segments rather than on a substring.
		{"a path that contains the letters",
			Config{BaseURL: "https://example.com/graylog-api"}, ""},

		{"credentials in the address",
			Config{BaseURL: "https://alice:pw@graylog.example"}, "not in the address"},
		{"half a login",
			Config{BaseURL: "https://graylog.example", Username: "alice"}, "needs a password"},

		// A token beside a half-filled login is fine: the token wins, so the
		// other half is never consulted.
		{"a token and a stray username",
			Config{BaseURL: "https://graylog.example", Token: "t", Username: "alice"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.withDefaults()
			err := cfg.Validate()
			switch {
			case tc.wants == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.wants != "" && err == nil:
				t.Fatalf("accepted; it should have named %q", tc.wants)
			case tc.wants != "" && !strings.Contains(err.Error(), tc.wants):
				t.Errorf("message = %v, want it to mention %q", err, tc.wants)
			}
		})
	}
}

// An address alone is not enough and neither is a credential alone, which is
// why this is one question rather than two booleans a caller has to combine.
func TestConfigured(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want bool
	}{
		{"nothing", Config{}, false},
		{"an address alone", Config{BaseURL: "https://g.example"}, false},
		{"a token alone", Config{Token: "t"}, false},
		{"an address and a token", Config{BaseURL: "https://g.example", Token: "t"}, true},
		{"an address and half a login",
			Config{BaseURL: "https://g.example", Username: "alice"}, false},
		{"an address and a login",
			Config{BaseURL: "https://g.example", Username: "alice", Password: "pw"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Configured(); got != tc.want {
				t.Errorf("Configured() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Zero is a real choice for the cache -- it means never reuse an answer -- so
// the default may only apply to a configuration that never mentioned it. Every
// other zero here is "not set" and takes the default.
func TestWithDefaults_LeavesAnExplicitZeroCacheAlone(t *testing.T) {
	cfg := Config{CacheSeconds: 0}
	cfg.withDefaults()
	if cfg.CacheSeconds != int(defaultCacheTTL.Seconds()) {
		t.Errorf("cache_seconds = %d, want the default", cfg.CacheSeconds)
	}

	// And a plugin built with caching off holds no cache at all, so "no
	// caching" costs nothing rather than costing a lookup.
	off := Config{BaseURL: "https://g.example", Token: "t", CacheSeconds: -1}
	off.withDefaults()
	if off.CacheSeconds != -1 {
		t.Errorf("an explicit value was overwritten: %d", off.CacheSeconds)
	}
	if err := off.Validate(); err == nil {
		t.Error("a negative cache duration was accepted")
	}
}

// Only the endpoints that describe how the installation is *arranged* may be
// held. Everything that answers "what is happening" is fetched every time, and
// the omissions are the point.
func TestCacheTTL(t *testing.T) {
	cfg := testConfig("https://g.example")

	held := []string{
		"/streams/paginated", "/streams/000000000000000000000001",
		"/events/definitions", "/events/definitions/d1", "/views/fields",
		"/system/inputs", "/system/indices/index_sets",
	}
	for _, path := range held {
		if cfg.cacheTTL(path) <= 0 {
			t.Errorf("%s is not cacheable and should be", path)
		}
	}

	never := []string{
		"/search/messages", "/search/aggregate", "/events/search",
		"/cluster", "/system/cluster/nodes", "/system/indexer/cluster/health",
		"/system/notifications",
		// The startup probe and the health section both read this. A liveness
		// check answered from memory is not one.
		"/system",
		// A sub-collection under a cacheable prefix is refused rather than
		// silently inheriting its parent's TTL.
		"/streams/s1/pipelines",
	}
	for _, path := range never {
		if cfg.cacheTTL(path) != 0 {
			t.Errorf("%s is cacheable and must not be", path)
		}
	}
}
