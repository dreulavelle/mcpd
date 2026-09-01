package textable

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The credential goes into the Authorization header as a plain bearer token.
// Pinned rather than left to the first live call: a header this API does not
// like produces "Invalid API Credentials", which is also what a revoked token
// produces, and the two are a day apart in diagnosis.
func TestClient_SendsTheCredentialAsABearerToken(t *testing.T) {
	var got string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.Get(context.Background(), "/api/v2/contacts/c1", nil); err != nil {
		t.Fatal(err)
	}
	if want := "Bearer " + testKey; got != want {
		t.Errorf("Authorization header was %q, want %q", got, want)
	}
}

// A service account token is sent whole, as an ordinary bearer credential --
// not the accountUid:apiKey pair a user token takes. Getting this wrong
// produces a 401 whose message is indistinguishable from a revoked token.
func TestClient_SendsTheTokenUnmodified(t *testing.T) {
	var got string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.Get(context.Background(), "/api/v2/contacts/c1", nil); err != nil {
		t.Fatal(err)
	}
	if want := "Bearer " + testKey; got != want {
		t.Errorf("Authorization header was %q, want %q", got, want)
	}
}

// A successful body here is somebody's personal data -- a contact's name and
// phone number, or every user on the instance. The debug log says what was
// asked and how much came back, and never what it was. The token must not
// appear either.
func TestClient_NeverLogsAResponseBodyOrTheCredential(t *testing.T) {
	var logged bytes.Buffer
	srv := httptest.NewServer(jsonOK(
		`{"id":"c1","phone_number":"+15551234567","full_name":"Jane Roe"}`))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	c := NewClient(srv.Client(), cfg, testKey,
		slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
		time.Now, nil, func(string, time.Duration) {})

	if _, err := c.Get(context.Background(), "/api/v2/contacts/c1", nil); err != nil {
		t.Fatal(err)
	}

	out := logged.String()
	if out == "" {
		t.Fatal("the debug log recorded nothing; the upstream call should be traceable")
	}
	for _, forbidden := range []string{"Jane Roe", "+15551234567", testKey} {
		if strings.Contains(out, forbidden) {
			t.Errorf("the log carried %q:\n%s", forbidden, out)
		}
	}
}

// The startup probe is in two halves because they fail differently, and the
// difference is the whole value of probing. /health is unauthenticated, so a
// wrong address and a wrong key produce two different sentences rather than one
// confusing one.
func TestProbe_SeparatesReachabilityFromTheCredential(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/health":
			_, _ = w.Write([]byte(`{"status":"pass","version":"7.9.7","releaseId":"abc"}`))
		case r.URL.Path == "/api/v2/tenants":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"_errType":"TXBDEV_API_ERROR_V1",` +
				`"message":"Invalid API Credentials","referenceCode":"ref-1"}`))
		default:
			t.Errorf("unexpected probe request to %s", r.URL.Path)
		}
	})

	health, err := c.Probe(context.Background())
	if err != nil {
		t.Fatalf("the instance is reachable, so /health should succeed: %v", err)
	}
	if !health.ok() || health.Version != "7.9.7" {
		t.Errorf("health report did not decode: %+v", health)
	}

	err = c.ProbeAuth(context.Background())
	if err == nil {
		t.Fatal("a rejected token should fail the second half of the probe")
	}
	// The reference code is the only string somebody can quote to Textable, so
	// it goes into the error a support call will read back.
	if !strings.Contains(err.Error(), "ref-1") {
		t.Errorf("the failure should carry Textable's reference code, got: %v", err)
	}
}

// The auth probe reads the tenant listing, which is both the cheapest proof the
// token works and the first call the directory makes anyway.
//
// It deliberately does not probe GET /api/v2/users/{id}: that endpoint is
// documented as accepting a service account and answers 401 to a valid one, so
// probing with it would report every healthy installation as broken.
func TestProbeAuth_PassesOnTheTenantListing(t *testing.T) {
	var path string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"tenants":[]}`))
	})
	if err := c.ProbeAuth(context.Background()); err != nil {
		t.Errorf("a 200 from the tenant listing means the token works: %v", err)
	}
	if path != "/api/v2/tenants" {
		t.Errorf("the probe read %q, want the tenant listing", path)
	}
}

