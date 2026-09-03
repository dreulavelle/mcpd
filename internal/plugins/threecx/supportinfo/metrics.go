package supportinfo

import (
	"fmt"
	"sort"
	"time"
)

/*
The two metric tables nobody reads, because they arrive as numbered rows.

tsdb.network.csv is a byte counter per interface every two minutes. Counters are
useless as drawn — they only ever go up, so the chart is a straight line at any
scale — but the difference between two readings over the time between them is
throughput, which is the thing somebody actually wants when the question is
whether the site's uplink is saturated at four o'clock every afternoon.

tsdb.services.csv is memory, threads and handles for each 3CX service. It keys
on a number, and a report that says "service 9 is using 362 MB" is not worth
printing. The numbers are indices into the service enum the XAPI publishes, and
the fit is checkable rather than assumed: index 0 shows fifteen processes and no
threads, which is Postgres, and index 13 shows three, which is nginx and its
workers. Both land exactly where the enum says Database and Nginx are.
*/

// serviceNames are the 3CX services in the order the XAPI enumerates them,
// which is the order the metrics table indexes them by. The two misspellings
// are 3CX's own and are kept so this list can be diffed against the API.
var serviceNames = []string{
	"Database", "Configuration", "AudioProvider", "SIPService", "SystemService",
	"IVR", "CallFlow", "QueueMananger", "Tunnel", "ManagementConsole",
	"MediaServer", "HotelModule", "EventNotiificationManager", "Nginx",
	"Gateway", "AiBridge",
}

// Network is throughput, derived from the counters rather than drawn from them.
type Network struct {
	Interface string  `json:"interface"`
	SentMbps  []Point `json:"sent_mbps,omitempty"`
	RecvMbps  []Point `json:"received_mbps,omitempty"`
	PeakSent  float64 `json:"peak_sent_mbps,omitempty"`
	PeakRecv  float64 `json:"peak_received_mbps,omitempty"`
	TotalSent float64 `json:"total_sent_gb,omitempty"`
	TotalRecv float64 `json:"total_received_gb,omitempty"`
}

// Service is what one 3CX service was doing with the machine.
type Service struct {
	Name string `json:"name"`
	// MemoryMB is where it finished, PeakMemoryMB the worst it got, and
	// GrowthMB how much more it held at the end than at the beginning — which
	// is what a slow leak looks like from outside.
	MemoryMB     float64 `json:"memory_mb"`
	PeakMemoryMB float64 `json:"peak_memory_mb,omitempty"`
	GrowthMB     float64 `json:"growth_mb,omitempty"`
	Threads      int     `json:"threads,omitempty"`
	Processes    int     `json:"processes,omitempty"`
}

// networkTotals accumulates the counters, one interface at a time.
type networkTotals struct {
	byInterface map[string]*interfaceCounter
}

type interfaceCounter struct {
	name              string
	lastAt            time.Time
	lastSent, lastRcv float64
	firstSent         float64
	firstRcv          float64
	started           bool
	sent, received    []Point
	peakSent          float64
	peakRecv          float64
}

func newNetworkTotals() *networkTotals {
	return &networkTotals{byInterface: map[string]*interfaceCounter{}}
}

// readNetwork takes one row, turning the counter into a rate.
func (n *networkTotals) readNetwork(name string, at time.Time, sent, received float64) {
	if name == "" || at.IsZero() {
		return
	}
	c, ok := n.byInterface[name]
	if !ok {
		c = &interfaceCounter{name: name, firstSent: sent, firstRcv: received}
		n.byInterface[name] = c
	}

	if c.started {
		elapsed := at.Sub(c.lastAt).Seconds()
		// A counter that went backwards is a service restart or a wrap, and
		// the difference across it is not a rate. Skipped rather than drawn as
		// a spike, because a spike is what somebody would investigate.
		if elapsed > 0 && sent >= c.lastSent && received >= c.lastRcv {
			sentMbps := round((sent-c.lastSent)*8/elapsed/1e6, 3)
			recvMbps := round((received-c.lastRcv)*8/elapsed/1e6, 3)
			c.sent = append(c.sent, Point{At: at, Value: sentMbps})
			c.received = append(c.received, Point{At: at, Value: recvMbps})
			if sentMbps > c.peakSent {
				c.peakSent = sentMbps
			}
			if recvMbps > c.peakRecv {
				c.peakRecv = recvMbps
			}
		}
	}
	c.started = true
	c.lastAt, c.lastSent, c.lastRcv = at, sent, received
}

