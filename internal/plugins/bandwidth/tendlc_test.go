package bandwidth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingServer answers the token exchange, then hands the campaign query
// to body so a test can assert on what was actually asked for upstream. The
// paging defects these tests defend against are only visible in the request,
// not in the response.
func recordingServer(t *testing.T, body func(query map[string]string) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			_, _ = io.WriteString(w, `{"access_token":"`+
				jwt(t, `{"accounts":["5009021"]}`)+`","expires_in":3600}`)
			return
		}
		q := make(map[string]string, len(r.URL.Query()))
		for k, v := range r.URL.Query() {
			if len(v) > 0 {
				q[k] = v[0]
			}
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, body(q))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func tenDLCPlugin(t *testing.T, srv *httptest.Server) *Plugin {
	t.Helper()
	return newFor(t, Config{
		ClientID: "client", ClientSecret: "shh", DefaultAccountID: "5009021",
		APIURL: srv.URL, VoiceURL: srv.URL, MessagingURL: srv.URL,
	}, srv.Client())
}

// Bandwidth's page is zero-based on this endpoint, and the shared setPage
// helper floored it at 1 -- so page 1 asked for the second page and the first
// was unreachable at any page size. On a live account that hid the first eight
// of twenty-one campaigns behind a listing that reported TotalCount 21 and
// returned 13, without failing.
func TestCampaignPagingIsZeroBasedUpstream(t *testing.T) {
	var gotPage, gotSize string
	srv := recordingServer(t, func(q map[string]string) string {
		gotPage, gotSize = q["page"], q["size"]
		return `<CampaignsResponse><Campaigns>
			<Campaign><CampaignId>C1</CampaignId></Campaign>
		</Campaigns><TotalCount>21</TotalCount></CampaignsResponse>`
	})
	p := tenDLCPlugin(t, srv)

	if _, err := p.listCampaigns(context.Background(), CampaignsInput{Page: 1}); err != nil {
		t.Fatalf("listCampaigns: %v", err)
	}
	if gotPage != "0" {
		t.Errorf("page 1 asked upstream for page %q, want %q", gotPage, "0")
	}
	if gotSize != "8" {
		t.Errorf("size = %q, want the endpoint's maximum %q", gotSize, "8")
	}
}

// The plugin-wide ceiling defaults to 200, which this endpoint answers with
// "size must be between 1 and 8" inside a 200 -- so every list_campaigns and
// list_brands call that did not name a smaller limit failed outright.
func TestCampaignPageSizeIsCappedAtTheEndpointsMaximum(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		want  string
	}{
		{"unset falls back to the maximum", 0, "8"},
		{"a larger ask is capped", 200, "8"},
		{"a smaller ask is honoured", 3, "3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotSize string
			srv := recordingServer(t, func(q map[string]string) string {
				gotSize = q["size"]
				return `<CampaignsResponse><Campaigns/></CampaignsResponse>`
			})
			p := tenDLCPlugin(t, srv)
			if _, err := p.listCampaigns(context.Background(),
				CampaignsInput{Limit: tc.limit}); err != nil {
				t.Fatalf("listCampaigns: %v", err)
			}
			if gotSize != tc.want {
				t.Errorf("size = %q, want %q", gotSize, tc.want)
			}
		})
	}
}

// An agent told to read every campaign has to know whether it has. TotalCount
// is a fact where the endpoint sends one, and "the page was full" is a guess;
// has_more must come from the first when it exists.
func TestCampaignListingReportsWhereItIsInTheListing(t *testing.T) {
	srv := recordingServer(t, func(map[string]string) string {
		return `<CampaignsResponse><Campaigns>
			<Campaign><CampaignId>C1</CampaignId></Campaign>
			<Campaign><CampaignId>C2</CampaignId></Campaign>
		</Campaigns><TotalCount>21</TotalCount></CampaignsResponse>`
	})
	p := tenDLCPlugin(t, srv)

	got, err := p.listCampaigns(context.Background(), CampaignsInput{Page: 1, Limit: 2})
	if err != nil {
		t.Fatalf("listCampaigns: %v", err)
	}
	if got.TotalCount != 21 {
		t.Errorf("total_count = %d, want 21 (a string body must still parse)", got.TotalCount)
	}
	if !got.HasMore || got.NextPage != 2 {
		t.Errorf("has_more=%v next_page=%d, want true/2", got.HasMore, got.NextPage)
	}
	if got.Page != 1 || got.PageSize != 2 {
		t.Errorf("page=%d page_size=%d, want 1/2", got.Page, got.PageSize)
	}
}

// The last page must not invite a page that does not exist.
func TestCampaignListingStopsAtTheEnd(t *testing.T) {
	srv := recordingServer(t, func(map[string]string) string {
		return `<CampaignsResponse><Campaigns>
			<Campaign><CampaignId>C21</CampaignId></Campaign>
		</Campaigns><TotalCount>21</TotalCount></CampaignsResponse>`
	})
	p := tenDLCPlugin(t, srv)

	got, err := p.listCampaigns(context.Background(), CampaignsInput{Page: 3, Limit: 8})
	if err != nil {
		t.Fatalf("listCampaigns: %v", err)
	}
	if got.HasMore || got.NextPage != 0 {
		t.Errorf("page 3 of 21 at size 8 is the last: has_more=%v next=%d",
			got.HasMore, got.NextPage)
	}
}

