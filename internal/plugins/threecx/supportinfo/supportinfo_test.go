package supportinfo_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/plugins/threecx/supportinfo"
)

// bundle builds a zip shaped like 3CX's, from the files a test cares about.
func bundle(t *testing.T, files map[string]string) (*bytes.Reader, int64) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes()), int64(buf.Len())
}

// 3CX writes a byte-order mark on its text files. Left in place it becomes part
// of the first key and every lookup in the file silently misses.
const systemInfo = "\ufeffSystem Info\n\n[General]\n" +
	"ProcessorCount = 2\nTotalDiskSpace = 47 GB\nFreeDiskSpace = 40 GB\n" +
	"TotalPhysicalMemory = 3.823 GB\nFreePhysicalMemory = 1.728 GB\n" +
	"OSDescription = Debian GNU/Linux 12 (bookworm)\n\n" +
	"[3CX PhoneSystem Info]\nNumber of Extensions = 7\nNumber of Queues = 1\n" +
	"Number of RingGroups = 1\nNumber of IVRs = 4\nVERSION = 20.0.8.1131\n"

func TestReadsTheFactsThroughAByteOrderMark(t *testing.T) {
	r, size := bundle(t, map[string]string{
		"ExtraLogging/tcxSystemInfo_260012110050.txt": systemInfo,
	})
	snap, err := supportinfo.Read(r, size)
	if err != nil {
		t.Fatal(err)
	}
	if snap.System.Version != "20.0.8.1131" {
		t.Errorf("version = %q — the byte-order mark probably ate the first key", snap.System.Version)
	}
	if snap.System.Extensions != 7 || snap.System.IVRs != 4 {
		t.Errorf("counts wrong: %+v", snap.System)
	}
	if snap.System.TotalDiskGB != 47 || snap.System.FreeDiskGB != 40 {
		t.Errorf("disk wrong: %+v", snap.System)
	}
}

/*
The finding this whole thing exists for.

"No RTP packets were received" means the call set up, both ends agreed where to
send audio, and none arrived — dead air to whoever was on the phone. The remote
address is the part that turns "calls are bad" into something to go and check.
*/
func TestFindsCallsWithNoAudio(t *testing.T) {
	log := "#Date: 2026/08/07\n" +
		"17:18:36.205|7ff6e96c46c0| Warn|MSEndPoint.cpp(1086): 5:[MS105000] C:3218.1: No RTP packets were received:remoteAddr=198.51.100.19:57046,extAddr=<none>,localAddr=203.0.113.28:9154\n" +
		"18:06:40.311|7ff6db7fe6c0| Warn|MSEndPoint.cpp(1086): 5:[MS105000] C:3275.1: No RTP packets were received:remoteAddr=198.51.100.19:36424,extAddr=<none>,localAddr=203.0.113.28:9260\n"

	r, size := bundle(t, map[string]string{
		"ExtraLogging/tcxSystemInfo_1.txt":   systemInfo,
		"Logs/3CXMediaServer.2026-08-07.log": log,
	})
	snap, err := supportinfo.Read(r, size)
	if err != nil {
		t.Fatal(err)
	}

	var found *supportinfo.Finding
	for i := range snap.Findings {
		if strings.Contains(snap.Findings[i].Title, "no audio") {
			found = &snap.Findings[i]
		}
	}
	if found == nil {
		t.Fatal("the silent-audio finding was not produced")
	}
	if found.Severity != supportinfo.Critical {
		t.Errorf("severity = %s, want critical", found.Severity)
	}
	if found.Occurrences != 2 {
		t.Errorf("occurrences = %d, want 2", found.Occurrences)
	}
	if !strings.Contains(found.Detail, "198.51.100.19") {
		t.Error("the detail does not name the address that stayed silent")
	}
	if len(found.Evidence) == 0 {
		t.Error("no evidence, so nobody can check it against the bundle")
	}
}

/*
One fault across several logs is one finding.

A bundle carries a media server log per service restart and the same fault
appears in all of them. Reporting each separately read as several problems and
split the count between them, hiding how big the one actually was.
*/
func TestOneFaultAcrossManyLogsIsOneFinding(t *testing.T) {
	line := "17:18:36.205|x| Warn|MSEndPoint.cpp(1086): C:1: No RTP packets were received:remoteAddr=10.0.0.9:5000,extAddr=<none>\n"
	r, size := bundle(t, map[string]string{
		"ExtraLogging/tcxSystemInfo_1.txt":   systemInfo,
		"Logs/3CXMediaServer.2026-08-01.log": strings.Repeat(line, 3),
		"Logs/3CXMediaServer.2026-08-05.log": strings.Repeat(line, 4),
		"Logs/3CXMediaServer.2026-08-07.log": strings.Repeat(line, 5),
	})
	snap, err := supportinfo.Read(r, size)
	if err != nil {
		t.Fatal(err)
	}

	count := 0
	total := 0
	for _, f := range snap.Findings {
		if strings.Contains(f.Title, "no audio") {
			count++
			total = f.Occurrences
		}
	}
	if count != 1 {
		t.Errorf("got %d silent-audio findings across three logs, want one", count)
	}
	if total != 12 {
		t.Errorf("occurrences = %d, want all 12 counted together", total)
	}
}

