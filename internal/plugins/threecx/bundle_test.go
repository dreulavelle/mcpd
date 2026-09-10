package threecx

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// bundleZip builds a small but recognisable support bundle: the system info
// file the parser identifies a bundle by, a health check, and a media server
// log with the one finding a test can look for.
func bundleZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	files := map[string]string{
		"ExtraLogging/tcxSystemInfo_1.txt": "\ufeffSystem Info\n\n[General]\nProcessorCount = 2\nTotalDiskSpace = 47 GB\nFreeDiskSpace = 40 GB\n" +
			"TotalPhysicalMemory = 3.823 GB\nFreePhysicalMemory = 0.3 GB\nOSDescription = Debian GNU/Linux 12 (bookworm)\n\n" +
			"[3CX PhoneSystem Info]\nNumber of Extensions = 7\nVERSION = 20.0.9.995\n",
		"ExtraLogging/Health_1.txt": "Firewall check: passed\nDNS check: failed\n",
		"Logs/3CXMediaServer.log": "#Date: 2026/09/01\n" +
			"10:00:00.000|7ff6e96c46c0| Warn|MSEndPoint.cpp(1086): 5:[MS105000] C:1.1: No RTP packets were received:remoteAddr=198.51.100.19:57046,extAddr=<none>,localAddr=203.0.113.28:9154\n",
	}
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bundlePBX is a fake that also serves the bundle zip on the raw endpoint.
func bundlePBX(t *testing.T, zipBody []byte, delay time.Duration) (*Plugin, *fakePBX) {
	t.Helper()
	f, srv := newFakePBX(t, map[string]string{"SystemStatus": `{"Version":"20.0.9"}`})
	f.raw = map[string]func(w http.ResponseWriter){
		"SupportInfo": func(w http.ResponseWriter) {
			time.Sleep(delay)
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipBody)
		},
	}
	p := pluginFor(t, srv.Client(), Customer{Name: "Acme", Host: srv.URL, Extension: "100", Password: "right-password"})
	// Somewhere of its own to spool to, so a test can prove nothing is left
	// behind and never writes into the repository.
	p.deps.Scratch = t.TempDir()
	return p, f
}

