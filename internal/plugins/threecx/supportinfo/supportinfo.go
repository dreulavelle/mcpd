/*
Package supportinfo reads a 3CX support bundle and says what is wrong with it.

A support bundle is what 3CX produces when you press "collect support info": a
zip of several hundred files, tens of megabytes, holding a week of metrics, a
packet capture, every service log and the whole configuration. It is the thing
an engineer asks for when a problem has resisted everything else, and it is
almost never read, because reading it means unzipping it and knowing which
dozen of those four hundred files matter.

This reads that dozen: the system's own account of itself, its event log, the
call quality it measured, the packets it captured, and a fortnight of metrics
for the machine, its network and each service on it.

What comes out is deliberately small — a few hundred kilobytes from forty
megabytes. A page of facts, some series worth drawing, and a list of findings,
each one a sentence about something actually wrong with the evidence that says
so. The bundle itself is not kept. It belongs to a customer, it is enormous,
and every useful thing in it survives as a finding.
*/
package supportinfo

import (
	"archive/zip"
	"bufio"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Limits, because a support bundle is somebody else's file and its size is not
// something this process gets to trust.
const (
	// maxFile is how much of any one file will be read. The largest log in a
	// real bundle is eighteen megabytes of tunnel chatter; the findings live in
	// the first part of it and nothing is served by holding the rest.
	maxFile = 24 << 20
	// maxSeries is how many points a chart keeps.
	//
	// A week at two-minute intervals is five thousand, and there are now five
	// of these — which was most of a megabyte of stored report to draw charts
	// a few hundred pixels wide. Twelve hundred is finer than any screen can
	// resolve and a fifth of the size.
	maxSeries = 1200
	// maxEvidence bounds how many example lines one finding carries. Three is
	// enough to recognise a pattern; three hundred is a log file again.
	maxEvidence = 3
	// maxRows bounds a streamed table. The audit log on a busy system runs to
	// half a million rows and this is a web request, not a batch job.
	maxRows = 1_000_000
)

// Snapshot is everything worth keeping from one bundle.
type Snapshot struct {
	System   System    `json:"system"`
	Health   []Check   `json:"health"`
	Findings []Finding `json:"findings"`
	Series   Series    `json:"series"`
	// Network is throughput, derived from the interface counters.
	Network *Network `json:"network,omitempty"`
	// Services is what each 3CX service was doing with the machine.
	Services []Service `json:"services,omitempty"`
	// Events is the phone system's own event log, grouped.
	Events Events `json:"events"`
	// Quality is what callers actually heard, where the log carried it.
	Quality *Quality `json:"quality,omitempty"`
	// Capture is the packet capture, where the bundle had one.
	Capture *Capture `json:"capture,omitempty"`
	// Phones are the handsets with no provisioning template.
	Phones []Phone `json:"phones,omitempty"`
	// Changes is who changed what on this phone system.
	Changes *Changes `json:"changes,omitempty"`
	// Read is what the parser actually found and understood, so a bundle from
	// a version that moved things is visibly under-read rather than silently
	// thin.
	Read []string `json:"files_read"`
	// Missing names the files this looks for and did not find.
	Missing []string `json:"files_missing,omitempty"`
}

// System is the page of facts about the machine and the PBX on it.
type System struct {
	Version string `json:"version,omitempty"`
	// FQDN is the phone system's own address, which is how a bundle says
	// which customer it came from without anybody being asked.
	FQDN          string    `json:"fqdn,omitempty"`
	OS            string    `json:"os,omitempty"`
	CPUModel      string    `json:"cpu_model,omitempty"`
	CPUCount      int       `json:"cpu_count,omitempty"`
	TotalMemoryGB float64   `json:"total_memory_gb,omitempty"`
	FreeMemoryGB  float64   `json:"free_memory_gb,omitempty"`
	TotalDiskGB   float64   `json:"total_disk_gb,omitempty"`
	FreeDiskGB    float64   `json:"free_disk_gb,omitempty"`
	Extensions    int       `json:"extensions,omitempty"`
	Queues        int       `json:"queues,omitempty"`
	RingGroups    int       `json:"ring_groups,omitempty"`
	IVRs          int       `json:"ivrs,omitempty"`
	Virtualised   string    `json:"virtualised,omitempty"`
	CapturedAt    time.Time `json:"captured_at,omitempty"`
}

// Check is one of 3CX's own self-tests, as it reported itself.
type Check struct {
	Name string `json:"name"`
	Says string `json:"says"`
	OK   bool   `json:"ok"`
}

// Severity is how much a finding matters.
type Severity string

const (
	// Critical means calls are failing now.
	Critical Severity = "critical"
	// Warning means something will fail, or already did intermittently.
	Warning Severity = "warning"
	// Note is context worth having while reading the rest.
	Note Severity = "note"
)

/*
Finding is one thing wrong, said as a sentence.

Title is what happened. Detail is why it matters, in the terms a technician
would use to a customer. Evidence is the lines it was read from, so nobody has
to take this file's word for anything — a finding that cannot be checked against
the bundle is a finding that will eventually be wrong and believed anyway.
*/
type Finding struct {
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Evidence []string `json:"evidence,omitempty"`
	// Occurrences is how many times this was seen, when it is a pattern rather
	// than a single event. Once is a glitch and ninety times is the problem.
	Occurrences int `json:"occurrences,omitempty"`
	// Source names the file, so the finding can be argued with.
	Source string `json:"source,omitempty"`
}

// Point is one reading.
type Point struct {
	At    time.Time `json:"at"`
	Value float64   `json:"value"`
}

// Series is what is worth drawing.
type Series struct {
	CPU        []Point `json:"cpu_percent,omitempty"`
	FreeMemory []Point `json:"free_memory_gb,omitempty"`
	FreeDisk   []Point `json:"free_disk_gb,omitempty"`
}

// wanted names the files this reads, so what is missing can be reported rather
// than quietly producing an empty result.
var wanted = []string{
	"ExtraLogging/tcxSystemInfo",
	"ExtraLogging/Health",
	"ExtraLogging/tcxNslookup",
	"ExtraLogging/tcxVirtWhat",
	"ExtraLogging/unsupportedPhones",
	"DbTables/tsdb.system.csv",
	"DbTables/tsdb.network.csv",
	"DbTables/tsdb.services.csv",
	"DbTables/eventlog.csv",
	"DbTables/audit_log.csv",
	"Logs/3CXMediaServer",
	"Logs/dump.pcap",
	"LinuxLogs/auth.log",
}

/*
Read parses a bundle.

Everything is best-effort by design. A bundle from a different 3CX version, or
one somebody has already unzipped and rezipped, should give up whatever it still
has rather than failing whole — a partial answer to "why did this call drop" is
worth a great deal and an error is worth nothing.
*/
func Read(r io.ReaderAt, size int64) (Snapshot, error) {
	archive, err := zip.NewReader(r, size)
	if err != nil {
		return Snapshot{}, fmt.Errorf("that does not look like a support info zip: %w", err)
	}

	snap := Snapshot{Health: []Check{}, Findings: []Finding{}, Read: []string{}}
	seen := map[string]bool{}

	// Accumulated across every matching file rather than per file. A bundle
	// carries several media server logs — one per restart — and reporting each
	// one separately produced the same finding three times with the counts
	// split between them, which reads as three problems and hides the size of
	// the one.
	media := mediaTotals{silent: map[string]int{}}
	auth := authTotals{from: map[string]int{}}
	events := newEventTotals()
	quality := newQualityTotals()
	network := newNetworkTotals()
	audit := newAuditTotals()
	services := newServiceTotals()

	for _, f := range archive.File {
		name := f.Name
		switch {
		case strings.Contains(name, "ExtraLogging/tcxSystemInfo"):
			if body, err := readAll(f); err == nil {
				system := readSystemInfo(body)
				// Merged rather than assigned: the FQDN may already have been
				// read from the DNS lookup, and this file does not carry it.
				system.FQDN = snap.System.FQDN
				system.Virtualised = snap.System.Virtualised
				snap.System = system
				snap.System.CapturedAt = f.Modified
				mark(&snap, seen, "ExtraLogging/tcxSystemInfo", name)
			}

		case strings.Contains(name, "ExtraLogging/tcxNslookup"):
			if body, err := readAll(f); err == nil {
				snap.System.FQDN = readFQDN(body)
				mark(&snap, seen, "ExtraLogging/tcxNslookup", name)
			}

		case strings.Contains(name, "ExtraLogging/unsupportedPhones"):
			if body, err := readAll(f); err == nil {
				snap.Phones = readPhones(body)
				mark(&snap, seen, "ExtraLogging/unsupportedPhones", name)
			}

		case strings.Contains(name, "ExtraLogging/Health"):
			if body, err := readAll(f); err == nil {
				snap.Health = append(snap.Health, readHealth(body)...)
				mark(&snap, seen, "ExtraLogging/Health", name)
			}

		case strings.Contains(name, "ExtraLogging/tcxVirtWhat"):
			if body, err := readAll(f); err == nil {
				snap.System.Virtualised = strings.TrimSpace(clean(body))
				mark(&snap, seen, "ExtraLogging/tcxVirtWhat", name)
			}

		case strings.HasSuffix(name, "DbTables/tsdb.system.csv"):
			if body, err := readAll(f); err == nil {
				snap.Series = readSystemSeries(body)
				mark(&snap, seen, "DbTables/tsdb.system.csv", name)
			}

		case strings.HasSuffix(name, "DbTables/tsdb.network.csv"):
			if err := stream(f, func(get column) {
				at, ok := parseTime(get("time"))
				if !ok {
					return
				}
				sent, _ := number(get("bytes_sent"))
				received, _ := number(get("bytes_received"))
				network.readNetwork(strings.TrimSpace(get("id")), at, sent, received)
			}); err == nil {
				mark(&snap, seen, "DbTables/tsdb.network.csv", name)
			}

		case strings.HasSuffix(name, "DbTables/tsdb.services.csv"):
			if err := stream(f, func(get column) {
				memory, ok := number(get("private_memory"))
				if !ok {
					return
				}
				services.readService(atoi(get("service")), memory,
					atoi(get("thread_count")), atoi(get("process_count")))
			}); err == nil {
				mark(&snap, seen, "DbTables/tsdb.services.csv", name)
			}

		case strings.HasSuffix(name, "DbTables/audit_log.csv"):
			if err := stream(f, func(get column) {
				at, _ := parseTime(get("time_stamp"))
				audit.readAudit(at, get("user_name"), get("ip"),
					get("object_name"), get("prev_data"), get("new_data"))
			}); err == nil {
				mark(&snap, seen, "DbTables/audit_log.csv", name)
			}

		case strings.HasSuffix(name, "DbTables/eventlog.csv"):
			if err := stream(f, func(get column) {
				at, _ := parseTime(get("timegenerated"))
				id := strings.TrimSpace(get("eventid"))
				message := get("message")
				events.readEvent(strings.TrimSpace(get("source")), id,
					strings.TrimSpace(get("severity")), message, at)
				// The call quality reports live inside the event log as
				// escaped JSON on one particular event id.
				if id == "10034" {
					quality.readQuality(message)
				}
			}); err == nil {
				events.source = name
				mark(&snap, seen, "DbTables/eventlog.csv", name)
			}

		case strings.HasSuffix(name, ".pcap"):
			// Streamed rather than read: a capture is the largest thing in the
			// bundle after the audit log and none of it needs to be held.
			if rc, err := f.Open(); err == nil {
				capture, err := readPcap(rc)
				_ = rc.Close()
				if err == nil {
					snap.Capture = capture
					mark(&snap, seen, "Logs/dump.pcap", name)
				}
			}

		case strings.Contains(name, "Logs/3CXMediaServer"):
			if body, err := readAll(f); err == nil {
				readMediaLog(body, name, &media)
				mark(&snap, seen, "Logs/3CXMediaServer", name)
			}

		case strings.HasSuffix(name, "LinuxLogs/auth.log"):
			if body, err := readAll(f); err == nil {
				readAuthLog(body, name, &auth)
				mark(&snap, seen, "LinuxLogs/auth.log", name)
			}
		}
	}

	snap.Events = events.result()
	snap.Quality = quality.result()
	snap.Network = network.result()
	snap.Changes = audit.result()
	snap.Services = services.result()

	if len(snap.Read) == 0 {
		return Snapshot{}, errors.New("nothing in that zip looked like a 3CX support bundle")
	}

	// Windows bundles have no auth.log and no virt-what, so naming them as
	// missing would report the operating system as a fault.
	linux := strings.Contains(strings.ToLower(snap.System.OS), "linux") ||
		strings.Contains(strings.ToLower(snap.System.OS), "debian")
	for _, w := range wanted {
		if seen[w] {
			continue
		}
		if !linux && (w == "LinuxLogs/auth.log" || w == "ExtraLogging/tcxVirtWhat") {
			continue
		}
		snap.Missing = append(snap.Missing, w)
	}

	snap.Findings = append(snap.Findings, media.finding()...)
	snap.Findings = append(snap.Findings, auth.finding()...)
	snap.Findings = append(snap.Findings, events.findings()...)
	snap.Findings = append(snap.Findings, quality.findings(events.source)...)
	snap.Findings = append(snap.Findings, captureFindings(snap.Capture, "Logs/dump.pcap")...)
	snap.Findings = append(snap.Findings, phoneFindings(snap.Phones)...)
	snap.Findings = append(snap.Findings, audit.findings("DbTables/audit_log.csv")...)
	snap.Findings = append(snap.Findings, serviceFindings(snap.Services, snap.System.TotalMemoryGB)...)
	snap.Findings = append(snap.Findings, networkFindings(snap.Network)...)
	snap.Findings = append(snap.Findings, judgeResources(snap.System, snap.Series)...)
	snap.Findings = append(snap.Findings, judgeHealth(snap.Health)...)
	sortFindings(snap.Findings)
	return snap, nil
}

func mark(snap *Snapshot, seen map[string]bool, key, actual string) {
	if !seen[key] {
		seen[key] = true
		snap.Read = append(snap.Read, actual)
	}
}

/*
column reads one field of the row being streamed, by header name.

Case-insensitive, because the same table is spelled TimeGenerated in the API
and timegenerated in the CSV dump of it, and a lookup that silently returns
nothing is how a section ends up empty with no error anywhere.
*/
type column func(name string) string

/*
stream reads a CSV entry a row at a time, without holding it.

This exists for the two tables that cannot be read any other way. The event log
is fifty thousand rows, and the audit log on a busy system is half a million and
a hundred and twenty megabytes — larger than the rest of the bundle put
together. Reading either into a []byte first would mean holding it, and the
limit that stops that would truncate the file mid-row.

encoding/csv rather than splitting on newlines, because the messages contain
them: an unidentified-call event carries the whole INVITE it could not place,
line breaks and all, quoted inside one field.
*/
func stream(f *zip.File, fn func(get column)) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close() //nolint:errcheck // read-only

	reader := csv.NewReader(bufio.NewReaderSize(rc, 1<<20))
	reader.FieldsPerRecord = -1
	// A log line with an unbalanced quote in it should cost that row, not the
	// whole table.
	reader.LazyQuotes = true
	reader.ReuseRecord = true

	head, err := reader.Read()
	if err != nil {
		return err
	}
	at := make(map[string]int, len(head))
	for i, name := range head {
		at[strings.ToLower(strings.TrimSpace(clean([]byte(name))))] = i
	}

	var row []string
	get := column(func(name string) string {
		if i, ok := at[strings.ToLower(name)]; ok && i < len(row) {
			return row[i]
		}
		return ""
	})

	for read := 0; read < maxRows; read++ {
		row, err = reader.Read()
		if err != nil {
			break
		}
		fn(get)
	}
	return nil
}