// A Windows bundle has no auth.log and no virt-what. Naming them as missing
// would report the operating system as a fault.
func TestWindowsBundleDoesNotReportLinuxFilesMissing(t *testing.T) {
	windows := strings.Replace(systemInfo,
		"OSDescription = Debian GNU/Linux 12 (bookworm)",
		"OSDescription = Microsoft Windows 10.0.17763", 1)

	r, size := bundle(t, map[string]string{"ExtraLogging/tcxSystemInfo_1.txt": windows})
	snap, err := supportinfo.Read(r, size)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range snap.Missing {
		if strings.Contains(m, "auth.log") || strings.Contains(m, "VirtWhat") {
			t.Errorf("reported %s missing on a Windows bundle", m)
		}
	}
}

// The metrics have to become something drawable, and the units have to be the
// ones a person reads.
func TestReadsTheMetricSeries(t *testing.T) {
	csv := "time,cpu_usage,total_virt_mem,free_virt_mem,total_phys_mem,free_phys_mem,total_disk_space,free_disk_space,tick_count\n"
	for i := range 5 {
		csv += fmt.Sprintf("2026-08-05 1%d:02:00.039293+00,12.5,1,1,4105482240,1821032448,51510796288,43856441344,1\n", i)
	}
	r, size := bundle(t, map[string]string{
		"ExtraLogging/tcxSystemInfo_1.txt": systemInfo,
		"DbTables/tsdb.system.csv":         csv,
	})
	snap, err := supportinfo.Read(r, size)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Series.CPU) != 5 {
		t.Fatalf("cpu points = %d, want 5", len(snap.Series.CPU))
	}
	if snap.Series.CPU[0].Value != 12.5 {
		t.Errorf("cpu = %v, want 12.5", snap.Series.CPU[0].Value)
	}
	// Bytes in the file, gigabytes on the screen.
	if got := snap.Series.FreeMemory[0].Value; got < 1.6 || got > 1.8 {
		t.Errorf("free memory = %v GB, want about 1.7", got)
	}
}

// A disk quietly filling is the fault nobody notices until calls stop, and a
// single reading cannot show it.
func TestNoticesADiskFilling(t *testing.T) {
	csv := "time,cpu_usage,total_virt_mem,free_virt_mem,total_phys_mem,free_phys_mem,total_disk_space,free_disk_space,tick_count\n"
	free := 20 << 30
	for day := range 12 {
		csv += fmt.Sprintf("2026-08-%02d 10:00:00+00,5,1,1,4105482240,1821032448,51510796288,%d,1\n", day+1, free)
		free -= 1 << 30
	}
	r, size := bundle(t, map[string]string{
		"ExtraLogging/tcxSystemInfo_1.txt": systemInfo,
		"DbTables/tsdb.system.csv":         csv,
	})
	snap, err := supportinfo.Read(r, size)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range snap.Findings {
		if strings.Contains(f.Title, "filling") {
			return
		}
	}
	t.Errorf("a disk losing a gigabyte a day was not noticed: %+v", snap.Findings)
}

// Something that is not a support bundle has to say so rather than returning
// an empty report that looks like a healthy system.
func TestRefusesSomethingThatIsNotABundle(t *testing.T) {
	r, size := bundle(t, map[string]string{"holiday/photo.jpg": "not a pbx"})
	if _, err := supportinfo.Read(r, size); err == nil {
		t.Error("a zip of unrelated files was accepted as a support bundle")
	}
}

// The worst thing first, and within a severity the most frequent.
func TestFindingsAreOrderedByWhatMatters(t *testing.T) {
	log := "17:18:36.205|x| Warn|MSEndPoint.cpp(1086): C:1: No RTP packets were received:remoteAddr=10.0.0.9:5000,extAddr=<none>\n"
	r, size := bundle(t, map[string]string{
		"ExtraLogging/tcxSystemInfo_1.txt": systemInfo,
		"ExtraLogging/Health_1.txt":        "\ufeffFirewall check: FAILED\nSIP trunks: OK\n",
		"Logs/3CXMediaServer.log":          log,
	})
	snap, err := supportinfo.Read(r, size)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Findings) < 2 {
		t.Fatalf("expected several findings, got %+v", snap.Findings)
	}
	if snap.Findings[0].Severity != supportinfo.Critical {
		t.Errorf("the critical finding is not first: %+v", snap.Findings)
	}
}
