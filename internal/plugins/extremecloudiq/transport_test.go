package extremecloudiq

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The read-only guarantee lives here and nowhere else.
//
// It is an allow-list rather than a method check, and the reason is worth a
// test of its own: every read this integration makes is a GET, so a method
// check would have covered every call it makes today -- and would also have
// permitted GET /account/viq/default-device-password, which is the default
// password every device on the estate is onboarded with.
func TestGuard_RefusesReadsThatAreCredentialDumps(t *testing.T) {
	for _, path := range []string{
		"/account/viq/default-device-password",
		"/acct-api-token/export",
		"/acct-api-token",
		"/packetcaptures/files",
		"/endusers",
		"/users",
		"/certificates",
		"/account/viq/download",
	} {
		t.Run(path, func(t *testing.T) {
			err := roundTrip(t, http.MethodGet, path)
			if err == nil {
				t.Fatalf("GET %s was permitted; it is a read in the HTTP sense "+
					"and a credential dump in every other", path)
			}
			if !strings.Contains(err.Error(), "not one of the endpoints") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
		})
	}
}

// Everything the tools actually call has to pass, or the guard is a bug that
// only shows up against a real account.
func TestGuard_PermitsEveryEndpointTheToolsUse(t *testing.T) {
	for _, path := range []string{
		"/auth/apitoken/info",
		"/devices",
		"/devices/stats",
		"/devices/4711",
		"/devices/4711/location",
		"/devices/4711/network-policy",
		"/devices/4711/alarms",
		"/devices/4711/history/cpu-mem",
		"/devices/4711/interfaces/wifi",
		"/clients/active",
		"/clients/active/count",
		"/clients/summary",
		"/alerts",
		"/alerts/count-by-SEVERITY",
		"/logs/audit",
		"/locations/tree",
		"/network-policies",
		"/network-policies/9/ssids",
		"/ssids",
	} {
		t.Run(path, func(t *testing.T) {
			if err := roundTrip(t, http.MethodGet, path); err != nil {
				t.Fatalf("GET %s was refused by this plugin's own guard: %v", path, err)
			}
		})
	}
}

// An endpoint no tool calls is not on the list, however harmless it looks. A
// permission granted in advance for a read nobody has argued for is the habit
// the allow-list exists to prevent.
func TestGuard_NamesOnlyWhatIsReached(t *testing.T) {
	for _, path := range []string{"/account/home", "/locations/site"} {
		if err := roundTrip(t, http.MethodGet, path); err == nil {
			t.Errorf("GET %s is permitted, but no tool calls it", path)
		}
	}
}

// A write is refused even on a path the guard knows, and the refusal says what
// the path is for -- because that is the shape a bug in this package takes,
// and naming the endpoint is what makes it findable.
func TestGuard_RefusesEveryWrite(t *testing.T) {
	for _, method := range []string{
		http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
	} {
		t.Run(method, func(t *testing.T) {
			err := roundTrip(t, method, "/devices")
			if err == nil {
				t.Fatalf("%s /devices was permitted", method)
			}
			if !strings.Contains(err.Error(), "only reads") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
			if !strings.Contains(err.Error(), "listing devices") {
				t.Errorf("the refusal does not say what /devices is for: %v", err)
			}
		})
	}
}

// The dangerous neighbours of endpoints that are allowed.
//
// Each of these is one path segment away from a read this plugin makes, and
// each of them changes the estate. An anchored pattern is what keeps them out;
// a prefix match would let all four through.
func TestGuard_RefusesTheNeighboursOfWhatItAllows(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/devices/4711/:reboot"},
		{http.MethodGet, "/devices/4711/gallery-image"},
		{http.MethodGet, "/devices/4711/installation-report"},
		{http.MethodPost, "/devices/4711/:cli"},
		{http.MethodGet, "/network-policies/9/ssids/3"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			if err := roundTrip(t, tc.method, tc.path); err == nil {
				t.Fatalf("%s %s was permitted", tc.method, tc.path)
			}
		})
	}
}

// A path reaching the guard by a different spelling is compared in the form
// the server will route on, or an anchored pattern is walked past by writing
// the same request another way.
func TestGuard_NormalisesBeforeMatching(t *testing.T) {
	// Both spellings of a permitted path still pass...
	for _, path := range []string{"//devices", "/devices/"} {
		if err := roundTrip(t, http.MethodGet, path); err != nil {
			t.Errorf("GET %s was refused, though it is /devices: %v", path, err)
		}
	}
	// ...and a refused one is not reachable by adding a segment separator.
	for _, path := range []string{"//endusers", "/endusers/"} {
		if err := roundTrip(t, http.MethodGet, path); err == nil {
			t.Errorf("GET %s was permitted", path)
		}
	}
}

// An installation behind a gateway that prefixes a path is an ordinary
// deployment, and its requests arrive as /gateway/devices. A guard that
// trimmed a fixed prefix would refuse every single one of them.
func TestGuard_HandlesAGatewayPrefix(t *testing.T) {
	c := readOnly(nil, "/gateway")
	if err := clientErr(c, http.MethodGet, "http://host.invalid/gateway/devices"); err != nil {
		t.Errorf("a prefixed path was refused: %v", err)
	}
	// And the prefix is a whole segment, so a host that merely starts with the
	// same letters is not under it.
	if err := clientErr(c, http.MethodGet, "http://host.invalid/gatewayfoo/devices"); err == nil {
		t.Error("a path that only shares the prefix's letters was permitted")
	}
}

// A redirect is never followed, because following one would carry the bearer
// token to whatever host the redirect named.
func TestGuard_DoesNotFollowARedirect(t *testing.T) {
	var reached int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		http.Redirect(w, r, "/devices/stats", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	c := readOnly(srv.Client(), "")
	resp, err := c.Get(srv.URL + "/devices")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("the redirect was followed; status %d", resp.StatusCode)
	}
	if reached != 1 {
		t.Errorf("the server was reached %d times, so the redirect was chased", reached)
	}
}

// roundTrip runs one request through the guard and reports what it decided.
// The transport underneath is never reached, so nothing here needs a server.
func roundTrip(t *testing.T, method, path string) error {
	t.Helper()
	return clientErr(readOnly(nil, ""), method, "http://host.invalid"+path)
}

func clientErr(c *http.Client, method, target string) error {
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		return err
	}
	g, ok := c.Transport.(guard)
	if !ok {
		return errNotGuarded
	}
	// The guard's own decision, without a network underneath it: a permitted
	// request would otherwise fail on DNS and be indistinguishable from a
	// refused one.
	g.base = okTransport{}
	_, err = g.RoundTrip(req)
	return err
}

type okTransport struct{}

func (okTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK, Body: http.NoBody, Request: r,
	}, nil
}

var errNotGuarded = errNotGuardedType{}

type errNotGuardedType struct{}

func (errNotGuardedType) Error() string {
	return "the client's transport is not the read-only guard"
}
