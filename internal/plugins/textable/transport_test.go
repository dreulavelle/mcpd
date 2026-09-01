package textable

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The guard is the only place the read-only guarantee is enforced, so these
// tests are about what it refuses rather than about what it lets through.

func TestGuard_PermitsOnlyTheReadsThisIntegrationMakes(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		allow  bool
	}{
		{"health", http.MethodGet, "/health", true},
		{"the tenant listing", http.MethodGet, "/api/v2/tenants", true},
		{"the tenant report", http.MethodGet, "/api/v2/billing/tenantReport", true},
		{"one tenant", http.MethodGet, "/api/v2/billing/tenantReport/t1", true},
		{"organizations by tenant", http.MethodGet, "/api/v2/organizations", true},
		{"one organization", http.MethodGet, "/api/v2/organizations/o1", true},
		{"one contact", http.MethodGet, "/api/v2/contacts/xyz789", true},

		// The whole reason this is an allow-list rather than a method check. A
		// service account token may hold edit scopes an operator granted for
		// something else, so what stops this plugin writing has to be here.
		{"minting a user token", http.MethodPost, "/api/v2/users/abc123/token", false},
		{"deleting a user", http.MethodDelete, "/api/v2/users/abc123", false},
		{"deleting a contact", http.MethodDelete, "/api/v2/contacts/xyz789", false},
		{"deleting an organization", http.MethodDelete, "/api/v2/organizations/o1", false},
		{"editing a contact", http.MethodPatch, "/api/v2/contacts/xyz789", false},
		{"creating a tenant", http.MethodPost, "/api/v2/tenants/", false},
		{"changing a password", http.MethodPost, "/api/v2/users/abc123/changePassword", false},
		{"revoking a token", http.MethodDelete, "/api/v2/users/abc123/token/t1", false},

		// Reads this integration does not make. Default-deny means a tool added
		// later has to name its endpoint rather than inherit permission.
		{"the v1 contact listing", http.MethodGet, "/api/contacts", false},
		{"the v1 user listing", http.MethodGet, "/api/users", false},

		// Not permitted because it does not work: the specification lists
		// SystemToken among the credentials this accepts, and a live instance
		// answers 401 to a valid service account token with every read scope.
		// Naming it would permit a call that can only ever fail.
		{"one user", http.MethodGet, "/api/v2/users/abc123", false},

		// The specification's spelling of the billing path. It 404s on a live
		// instance, so permitting it would only let a typo reach the network.
		{"the spec's misspelled report", http.MethodGet, "/api/v2/biling/tenantReport", false},
		{"installed integrations", http.MethodGet, "/api/v2/integrations/installed", false},
		{"admin integrations", http.MethodGet, "/api/v2/admin-integrations", false},

		// Sub-resources under a permitted path. The anchoring is what stops
		// /api/v2/users/{id} also covering everything beneath it -- including
		// the endpoint that mints a credential.
		{"a user's message settings", http.MethodGet, "/api/v2/users/abc/messageSettings", false},
		{"an organization's move warnings", http.MethodGet, "/api/v2/organizations/o1/move-warnings", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reached bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, "{}")
			}))
			defer srv.Close()

			c := readOnly(srv.Client(), "")
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := c.Do(req)
			if resp != nil {
				resp.Body.Close()
			}

			if tc.allow {
				if err != nil {
					t.Fatalf("%s %s should be permitted: %v", tc.method, tc.path, err)
				}
				if !reached {
					t.Errorf("%s %s was permitted but never reached the server",
						tc.method, tc.path)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s %s should have been refused", tc.method, tc.path)
			}
			if reached {
				t.Errorf("%s %s reached the server despite being refused",
					tc.method, tc.path)
			}
		})
	}
}

// A refusal on a path the integration knows says what that path is for, so a
// bug in this package is findable rather than merely blocked.
func TestGuard_NamesWhatAKnownPathIsForWhenTheMethodIsWrong(t *testing.T) {
	c := readOnly(nil, "")
	req, err := http.NewRequest(http.MethodDelete, "https://textable.invalid/api/v2/contacts/c1", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Do(req)
	if err == nil {
		t.Fatal("a DELETE of a contact should be refused")
	}
	if !strings.Contains(err.Error(), "reading one contact") {
		t.Errorf("the refusal should say what the path is for, got: %v", err)
	}
}

// A path reaching the guard by a different spelling must be compared in the
// form the server will route on, or an anchored pattern is walked past.
func TestGuard_NormalisesBeforeMatching(t *testing.T) {
	c := readOnly(nil, "")
	for _, path := range []string{
		"/api//v2//users/abc/token", // doubled separators
		"/api/v2/users/abc/token/",  // trailing slash
	} {
		req, err := http.NewRequest(http.MethodGet, "https://textable.invalid"+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Do(req); err == nil {
			t.Errorf("%s should be refused; it is the token endpoint spelled differently", path)
		}
	}
}

// An instance behind a reverse proxy at a sub-path is an ordinary deployment,
// and trimming a fixed prefix would leave every request unmatched by every
// pattern -- so the plugin would refuse its own reads.
func TestGuard_HonoursAConfiguredSubPath(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()

	c := readOnly(srv.Client(), "/txb")
	resp, err := c.Get(srv.URL + "/txb/api/v2/billing/tenantReport")
	if err != nil {
		t.Fatalf("the tenant report under the configured sub-path should be permitted: %v", err)
	}
	resp.Body.Close()
	if !reached {
		t.Error("the request never reached the server")
	}

	// And a path whose prefix merely starts with the same letters is not under
	// this instance at all.
	if _, err := c.Get(srv.URL + "/txbother/api/v2/billing/tenantReport"); err == nil {
		t.Error("a path outside the configured sub-path should be refused")
	}
}

// A redirect must arrive as a redirect. Following one would carry the
// Authorization header -- a live account credential -- to a host the operator
// never named.
func TestGuard_DoesNotFollowRedirects(t *testing.T) {
	var elsewhere bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			elsewhere = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, other.URL+"/health", http.StatusFound)
	}))
	defer srv.Close()

	c := readOnly(srv.Client(), "")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("the redirect should be returned, not chased: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("expected the redirect itself, got %d", resp.StatusCode)
	}
	if elsewhere {
		t.Error("the credential was carried to the redirect target")
	}
}
