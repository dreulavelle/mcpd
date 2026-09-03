package threecx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/spoked/mcpd/internal/plugins"
	"github.com/spoked/mcpd/internal/plugins/threecx/supportinfo"
)

// The support bundle, as a last resort.
//
// 3CX builds one on demand at GET /xapi/v1/SupportInfo: the same zip the
// console's "collect support info" button produces, with a week of metrics, a
// packet capture, every service log and the whole configuration. It is what an
// engineer asks for when the ordinary reads have not explained a fault, and it
// is the one read here that costs the phone system something -- minutes of
// walking its own logs on a large site -- which is why it is two tools, rate
// limited, and described as the thing to reach for last.
//
// Two tools because a tool call cannot wait minutes. The first starts a
// capture and returns at once; the second reports where it got to and, once
// it is done, the digest. The zip itself is never returned: it is tens of
// megabytes of a customer's logs, and every useful thing in it survives as a
// finding with the lines it was read from.

const (
	// bundleTimeout bounds a capture from request to parsed digest.
	bundleTimeout = 10 * time.Minute
	// bundleTTL is how long a finished digest is kept for follow-up questions
	// before a fresh capture is needed.
	bundleTTL = time.Hour
	// bundleRate is how often a capture may be started, in requests per
	// second: one every ten minutes. The PBX does real work for each.
	bundleRate = 1.0 / 600
	// maxFindings and maxRows bound what one answer carries of a digest.
	maxFindings = 40
	maxRows     = 60
)

// bundleJob is one capture, running or finished.
type bundleJob struct {
	id       string
	started  time.Time
	finished time.Time
	err      error
	report   *supportinfo.Snapshot
}

func (j *bundleJob) state() string {
	switch {
	case j == nil:
		return "none"
	case j.finished.IsZero():
		return "running"
	case j.err != nil:
		return "failed"
	}
	return "done"
}

func newBundleID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "bundle_" + hex.EncodeToString(b[:])
}

func (p *Plugin) registerBundleTools(r *plugins.Registry) {
	plugins.Tool(r, plugins.ToolSpec{
		Name:  "aggregate_support_bundle",
		Title: "Collect a support bundle",
		Description: "Last resort. Asks the phone system to build its support " +
			"bundle -- a week of metrics, the packet capture, every service log " +
			"-- and reads it into a digest of findings. Takes seconds on a small " +
			"system and minutes on a large one, so this returns a job at once; " +
			"poll get_support_bundle_report for the result. Use the ordinary " +
			"tools first; reach for this when they have not explained a fault.",
		Idempotent: false,
		RateLimit:  bundleRate,
	}, p.startBundle)

	plugins.Tool(r, plugins.ToolSpec{
		Name:  "get_support_bundle_report",
		Title: "Read the support bundle digest",
		Description: "The state of the last support bundle capture and, once " +
			"done, its digest: what the system says about itself, its findings " +
			"with evidence, and one section in detail on request -- events, " +
			"changes, quality, capture, services, network, health or files.",
		Idempotent: true,
	}, p.bundleReport)
}

// --- starting ---------------------------------------------------------------------

type startBundleArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Force    bool   `json:"force,omitempty" jsonschema:"start a fresh capture even if a digest from the last hour exists"`
}