func waitForBundle(t *testing.T, p *Plugin) BundleReport {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, err := p.bundleReport(context.Background(), bundleReportArgs{})
		if err != nil {
			t.Fatal(err)
		}
		if r.State != "running" {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the capture did not finish")
	return BundleReport{}
}

// A capture is started and returns at once; the digest is read once the PBX
// has answered, with the findings and the system facts and never the zip.
func TestBundle_StartsThenReports(t *testing.T) {
	p, f := bundlePBX(t, bundleZip(t), 100*time.Millisecond)
	ctx := context.Background()

	before, err := p.bundleReport(ctx, bundleReportArgs{})
	if err != nil || before.State != "none" || !strings.Contains(before.Note, "aggregate_support_bundle") {
		t.Fatalf("before any capture: %+v %v", before, err)
	}

	st, err := p.startBundle(ctx, startBundleArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "running" || st.JobID == "" || st.Customer != "Acme" {
		t.Errorf("starting should return a running job at once: %+v", st)
	}

	// Asked again while running, it does not start a second one.
	again, _ := p.startBundle(ctx, startBundleArgs{})
	if again.JobID != st.JobID || !strings.Contains(again.Note, "already running") {
		t.Errorf("a second start while running should report the same job: %+v", again)
	}

	r := waitForBundle(t, p)
	if r.State != "done" {
		t.Fatalf("capture: %+v", r)
	}
	if r.System == nil || r.System.Version != "20.0.9.995" || r.System.OS == "" {
		t.Errorf("system facts: %+v", r.System)
	}
	if r.Counts == nil || r.Counts.Findings == 0 || r.Counts.Health != 2 {
		t.Errorf("counts: %+v", r.Counts)
	}
	found := false
	for _, fd := range r.Findings {
		if strings.Contains(fd.Title, "no audio") {
			found = true
			if len(fd.Evidence) == 0 || !strings.Contains(fd.Detail, "198.51.100.19") {
				t.Errorf("a finding carries its evidence and names the address: %+v", fd)
			}
		}
	}
	if !found {
		t.Errorf("the silent-call finding should be reported: %+v", r.Findings)
	}
	mustNotContain(t, r, "MSEndPoint.cpp(1086): 5:[MS105000] C:1.1: No RTP packets were received:remoteAddr=198.51.100.19:57046,extAddr=<none>,localAddr=203.0.113.28:9154\n#Date")

	health, err := p.bundleReport(ctx, bundleReportArgs{Section: "health"})
	if err != nil || len(health.Health) != 2 || health.Health[1].OK {
		t.Errorf("the health section: %+v %v", health.Health, err)
	}
	if _, err := p.bundleReport(ctx, bundleReportArgs{Section: "everything"}); err == nil {
		t.Error("an unknown section should be refused")
	}

	// The bundle was fetched once, from the raw endpoint, with the token.
	fetched := 0
	for _, seen := range f.seen {
		if strings.Contains(seen, "SupportInfo") {
			fetched++
		}
	}
	if fetched != 1 {
		t.Errorf("the bundle should be fetched once, saw %v", f.seen)
	}

	// A finished digest is reused inside the hour unless forced.
	reuse, _ := p.startBundle(ctx, startBundleArgs{})
	if reuse.State != "done" || !strings.Contains(reuse.Note, "force") {
		t.Errorf("a fresh digest should be offered rather than recaptured: %+v", reuse)
	}
	forced, _ := p.startBundle(ctx, startBundleArgs{Force: true})
	if forced.State != "running" || forced.JobID == reuse.JobID {
		t.Errorf("force should start a new capture: %+v", forced)
	}
	waitForBundle(t, p)
}

// A zip that is not a bundle, or a PBX that will not produce one, is a failed
// job the report explains rather than a hang or a panic.
func TestBundle_FailureIsReported(t *testing.T) {
	p, _ := bundlePBX(t, []byte("this is not a zip"), 0)
	ctx := context.Background()
	if _, err := p.startBundle(ctx, startBundleArgs{}); err != nil {
		t.Fatal(err)
	}
	r := waitForBundle(t, p)
	if r.State != "failed" || !strings.Contains(r.Error, "could not be read") {
		t.Errorf("an unreadable bundle should fail the job with a reason: %+v", r)
	}
}

// The bundle endpoint is the one read without a $select; the guard lets it
// through as a GET and nothing else.
func TestGuard_SupportInfoIsTheOneRawRead(t *testing.T) {
	c, rec := guarded(t)
	if err := try(t, c, http.MethodGet, "https://pbx.example/xapi/v1/SupportInfo"); err != nil {
		t.Errorf("the bundle download should be permitted without $select: %v", err)
	}
	if err := try(t, c, http.MethodPost, "https://pbx.example/xapi/v1/SupportInfo"); err == nil {
		t.Error("a POST to the bundle endpoint should be refused")
	}
	if err := try(t, c, http.MethodGet, "https://pbx.example/xapi/v1/SupportInfoExtra"); err == nil {
		t.Error("a path sharing the prefix should be refused")
	}
	if len(rec.reached) != 1 {
		t.Errorf("only the download should have reached the network: %v", rec.reached)
	}
}

// An edit the audit log recorded may be a credential being changed. The value
// is removed on the way out and the edit stays visible as an edit.
func TestScrubSecrets_RemovesCredentialValues(t *testing.T) {
	cases := map[string]string{
		`{"Number":"101","AuthPassword":"s3cret","Enabled":true}`:              `{"AuthPassword":"[redacted]","Enabled":true,"Number":"101"}`,
		`{"Gateway":{"Host":"sip.example","AuthPassword":"p"},"VMPIN":"1234"}`: `{"Gateway":{"AuthPassword":"[redacted]","Host":"sip.example"},"VMPIN":"[redacted]"}`,
		`AuthPassword: "s3cret", Number: "101"`:                                `AuthPassword: "[redacted]", Number: "101"`,
		`plain text with no credential`:                                        `plain text with no credential`,
		``:                                                                     ``,
	}
	for in, want := range cases {
		if got := scrubSecrets(in); got != want {
			t.Errorf("scrubSecrets(%q) = %q, want %q", in, got, want)
		}
	}
}

// The bundle is spooled to disk rather than held, so a bundle larger than this
// process would hold in memory is read -- 0.24.0 refused anything over a
// hundred megabytes, which a busy site reaches with a packet capture in it --
// and the spool file is gone whichever way the job ends.
func TestBundle_SpooledToDiskAndCleanedUp(t *testing.T) {
	p, _ := bundlePBX(t, bundleZip(t), 0)
	before := openSpools(t)
	if _, err := p.startBundle(context.Background(), startBundleArgs{}); err != nil {
		t.Fatal(err)
	}
	if r := waitForBundle(t, p); r.State != "done" {
		t.Fatalf("capture: %+v", r)
	}
	if left, err := os.ReadDir(p.deps.Scratch); err == nil && len(left) != 0 {
		t.Errorf("the spooled bundle should be gone, found %d files in %s", len(left), p.deps.Scratch)
	}
	if after := openSpools(t); after > before {
		t.Errorf("the spool file should be closed: %d open before the job, %d after", before, after)
	}
}

// openSpools counts the descriptors this process still holds on a spooled
// bundle. That is the fact worth defending: the file is unlinked the moment it
// is created, so an empty directory proves nothing, while a descriptor left
// open holds the whole gigabyte until the process ends. Linux only -- the
// check is skipped elsewhere rather than the test being.
func openSpools(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	open := 0
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err == nil && strings.Contains(target, "threecx-bundle-") {
			open++
		}
	}
	return open
}

// A bundle past the ceiling is refused with a sentence saying what to do about
// it, and leaves nothing behind. The ceiling is lowered here rather than a
// gigabyte being generated.
func TestBundle_PastTheCeilingIsRefused(t *testing.T) {
	p, _ := bundlePBX(t, bundleZip(t), 0)
	p.bundleCeiling = 64
	before := openSpools(t)
	if _, err := p.startBundle(context.Background(), startBundleArgs{}); err != nil {
		t.Fatal(err)
	}
	r := waitForBundle(t, p)
	if r.State != "failed" || !strings.Contains(r.Error, "larger than") {
		t.Fatalf("an oversized bundle should fail with a reason: %+v", r)
	}
	if left, _ := os.ReadDir(p.deps.Scratch); len(left) != 0 {
		t.Errorf("a refused download should leave nothing behind, found %d files", len(left))
	}
	if after := openSpools(t); after > before {
		t.Errorf("a refused download should close its spool file: %d open before, %d after", before, after)
	}
}

// A bundle the phone system says up front is too large is refused before any
// of it is spooled.
func TestBundle_RefusedOnTheLengthItAnnounces(t *testing.T) {
	body := bundleZip(t)
	f, srv := newFakePBX(t, map[string]string{"SystemStatus": `{"Version":"20.0.9"}`})
	f.raw = map[string]func(w http.ResponseWriter){
		"SupportInfo": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
		},
	}
	p := pluginFor(t, srv.Client(), Customer{Name: "Acme", Host: srv.URL, Extension: "100", Password: "right-password"})
	p.deps.Scratch = t.TempDir()
	p.bundleCeiling = 8

	if _, err := p.startBundle(context.Background(), startBundleArgs{}); err != nil {
		t.Fatal(err)
	}
	r := waitForBundle(t, p)
	if r.State != "failed" || !strings.Contains(r.Error, "larger than") {
		t.Fatalf("an announced size past the ceiling should be refused: %+v", r)
	}
	if left, _ := os.ReadDir(p.deps.Scratch); len(left) != 0 {
		t.Errorf("nothing should have been spooled, found %d files", len(left))
	}
}

