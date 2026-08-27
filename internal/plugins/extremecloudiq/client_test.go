package extremecloudiq

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Every collection is paginated at a hundred and an estate is not, so a
// listing that stopped at the first page would report a fraction of the estate
// as the whole of it.
func TestCollect_WalksEveryPage(t *testing.T) {
	var asked []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		asked = append(asked, r.URL.Query().Get("page"))
		rows := make([]string, 0, 2)
		for i := range 2 {
			rows = append(rows, fmt.Sprintf(`{"id":%d}`, (page-1)*2+i+1))
		}
		fmt.Fprintf(w, `{"page":%d,"count":2,"total_pages":3,"total_count":6,"data":[%s]}`,
			page, strings.Join(rows, ","))
	})

	got, err := c.Collect(context.Background(), "/devices", url.Values{}, 0, 2, 0)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got.Rows) != 6 {
		t.Errorf("collected %d rows across %v, want all 6", len(got.Rows), asked)
	}
	if got.Total != 6 {
		t.Errorf("Total = %d, want 6", got.Total)
	}
	if got.Truncated {
		t.Error("a complete walk reported itself truncated")
	}
}

// A short page ends the walk even when the API says nothing about how many
// pages there are. Several of these collections leave total_pages out, and a
// walk that only trusted it would loop until the row limit on every one.
func TestCollect_StopsOnAShortPage(t *testing.T) {
	var requests int
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = io.WriteString(w, `{"page":1,"count":1,"data":[{"id":1}]}`)
	})

	got, err := c.Collect(context.Background(), "/devices", nil, 0, 10, 0)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if requests != 1 {
		t.Errorf("made %d requests for a page that was not full", requests)
	}
	if len(got.Rows) != 1 {
		t.Errorf("collected %d rows, want 1", len(got.Rows))
	}
}

// Truncation says which ceiling stopped it and how many exist. "Here are 200
// of 4,317 devices" is an answer somebody can narrow; 200 devices with nothing
// said about the rest is a wrong answer to "how many access points do we have"
// and a model has no way to tell the two apart.
func TestCollect_SaysWhatItStoppedShortOf(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		rows := make([]string, 0, 10)
		for i := range 10 {
			rows = append(rows, fmt.Sprintf(`{"id":%d}`, (page-1)*10+i))
		}
		fmt.Fprintf(w, `{"page":%d,"count":10,"total_count":4317,"data":[%s]}`,
			page, strings.Join(rows, ","))
	})

	got, err := c.Collect(context.Background(), "/devices", nil, 25, 10, 0)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got.Rows) != 25 {
		t.Fatalf("collected %d rows, want the limit of 25", len(got.Rows))
	}
	if !got.Truncated {
		t.Fatal("stopping at 25 of 4317 was not reported as truncation")
	}
	if !strings.Contains(got.Reason, "4317") {
		t.Errorf("the reason does not say how many exist: %q", got.Reason)
	}
	if got.Total != 4317 {
		t.Errorf("Total = %d, want 4317", got.Total)
	}
}

// A result is charged twice on the wire and cut by the client past its own
// ceiling, so the size ceiling has to bite before the row ceiling does when
// the rows are large.
func TestCollect_StopsOnSizeAsWellAsCount(t *testing.T) {
	fat := strings.Repeat("x", 2000)
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		rows := make([]string, 0, 10)
		for i := range 10 {
			rows = append(rows, fmt.Sprintf(`{"id":%d,"blob":%q}`, i, fat))
		}
		fmt.Fprintf(w, `{"page":1,"count":10,"total_count":10,"data":[%s]}`,
			strings.Join(rows, ","))
	})

	got, err := c.Collect(context.Background(), "/devices", nil, 100, 10, 5000)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(got.Rows) >= 10 {
		t.Fatalf("collected %d rows; the size ceiling did not bite", len(got.Rows))
	}
	if !strings.Contains(got.Reason, "large") {
		t.Errorf("the reason does not say the rows were large: %q", got.Reason)
	}
	// An answer of nothing at all, because the one matching record was large,
	// is worse than an answer of one large record.
	if len(got.Rows) == 0 {
		t.Error("the first row was refused for size; it never should be")
	}
}

