package threecx

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The password is exchanged for a token once, and the token is what travels on
// every read. Three reads cost one sign-in.
func TestClient_SignsInOnceAndReusesTheToken(t *testing.T) {
	p, f := toolPlugin(t, map[string]string{
		"SystemStatus": `{"Version":"20.0.9","FQDN":"pbx.example"}`,
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		var s struct{ Version string }
		if err := p.client.get(ctx, "SystemStatus", url.Values{"$select": {"Version"}}, &s); err != nil {
			t.Fatal(err)
		}
		if s.Version != "20.0.9" {
			t.Errorf("read %d: version %q", i, s.Version)
		}
	}
	if got := f.logins.Load(); got != 1 {
		t.Errorf("signed in %d times for three reads, want once", got)
	}
	if got := f.reads.Load(); got != 3 {
		t.Errorf("%d reads reached the PBX, want 3", got)
	}
}

// A token the PBX stops accepting -- it restarted, or the password changed --
// is replaced by one fresh sign-in rather than an hour of failures. A second
// refusal is reported, not retried forever.
func TestClient_SignsInAgainWhenTheTokenIsRefused(t *testing.T) {
	p, f := toolPlugin(t, map[string]string{
		"SystemStatus": `{"Version":"20.0.9"}`,
	})
	ctx := context.Background()
	sel := url.Values{"$select": {"Version"}}
	if err := p.client.get(ctx, "SystemStatus", sel, nil); err != nil {
		t.Fatal(err)
	}
	f.rejectToken.Store(true)
	if err := p.client.get(ctx, "SystemStatus", sel, nil); err != nil {
		t.Fatalf("a refused token should be replaced by a fresh sign-in: %v", err)
	}
	if got := f.logins.Load(); got != 2 {
		t.Errorf("signed in %d times, want 2", got)
	}

	// Now the PBX refuses the new token too. One retry, then an error that
	// says what to check.
	f.rejectToken.Store(true)
	f.loginOK = false
	err := p.client.get(ctx, "SystemStatus", sel, nil)
	if err == nil || !strings.Contains(err.Error(), "extension and password") {
		t.Errorf("a second refusal should be reported as a credential problem, got %v", err)
	}
}

// A token near its expiry is not used. The margin is there because a token
// that runs out mid-request fails in a way that reads like the PBX being down.
func TestClient_RefreshesBeforeExpiry(t *testing.T) {
	p, f := toolPlugin(t, map[string]string{"SystemStatus": `{}`})
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	p.client.now = func() time.Time { return now }
	ctx := context.Background()
	sel := url.Values{"$select": {"Version"}}
	if err := p.client.get(ctx, "SystemStatus", sel, nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(58 * time.Minute)
	if err := p.client.get(ctx, "SystemStatus", sel, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.logins.Load(); got != 1 {
		t.Errorf("a token with two minutes left is still good; signed in %d times", got)
	}
	now = now.Add(90 * time.Second)
	if err := p.client.get(ctx, "SystemStatus", sel, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.logins.Load(); got != 2 {
		t.Errorf("a token inside its last minute should be replaced; signed in %d times", got)
	}
}

// The status codes 3CX uses mean specific things, and the message says which.
func TestClient_ExplainsRefusals(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{403, ``, "System Owner role"},
		{404, ``, "does not offer"},
		{400, `{"error":{"code":"","message":"Could not find a property named 'Nope' on type 'Pbx.User'.","details":[]}}`, "Could not find a property named 'Nope'"},
		{500, ``, "HTTP 500 with an empty body"},
		{502, `<html><body>Bad gateway</body></html>`, "HTML page"},
	}
	for _, c := range cases {
		err := explainRequestFailure(c.status, "Users", []byte(c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("HTTP %d: want %q in the message, got %v", c.status, c.want, err)
		}
	}
}

// A sign-in that is accepted but issues no token -- the shape a second factor
// takes -- is explained as such rather than as a wrong password.
func TestClient_SecondFactorIsExplained(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Status":"AuthSecurityCodeRequired","Token":null}`))
	})
	p, err := New(testDeps(), Config{Host: srv.URL, Extension: "100", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	p.client.http = readOnly(srv.Client(), p.cfg.root())
	_, err = p.client.bearer(context.Background())
	if err == nil || !strings.Contains(err.Error(), "two-factor") {
		t.Errorf("want a message about a second factor, got %v", err)
	}
}

// Something that is not a 3CX answering the sign-in path is named as such.
func TestClient_NotAPBXIsExplained(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html>Welcome to nginx</html>`))
	})
	p, err := New(testDeps(), Config{Host: srv.URL, Extension: "100", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	p.client.http = readOnly(srv.Client(), p.cfg.root())
	_, err = p.client.bearer(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not the phone system's JSON") {
		t.Errorf("want the address to be doubted, got %v", err)
	}
}

// Paging asks for at most 100 at a time, stops at the caller's ceiling, and
// reports whether more exist from the count the PBX gave.
func TestClient_PagesAtOneHundredAndStopsAtTheCeiling(t *testing.T) {
	var pages []string
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"Status":"AuthSuccess","Token":{"access_token":"` + testToken + `","expires_in":3600}}`))
			return
		}
		q := r.URL.Query()
		pages = append(pages, q.Get("$top")+"/"+q.Get("$skip")+"/"+q.Get("$count"))
		top := 100
		if q.Get("$top") == "50" {
			top = 50
		}
		rows := make([]string, top)
		for i := range rows {
			rows[i] = `{"Number":"x"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(collection(1000, rows...)))
	})
	p, err := New(testDeps(), Config{Host: srv.URL, Extension: "100", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	p.client.http = readOnly(srv.Client(), p.cfg.root())

	type row struct{ Number string }
	got, err := list[row](context.Background(), p.client, "Users", url.Values{"$select": {"Number"}}, 250)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 250 {
		t.Errorf("fetched %d rows, want the ceiling of 250", len(got.Rows))
	}
	if got.Total != 1000 || !got.Truncated {
		t.Errorf("total %d truncated %v; want 1000 and true", got.Total, got.Truncated)
	}
	want := "100/0/true,100/100/,50/200/"
	if strings.Join(pages, ",") != want {
		t.Errorf("pages asked for: %v, want %s", pages, want)
	}
}
