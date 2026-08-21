package cnmaestro

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeAPI stands in for cnMaestro. It answers the token endpoint and whatever
// data routes a test registers.
type fakeAPI struct {
	server *httptest.Server
	mux    *http.ServeMux

	tokenCalls atomic.Int32
	// redirectTo is what the token response names as the regional host. Empty
	// means the field is absent.
	redirectTo   string
	tokenStatus  int
	expiresIn    int
	lastAuthHdr  atomic.Value
	lastQuery    atomic.Value
	dataRequests atomic.Int32
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{mux: http.NewServeMux(), tokenStatus: http.StatusOK, expiresIn: 3600}

	f.mux.HandleFunc("POST /api/v2/access/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenCalls.Add(1)
		f.lastAuthHdr.Store(r.Header.Get("Authorization"))
		if f.tokenStatus != http.StatusOK {
			w.WriteHeader(f.tokenStatus)
			_, _ = io.WriteString(w, `{"error":{"message":"nope"}}`)
			return
		}
		resp := map[string]any{
			"access_token": "tok-" + strconv.Itoa(int(f.tokenCalls.Load())),
			"token_type":   "bearer",
			"expires_in":   f.expiresIn,
		}
		if f.redirectTo != "" {
			resp["redirect_uri"] = f.redirectTo
		}
		_ = json.NewEncoder(w).Encode(resp)
	})

	f.server = httptest.NewServer(f.mux)
	t.Cleanup(f.server.Close)
	return f
}

// handle registers a data route, recording the query it was called with.
func (f *fakeAPI) handle(path string, fn func(w http.ResponseWriter, r *http.Request)) {
	f.mux.HandleFunc("GET /api/v2"+path, func(w http.ResponseWriter, r *http.Request) {
		f.dataRequests.Add(1)
		f.lastQuery.Store(r.URL.Query())
		fn(w, r)
	})
}

func (f *fakeAPI) query() url.Values {
	v, _ := f.lastQuery.Load().(url.Values)
	return v
}

func testClient(t *testing.T, f *fakeAPI, mutate func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		BaseURL:      f.server.URL,
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		// Fast enough that a paging test is not a sleep.
		RequestsPerSecond: 1000,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	cfg.withDefaults()
	return NewClient(f.server.Client(), cfg, "client-id", "client-secret",
		discardLogger(), time.Now)
}

// The credentials go in the Authorization header rather than the body. Both
// are accepted upstream, and a body is the thing most likely to be logged by a
// proxy or captured in a diagnostic.
func TestToken_SendsBasicCredentials(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/networks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"paging":{"total":0}}`)
	})
	c := testClient(t, f, nil)

	if _, err := c.List(context.Background(), "/networks", nil); err != nil {
		t.Fatalf("List: %v", err)
	}

	hdr, _ := f.lastAuthHdr.Load().(string)
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("client-id:client-secret"))
	if hdr != want {
		t.Fatalf("Authorization = %q, want basic credentials", hdr)
	}
}

// The bug this guards is the classic first-integration failure: tokens are
// issued by the front door and name a regional host, and a client that keeps
// calling the front door holds a valid token pointed at the wrong shard.
func TestToken_DataCallsFollowRedirectURI(t *testing.T) {
	region := newFakeAPI(t)
	region.handle("/networks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"name":"regional"}],"paging":{"total":1}}`)
	})

	front := newFakeAPI(t)
	front.redirectTo = region.server.URL
	front.handle("/networks", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("data call reached the token host instead of the region named by redirect_uri")
		w.WriteHeader(http.StatusInternalServerError)
	})

	c := testClient(t, front, nil)
	page, err := c.List(context.Background(), "/networks", nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 1 || !strings.Contains(fmt.Sprint(page.Items[0]), "regional") {
		t.Fatalf("items = %v, want the regional host's answer", page.Items)
	}
	if c.APIHost() != region.server.URL {
		t.Errorf("APIHost = %q, want %q", c.APIHost(), region.server.URL)
	}
}

// A redirect_uri that does not parse is ignored rather than fatal. The token
// is usable and the front door works for a single-region account, so refusing
// would turn a cosmetic upstream change into an outage.
func TestToken_UnusableRedirectFallsBack(t *testing.T) {
	f := newFakeAPI(t)
	f.redirectTo = "not a url"
	f.handle("/networks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"paging":{"total":0}}`)
	})
	c := testClient(t, f, nil)

	if _, err := c.List(context.Background(), "/networks", nil); err != nil {
		t.Fatalf("List: %v", err)
	}
	if c.APIHost() != f.server.URL {
		t.Errorf("APIHost = %q, want the token host as a fallback", c.APIHost())
	}
}