// The token is a plain bearer token here, unlike Graylog's, where an access
// token is presented as a basic-auth username. Getting it wrong produces a 401
// that says nothing about which half was wrong.
func TestClient_SendsABearerToken(t *testing.T) {
	var header string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{}`)
	})
	if _, err := c.Get(context.Background(), "/devices/stats", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if header != "Bearer tok" {
		t.Errorf("Authorization = %q, want a bearer token", header)
	}
}

// Debug logging is what a support call turns on, and it must never carry the
// answer or the question. A successful body here is somebody's estate --
// hostnames, MAC addresses, the names of the people connected to it -- and the
// query names their sites.
func TestClient_DebugLogsCarryNoEstateData(t *testing.T) {
	var logged bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"hostname":"ap-reception-3","mac_address":"AA:BB:CC:DD:EE:FF"}]}`)
	}))
	t.Cleanup(srv.Close)

	cfg := testConfig(srv.URL)
	c := NewClient(srv.Client(), cfg, "s3cret-token",
		slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
		at(fixedNow), nil, func(string, time.Duration) {})

	if _, err := c.Get(context.Background(), "/devices",
		url.Values{"hostnames": {"ap-reception-3"}}); err != nil {
		t.Fatalf("Get: %v", err)
	}

	for _, secret := range []string{"ap-reception-3", "AA:BB:CC:DD:EE:FF", "s3cret-token"} {
		if strings.Contains(logged.String(), secret) {
			t.Errorf("the debug log carries %q:\n%s", secret, logged.String())
		}
	}
	// It still has to be useful, or nobody turns it on twice.
	if !strings.Contains(logged.String(), "/devices") {
		t.Errorf("the debug log does not say what was called:\n%s", logged.String())
	}
}

// Each failure has a different fix, and the raw response says none of them.
// The 401 in particular: an expired token and a revoked one are the same
// status, so the message has to name both or somebody checks one and stops.
func TestExplainRequestFailure_SaysWhatToDo(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusUnauthorized, ``, "expires at the time it was created"},
		{http.StatusForbidden, ``, "scopes"},
		{http.StatusTooManyRequests, ``, "per account per hour"},
		{http.StatusBadRequest, `{"error_message":"startTime is required"}`, "milliseconds"},
		{http.StatusFound, ``, "does not redirect"},
		{http.StatusNotFound, ``, "does not exist in this account"},
		{http.StatusInternalServerError, `{"error_message":"boom","error_id":"abc123"}`, "abc123"},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			err := explainRequestFailure(tc.status, "/devices", []byte(tc.body))
			if err == nil {
				t.Fatal("a failure produced no error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the message does not say %q: %v", tc.want, err)
			}
		})
	}
}

// An HTML body means something other than the API answered, and saying which
// is more useful than quoting the first 200 bytes of a <head>.
func TestSummarise_NamesAnHTMLPageRatherThanQuotingIt(t *testing.T) {
	got := summarise(http.StatusOK, []byte("<!DOCTYPE html><html><head><title>Sign in"))
	if !strings.Contains(got, "HTML page") {
		t.Errorf("an HTML body was quoted rather than named: %q", got)
	}
}

// The startup probe proves the credential without reading a row of anybody's
// estate, and it refuses a JSON API that is not this one rather than leaving
// that to be discovered by the first tool call.
func TestProbe_RefusesSomethingThatIsNotTheAPI(t *testing.T) {
	c, _ := testClient(t, jsonOK(`{"status":"ok","service":"something else"}`))
	if _, err := c.Probe(context.Background()); err == nil {
		t.Fatal("a JSON body with none of the API's fields was accepted as ExtremeCloud IQ")
	}

	good, _ := testClient(t, jsonOK(
		`{"user_name":"api@example.net","role":"MONITOR","owner_id":42,`+
			`"data_center":"US-EAST","expires_in":2592000}`))
	info, err := good.Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if info.DataCenter != "US-EAST" {
		t.Errorf("data centre = %q; it is what explains two tokens on one "+
			"address reading two different estates", info.DataCenter)
	}
	// An expiry is only useful before it happens; afterwards it is a 401
	// indistinguishable from a revoked token.
	expiry, ok := info.Expiry(fixedNow)
	if !ok {
		t.Fatal("a token with expires_in reported no expiry")
	}
	if want := fixedNow.Add(30 * 24 * time.Hour); !expiry.Equal(want) {
		t.Errorf("expiry = %s, want %s", expiry, want)
	}
}