// result reports the busiest interface. A machine has a loopback and often a
// tunnel; the one that carried the traffic is the one worth a chart.
func (n *networkTotals) result() *Network {
	var best *interfaceCounter
	for _, c := range n.byInterface {
		if best == nil || (c.lastRcv-c.firstRcv)+(c.lastSent-c.firstSent) >
			(best.lastRcv-best.firstRcv)+(best.lastSent-best.firstSent) {
			best = c
		}
	}
	if best == nil || len(best.sent) == 0 {
		return nil
	}
	return &Network{
		Interface: best.name,
		SentMbps:  thin(best.sent),
		RecvMbps:  thin(best.received),
		PeakSent:  best.peakSent,
		PeakRecv:  best.peakRecv,
		TotalSent: round((best.lastSent-best.firstSent)/(1<<30), 2),
		TotalRecv: round((best.lastRcv-best.firstRcv)/(1<<30), 2),
	}
}

// serviceTotals accumulates the per-service table.
type serviceTotals struct {
	byID map[int]*serviceCounter
}

type serviceCounter struct {
	id                 int
	firstMem, lastMem  float64
	peakMem            float64
	threads, processes int
	started            bool
}

func newServiceTotals() *serviceTotals {
	return &serviceTotals{byID: map[int]*serviceCounter{}}
}

func (s *serviceTotals) readService(id int, memory float64, threads, processes int) {
	c, ok := s.byID[id]
	if !ok {
		c = &serviceCounter{id: id, firstMem: memory}
		s.byID[id] = c
	}
	if !c.started {
		c.firstMem = memory
		c.started = true
	}
	c.lastMem = memory
	if memory > c.peakMem {
		c.peakMem = memory
	}
	c.threads, c.processes = threads, processes
}

func (s *serviceTotals) result() []Service {
	out := make([]Service, 0, len(s.byID))
	for _, c := range s.byID {
		out = append(out, Service{
			Name:         serviceName(c.id),
			MemoryMB:     round(c.lastMem/1e6, 1),
			PeakMemoryMB: round(c.peakMem/1e6, 1),
			GrowthMB:     round((c.lastMem-c.firstMem)/1e6, 1),
			Threads:      c.threads,
			Processes:    c.processes,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].MemoryMB > out[j].MemoryMB })
	return out
}

// serviceName decodes the index, and says so plainly when it cannot.
func serviceName(id int) string {
	if id >= 0 && id < len(serviceNames) {
		return serviceNames[id]
	}
	return fmt.Sprintf("service %d", id)
}

/*
serviceFindings looks for a service holding more memory than it started with.

Deliberately blunt about what it does not know. A week is long enough for a
genuine leak to show and far too short to prove one — a service that was
restarted mid-capture, or that cached something it will release, looks
identical from here. So the threshold is high, the wording says "worth
watching" rather than "leaking", and the number it grew by is quoted so
somebody can judge it themselves.
*/
func serviceFindings(services []Service, totalMemoryGB float64) []Finding {
	// Under a quarter of a gigabyte of growth is not worth anybody's attention
	// on a machine that measures memory in gigabytes.
	const growthMB = 256

	var out []Finding
	for _, s := range services {
		if s.GrowthMB < growthMB {
			continue
		}
		detail := fmt.Sprintf("%s finished the capture holding %.0f MB, which is %.0f MB more than it "+
			"started with, peaking at %.0f MB. That may be a leak or it may be a cache it will give back; "+
			"a capture is too short to tell them apart. Worth watching if the phone system has been "+
			"restarting on its own.", s.Name, s.MemoryMB, s.GrowthMB, s.PeakMemoryMB)
		severity := Note
		// Growth worth a warning is growth measured against the machine it is
		// running on, not an absolute.
		if totalMemoryGB > 0 && s.PeakMemoryMB/1024 > totalMemoryGB*0.25 {
			severity = Warning
			detail += fmt.Sprintf(" At its peak that was a quarter of the %.1f GB on this machine.", totalMemoryGB)
		}
		out = append(out, Finding{
			Severity: severity,
			Title:    fmt.Sprintf("%s grew by %.0f MB over the capture", s.Name, s.GrowthMB),
			Detail:   detail,
			Source:   "DbTables/tsdb.services.csv",
		})
	}
	return out
}

// networkFindings reports a link that spent the capture near its limit.
func networkFindings(n *Network) []Finding {
	if n == nil {
		return nil
	}
	// 100 Mbps is the smallest link anybody still runs a phone system behind,
	// and 80% of it is where voice starts queueing behind everything else.
	const busyMbps = 80

	if n.PeakRecv < busyMbps && n.PeakSent < busyMbps {
		return nil
	}
	return []Finding{{
		Severity: Note,
		Title:    fmt.Sprintf("Peak throughput on %s reached %.0f Mbps", n.Interface, maxOf(n.PeakSent, n.PeakRecv)),
		Detail: fmt.Sprintf("Sent peaked at %.0f Mbps and received at %.0f Mbps, %.0f GB and %.0f GB over the "+
			"capture. That is only a problem on a link small enough to fill, but if calls go bad at a "+
			"predictable time of day this is the chart to line them up against.",
			n.PeakSent, n.PeakRecv, n.TotalSent, n.TotalRecv),
		Source: "DbTables/tsdb.network.csv",
	}}
}

func maxOf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
