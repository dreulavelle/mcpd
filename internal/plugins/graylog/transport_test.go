package graylog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The read-only guarantee is enforced at the transport, which is the last
// thing every request passes through. Checking where paths are built would be
// checking one place among several, and the wrong one: a path is a string
// until it becomes a URL, and the two are not compared the same way.
func TestGuard_AllowsOnlyTheNamedReads(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
		why          string
	}{
		// The three reads that have to be POSTs. A method-only guard, which
		// is what the sibling integrations use, would refuse every one of
		// them -- and they are the reason this plugin exists.
		{http.MethodPost, "/search/messages", true, "searching is a POST"},
		{http.MethodPost, "/search/aggregate", true, "aggregating is a POST"},
		{http.MethodPost, "/events/search", true, "searching events is a POST"},

		{http.MethodGet, "/system", true, "the probe"},
		{http.MethodGet, "/streams/paginated", true, "listing streams"},
		{http.MethodGet, "/streams/000000000000000000000001", true, "one stream"},
		{http.MethodGet, "/views/fields", true, "every field"},
		{http.MethodPost, "/views/fields", true, "fields for named streams"},

		// Writes. Every one of these is an ordinary part of Graylog's API and
		// none of them is reachable from here.
		{http.MethodPost, "/streams", false, "creating a stream"},
		{http.MethodDelete, "/streams/000000000000000000000001", false, "deleting a stream"},
		{http.MethodPut, "/streams/000000000000000000000001", false, "editing a stream"},
		{http.MethodPost, "/streams/000000000000000000000001/pause", false, "pausing a stream"},
		{http.MethodDelete, "/system/indices/index_sets/x", false, "deleting an index set"},
		{http.MethodPost, "/system/inputs", false, "creating an input"},
		{http.MethodDelete, "/events/definitions/x", false, "deleting an alert rule"},

		// The one that anchoring buys. /views/fields/poll is a POST that
		// triggers a cluster-wide refresh of the field type cache, one path
		// segment away from a read this plugin makes constantly.
		{http.MethodPost, "/views/fields/poll", false, "refreshing the field cache"},

		// A read this integration does not make is still refused. The guard
		// is an allow-list, so a tool added later has to name its endpoint.
		{http.MethodGet, "/system/inputstates", false, "an endpoint nothing here calls"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			var reached bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
			}))
			defer srv.Close()

			client := readOnly(srv.Client(), "")
			req, err := http.NewRequest(tc.method, srv.URL+apiPrefix+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}

			switch {
			case tc.allowed && err != nil:
				t.Fatalf("%s (%s) was refused: %v", tc.path, tc.why, err)
			case tc.allowed && !reached:
				t.Fatalf("%s (%s) did not reach the upstream", tc.path, tc.why)
			case !tc.allowed && err == nil:
				t.Fatalf("%s (%s) was allowed through", tc.path, tc.why)
			case !tc.allowed && reached:
				t.Fatalf("%s (%s) was refused but still reached the upstream", tc.path, tc.why)
			}
		})
	}
}

// A path reaching the guard by a different spelling is compared in the form
// the server will route on. An anchored pattern and a percent-escaped
// separator disagree about how many segments a path has, and the server sides
// with the escape.
func TestGuard_NormalisesBeforeMatching(t *testing.T) {
	for _, path := range []string{
		"//views//fields//poll",
		"/views/fields/poll/",
		"/views/fields/poll?force=true",
		"/views/fields%2Fpoll",
	} {
		t.Run(path, func(t *testing.T) {
			var reached bool
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				reached = true
			}))
			defer srv.Close()

			req, err := http.NewRequest(http.MethodPost, srv.URL+apiPrefix+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := readOnly(srv.Client(), "").Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil || reached {
				t.Fatalf("%s reached the field-cache refresh past the guard", path)
			}
		})
	}
}

// A path the guard knows, reached with a method it does not, is the shape a
// bug in this package takes. Saying what the path is for is what makes it
// findable; "not allowed" alone sends somebody looking at the allow-list
// rather than at the call site.
func TestGuard_NamesWhatAKnownPathIsFor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+apiPrefix+"/search/messages", nil)
	resp, err := readOnly(srv.Client(), "").Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("DELETE on a read endpoint was allowed")
	}
	if !strings.Contains(err.Error(), "running a search") {
		t.Errorf("the refusal should say what the path is for, got: %v", err)
	}
}

// Not following redirects is not tidiness. A redirect to another host would
// carry the Authorization header somewhere the operator never named, and a
// redirect to a sign-in page turns a diagnosable refusal into an HTML body
// parsed as JSON.
func TestGuard_DoesNotFollowRedirects(t *testing.T) {
	var elsewhere bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		elsewhere = true
	}))
	defer other.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, other.URL+"/sso/login", http.StatusFound)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+apiPrefix+"/system", nil)
	resp, err := readOnly(srv.Client(), "").Do(req)
	if err != nil {
		t.Fatalf("the redirect should be returned, not raised: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want the redirect itself", resp.StatusCode)
	}
	if elsewhere {
		t.Error("the credential was carried to a host nobody configured")
	}
}

// A Graylog behind a reverse proxy at /graylog is an ordinary deployment, and
// its requests arrive as /graylog/api/search/messages. A guard that trimmed a
// fixed "/api" would leave that unmatched by every pattern it holds, so the
// installation would have every single request refused by its own transport --
// and the message would say the endpoint was not permitted, which is exactly
// the wrong place to send somebody looking.
func TestGuard_WorksBehindAReverseProxy(t *testing.T) {
	var reached string
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		reached = r.URL.Path
	}))
	defer srv.Close()

	client := readOnly(srv.Client(), "/graylog")
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/graylog"+apiPrefix+"/search/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("a proxied search was refused: %v", err)
	}
	if reached != "/graylog/api/search/messages" {
		t.Errorf("reached %q, want the proxied path", reached)
	}
}

// The prefix is not decoration: a request that is not under this instance's
// own API root is refused outright rather than trimmed to something whose tail
// happens to look familiar.
func TestGuard_RefusesWhatIsNotUnderTheAPIRoot(t *testing.T) {
	for _, tc := range []struct{ prefix, path string }{
		{"/graylog", "/api/search/messages"},
		{"/graylog", "/elsewhere/api/search/messages"},
		// CutPrefix alone would accept this and hand the allow-list
		// "foo/search/messages".
		{"", "/apifoo/search/messages"},
	} {
		t.Run(tc.prefix+" "+tc.path, func(t *testing.T) {
			var reached bool
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				reached = true
			}))
			defer srv.Close()

			req, _ := http.NewRequest(http.MethodPost, srv.URL+tc.path, nil)
			resp, err := readOnly(srv.Client(), tc.prefix).Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			if err == nil || reached {
				t.Fatalf("%s was allowed through with prefix %q", tc.path, tc.prefix)
			}
		})
	}
}

// The path a guard is built for comes from the configured address, so the two
// cannot disagree. A base path with a trailing slash is the spelling somebody
// pastes out of a browser.
func TestBasePath(t *testing.T) {
	for in, want := range map[string]string{
		"https://graylog.example":      "",
		"https://graylog.example:9000": "",
		"https://example.com/graylog":  "/graylog",
		"https://example.com/graylog/": "/graylog/",
		"https://example.com/a/b":      "/a/b",
	} {
		if got := basePath(in); got != want {
			t.Errorf("basePath(%q) = %q, want %q", in, got, want)
		}
	}
}