// The raw reason is what a carrier wrote and stays untouched; the parse sits
// beside it, keyed by campaign, so the two are never confused for each other.
func TestDeclineReasonsAreParsedWithoutTouchingTheRaw(t *testing.T) {
	const raw = `Bandwidth: Rejection Code 8102: Invalid Sample Messages - No sample found for these usecases: ['2FA'].
Rejection Code 5105: Missing Mandatory Message Terminology - The opt-in message must contain disclosures on message frequency.`
	srv := recordingServer(t, func(map[string]string) string {
		return `<CampaignsResponse><Campaigns><Campaign>
			<CampaignId>CHBGUEB</CampaignId>
			<SecondaryDcaSharingStatus>DECLINED</SecondaryDcaSharingStatus>
			<SecondaryDcaDeclineReason>` + raw + `</SecondaryDcaDeclineReason>
		</Campaign></Campaigns></CampaignsResponse>`
	})
	p := tenDLCPlugin(t, srv)

	got, err := p.listCampaigns(context.Background(), CampaignsInput{})
	if err != nil {
		t.Fatalf("listCampaigns: %v", err)
	}
	if s, _ := got.Items[0]["SecondaryDcaDeclineReason"].(string); !strings.Contains(s, "Rejection Code 8102") {
		t.Fatalf("the raw reason must survive verbatim, got %q", s)
	}
	rs := got.DeclineReasons["CHBGUEB"]
	if len(rs) != 2 {
		t.Fatalf("parsed %d reasons, want 2: %+v", len(rs), rs)
	}
	if rs[0].Code != "8102" || rs[0].Title != "Invalid Sample Messages" {
		t.Errorf("first reason = %+v", rs[0])
	}
	if rs[0].Source != "Bandwidth" {
		t.Errorf("source = %q, want Bandwidth", rs[0].Source)
	}
	if rs[1].Code != "5105" || !strings.Contains(rs[1].Description, "message frequency") {
		t.Errorf("second reason = %+v", rs[1])
	}
}

// A filter that quietly returns fewer rows than the page held would read as
// "there are none". It says what it dropped, and that it only saw one page.
func TestDCAStatusFilterSaysWhatItDropped(t *testing.T) {
	srv := recordingServer(t, func(map[string]string) string {
		return `<CampaignsResponse><Campaigns>
			<Campaign><CampaignId>C1</CampaignId><SecondaryDcaSharingStatus>DECLINED</SecondaryDcaSharingStatus></Campaign>
			<Campaign><CampaignId>C2</CampaignId><SecondaryDcaSharingStatus>ACCEPTED</SecondaryDcaSharingStatus></Campaign>
		</Campaigns></CampaignsResponse>`
	})
	p := tenDLCPlugin(t, srv)

	got, err := p.listCampaigns(context.Background(),
		CampaignsInput{DCAStatus: "declined"})
	if err != nil {
		t.Fatalf("listCampaigns: %v", err)
	}
	if got.Returned != 1 || got.Items[0]["CampaignId"] != "C1" {
		t.Fatalf("filter kept %+v", got.Items)
	}
	if !strings.Contains(got.Note, "page through") {
		t.Errorf("note should warn the filter saw one page only: %q", got.Note)
	}
}

func TestParseDeclineReasons(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		codes []string
		src   string
	}{
		{
			name:  "a DCA writes prose with the code in parentheses",
			raw:   "DCA2: Unable to verify, needs compliant and accurate CTA information.  (806)",
			codes: []string{"806"},
			src:   "DCA2",
		},
		{
			name: "the registry writes one line per code",
			raw: "Bandwidth: Rejection Code 2120: Invalid Call To Action - No opt-in URL found.\n" +
				"Rejection Code 1100: Brand Inconsistencies - No website URL found.",
			codes: []string{"2120", "1100"},
			src:   "Bandwidth",
		},
		{
			name:  "an unrecognised reason is still a reason",
			raw:   "something nobody has seen before",
			codes: []string{""},
		},
		{name: "nothing to parse", raw: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDeclineReasons(tc.raw)
			if len(got) != len(tc.codes) {
				t.Fatalf("got %d reasons, want %d: %+v", len(got), len(tc.codes), got)
			}
			for i, want := range tc.codes {
				if got[i].Code != want {
					t.Errorf("reason %d code = %q, want %q", i, got[i].Code, want)
				}
				if tc.src != "" && got[i].Source != tc.src {
					t.Errorf("reason %d source = %q, want %q", i, got[i].Source, tc.src)
				}
				if got[i].Description == "" {
					t.Errorf("reason %d has no description: %+v", i, got[i])
				}
			}
		})
	}
}
