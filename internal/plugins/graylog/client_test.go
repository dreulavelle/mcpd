package graylog

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// A Graylog access token is presented as the *username* of a basic-auth pair
// with the literal password "token". It looks exactly like a bearer token and
// is not one; sending it as one gets a 401 that says nothing about which half
// was wrong, which is a long afternoon.
func TestClient_TokenIsAUsername(t *testing.T) {
	var gotUser, gotPass string
	var gotAuthHeader string
	var ok bool
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, ok = r.BasicAuth()
		gotAuthHeader = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"version":"7.1.0","node_id":"n1"}`))
	})

	if _, err := c.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !ok {
		t.Fatal("no basic auth was sent")
	}
	if gotUser != "tok" || gotPass != "token" {
		t.Errorf("basic auth = %q:%q, want the token as the username and "+
			"the literal \"token\" as the password", gotUser, gotPass)
	}
	if strings.HasPrefix(gotAuthHeader, "Bearer") {
		t.Error("the token was sent as a bearer token, which Graylog rejects")
	}
}

// Graylog refuses a POST without X-Requested-By as a 400 that reads like a
// malformed request. It is sent on every call, GETs included, because a header
// that is only sometimes present is one somebody eventually forgets to add.
func TestClient_AlwaysSendsRequestedBy(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Client) error
	}{
		{"get", func(c *Client) error {
			_, err := c.Get(context.Background(), "/system", nil)
			return err
		}},
		{"post", func(c *Client) error {
			_, err := c.Post(context.Background(), "/search/messages", map[string]any{})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				got = r.Header.Get("X-Requested-By")
				_, _ = w.Write([]byte(`{}`))
			})
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got == "" {
				t.Error("X-Requested-By was not sent; Graylog would refuse this")
			}
		})
	}
}

// A 400 naming the header is a bug in this package, not a bad query. Reporting
// it as "your query was invalid" sends an operator looking at their query for
// a long time.
func TestClient_TellsTheCSRFRefusalApartFromABadQuery(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"ApiError","message":"Missing X-Requested-By header"}`))
	})

	_, err := c.Post(context.Background(), "/search/messages", map[string]any{})
	if err == nil {
		t.Fatal("a 400 was reported as success")
	}
	if !strings.Contains(err.Error(), "bug") {
		t.Errorf("a missing X-Requested-By should be named as a bug here, got: %v", err)
	}
}

// A redirect is the most informative failure this integration gets and it has
// one overwhelmingly likely cause. "Unexpected status 302" would be true and
// useless.
func TestClient_ExplainsARedirect(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/sso/login", http.StatusFound)
	})

	_, err := c.Get(context.Background(), "/system", nil)
	if err == nil {
		t.Fatal("a redirect was reported as success")
	}
	if !strings.Contains(err.Error(), "proxy") {
		t.Errorf("the message should name the likely cause, got: %v", err)
	}
}

// A JSON body that decoded but names neither a version nor a node id is a JSON
// API that is not this one. Saying so at startup beats failing later against
// an endpoint that does not exist, where it reads as a permissions problem.
func TestProbe_RefusesSomethingThatIsNotGraylog(t *testing.T) {
	c, _ := testClient(t, jsonOK(`{"hello":"world"}`))

	_, err := c.Probe(context.Background())
	if err == nil {
		t.Fatal("a non-Graylog JSON API was accepted")
	}
	if !strings.Contains(err.Error(), "probably not Graylog") {
		t.Errorf("the message should say what it thinks happened, got: %v", err)
	}
}