// A burst of calls must produce one token request, not one per call.
func TestToken_IsReusedAcrossCalls(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/networks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"paging":{"total":0}}`)
	})
	c := testClient(t, f, nil)

	for range 5 {
		if _, err := c.List(context.Background(), "/networks", nil); err != nil {
			t.Fatalf("List: %v", err)
		}
	}
	if got := f.tokenCalls.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
}

// A credential rejected before its expiry has been revoked or rotated
// upstream. Holding it would mean every later call fails the same way.
func TestUnauthorizedDropsTheCachedToken(t *testing.T) {
	f := newFakeAPI(t)
	var calls atomic.Int32
	f.handle("/networks", func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":[],"paging":{"total":0}}`)
	})
	c := testClient(t, f, nil)

	if _, err := c.List(context.Background(), "/networks", nil); err == nil {
		t.Fatal("a 401 must surface as an error")
	}
	if _, err := c.List(context.Background(), "/networks", nil); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := f.tokenCalls.Load(); got != 2 {
		t.Fatalf("token requests = %d, want a fresh token after the 401", got)
	}
}

// Offset paging: walk until the reported total is reached.
func TestList_FollowsOffsetPaging(t *testing.T) {
	f := newFakeAPI(t)
	const total = 7
	f.handle("/devices", func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var items []string
		for i := offset; i < min(offset+limit, total); i++ {
			items = append(items, fmt.Sprintf(`{"n":%d}`, i))
		}
		fmt.Fprintf(w, `{"data":[%s],"paging":{"offset":%d,"limit":%d,"total":%d}}`,
			strings.Join(items, ","), offset, limit, total)
	})
	c := testClient(t, f, func(cfg *Config) { cfg.PageSize = 3 })

	page, err := c.List(context.Background(), "/devices", nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != total {
		t.Fatalf("got %d items, want %d", len(page.Items), total)
	}
	if page.Total != total {
		t.Errorf("Total = %d, want %d", page.Total, total)
	}
}

// Continuation paging: follow the token until a response carries none.
//
// Four endpoints use this and offset is removed from them in 6.4.0. The scheme
// is chosen by what the response carries rather than by a table of endpoints,
// so one moving across does not need this client changed.
func TestList_FollowsContinuationTokens(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/events", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("continuation_token") {
		case "":
			_, _ = io.WriteString(w,
				`{"data":[{"n":1}],"paging":{"next_continuation_token":"c1","offset":0,"total":3}}`)
		case "c1":
			_, _ = io.WriteString(w,
				`{"data":[{"n":2}],"paging":{"next_continuation_token":"c2"}}`)
		case "c2":
			_, _ = io.WriteString(w, `{"data":[{"n":3}],"paging":{}}`)
		default:
			t.Errorf("unexpected continuation_token %q", r.URL.Query().Get("continuation_token"))
		}
	})
	c := testClient(t, f, nil)

	page, err := c.List(context.Background(), "/events", nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(page.Items))
	}
	// offset and total come back on the first response for backward
	// compatibility. Mixing them with a token would send a contradiction.
	if q := f.query(); q.Get("offset") != "" {
		t.Errorf("offset=%q was sent alongside a continuation token", q.Get("offset"))
	}
}

// MaxItems bounds what one tool call accumulates, and says so. A model that
// mistakes a truncated list for a whole estate reasons from missing devices.
func TestList_TruncatesAndSaysSo(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/devices", func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		var items []string
		for i := range 10 {
			items = append(items, fmt.Sprintf(`{"n":%d}`, offset+i))
		}
		fmt.Fprintf(w, `{"data":[%s],"paging":{"offset":%d,"limit":10,"total":1000}}`,
			strings.Join(items, ","), offset)
	})
	c := testClient(t, f, func(cfg *Config) { cfg.PageSize = 10; cfg.MaxItems = 25 })

	page, err := c.List(context.Background(), "/devices", nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 25 {
		t.Fatalf("got %d items, want the cap of 25", len(page.Items))
	}
	if !page.Truncated {
		t.Error("a capped walk must report itself truncated")
	}
}

// An endpoint reporting a total it cannot deliver would otherwise spin.
func TestList_StopsOnAnEmptyPage(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/devices", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"paging":{"total":500}}`)
	})
	c := testClient(t, f, nil)

	page, err := c.List(context.Background(), "/devices", nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("got %d items, want none", len(page.Items))
	}
	if got := f.dataRequests.Load(); got != 1 {
		t.Errorf("made %d requests, want 1 -- an empty page ends the walk", got)
	}
}

