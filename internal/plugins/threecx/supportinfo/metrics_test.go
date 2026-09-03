package supportinfo

import (
	"testing"
	"time"
)

/*
Zero is not a quality score.

3CX files a call it could not measure — too short, or no RTCP coming back —
with a MOS of 0. Averaging those in reports a healthy phone system as
catastrophic, which is the kind of wrong that gets believed because it looks
like a number.
*/
func TestUnmeasurableCallsDoNotCountAsBad(t *testing.T) {
	totals := newQualityTotals()

	// Two good calls and one the system could not score.
	totals.readQuality(`{"Reason":"","MOS":4.4,
		"Party1":{"Number":"100","Codec":"PCMU","MOSFromPBX":4.4,"RxPackets":500},
		"Party2":{"Number":"5551234","Codec":"PCMU","MOSFromPBX":4.3,"RxPackets":500}}`)
	totals.readQuality(`{"Reason":"Too small call duration for quality estimation.","MOS":0,
		"Party1":{"Number":"101","Codec":"PCMU","MOSFromPBX":0,"RxPackets":1},
		"Party2":{"Number":"5555678","Codec":"PCMU","MOSFromPBX":0,"RxPackets":1}}`)

	result := totals.result()
	if result.Calls != 2 {
		t.Errorf("counted %d calls, want 2", result.Calls)
	}
	if result.RatedLegs != 2 {
		t.Errorf("rated %d legs, want the 2 that carried a score", result.RatedLegs)
	}
	if result.Poor != 0 {
		t.Errorf("counted %d poor legs; an unmeasured call is not a bad one", result.Poor)
	}
	if result.MedianMOS < 4 {
		t.Errorf("median MOS came out at %.2f, want the average of the calls that were measured", result.MedianMOS)
	}
}

// A leg somebody would complain about has to be reported, and named.
func TestPoorCallsAreFound(t *testing.T) {
	totals := newQualityTotals()
	for range 3 {
		totals.readQuality(`{"Reason":"No RTCP from 5551234 (audio from PBX can't get through?).","MOS":2.1,
			"Party1":{"Number":"102 Sales","Codec":"PCMU","MOSFromPBX":2.1,"RxJitter":40,"RxLost":50,"RxPackets":450,
			          "EndpointType":"TUNNEL","UserAgent":"3CXPhone for iOS"}}`)
	}
	result := totals.result()
	if result.Poor != 3 {
		t.Fatalf("found %d poor legs, want 3", result.Poor)
	}
	if len(result.Worst) == 0 || result.Worst[0].Number != "102 Sales" {
		t.Errorf("worst calls read as %+v", result.Worst)
	}
	if got := result.Worst[0].LossPct; got < 9 || got > 11 {
		t.Errorf("loss came out at %.1f%%, want about 10", got)
	}
	if len(totals.findings("test")) == 0 {
		t.Error("three unusable call legs produced no finding")
	}
}

/*
A counter that went backwards is a restart, not a rate.

The network table holds cumulative byte counts. Subtracting across a service
restart gives an enormous negative, and drawing it as throughput would put a
spike on the chart at exactly the moment somebody is looking for one.
*/
func TestNetworkIgnoresCounterResets(t *testing.T) {
	totals := newNetworkTotals()
	base := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

	totals.readNetwork("eth0", base, 1_000_000, 2_000_000)
	totals.readNetwork("eth0", base.Add(2*time.Minute), 2_200_000, 2_500_000)
	// Restarted: the counters begin again.
	totals.readNetwork("eth0", base.Add(4*time.Minute), 1_000, 2_000)
	totals.readNetwork("eth0", base.Add(6*time.Minute), 1_201_000, 2_002_000)

	result := totals.result()
	if result == nil {
		t.Fatal("no network result")
	}
	if len(result.SentMbps) != 2 {
		t.Fatalf("drew %d points, want 2 — the reset is not a reading", len(result.SentMbps))
	}
	for _, p := range result.SentMbps {
		if p.Value < 0 {
			t.Errorf("negative throughput %.3f Mbps drawn from a counter reset", p.Value)
		}
	}
	// 1.2 MB over two minutes is 0.08 Mbps; both intervals are the same size.
	if result.PeakSent > 1 {
		t.Errorf("peak came out at %.3f Mbps, which is the reset being counted", result.PeakSent)
	}
}

// The services table keys on a number, and a report saying "service 9 is using
// 362 MB" is not worth printing.
func TestServicesAreNamed(t *testing.T) {
	totals := newServiceTotals()
	totals.readService(0, 60_000_000, 0, 15) // Postgres: many processes, no threads
	totals.readService(13, 9_800_000, 0, 3)  // nginx and its workers
	totals.readService(99, 1_000_000, 1, 1)  // not in the enum

	byName := map[string]Service{}
	for _, s := range totals.result() {
		byName[s.Name] = s
	}
	if _, ok := byName["Database"]; !ok {
		t.Error("service 0 was not named Database")
	}
	if _, ok := byName["Nginx"]; !ok {
		t.Error("service 13 was not named Nginx")
	}
	if _, ok := byName["service 99"]; !ok {
		t.Error("an unknown service index should say so plainly rather than be dropped")
	}
}

/*
The audit log is half a million rows of somebody opening the web client and
perhaps fifty that changed a setting. Only the fifty are worth a page.
*/
func TestAuditKeepsOnlyRealChanges(t *testing.T) {
	totals := newAuditTotals()
	at := time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC)

	for range 500 {
		totals.readAudit(at, "Jack Benson", "10.0.0.5", "Web Client", "", "")
	}
	totals.readAudit(at, "Jack Benson", "10.0.0.5", "2971 Weiland, Kennedy",
		`{"ForwardingProfiles":[{"Name":"Away"}]}`, `{"ForwardingProfiles":[{"Name":"Available"}]}`)

	result := totals.result()
	if result.Rows != 501 {
		t.Errorf("read %d rows, want 501", result.Rows)
	}
	if len(result.Edits) != 1 {
		t.Fatalf("kept %d edits, want the 1 that changed something", len(result.Edits))
	}
	if result.Edits[0].Object != "2971 Weiland, Kennedy" {
		t.Errorf("edit read as %+v", result.Edits[0])
	}
	if len(result.Signins) == 0 {
		t.Error("nobody was recorded as having used the system")
	}
}

// The FQDN is what attaches a bundle to a customer, and nslookup formats
// differ between the Windows and Linux builds of 3CX.
func TestFQDNReadsBothPlatforms(t *testing.T) {
	windows := "\ufeffServer:  dns.google\r\nAddress:  8.8.8.8\r\n\r\nName:    acme.ny.3cx.us\r\nAddress:  198.51.100.20\r\n"
	linux := "\ufeffServer:\t\t8.8.8.8\nAddress:\t8.8.8.8#53\n\nNon-authoritative answer:\nName:\tglobex.la.3cx.us\nAddress: 203.0.113.28\n"

	if got := readFQDN([]byte(windows)); got != "acme.ny.3cx.us" {
		t.Errorf("Windows lookup read as %q", got)
	}
	if got := readFQDN([]byte(linux)); got != "globex.la.3cx.us" {
		t.Errorf("Linux lookup read as %q", got)
	}
}