// BundleStatus is where a capture has got to.
type BundleStatus struct {
	Customer string `json:"customer"`
	JobID    string `json:"job_id,omitempty"`
	// State is none, running, done or failed.
	State      string `json:"state"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	Error      string `json:"error,omitempty"`
	Note       string `json:"note"`
}

func (p *Plugin) startBundle(ctx context.Context, args startBundleArgs) (BundleStatus, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return BundleStatus{}, err
	}
	acct.bundleMu.Lock()
	defer acct.bundleMu.Unlock()

	if j := acct.bundle; j != nil {
		switch {
		case j.state() == "running":
			st := statusOf(acct.name, j)
			st.Note = "a capture is already running; poll get_support_bundle_report"
			return st, nil
		case j.state() == "done" && !args.Force && time.Since(j.finished) < bundleTTL:
			st := statusOf(acct.name, j)
			st.Note = "a digest from the last hour is ready; read it with get_support_bundle_report, or pass force to capture again"
			return st, nil
		}
	}

	job := &bundleJob{id: newBundleID(), started: time.Now()}
	acct.bundle = job
	// Detached from the tool call: the PBX may take minutes, and the caller is
	// a model with a deadline. Bounded by its own timeout instead.
	go p.runBundle(acct, job)

	st := statusOf(acct.name, job)
	st.Note = "capture started; the phone system is building the bundle. Poll get_support_bundle_report -- seconds on a small system, minutes on a large one"
	return st, nil
}

// runBundle fetches and parses one bundle, recording the outcome on the job.
func (p *Plugin) runBundle(acct *account, job *bundleJob) {
	ctx, cancel := context.WithTimeout(context.Background(), bundleTimeout)
	defer cancel()

	raw, err := acct.client.fetchBundle(ctx)
	var report *supportinfo.Snapshot
	if err == nil {
		var snap supportinfo.Snapshot
		snap, err = supportinfo.Read(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			err = fmt.Errorf("3cx: the bundle could not be read: %w", err)
		} else {
			report = &snap
		}
	}
	raw = nil //nolint:ineffassign // let the zip go before the digest is kept
	_ = raw

	acct.bundleMu.Lock()
	job.finished = time.Now()
	job.err = err
	job.report = report
	acct.bundleMu.Unlock()
	acct.note(err)
	if err != nil {
		p.deps.Log.WarnContext(ctx, "3cx support bundle failed", "customer", acct.name, "error", err)
		return
	}
	p.deps.Log.InfoContext(ctx, "3cx support bundle read", "customer", acct.name,
		"findings", len(report.Findings), "files", len(report.Read), "took", job.finished.Sub(job.started))
}

func statusOf(customer string, j *bundleJob) BundleStatus {
	st := BundleStatus{Customer: customer, State: j.state()}
	if j == nil {
		st.Note = "no capture has been started; call aggregate_support_bundle first"
		return st
	}
	st.JobID = j.id
	st.StartedAt = j.started.UTC().Format(time.RFC3339)
	if !j.finished.IsZero() {
		st.FinishedAt = j.finished.UTC().Format(time.RFC3339)
	}
	if j.err != nil {
		st.Error = plugins.Explain(j.err).Error()
	}
	return st
}

// --- reading ----------------------------------------------------------------------

type bundleReportArgs struct {
	Customer string `json:"customer,omitempty" jsonschema:"which customer's phone system, by business name or alias; needed when this instance serves more than one"`
	Section  string `json:"section,omitempty" jsonschema:"one section in detail: events, changes, quality, capture, services, network, health or files; left out for the summary"`
}

// BundleReport is the digest of a support bundle, bounded to one answer.
type BundleReport struct {
	BundleStatus
	CapturedAt string `json:"captured_at,omitempty"`

	// System is the page of facts about the machine and the PBX on it.
	System *BundleSystem `json:"system,omitempty"`
	// Findings are the things wrong, most serious first, each with the lines
	// it was read from.
	Findings []FindingRow  `json:"findings,omitempty"`
	Counts   *BundleCounts `json:"counts,omitempty"`

	// One of these is filled in when a section is asked for.
	Health   []CheckRow      `json:"health,omitempty"`
	Events   []EventGroupRow `json:"events,omitempty"`
	Changes  []EditRow       `json:"changes,omitempty"`
	Signins  []CountedRow    `json:"signins,omitempty"`
	Quality  *QualityRow     `json:"quality,omitempty"`
	Capture  *CaptureRow     `json:"capture,omitempty"`
	Streams  []StreamRow     `json:"streams,omitempty"`
	Services []BundleService `json:"services,omitempty"`
	Network  []NetworkRow    `json:"network,omitempty"`
	Files    []string        `json:"files_read,omitempty"`
	Missing  []string        `json:"files_missing,omitempty"`
	truncation
}

// BundleSystem is what the bundle says about the machine, with the metric
// series summarised rather than listed.
type BundleSystem struct {
	FQDN          string  `json:"fqdn,omitempty"`
	Version       string  `json:"version,omitempty"`
	OS            string  `json:"os,omitempty"`
	CPU           string  `json:"cpu,omitempty"`
	CPUs          int     `json:"cpus,omitempty"`
	Virtualised   string  `json:"virtualised,omitempty"`
	TotalMemoryGB float64 `json:"total_memory_gb,omitempty"`
	FreeMemoryGB  float64 `json:"free_memory_gb,omitempty"`
	TotalDiskGB   float64 `json:"total_disk_gb,omitempty"`
	FreeDiskGB    float64 `json:"free_disk_gb,omitempty"`
	Extensions    int     `json:"extensions,omitempty"`
	Queues        int     `json:"queues,omitempty"`
	RingGroups    int     `json:"ring_groups,omitempty"`
	IVRs          int     `json:"ivrs,omitempty"`
	// Over the metrics the bundle carries, usually a week.
	CPUPeakPercent    float64 `json:"cpu_peak_percent,omitempty"`
	CPUAveragePercent float64 `json:"cpu_average_percent,omitempty"`
	FreeMemoryMinGB   float64 `json:"free_memory_min_gb,omitempty"`
	FreeDiskMinGB     float64 `json:"free_disk_min_gb,omitempty"`
}

// BundleCounts says how much of each section there is, so a caller knows
// which to ask for.
type BundleCounts struct {
	Findings int `json:"findings"`
	Health   int `json:"health_checks"`
	Events   int `json:"event_groups"`
	Changes  int `json:"config_edits"`
	Streams  int `json:"audio_streams"`
	Services int `json:"services"`
	Phones   int `json:"unprovisionable_phones"`
}

// FindingRow is one thing wrong, said as a sentence.
type FindingRow struct {
	Severity    string   `json:"severity"`
	Title       string   `json:"title"`
	Detail      string   `json:"detail,omitempty"`
	Occurrences int      `json:"occurrences,omitempty"`
	Source      string   `json:"source,omitempty"`
	Evidence    []string `json:"evidence,omitempty"`
}

// CheckRow is one of 3CX's own self-tests.
type CheckRow struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Says string `json:"says,omitempty"`
}

// EventGroupRow is one kind of event, counted.
type EventGroupRow struct {
	Source   string `json:"source,omitempty"`
	ID       string `json:"id,omitempty"`
	Label    string `json:"label,omitempty"`
	Severity string `json:"severity"`
	Count    int    `json:"count"`
	First    string `json:"first,omitempty"`
	Last     string `json:"last,omitempty"`
	Sample   string `json:"sample,omitempty"`
	Says     string `json:"says,omitempty"`
}

// EditRow is one setting somebody changed.
type EditRow struct {
	At     string `json:"at"`
	User   string `json:"user,omitempty"`
	IP     string `json:"ip,omitempty"`
	Object string `json:"object,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// CountedRow is a name and how often it appeared.
type CountedRow struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// QualityRow is what callers heard, where the log carried it.
type QualityRow struct {
	Calls       int     `json:"calls"`
	RatedLegs   int     `json:"rated_legs"`
	Poor        int     `json:"poor"`
	MedianMOS   float64 `json:"median_mos,omitempty"`
	WorstMOS    float64 `json:"worst_mos,omitempty"`
	LossPercent float64 `json:"loss_percent,omitempty"`
	JitterMS    float64 `json:"jitter_ms,omitempty"`
}

// CaptureRow is what the packet capture showed, in totals.
type CaptureRow struct {
	Packets   int          `json:"packets"`
	Seconds   float64      `json:"seconds,omitempty"`
	From      string       `json:"from,omitempty"`
	To        string       `json:"to,omitempty"`
	Truncated bool         `json:"truncated,omitempty"`
	Protocols []CountedRow `json:"protocols,omitempty"`
	SIP       []CountedRow `json:"sip_methods,omitempty"`
	OneWay    []string     `json:"one_way,omitempty"`
}

// StreamRow is one direction of audio.
type StreamRow struct {
	From    string  `json:"from"`
	To      string  `json:"to"`
	Codec   string  `json:"codec,omitempty"`
	Packets int     `json:"packets"`
	Lost    int     `json:"lost,omitempty"`
	LossPct float64 `json:"loss_percent,omitempty"`
	Jitter  float64 `json:"jitter_ms,omitempty"`
	Seconds float64 `json:"seconds,omitempty"`
}

// BundleService is one 3CX service and what it did with the machine.
type BundleService struct {
	Name         string  `json:"name"`
	MemoryMB     float64 `json:"memory_mb"`
	PeakMemoryMB float64 `json:"peak_memory_mb,omitempty"`
	GrowthMB     float64 `json:"growth_mb,omitempty"`
	Threads      int     `json:"threads,omitempty"`
}

// NetworkRow is one interface's throughput.
type NetworkRow struct {
	Interface string  `json:"interface"`
	PeakSent  float64 `json:"peak_sent_mbps,omitempty"`
	PeakRecv  float64 `json:"peak_received_mbps,omitempty"`
	TotalSent float64 `json:"total_sent_gb,omitempty"`
	TotalRecv float64 `json:"total_received_gb,omitempty"`
}

var bundleSections = map[string]bool{
	"": true, "summary": true, "events": true, "changes": true, "quality": true,
	"capture": true, "services": true, "network": true, "health": true, "files": true,
}

func (p *Plugin) bundleReport(_ context.Context, args bundleReportArgs) (BundleReport, error) {
	acct, err := p.resolve(args.Customer)
	if err != nil {
		return BundleReport{}, err
	}
	section := strings.ToLower(strings.TrimSpace(args.Section))
	if !bundleSections[section] {
		return BundleReport{}, fmt.Errorf("section %q is not one of events, changes, quality, capture, services, network, health or files", args.Section)
	}

	acct.bundleMu.Lock()
	job := acct.bundle
	acct.bundleMu.Unlock()

	out := BundleReport{BundleStatus: statusOf(acct.name, job)}
	if job == nil {
		return out, nil
	}
	switch job.state() {
	case "running":
		out.Note = fmt.Sprintf("still collecting, %s so far; ask again shortly", time.Since(job.started).Round(time.Second))
		return out, nil
	case "failed":
		out.Note = "the capture failed; fix the cause and call aggregate_support_bundle again"
		return out, nil
	}
	snap := job.report
	out.Note = "digest of the bundle collected at " + out.FinishedAt
	if !snap.System.CapturedAt.IsZero() {
		out.CapturedAt = snap.System.CapturedAt.UTC().Format(time.RFC3339)
	}
	out.Counts = &BundleCounts{
		Findings: len(snap.Findings), Health: len(snap.Health), Events: len(snap.Events.Groups),
		Services: len(snap.Services), Phones: len(snap.Phones),
	}
	if snap.Changes != nil {
		out.Counts.Changes = len(snap.Changes.Edits)
	}
	if snap.Capture != nil {
		out.Counts.Streams = len(snap.Capture.Streams)
	}
	out.System = systemOf(snap)

	cut := false
	switch section {
	case "", "summary":
		out.Findings, cut = findingRows(snap.Findings, maxFindings)
	case "health":
		for _, c := range snap.Health {
			out.Health = append(out.Health, CheckRow{Name: c.Name, OK: c.OK, Says: c.Says})
		}
	case "events":
		for i, g := range snap.Events.Groups {
			if i >= maxRows {
				cut = true
				break
			}
			out.Events = append(out.Events, EventGroupRow{
				Source: g.Source, ID: g.ID, Label: g.Label, Severity: string(g.Severity), Count: g.Count,
				First: when(g.First), Last: when(g.Last), Sample: g.Sample, Says: g.Says,
			})
		}
	case "changes":
		if snap.Changes != nil {
			for i, e := range snap.Changes.Edits {
				if i >= maxRows {
					cut = true
					break
				}
				out.Changes = append(out.Changes, EditRow{
					At: when(e.At), User: e.User, IP: e.IP, Object: e.Object, Before: e.Before, After: e.After,
				})
			}
			for _, c := range snap.Changes.Signins {
				out.Signins = append(out.Signins, CountedRow{Name: c.Name, Count: c.Count})
			}
		}
	case "quality":
		if q := snap.Quality; q != nil {
			out.Quality = &QualityRow{
				Calls: q.Calls, RatedLegs: q.RatedLegs, Poor: q.Poor, MedianMOS: q.MedianMOS,
				WorstMOS: q.WorstMOS, LossPercent: q.LossPercent, JitterMS: q.JitterMS,
			}
		}
	case "capture":
		if c := snap.Capture; c != nil {
			out.Capture = &CaptureRow{
				Packets: c.Packets, Seconds: c.Seconds, From: when(c.From), To: when(c.To),
				Truncated: c.Truncated, OneWay: c.OneWay,
			}
			for _, x := range c.Protocols {
				out.Capture.Protocols = append(out.Capture.Protocols, CountedRow{Name: x.Name, Count: x.Count})
			}
			for _, x := range c.SIP {
				out.Capture.SIP = append(out.Capture.SIP, CountedRow{Name: x.Name, Count: x.Count})
			}
			// Worst first: the lossy streams are the ones somebody is looking for.
			streams := append([]supportinfo.Stream(nil), c.Streams...)
			sort.SliceStable(streams, func(a, b int) bool { return streams[a].LossPct > streams[b].LossPct })
			for i, s := range streams {
				if i >= maxRows {
					cut = true
					break
				}
				out.Streams = append(out.Streams, StreamRow{
					From: s.From, To: s.To, Codec: s.Codec, Packets: s.Packets, Lost: s.Lost,
					LossPct: s.LossPct, Jitter: s.Jitter, Seconds: s.Seconds,
				})
			}
		}
	case "services":
		for _, s := range snap.Services {
			out.Services = append(out.Services, BundleService{
				Name: s.Name, MemoryMB: s.MemoryMB, PeakMemoryMB: s.PeakMemoryMB, GrowthMB: s.GrowthMB, Threads: s.Threads,
			})
		}
	case "network":
		if n := snap.Network; n != nil {
			out.Network = append(out.Network, NetworkRow{
				Interface: n.Interface, PeakSent: n.PeakSent, PeakRecv: n.PeakRecv, TotalSent: n.TotalSent, TotalRecv: n.TotalRecv,
			})
		}
	case "files":
		out.Files, out.Missing = snap.Read, snap.Missing
	}
	if cut {
		out.truncation = truncation{Truncated: true, Reason: "the section holds more rows than one answer carries; the first are shown"}
	}
	return out, nil
}

// findingRows renders findings most serious first, bounded.
func findingRows(findings []supportinfo.Finding, limit int) ([]FindingRow, bool) {
	rows := make([]FindingRow, 0, min(limit, len(findings)))
	for i, f := range findings {
		if i >= limit {
			return rows, true
		}
		evidence := f.Evidence
		if len(evidence) > 5 {
			evidence = evidence[:5]
		}
		rows = append(rows, FindingRow{
			Severity: string(f.Severity), Title: f.Title, Detail: f.Detail,
			Occurrences: f.Occurrences, Source: f.Source, Evidence: evidence,
		})
	}
	return rows, false
}

// systemOf summarises the machine and its metric series.
func systemOf(snap *supportinfo.Snapshot) *BundleSystem {
	s := snap.System
	out := &BundleSystem{
		FQDN: s.FQDN, Version: s.Version, OS: s.OS, CPU: s.CPUModel, CPUs: s.CPUCount,
		Virtualised: s.Virtualised, TotalMemoryGB: s.TotalMemoryGB, FreeMemoryGB: s.FreeMemoryGB,
		TotalDiskGB: s.TotalDiskGB, FreeDiskGB: s.FreeDiskGB,
		Extensions: s.Extensions, Queues: s.Queues, RingGroups: s.RingGroups, IVRs: s.IVRs,
	}
	if peak, avg, ok := stats(snap.Series.CPU); ok {
		out.CPUPeakPercent, out.CPUAveragePercent = round1(peak), round1(avg)
	}
	if lo, ok := minimum(snap.Series.FreeMemory); ok {
		out.FreeMemoryMinGB = round1(lo)
	}
	if lo, ok := minimum(snap.Series.FreeDisk); ok {
		out.FreeDiskMinGB = round1(lo)
	}
	return out
}

func stats(points []supportinfo.Point) (peak, avg float64, ok bool) {
	if len(points) == 0 {
		return 0, 0, false
	}
	var sum float64
	for _, pt := range points {
		sum += pt.Value
		peak = math.Max(peak, pt.Value)
	}
	return peak, sum / float64(len(points)), true
}

func minimum(points []supportinfo.Point) (float64, bool) {
	if len(points) == 0 {
		return 0, false
	}
	lo := points[0].Value
	for _, pt := range points {
		lo = math.Min(lo, pt.Value)
	}
	return lo, true
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