// A 403 is the token being accepted and not granted what this needs. The fix is
// a scope on the service account rather than a new token, so it says so.
func TestProbeAuth_NamesTheScopesWhenTheTokenIsUnderPrivileged(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"_errType":"TXBDEV_API_ERROR_V1","message":"Forbidden"}`))
	})
	err := c.ProbeAuth(context.Background())
	if err == nil {
		t.Fatal("a 403 on the probe should be reported")
	}
	if !strings.Contains(err.Error(), "read-all-users") {
		t.Errorf("the error should name the scopes to grant, got: %v", err)
	}
}

// Something that answers JSON but is not Textable is a real deployment mistake
// -- an address pointed at a proxy, a gateway or the wrong app -- and it should
// be named at startup rather than failing later against an endpoint that does
// not exist.
func TestProbe_RefusesJSONThatIsNotTextable(t *testing.T) {
	c, _ := testClient(t, jsonOK(`{"hello":"world"}`))
	if _, err := c.Probe(context.Background()); err == nil {
		t.Fatal("JSON naming no status should be refused")
	} else if !strings.Contains(err.Error(), "probably not Textable") {
		t.Errorf("the error should say what is wrong, got: %v", err)
	}
}

// A 403 means the plugin is working and the key is the limit. The fix is never
// in mcpd, so the message says where it is.
func TestErrors_ExplainThatAKeyIsScopedRatherThanBroken(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"_errType":"TXBDEV_API_ERROR_V1",` +
			`"message":"Forbidden","reason":"admin required","referenceCode":"ref-2"}`))
	})
	_, err := c.Get(context.Background(), "/api/v2/organizations", nil)
	if err == nil {
		t.Fatal("a 403 should be an error")
	}
	for _, want := range []string{"admin", "ref-2", "key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the 403 should mention %q, got: %v", want, err)
		}
	}
}

// An HTML body means something other than the API's own handler answered, and
// what that implies depends entirely on the status.
//
// A 401 or a redirect carrying HTML really can be a misconfigured address --
// a gateway or sign-in page where the API should be. A 5xx carrying HTML is a
// working API whose gateway is reporting that something behind it failed, and
// telling somebody their address might be wrong sends them to re-check a
// configuration that is fine. This package said the same thing for both.
func TestErrors_ReadAnHTMLBodyAccordingToItsStatus(t *testing.T) {
	html := "<!doctype html><html><head><title>502</title></head></html>"

	serverError, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(html))
	})
	_, err := serverError.Get(context.Background(), "/api/v2/organizations", nil)
	if err == nil {
		t.Fatal("a 502 should be an error")
	}
	if !strings.Contains(err.Error(), "the API is reachable") {
		t.Errorf("a 5xx should not cast doubt on the address, got: %v", err)
	}
	if strings.Contains(err.Error(), "the address may be") {
		t.Errorf("a 5xx still blamed the configured address: %v", err)
	}

	unauthorised, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(html))
	})
	_, err = unauthorised.Get(context.Background(), "/api/v2/organizations", nil)
	if err == nil {
		t.Fatal("a 401 should be an error")
	}
	if !strings.Contains(err.Error(), "sign-in page") {
		t.Errorf("a 401 carrying HTML is the case where the address really may "+
			"be wrong, got: %v", err)
	}
}

// This upstream answers a read by id with 502 rather than 404 when the id does
// not exist -- measured on organizations and contacts. A model told only "the
// API failed" concludes there is an outage and reports one; the id it passed is
// the actual cause and is the thing it can fix.
func TestErrors_PointAtTheIdWhenAByIdReadReturns5xx(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	})

	_, err := c.Get(context.Background(), "/api/v2/contacts/does-not-exist", nil)
	if err == nil {
		t.Fatal("a 502 should be an error")
	}
	if !strings.Contains(err.Error(), "check the id") {
		t.Errorf("a by-id 5xx should point at the id, got: %v", err)
	}

	// A collection read has no id to blame, so it must not invent one.
	_, err = c.Get(context.Background(), "/api/v2/tenants", nil)
	if err == nil {
		t.Fatal("a 502 should be an error")
	}
	if strings.Contains(err.Error(), "check the id") {
		t.Errorf("a collection read has no id to blame, got: %v", err)
	}
}
