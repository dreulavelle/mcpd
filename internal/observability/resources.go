package observability

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Resources is what this process is costing the machine it runs on.
//
// Separate from the Prometheus endpoint on purpose. /metrics is for a
// monitoring system that will scrape it every fifteen seconds forever; this is
// for a person who has opened the dashboard once because something feels slow,
// and wants an answer without standing up Prometheus first. The two would
// otherwise be the same numbers shaped for very different readers.
//
// Everything here is read from the Go runtime and from /proc, so it costs a
// syscall or two and needs no privileges. Fields that cannot be read on this
// platform are omitted rather than zeroed: nought bytes resident is a
// measurement, and "we could not tell" is not.
type Resources struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	UptimeSec int64     `json:"uptime_seconds"`

	// Go runtime.
	Goroutines int `json:"goroutines"`
	OSThreads  int `json:"os_threads"`
	NumCPU     int `json:"num_cpu"`
	GOMAXPROCS int `json:"gomaxprocs"`

	// Memory, in bytes. HeapInUse is what the program is actually holding;
	// Sys is what it has taken from the OS and may not have given back. The
	// two diverge enough that showing only one invites a wrong conclusion.
	HeapInUse   uint64 `json:"heap_in_use_bytes"`
	HeapAlloc   uint64 `json:"heap_alloc_bytes"`
	StackInUse  uint64 `json:"stack_in_use_bytes"`
	Sys         uint64 `json:"sys_bytes"`
	TotalAlloc  uint64 `json:"total_alloc_bytes"`
	ResidentRSS uint64 `json:"resident_bytes,omitempty"`

	// MemoryLimit is the cgroup limit this process runs under, when there is
	// one. Without it a memory figure has no scale: 300 MB is comfortable
	// under a 1 GB limit and nearly fatal under 320 MB.
	MemoryLimit uint64 `json:"memory_limit_bytes,omitempty"`

	// Garbage collection.
	NumGC        uint32  `json:"gc_cycles"`
	GCPauseMs    float64 `json:"gc_pause_total_ms"`
	LastGC       string  `json:"last_gc,omitempty"`
	GCCPUPercent float64 `json:"gc_cpu_percent"`

	// CPU seconds this process has used, user and system together. A rate is
	// left to the caller: two readings a few seconds apart give utilisation,
	// and computing it here would mean holding state for a page that may
	// never be opened twice.
	CPUSeconds float64 `json:"cpu_seconds,omitempty"`

	// OpenFiles is how many descriptors are open, which is the resource a
	// long-running host with many upstreams actually exhausts first.
	OpenFiles int `json:"open_files,omitempty"`
}

// Snapshot reads the current resource usage.
func Snapshot(version string, startedAt time.Time, now time.Time) Resources {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	out := Resources{
		Version:      version,
		StartedAt:    startedAt,
		UptimeSec:    int64(now.Sub(startedAt).Seconds()),
		Goroutines:   runtime.NumGoroutine(),
		OSThreads:    osThreads(),
		NumCPU:       runtime.NumCPU(),
		GOMAXPROCS:   runtime.GOMAXPROCS(0),
		HeapInUse:    m.HeapInuse,
		HeapAlloc:    m.HeapAlloc,
		StackInUse:   m.StackInuse,
		Sys:          m.Sys,
		TotalAlloc:   m.TotalAlloc,
		NumGC:        m.NumGC,
		GCPauseMs:    float64(m.PauseTotalNs) / float64(time.Millisecond),
		GCCPUPercent: m.GCCPUFraction * 100,
	}
	if m.LastGC > 0 {
		out.LastGC = time.Unix(0, int64(m.LastGC)).UTC().Format(time.RFC3339)
	}
	out.ResidentRSS = residentBytes()
	out.MemoryLimit = memoryLimit()
	out.CPUSeconds = cpuSeconds()
	out.OpenFiles = openFiles()
	return out
}

// osThreads reports threads the runtime has created. There is no exported
// accessor, so this reads the same counter the runtime publishes to /proc.
func osThreads() int {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "Threads:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(rest))
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

// residentBytes reads RSS from /proc/self/statm, whose second field is the
// resident set in pages.
func residentBytes() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

// memoryLimit reads the cgroup memory ceiling, v2 first and then v1.
//
// "max" means no limit was set, which is reported as no limit rather than as
// an enormous number: a progress bar against 9223372036854771712 bytes is
// worse than no progress bar.
func memoryLimit() uint64 {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" || text == "max" {
			continue
		}
		n, err := strconv.ParseUint(text, 10, 64)
		if err != nil {
			continue
		}
		// cgroup v1 reports "unlimited" as a number near the top of the range.
		if n >= 1<<62 {
			continue
		}
		return n
	}
	return 0
}

// cpuSeconds reads utime+stime from /proc/self/stat, fields 14 and 15.
//
// The command name in field 2 is wrapped in parentheses and may contain
// spaces, so the fields are counted from after the closing one rather than by
// splitting the whole line.
func cpuSeconds() float64 {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	line := string(data)
	end := strings.LastIndex(line, ")")
	if end < 0 || end+2 > len(line) {
		return 0
	}
	fields := strings.Fields(line[end+2:])
	// After the comm field, state is index 0, so utime (field 14) is index 11
	// and stime (field 15) is index 12.
	if len(fields) < 13 {
		return 0
	}
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	if err1 != nil || err2 != nil {
		return 0
	}
	// USER_HZ is 100 on every Linux this runs on; there is no portable way to
	// read it without cgo, and getting it wrong scales a number nobody
	// compares across machines.
	const userHZ = 100.0
	return (utime + stime) / userHZ
}

// openFiles counts entries in /proc/self/fd, less the descriptor the read
// itself is holding.
func openFiles() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0
	}
	if len(entries) == 0 {
		return 0
	}
	return len(entries) - 1
}