// managed_account goes on every request when configured, because its absence
// means different things depending on whether a request names a network.
func TestManagedAccountIsSentOnEveryRequest(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/networks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"paging":{"total":0}}`)
	})
	c := testClient(t, f, func(cfg *Config) { cfg.ManagedAccount = "Base Infrastructure" })

	if _, err := c.List(context.Background(), "/networks", nil); err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := f.query().Get("managed_account"); got != "Base Infrastructure" {
		t.Fatalf("managed_account = %q, want it sent", got)
	}
}

// The API answers 200 with a partial result when part of an estate is
// unreachable. Dropping the warnings reports incomplete data as complete.
func TestList_PassesWarningsThrough(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/devices", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w,
			`{"data":[{"n":1}],"paging":{"total":1},"warnings":["one site did not respond"]}`)
	})
	c := testClient(t, f, nil)

	page, err := c.List(context.Background(), "/devices", nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Warnings) != 1 || page.Warnings[0] != "one site did not respond" {
		t.Fatalf("warnings = %v, want the API's own passed through", page.Warnings)
	}
}

// The deny-list is enforced in the client, so nothing reaches a blocked
// endpoint even by constructing the path directly.
func TestDo_RefusesBlockedPathsBeforeCalling(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/devices/AA:BB:CC:DD:EE:FF/cli", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a blocked path reached the API")
	})
	c := testClient(t, f, nil)

	if _, err := c.List(context.Background(), "/devices/AA:BB:CC:DD:EE:FF/cli", nil); err == nil {
		t.Fatal("a blocked path must be refused")
	}
	if got := f.dataRequests.Load(); got != 0 {
		t.Errorf("made %d requests, want none", got)
	}
	// And no token was spent establishing that we would refuse anyway.
	if got := f.tokenCalls.Load(); got != 0 {
		t.Errorf("obtained %d tokens for a refused call, want none", got)
	}
}

// Credentials must not reach an error a model reads back or a log.
func TestErrorsDoNotLeakCredentials(t *testing.T) {
	f := newFakeAPI(t)
	f.tokenStatus = http.StatusUnauthorized
	c := testClient(t, f, nil)

	_, err := c.List(context.Background(), "/networks", nil)
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, secret := range []string{"client-secret", "client-id"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaked %q: %v", secret, err)
		}
	}
}

// The three managed_account failures arrive as bare statuses against an
// ordinary read, and each has a different fix the status alone does not
// suggest. Matching the API's own message rather than the status is
// deliberate: the same statuses arrive for entirely unrelated reasons.
func TestManagedAccountFailuresAreExplained(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{
			name: "MSP feature is off", status: http.StatusBadRequest,
			body:    `{"error":{"message":"MSP feature is disabled"}}`,
			wantSub: "does not have the MSP",
		},
		{
			name: "tenant is disabled", status: http.StatusForbidden,
			body:    `{"error":{"message":"managed_account is disabled"}}`,
			wantSub: "disabled",
		},
		{
			name: "no such tenant", status: http.StatusNotFound,
			body:    `{"error":{"message":"managed_account not found"}}`,
			wantSub: "case-sensitive",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := explainRequestFailure(tc.status, "/devices", []byte(tc.body))
			if err == nil {
				t.Fatal("expected a failure")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}

// The same statuses arrive for unrelated reasons, so a body that says nothing
// about managed accounts must not be explained as though it did.
func TestOrdinaryFailuresAreNotMistakenForManagedAccountOnes(t *testing.T) {
	err := explainRequestFailure(http.StatusNotFound, "/devices/AA:BB:CC:DD:EE:FF",
		[]byte(`{"error":{"message":"device not found"}}`))
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), "managed account") {
		t.Fatalf("a device 404 was explained as a managed-account problem: %v", err)
	}
}

// An account named for one request beats the configured default, so an
// assistant can be asked about a tenant the instance is not pinned to.
func TestManagedAccount_NamedPerRequestWins(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/networks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"paging":{"total":0}}`)
	})
	c := testClient(t, f, func(cfg *Config) { cfg.ManagedAccount = MainAccount })

	params := url.Values{"managed_account": {"Acme Networks"}}
	if _, err := c.List(context.Background(), "/networks", params); err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := f.query().Get("managed_account"); got != "Acme Networks" {
		t.Fatalf("managed_account = %q, want the one the caller named", got)
	}
}

// With no account configured and none named, the parameter is absent rather
// than empty. The API treats an empty value as if it were not there, so
// sending it would only suggest an account had been chosen.
func TestManagedAccount_AbsentWhenThereIsNone(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("/networks", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[],"paging":{"total":0}}`)
	})
	c := testClient(t, f, func(cfg *Config) { cfg.ManagedAccount = "" })

	params := url.Values{"managed_account": {""}}
	if _, err := c.List(context.Background(), "/networks", params); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, present := f.query()["managed_account"]; present {
		t.Fatal("managed_account was sent empty; it must be absent instead")
	}
}

// "Check your credentials" is the wrong advice when the credentials are right,
// which is what an expired API client, an On-Premises host, or the wrong
// regional shard all look like. The address and the API's own words are what
// separate those from a mistyped secret.
func TestTokenFailure_NamesTheAddressAndTheReason(t *testing.T) {
	err := explainTokenFailure(http.StatusUnauthorized,
		"https://cloud.cambiumnetworks.com/api/v2/access/token",
		[]byte(`{"error":"invalid_client"}`))

	msg := err.Error()
	for _, want := range []string{"cloud.cambiumnetworks.com", "invalid_client", "On-Premises"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not mention %q", msg, want)
		}
	}
}