// readAll reads one entry, bounded.
func readAll(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck // read-only
	return io.ReadAll(io.LimitReader(rc, maxFile))
}

// clean strips the byte-order mark 3CX writes on its text files, which
// otherwise becomes part of the first key and stops every lookup matching.
func clean(b []byte) string {
	return strings.TrimPrefix(string(b), "\ufeff")
}

// --- the files ----------------------------------------------------------------

var (
	keyValue = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z0-9 ()_]*?)\s*[=:]\s*(.+?)\s*$`)
	sizeGB   = regexp.MustCompile(`([\d.]+)\s*GB`)
)

// readSystemInfo reads tcxSystemInfo, which is INI-ish with a lscpu dump glued
// to the end of it.
func readSystemInfo(body []byte) System {
	var s System
	for _, line := range strings.Split(clean(body), "\n") {
		m := keyValue.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key, value := m[1], strings.TrimSpace(m[2])
		switch key {
		case "VERSION":
			s.Version = value
		case "OSDescription":
			s.OS = value
		case "Model name":
			s.CPUModel = value
		case "ProcessorCount":
			s.CPUCount = atoi(value)
		case "TotalPhysicalMemory":
			s.TotalMemoryGB = gigabytes(value)
		case "FreePhysicalMemory":
			s.FreeMemoryGB = gigabytes(value)
		case "TotalDiskSpace":
			s.TotalDiskGB = gigabytes(value)
		case "FreeDiskSpace":
			s.FreeDiskGB = gigabytes(value)
		case "Number of Extensions":
			s.Extensions = atoi(value)
		case "Number of Queues":
			s.Queues = atoi(value)
		case "Number of RingGroups":
			s.RingGroups = atoi(value)
		case "Number of IVRs":
			s.IVRs = atoi(value)
		}
	}
	return s
}

// readHealth reads 3CX's own checks, which are one "Name: Verdict" per line.
func readHealth(body []byte) []Check {
	var out []Check
	for _, line := range strings.Split(clean(body), "\n") {
		name, says, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found || name == "" {
			continue
		}
		says = strings.TrimSpace(says)
		out = append(out, Check{
			Name: strings.TrimSpace(name),
			Says: says,
			OK:   strings.EqualFold(says, "OK"),
		})
	}
	return out
}

// readSystemSeries reads the two-minute metrics into something drawable.
func readSystemSeries(body []byte) Series {
	reader := csv.NewReader(strings.NewReader(clean(body)))
	reader.FieldsPerRecord = -1

	head, err := reader.Read()
	if err != nil {
		return Series{}
	}
	at := map[string]int{}
	for i, h := range head {
		at[strings.TrimSpace(h)] = i
	}

	var s Series
	for {
		row, err := reader.Read()
		if err != nil {
			break
		}
		when, ok := parseTime(field(row, at, "time"))
		if !ok {
			continue
		}
		if v, ok := number(field(row, at, "cpu_usage")); ok {
			s.CPU = append(s.CPU, Point{At: when, Value: round(v, 2)})
		}
		if v, ok := number(field(row, at, "free_phys_mem")); ok {
			s.FreeMemory = append(s.FreeMemory, Point{At: when, Value: round(v/(1<<30), 2)})
		}
		if v, ok := number(field(row, at, "free_disk_space")); ok {
			s.FreeDisk = append(s.FreeDisk, Point{At: when, Value: round(v/(1<<30), 2)})
		}
	}

	s.CPU = thin(s.CPU)
	s.FreeMemory = thin(s.FreeMemory)
	s.FreeDisk = thin(s.FreeDisk)
	return s
}

var (
	// The line that explains most one-way-audio complaints there are.
	noRTP = regexp.MustCompile(`No RTP packets were received:remoteAddr=([\d.]+):\d+`)
	// A warning or error, whatever the component.
	logLevel = regexp.MustCompile(`\|\s*(Warn|Error|Fail\w*)\s*\|`)
)

/*
mediaTotals is what every media server log in a bundle added up to.

A bundle carries one of these per service restart, and the same fault appears in
all of them. Counting across the lot is the difference between "three problems,
one seen twice" and "one problem, seen ninety times" — and frequency is most of
what separates a glitch from a fault.
*/
type mediaTotals struct {
	silent   map[string]int
	evidence []string
	warnings int
	source   string
}

/*
readMediaLog looks for the media server's account of why audio did not work.

"No RTP packets were received" is the one that matters. It means the call was
set up, both sides agreed where to send audio, and nothing ever arrived — which
is one-way audio or dead air to the person on the phone, and is nearly always a
firewall or a NAT problem rather than anything on the PBX. The line names the
address that stayed silent, which is what turns "calls are bad" into a specific
thing to go and check.
*/
func readMediaLog(body []byte, source string, into *mediaTotals) {
	if into.source == "" {
		into.source = source
	}
	scanner := bufio.NewScanner(strings.NewReader(clean(body)))
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if m := noRTP.FindStringSubmatch(line); m != nil {
			into.silent[m[1]]++
			if len(into.evidence) < maxEvidence {
				into.evidence = append(into.evidence, strings.TrimSpace(line))
			}
			continue
		}
		if logLevel.MatchString(line) {
			into.warnings++
		}
	}
}

func (m mediaTotals) finding() []Finding {
	var out []Finding
	if len(m.silent) > 0 {
		total := 0
		peers := make([]string, 0, len(m.silent))
		for addr, n := range m.silent {
			total += n
			peers = append(peers, fmt.Sprintf("%s (%d)", addr, n))
		}
		sort.Strings(peers)
		out = append(out, Finding{
			Severity: Critical,
			Title:    "Calls where no audio ever arrived",
			Detail: "The call connected and both ends agreed where to send audio, and none came back from " +
				strings.Join(peers, ", ") + ". That is one-way audio or dead air to whoever was on the " +
				"phone. It is almost always a firewall or NAT problem between here and that address " +
				"rather than anything on the phone system — check that RTP is allowed both ways, and " +
				"that the external address the PBX advertises is the one traffic actually returns to.",
			Evidence:    m.evidence,
			Occurrences: total,
			Source:      m.source,
		})
	}
	if m.warnings > 0 {
		out = append(out, Finding{
			Severity:    Note,
			Title:       fmt.Sprintf("%d warnings across the media server logs", m.warnings),
			Detail:      "Not all of these matter. Worth reading if audio problems are the complaint and nothing above explains them.",
			Occurrences: m.warnings,
			Source:      m.source,
		})
	}
	return out
}

var failedLogin = regexp.MustCompile(`Failed password for (?:invalid user )?(\S+) from ([\d.]+)`)

// authTotals is failed SSH logins across the bundle's auth logs.
type authTotals struct {
	from     map[string]int
	evidence []string
	source   string
}

// readAuthLog counts failed SSH logins. A PBX on a public address is scanned
// constantly, so this is only worth reporting when it is heavy enough to be
// somebody actually trying.
func readAuthLog(body []byte, source string, into *authTotals) {
	if into.source == "" {
		into.source = source
	}
	scanner := bufio.NewScanner(strings.NewReader(clean(body)))
	scanner.Buffer(make([]byte, 0, 128*1024), 512*1024)
	for scanner.Scan() {
		if m := failedLogin.FindStringSubmatch(scanner.Text()); m != nil {
			into.from[m[2]]++
			if len(into.evidence) < maxEvidence {
				into.evidence = append(into.evidence, strings.TrimSpace(scanner.Text()))
			}
		}
	}
}

func (a authTotals) finding() []Finding {
	if len(a.from) == 0 {
		return nil
	}
	total, worst, worstCount := 0, "", 0
	for addr, n := range a.from {
		total += n
		if n > worstCount {
			worst, worstCount = addr, n
		}
	}
	// Anything on the internet gets knocked on. A hundred is somebody trying.
	if total < 100 {
		return nil
	}
	return []Finding{{
		Severity: Warning,
		Title:    fmt.Sprintf("%d failed SSH logins from %d addresses", total, len(a.from)),
		Detail: fmt.Sprintf("The heaviest is %s with %d attempts. A machine on a public address is scanned "+
			"constantly and this is usually noise, but it is worth confirming that password authentication "+
			"is off and that SSH is not open to the whole internet.", worst, worstCount),
		Evidence:    a.evidence,
		Occurrences: total,
		Source:      a.source,
	}}
}

// --- judgement ----------------------------------------------------------------

// judgeResources turns the metrics into findings. Thresholds are stated once,
// here, so they can be argued with.
func judgeResources(sys System, series Series) []Finding {
	var out []Finding

	if sys.TotalDiskGB > 0 && sys.FreeDiskGB > 0 {
		free := sys.FreeDiskGB / sys.TotalDiskGB
		switch {
		case free < 0.05:
			out = append(out, Finding{
				Severity: Critical,
				Title:    fmt.Sprintf("Disk is %.0f%% full", (1-free)*100),
				Detail: fmt.Sprintf("%.0f GB free of %.0f GB. A 3CX box that fills up stops recording calls "+
					"and then stops taking them. Clear recordings or grow the disk.", sys.FreeDiskGB, sys.TotalDiskGB),
				Source: "ExtraLogging/tcxSystemInfo",
			})
		case free < 0.15:
			out = append(out, Finding{
				Severity: Warning,
				Title:    fmt.Sprintf("Disk is %.0f%% full", (1-free)*100),
				Detail: fmt.Sprintf("%.0f GB free of %.0f GB. Worth watching, particularly if call recording is on.",
					sys.FreeDiskGB, sys.TotalDiskGB),
				Source: "ExtraLogging/tcxSystemInfo",
			})
		}
	}

	if sys.TotalMemoryGB > 0 && sys.FreeMemoryGB/sys.TotalMemoryGB < 0.1 {
		out = append(out, Finding{
			Severity: Warning,
			Title:    "Very little free memory",
			Detail: fmt.Sprintf("%.1f GB free of %.1f GB at the moment the bundle was taken. On a busy PBX "+
				"that shows up as audio breaking up before it shows up as anything else.",
				sys.FreeMemoryGB, sys.TotalMemoryGB),
			Source: "ExtraLogging/tcxSystemInfo",
		})
	}

	// The series says things a single reading cannot: whether the disk is
	// filling steadily, and whether the CPU was pinned rather than busy.
	if len(series.FreeDisk) > 10 {
		first, last := series.FreeDisk[0], series.FreeDisk[len(series.FreeDisk)-1]
		lost := first.Value - last.Value
		days := last.At.Sub(first.At).Hours() / 24
		if lost > 1 && days >= 1 {
			perDay := lost / days
			if perDay > 0.2 && last.Value/perDay < 60 {
				out = append(out, Finding{
					Severity: Warning,
					Title:    fmt.Sprintf("Disk is filling at about %.1f GB a day", perDay),
					Detail: fmt.Sprintf("It lost %.1f GB over %.0f days of this capture. At that rate the "+
						"remaining %.0f GB lasts around %.0f days.", lost, days, last.Value, last.Value/perDay),
					Source: "DbTables/tsdb.system.csv",
				})
			}
		}
	}

	if len(series.CPU) > 10 {
		high := 0
		peak := 0.0
		for _, p := range series.CPU {
			if p.Value > 85 {
				high++
			}
			if p.Value > peak {
				peak = p.Value
			}
		}
		if share := float64(high) / float64(len(series.CPU)); share > 0.05 {
			out = append(out, Finding{
				Severity: Warning,
				Title:    fmt.Sprintf("CPU was above 85%% for %.0f%% of the capture", share*100),
				Detail: fmt.Sprintf("Peaking at %.0f%%. Sustained CPU on a PBX is heard as choppy audio, "+
					"because encoding a call is the work that gets starved first.", peak),
				Source: "DbTables/tsdb.system.csv",
			})
		}
	}

	return out
}

// judgeHealth turns 3CX's own failed checks into findings, in its words.
func judgeHealth(checks []Check) []Finding {
	var out []Finding
	for _, c := range checks {
		if c.OK {
			continue
		}
		out = append(out, Finding{
			Severity: Warning,
			Title:    c.Name + " check did not pass",
			Detail:   "The phone system reported: " + c.Says,
			Source:   "ExtraLogging/Health",
		})
	}
	return out
}

// sortFindings puts what matters first. Within a severity, the thing seen most
// often leads, because frequency is the difference between a glitch and a fault.
func sortFindings(f []Finding) {
	rank := map[Severity]int{Critical: 0, Warning: 1, Note: 2}
	sort.SliceStable(f, func(i, j int) bool {
		if rank[f[i].Severity] != rank[f[j].Severity] {
			return rank[f[i].Severity] < rank[f[j].Severity]
		}
		return f[i].Occurrences > f[j].Occurrences
	})
}

// --- small readers ------------------------------------------------------------

func field(row []string, at map[string]int, name string) string {
	if i, ok := at[name]; ok && i < len(row) {
		return row[i]
	}
	return ""
}

func number(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v, err == nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func gigabytes(s string) float64 {
	if m := sizeGB.FindStringSubmatch(s); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		return v
	}
	return 0
}

func round(v float64, places int) float64 {
	shift := 1.0
	for range places {
		shift *= 10
	}
	return float64(int64(v*shift+0.5)) / shift
}

// parseTime reads the timestamps Postgres wrote into the CSV.
func parseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05.999999+00",
		"2006-01-02 15:04:05-07",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// thin drops points evenly when a series is longer than a chart can use. Evenly
// rather than by truncating, so the shape survives and the last reading is
// still the last reading.
func thin(points []Point) []Point {
	if len(points) <= maxSeries {
		return points
	}
	step := float64(len(points)) / float64(maxSeries)
	out := make([]Point, 0, maxSeries)
	for i := range maxSeries {
		out = append(out, points[int(float64(i)*step)])
	}
	return append(out, points[len(points)-1])
}