// A search is a question about now. Answering "no errors in the last fifteen
// minutes" from a copy made five minutes ago is the worst answer this
// integration can give, because it is indistinguishable from a true one.
func TestCache_NeverHoldsASearch(t *testing.T) {
	var calls atomic.Int64
	p := cachingPlugin(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"schema":[],"datarows":[],"metadata":{}}`))
	})

	for i := 0; i < 3; i++ {
		if _, err := p.searchMessages(context.Background(), searchArgs{Query: "level:ERROR"}); err != nil {
			t.Fatalf("search: %v", err)
		}
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("upstream calls = %d, want 3; a search must never be served from cache", got)
	}
}

// How the installation is *arranged* changes when somebody changes it, so the
// stream list may be held -- which is what stops a model fanning out three
// tools from making the same request three times.
func TestCache_HoldsTheStreamList(t *testing.T) {
	var calls atomic.Int64
	p := cachingPlugin(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"total":0,"streams":[]}`))
	})

	for i := 0; i < 3; i++ {
		if _, err := p.listStreams(context.Background(), streamsArgs{}); err != nil {
			t.Fatalf("streams: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1", got)
	}
}

// Two callers asking different things must not share an answer. The key is
// built from the request that will actually be made, which is the only
// condition under which a shared cache is not a hole.
func TestCache_KeysOnTheWholeRequest(t *testing.T) {
	var calls atomic.Int64
	p := cachingPlugin(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"total":0,"streams":[]}`))
	})

	ctx := context.Background()
	_, _ = p.listStreams(ctx, streamsArgs{Query: "audit"})
	_, _ = p.listStreams(ctx, streamsArgs{Query: "billing"})
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2; two different questions shared an answer", got)
	}
}

// A failure is not an answer and is not held. A Graylog that is down should be
// reported as down on every call rather than remembered as an installation
// with no streams.
func TestCache_DoesNotHoldAFailure(t *testing.T) {
	var calls atomic.Int64
	p := cachingPlugin(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	})

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := p.listStreams(ctx, streamsArgs{}); err == nil {
			t.Fatal("a 500 was reported as success")
		}
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2; a failure was cached", got)
	}
}

// The body is what a POST is keyed on, and it is hashed rather than kept: a
// key that grows with a request body is one that stops being a key.
func TestRequestDigest_SeparatesBodies(t *testing.T) {
	a := requestDigest(http.MethodPost, "/views/fields", nil, []byte(`{"streams":["a"]}`))
	b := requestDigest(http.MethodPost, "/views/fields", nil, []byte(`{"streams":["b"]}`))
	if a == b {
		t.Fatal("two different bodies produced the same key")
	}
	if len(a) > 200 {
		t.Errorf("the key is %d bytes; the body should be hashed, not kept", len(a))
	}
}

// A successful body is somebody's log data and a query names their fields and
// hostnames. Neither may reach a log line, whatever the level.
func TestClient_NeverLogsABodyOrAQuery(t *testing.T) {
	var logged strings.Builder
	c, _ := testClient(t, jsonOK(`{"datarows":[["secret-hostname","password=hunter2"]]}`))
	c.log = debugLoggerTo(&logged)

	body := map[string]any{"query": "user:alice AND token:abc123"}
	if _, err := c.Post(context.Background(), "/search/messages", body); err != nil {
		t.Fatalf("post: %v", err)
	}
	for _, forbidden := range []string{"secret-hostname", "hunter2", "alice", "abc123"} {
		if strings.Contains(logged.String(), forbidden) {
			t.Errorf("the debug log carried %q; it must never carry a body or a query:\n%s",
				forbidden, logged.String())
		}
	}
	// The line itself should exist -- it is what a support call turns on.
	if !strings.Contains(logged.String(), "graylog API call") {
		t.Errorf("no debug line was written at all:\n%s", logged.String())
	}
}

// marshalling a request must not lose a nested time range, which is the field
// every search depends on and the one a wrong struct tag would silently drop.
func TestMessagesRequest_CarriesTheTimeRange(t *testing.T) {
	raw, err := json.Marshal(messagesRequest{
		Size:      10,
		Fields:    []string{"timestamp"},
		Timerange: timeRange{Type: "relative", Range: 900},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"timerange":{"type":"relative","range":900}`) {
		t.Errorf("the time range did not survive encoding: %s", raw)
	}
}
