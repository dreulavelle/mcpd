package flowroute

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The guard is the whole of the read-only guarantee: a Flowroute API key is
// not scoped, so the same credential that reads a number can release it. These
// tests defend that guarantee rather than any particular tool.

func TestGuardRefusesEverythingButAllowedReads(t *testing.T) {
	t.Parallel()

	base := "https://api.flowroute.com"
	client, err := readOnly(&http.Client{}, base)
	if err != nil {
		t.Fatalf("readOnly: %v", err)
	}
	g := client.Transport.(guard)

	cases := []struct {
		name    string
		method  string
		url     string
		allowed bool
		says    string
	}{
		{"listing numbers", http.MethodGet, base + "/v2/numbers", true, ""},
		{"one number", http.MethodGet, base + "/v2/numbers/12065550100", true, ""},
		{"listing routes", http.MethodGet, base + "/v2/routes", true, ""},
		{"edge strategies", http.MethodGet, base + "/v2/routes/edge_strategies", true, ""},
		{"one emergency address", http.MethodGet, base + "/v2/e911s/20155", true, ""},
		{"one port order's status", http.MethodGet, base + "/v2/portorders/41351/status", true, ""},
		{"an export job", http.MethodGet, base + "/v2/cdrs/exports/7", true, ""},

		// Releasing a number is the write this integration exists not to make.
		{"releasing a number", http.MethodDelete, base + "/v2/numbers/12065550100", false, "read-only"},
		{"buying a number", http.MethodPost, base + "/v2/numbers", false, "read-only"},
		{"repointing a route", http.MethodPatch, base + "/v2/numbers/12065550100", false, "read-only"},
		{"replacing an emergency address", http.MethodPut, base + "/v2/e911s/20155", false, "read-only"},

		// A GET is not enough on its own. Purchasable-number search is a read
		// Flowroute serves and this integration has no business making.
		{"searching numbers to buy", http.MethodGet, base + "/v2/numbers/available", false, "not one of the endpoints"},
		{"an undreamt path", http.MethodGet, base + "/v2/anything", false, "not one of the endpoints"},
		{"the API root", http.MethodGet, base + "/v2", false, "not one of the endpoints"},

		// Basic authentication puts the credential on every request, so a
		// request leaving for another host takes the account's keys with it.
		{"another host entirely", http.MethodGet, "https://evil.example/v2/numbers", false, "and nowhere else"},
		{"the right path, plain http", http.MethodGet, "http://api.flowroute.com/v2/numbers", false, "and nowhere else"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, tc.url, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			err = g.check(req)
			switch {
			case tc.allowed && err != nil:
				t.Fatalf("%s %s should be allowed, got %v", tc.method, tc.url, err)
			case !tc.allowed && err == nil:
				t.Fatalf("%s %s should be refused, was allowed", tc.method, tc.url)
			case !tc.allowed && !strings.Contains(err.Error(), tc.says):
				t.Fatalf("refusal should mention %q, said %q", tc.says, err)
			}
		})
	}
}

// The guard sits on the transport rather than at the call sites, so a redirect
// nobody wrote is checked too. This is the case that motivates it: the
// credential travels on every request, so a redirect off the configured host
// would hand the account's keys to whoever sent it.
func TestGuardRefusesARedirectOffTheHost(t *testing.T) {
	t.Parallel()

	var elsewhere *httptest.Server
	elsewhere = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request reached the other host at %s, carrying the credential", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer elsewhere.Close()

	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/v2/numbers", http.StatusFound)
	}))
	defer home.Close()

	client, err := readOnly(&http.Client{}, home.URL)
	if err != nil {
		t.Fatalf("readOnly: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, home.URL+"/v2/numbers", nil)
	req.SetBasicAuth(testAccessKey, testSecretKey)

	if _, err := client.Do(req); err == nil {
		t.Fatal("a redirect to another host should have been refused")
	} else if !strings.Contains(err.Error(), "and nowhere else") {
		t.Fatalf("refusal should name the pinned host, said %q", err)
	}
}

// readOnly refuses an address it cannot pin, rather than pinning nothing.
func TestReadOnlyRefusesAnAddressItCannotPin(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"", "api.flowroute.com", "/v2", "://"} {
		if _, err := readOnly(&http.Client{}, base); err == nil {
			t.Fatalf("readOnly(%q) should have failed", base)
		}
	}
}
