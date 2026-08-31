package bandwidth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// dashboardServer answers the token exchange and a set of XML paths.
func dashboardServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			_, _ = io.WriteString(w, `{"access_token":"`+
				jwt(t, `{"accounts":["5009021"]}`)+`","expires_in":3600}`)
			return
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "<Response><ErrorCode>404</ErrorCode></Response>")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func portingPlugin(t *testing.T, srv *httptest.Server) *Plugin {
	t.Helper()
	return newFor(t, Config{
		ClientID: "client", ClientSecret: "shh", DefaultAccountID: "5009021",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())
}

// The question a porting ticket actually asks is "why has this not moved", and
// the order alone does not answer it -- the current status is one word. The
// history and the notes are where the reason is written, which is why one call
// can fetch all three.
func TestGetPortInGathersHistoryAndNotes(t *testing.T) {
	const base = "/api/v2/accounts/5009021/portins/p-1"
	srv := dashboardServer(t, map[string]string{
		base: `<LnpOrderResponse><OrderId>p-1</OrderId>
			<ProcessingStatus>EXCEPTION</ProcessingStatus></LnpOrderResponse>`,
		base + "/history": `<OrderHistoryResponse>
			<OrderHistory><Status>SUBMITTED</Status></OrderHistory>
			<OrderHistory><Status>EXCEPTION</Status></OrderHistory>
		</OrderHistoryResponse>`,
		base + "/notes": `<NotesResponse>
			<Note><Id>1</Id><Description>Losing carrier rejected: name mismatch</Description></Note>
		</NotesResponse>`,
	})
	p := portingPlugin(t, srv)

	got, err := p.getPortIn(context.Background(), PortInInput{
		OrderID: "p-1", WithHistory: true, WithNotes: true,
	})
	if err != nil {
		t.Fatalf("getPortIn: %v", err)
	}
	if got.Order["ProcessingStatus"] != "EXCEPTION" {
		t.Errorf("order = %v", got.Order)
	}
	if len(got.History) != 2 {
		t.Errorf("want 2 history entries, got %d: %v", len(got.History), got.History)
	}
	// One note, which is the single-member case the XML list trap turns into a
	// map. It has to come back as a slice like any other.
	if len(got.Notes) != 1 {
		t.Fatalf("want 1 note, got %d: %v", len(got.Notes), got.Notes)
	}
	if !strings.Contains(got.Notes[0]["Description"].(string), "name mismatch") {
		t.Errorf("note = %v", got.Notes[0])
	}
	if got.Note != "" {
		t.Errorf("nothing failed, so there should be no note: %q", got.Note)
	}
}

// An enrichment that fails must not turn an order that was read into an error,
// and must not be silently dropped either -- a partial answer presented as a
// complete one is worse than the failure.
func TestGetPortInSaysWhichPartItCouldNotRead(t *testing.T) {
	const base = "/api/v2/accounts/5009021/portins/p-2"
	srv := dashboardServer(t, map[string]string{
		base: `<LnpOrderResponse><OrderId>p-2</OrderId></LnpOrderResponse>`,
		// history and notes are deliberately absent -> 404
	})
	p := portingPlugin(t, srv)

	got, err := p.getPortIn(context.Background(), PortInInput{
		OrderID: "p-2", WithHistory: true, WithNotes: true,
	})
	if err != nil {
		t.Fatalf("a failed enrichment turned a readable order into an error: %v", err)
	}
	if got.Order["OrderId"] != "p-2" {
		t.Errorf("the order itself was lost: %v", got.Order)
	}
	for _, want := range []string{"history", "notes"} {
		if !strings.Contains(got.Note, want) {
			t.Errorf("the note does not mention %s: %q", want, got.Note)
		}
	}
}

func TestListPortInsReturnsOrders(t *testing.T) {
	srv := dashboardServer(t, map[string]string{
		"/api/v2/accounts/5009021/portins": `<LnpOrderSummaryResponse>
			<LnpOrderSummary><OrderId>p-1</OrderId><ProcessingStatus>COMPLETE</ProcessingStatus></LnpOrderSummary>
			<LnpOrderSummary><OrderId>p-2</OrderId><ProcessingStatus>PENDING</ProcessingStatus></LnpOrderSummary>
		</LnpOrderSummaryResponse>`,
	})
	p := portingPlugin(t, srv)

	got, err := p.listPortIns(context.Background(), PortInsInput{Status: "PENDING"})
	if err != nil {
		t.Fatalf("listPortIns: %v", err)
	}
	if got.Returned != 2 {
		t.Fatalf("want 2 orders, got %d: %v", got.Returned, got.Items)
	}
}

// A sip_peer_id without its site is a request that cannot be built: peer ids
// are unique within a site, not across the account. Saying so beats a 404.
func TestListNumbersRefusesASipPeerWithoutItsSite(t *testing.T) {
	srv := dashboardServer(t, nil)
	p := portingPlugin(t, srv)

	_, err := p.listNumbers(context.Background(), NumbersInput{SipPeerID: "500017"})
	if err == nil || !strings.Contains(err.Error(), "site_id") {
		t.Fatalf("want a message naming site_id, got %v", err)
	}
}

// Half of Bandwidth speaks JSON and half speaks XML. A Dashboard read that
// asks for JSON is answered with a refusal rather than a translation, so the
// Accept header has to match the half being asked -- which it did not, and
// every Dashboard call failed.
func TestAcceptMatchesTheHalfOfBandwidthBeingAsked(t *testing.T) {
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			_, _ = io.WriteString(w, `{"access_token":"`+
				jwt(t, `{"accounts":["5009021"]}`)+`","expires_in":3600}`)
			return
		}
		seen[r.URL.Path] = r.Header.Get("Accept")
		if strings.Contains(r.URL.Path, "/portins") {
			_, _ = io.WriteString(w, `<LnpOrderSummaryResponse></LnpOrderSummaryResponse>`)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	t.Cleanup(srv.Close)
	p := portingPlugin(t, srv)

	if _, err := p.listCalls(context.Background(), CallsInput{}); err != nil {
		t.Fatalf("listCalls: %v", err)
	}
	if _, err := p.listPortIns(context.Background(), PortInsInput{}); err != nil {
		t.Fatalf("listPortIns: %v", err)
	}

	for path, accept := range seen {
		want := acceptJSON
		if strings.Contains(path, "/portins") {
			want = acceptXML
		}
		if accept != want {
			t.Errorf("%s asked for %q, want %q", path, accept, want)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("want 2 upstream calls, got %d: %v", len(seen), seen)
	}
}

// Insights answers "how much" and "is it getting worse", which list_calls
// cannot: it returns individual calls. The filters go on the parameter name
// rather than the value, which is the API's own deepObject convention and easy
// to get wrong silently -- a mistyped name is ignored, not refused, so the
// answer comes back unfiltered and looks right.
func TestAggregateCallsSendsDeepObjectFilters(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			_, _ = io.WriteString(w, `{"access_token":"`+
				jwt(t, `{"accounts":["5009021"]}`)+`","expires_in":3600}`)
			return
		}
		gotPath, gotQuery = r.URL.Path, r.URL.Query()
		_, _ = io.WriteString(w, `[{"timestamp":"2026-08-01T00:00:00Z","value":42}]`)
	}))
	t.Cleanup(srv.Close)

	p := newFor(t, Config{
		ClientID: "client", ClientSecret: "shh",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL, InsightsURL: srv.URL,
	}, srv.Client())

	got, err := p.aggregateCalls(context.Background(), AggregateCallsInput{
		Metric: "minutes", Direction: "outbound", CallType: "tollfree-out",
		Since: "2026-08-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("aggregateCalls: %v", err)
	}
	if gotPath != "/v1/monitors/voice/minutes-of-use" {
		t.Errorf("path = %q", gotPath)
	}
	for name, want := range map[string]string{
		"accountId[eq]":  "5009021",
		"direction[eq]":  "OUTBOUND",
		"callType[eq]":   "TOLLFREE-OUT",
		"timestamp[gte]": "2026-08-01T00:00:00Z",
	} {
		if got := gotQuery.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if len(got.Series) != 1 {
		t.Errorf("series = %v", got.Series)
	}
}

// An unknown metric is refused with the set named, rather than sent upstream
// to 404 on a path segment nobody can guess.
func TestAggregateCallsNamesTheMetricsItKnows(t *testing.T) {
	srv := dashboardServer(t, nil)
	p := portingPlugin(t, srv)

	_, err := p.aggregateCalls(context.Background(), AggregateCallsInput{Metric: "everything"})
	if err == nil {
		t.Fatal("an unknown metric was accepted")
	}
	for _, want := range []string{"minutes", "connection_rate"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the message does not name %s: %v", want, err)
		}
	}
}

