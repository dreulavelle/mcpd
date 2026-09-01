package bandwidth

import (
	"net/http"
	"strings"
	"testing"
)

// The allow-list is the only thing making this integration read-only.
//
// Bandwidth's roles are not split into read and write: "Campaign management"
// grants creating a campaign as well as reading one, and there is no role that
// grants looking without touching. So a credential scoped for these reads can
// also write, and the guarantee has to live in the transport.
func TestGuardRefusesEverythingThatIsNotAllowedRead(t *testing.T) {
	for name, tc := range map[string]struct {
		method, path string
		allowed      bool
		says         string
	}{
		"listing calls": {
			http.MethodGet, "/api/v2/accounts/5009021/calls", true, "",
		},
		"reading one call": {
			http.MethodGet, "/api/v2/accounts/5009021/calls/c-123", true, "",
		},
		"the token exchange": {
			http.MethodPost, tokenPath, true, "",
		},
		"searching messages": {
			http.MethodGet, "/api/v2/users/5009021/messages", true, "",
		},
		"endpoints under the other prefix": {
			http.MethodGet, "/v2/accounts/5009021/endpoints", true, "",
		},
		// The write this credential is capable of and this plugin must never
		// make. A POST to /calls places a real telephone call.
		"placing a call": {
			http.MethodPost, "/api/v2/accounts/5009021/calls", false, "only reads",
		},
		"hanging up a call": {
			http.MethodDelete, "/api/v2/accounts/5009021/calls/c-123", false, "only reads",
		},
		"sending a message": {
			http.MethodPost, "/api/v2/users/5009021/messages", false, "only reads",
		},
		"an endpoint nobody added": {
			http.MethodGet, "/api/v2/accounts/5009021/campaigns", false, "allow-list",
		},
		// Downloading recording audio is deliberately absent: the bytes are a
		// media file, and putting one into a model's context is not a read
		// anybody asked for.
		"downloading recording audio": {
			http.MethodGet, "/api/v2/accounts/5009021/calls/c-1/recordings/r-1/media",
			false, "allow-list",
		},

		// The Dashboard half. Reads allowed:
		"listing port-ins": {
			http.MethodGet, "/api/v2/accounts/5009021/portins", true, "",
		},
		"a port-in order's notes": {
			http.MethodGet, "/api/v2/accounts/5009021/portins/p-1/notes", true, "",
		},
		"E911 locations": {
			http.MethodGet, "/api/v2/accounts/5009021/e911s/locations", true, "",
		},
		"10DLC campaigns": {
			http.MethodGet, "/api/accounts/5009021/campaignManagement/10dlc/campaigns", true, "",
		},
		// ...and the writes that the same credential is perfectly capable of.
		// Each of these changes something in the real world, which is why the
		// allow-list is the guarantee rather than the credential.
		"submitting a port-in": {
			http.MethodPost, "/api/v2/accounts/5009021/portins", false, "only reads",
		},
		"ordering numbers": {
			http.MethodPost, "/api/v2/accounts/5009021/orders", false, "only reads",
		},
		"disconnecting numbers": {
			http.MethodPost, "/api/v2/accounts/5009021/disconnects", false, "only reads",
		},
		"changing an E911 address": {
			http.MethodPut, "/api/v2/accounts/5009021/e911s/locations/l-1", false, "only reads",
		},
		"deleting a site": {
			http.MethodDelete, "/api/v2/accounts/5009021/sites/407", false, "only reads",
		},
		"creating a 10DLC campaign": {
			http.MethodPost, "/api/accounts/5009021/campaignManagement/10dlc/campaigns", false, "only reads",
		},
		// Fetching a letter of authorisation document itself is absent on
		// purpose: it is a scan, usually of somebody's signature.
		"downloading an LOA document": {
			http.MethodGet, "/api/v2/accounts/5009021/portins/p-1/loas/f-1", false, "allow-list",
		},
	} {
		t.Run(name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, "https://voice.bandwidth.com"+tc.path, nil)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			_, err = guard{base: refusingTransport{}}.RoundTrip(req)

			if tc.allowed {
				if err == nil || !strings.Contains(err.Error(), "reached upstream") {
					t.Fatalf("%s %s should have been allowed through, got %v",
						tc.method, tc.path, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s %s was allowed through", tc.method, tc.path)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("refusal does not say %q: %v", tc.says, err)
			}
		})
	}
}

// refusingTransport stands in for the network: reaching it means the guard
// allowed the request, which is what the allowed cases assert.
type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errReachedUpstream
}

var errReachedUpstream = &upstreamError{}

type upstreamError struct{}

func (*upstreamError) Error() string { return "reached upstream" }

// A path is one string to a server and several to a regular expression.
func TestNormalisePath(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "/",
		"/":                             "/",
		"/api/v2/accounts/1/calls/":     "/api/v2/accounts/1/calls",
		"//api//v2//accounts//1//calls": "/api/v2/accounts/1/calls",
	} {
		if got := normalisePath(in); got != want {
			t.Errorf("normalisePath(%q) = %q, want %q", in, got, want)
		}
	}
}