// A disk that cannot be written says so, and says it about this host rather
// than about the phone system.
func TestBundle_DiskFailureNamesTheDisk(t *testing.T) {
	p, _ := bundlePBX(t, bundleZip(t), 0)
	// A file where the directory should be: every create in it fails.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.deps.Scratch = blocked

	if _, err := p.startBundle(context.Background(), startBundleArgs{}); err != nil {
		t.Fatal(err)
	}
	r := waitForBundle(t, p)
	if r.State != "failed" {
		t.Fatalf("a spool that cannot be opened should fail the job: %+v", r)
	}
	if !strings.Contains(r.Error, blocked) {
		t.Errorf("the failure should name the directory it could not use: %q", r.Error)
	}
}

// A running capture says which phase it is in and how much has landed, because
// "still collecting" for four minutes is indistinguishable from a hang. It
// never says when it will finish: 3CX reports nothing to base that on.
func TestBundle_ReportsItsPhase(t *testing.T) {
	body := bundleZip(t)
	f, srv := newFakePBX(t, map[string]string{"SystemStatus": `{"Version":"20.0.9"}`})
	release := make(chan struct{})
	f.raw = map[string]func(w http.ResponseWriter){
		"SupportInfo": func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/zip")
			// Headers, then a pause with the body half sent: the phase a
			// caller sees in the middle of this is the one being tested.
			_, _ = w.Write(body[:len(body)/2])
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			<-release
			_, _ = w.Write(body[len(body)/2:])
		},
	}
	p := pluginFor(t, srv.Client(), Customer{Name: "Acme", Host: srv.URL, Extension: "100", Password: "right-password"})
	p.deps.Scratch = t.TempDir()

	ctx := context.Background()
	if _, err := p.startBundle(ctx, startBundleArgs{}); err != nil {
		t.Fatal(err)
	}
	var phases []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := p.bundleReport(ctx, bundleReportArgs{})
		if err != nil {
			t.Fatal(err)
		}
		if r.Phase != "" {
			phases = append(phases, r.Phase)
		}
		if r.Phase == phaseDownloading {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	if len(phases) == 0 {
		t.Fatal("a running capture should say which phase it is in")
	}
	for _, got := range phases {
		if got != phaseGenerating && got != phaseDownloading && got != phaseAnalysing {
			t.Errorf("phase %q is not one of the three", got)
		}
	}
	r := waitForBundle(t, p)
	if r.State != "done" || r.Phase != "" {
		t.Errorf("a finished capture has no phase: %+v", r)
	}
}