// A window beyond what Insights keeps comes back empty rather than refused,
// and "no traffic" reads identically to "not kept". The answer says which.
func TestAggregateCallsWarnsPastTheRetentionWindow(t *testing.T) {
	if note := historyNote(fixedNow.Add(-2*365*24*time.Hour).Format(time.RFC3339), fixedNow); note == "" {
		t.Error("a two-year-old window drew no warning")
	}
	if note := historyNote(fixedNow.Add(-24*time.Hour).Format(time.RFC3339), fixedNow); note != "" {
		t.Errorf("a one-day window should need no warning: %q", note)
	}
}

// Bandwidth answers an empty collection with 204 and no body. Decoding that as
// XML fails, and reporting the failure turns "you have no ports in flight" --
// the ordinary state of most accounts -- into "the integration is broken".
func TestAnEmptyCollectionIsNotAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			_, _ = io.WriteString(w, `{"access_token":"`+
				jwt(t, `{"accounts":["5009021"]}`)+`","expires_in":3600}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	p := portingPlugin(t, srv)

	got, err := p.listPortIns(context.Background(), PortInsInput{})
	if err != nil {
		t.Fatalf("204 was reported as a failure: %v", err)
	}
	if got.Returned != 0 || len(got.Items) != 0 {
		t.Fatalf("want an empty listing, got %+v", got)
	}
}

// A 403 is Bandwidth's only signal that a role is missing and it does not say
// which, so the body it does send is worth quoting before this package guesses.
func TestForbiddenQuotesWhatBandwidthSaid(t *testing.T) {
	err := explainRequestFailure(http.StatusForbidden,
		"/api/v2/accounts/5009125/tendlc/campaigns/CMOECJY",
		[]byte(`{"message":"missing scope tendlc:read"}`))
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "missing scope tendlc:read") {
		t.Errorf("the upstream's own words were dropped: %v", err)
	}
	// 10DLC reads are governed by Campaign management, which was not mapped.
	if !strings.Contains(err.Error(), "Campaign management") {
		t.Errorf("the 10DLC role was not suggested: %v", err)
	}
	// A role guess reads as a diagnosis. Somebody whose credential already has
	// every role would go hunting for a role that does not exist, so the
	// message has to name the other explanation itself.
	if !strings.Contains(err.Error(), "not enabled for the product") {
		t.Errorf("the message does not cover a fully scoped credential: %v", err)
	}
}

// A 400 says which parameter it objected to, and no guess made from the path
// beats being told.
func TestBadRequestLeadsWithTheUpstreamsExplanation(t *testing.T) {
	err := explainRequestFailure(http.StatusBadRequest,
		"/api/v2/accounts/5009021/tnoptions/4053959300",
		[]byte("<Response><Description>Invalid order id</Description></Response>"))
	if err == nil || !strings.Contains(err.Error(), "Invalid order id") {
		t.Fatalf("the explanation was dropped: %v", err)
	}
}
