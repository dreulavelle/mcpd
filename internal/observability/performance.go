package observability

import (
	"math"
	"sort"

	dto "github.com/prometheus/client_model/go"
)

// Performance is the metrics surface shaped for the console.
//
// It is read from the same registry the /metrics endpoint serves, rather than
// from counters kept alongside it. Two sources for one number is how a
// dashboard comes to disagree with the exposition format nobody checks it
// against, and the disagreement is always discovered while something is
// already wrong.
//
// The shaping that does happen here is the shaping a chart needs and a scrape
// does not. Prometheus buckets are cumulative because that is what makes them
// aggregatable across instances; a bar chart wants the count that fell *in*
// each bucket. Doing that conversion once, here, is better than doing it in
// TypeScript against a wire format whose conventions the browser has no reason
// to know.
type Performance struct {
	// Tools is one entry per plugin and tool that has been called.
	Tools []ToolStats `json:"tools"`
	// Upstream is how long each plugin's own API took, which is what says
	// whether a slow tool is this host's fault or the far end's.
	Upstream []UpstreamStats `json:"upstream"`
	// Cache is what each plugin's read cache did, by the kind of read.
	Cache []CacheStats `json:"cache"`
	// ResultBudgetBytes is the ceiling a plugin builds an answer against, sent
	// so the console can mark it without keeping a second copy of a number
	// that lives in Go. A browser that hardcoded it would go on drawing a line
	// at the old value the day the budget moved.
	ResultBudgetBytes int `json:"result_budget_bytes"`
}

// ToolStats is everything known about one tool.
type ToolStats struct {
	Plugin string `json:"plugin"`
	Tool   string `json:"tool"`
	// Calls counts every attempt, including the ones refused before the
	// handler ran. A tool that is mostly denied is a grant problem, and it
	// looks identical to an unused tool if only successes are counted.
	Calls Outcomes `json:"calls"`
	// Duration covers successful and failed calls; refusals are not timed.
	Duration *Distribution `json:"duration,omitempty"`
	// ResultBytes covers successful calls only. Absent means nothing has
	// been measured yet, which is not the same as answers of size zero.
	ResultBytes *Distribution `json:"result_bytes,omitempty"`
}

// Outcomes is the four ways a call ends.
type Outcomes struct {
	OK          uint64 `json:"ok"`
	Error       uint64 `json:"error"`
	Denied      uint64 `json:"denied"`
	RateLimited uint64 `json:"rate_limited"`
}

// Total is every attempt, however it ended.
func (o Outcomes) Total() uint64 { return o.OK + o.Error + o.Denied + o.RateLimited }

// UpstreamStats is one plugin's own API, by whether the request worked.
type UpstreamStats struct {
	Plugin   string       `json:"plugin"`
	Outcome  string       `json:"outcome"`
	Duration Distribution `json:"duration"`
}

// CacheStats is one plugin's read cache for one kind of read.
type CacheStats struct {
	Plugin string `json:"plugin"`
	Kind   string `json:"kind"`
	Hit    uint64 `json:"hit"`
	Miss   uint64 `json:"miss"`
	// Shared joined a fetch already in flight. Counted apart from a hit
	// because it is the half of the win a hit rate does not show.
	Shared uint64 `json:"shared"`
}

// Distribution is a histogram in the shape a chart wants.
type Distribution struct {
	Count uint64  `json:"count"`
	Sum   float64 `json:"sum"`
	// Buckets hold the count that fell in each one, not the running total.
	Buckets []Bucket `json:"buckets"`
	// P50 and P95 are interpolated within the bucket they land in, the same
	// estimate histogram_quantile makes and with the same caveat: a quantile
	// is no finer than the boundaries either side of it.
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

// Bucket is one bar.
type Bucket struct {
	// LE is the inclusive upper bound. Nil is the overflow bucket, which has
	// no bound -- the observations past the last boundary. Rendering it as a
	// number would invent a ceiling the data does not have.
	LE    *float64 `json:"le"`
	Count uint64   `json:"count"`
}

// Performance reads the registry and shapes it.
//
// A nil *Metrics reports an empty surface rather than failing: a host with the
// endpoint switched off has a console that shows nothing, which is honest.
func (m *Metrics) Performance() Performance {
	out := Performance{
		Tools:             []ToolStats{},
		Upstream:          []UpstreamStats{},
		Cache:             []CacheStats{},
		ResultBudgetBytes: ResultBudgetBytes,
	}
	if m == nil {
		return out
	}
	families, err := m.registry.Gather()
	if err != nil && len(families) == 0 {
		// Gather reports partial failure alongside whatever it did collect, so
		// only a total failure is nothing to show.
		return out
	}

	tools := map[[2]string]*ToolStats{}
	toolAt := func(plugin, tool string) *ToolStats {
		key := [2]string{plugin, tool}
		if t, ok := tools[key]; ok {
			return t
		}
		t := &ToolStats{Plugin: plugin, Tool: tool}
		tools[key] = t
		return t
	}
	caches := map[[2]string]*CacheStats{}

	for _, f := range families {
		switch f.GetName() {
		case "mcpd_tool_calls_total":
			for _, mt := range f.GetMetric() {
				t := toolAt(label(mt, "plugin"), label(mt, "tool"))
				n := uint64(mt.GetCounter().GetValue())
				switch label(mt, "outcome") {
				case OutcomeOK:
					t.Calls.OK += n
				case OutcomeError:
					t.Calls.Error += n
				case OutcomeDenied:
					t.Calls.Denied += n
				case OutcomeRateLimited:
					t.Calls.RateLimited += n
				}
			}
		case "mcpd_tool_call_duration_seconds":
			for _, mt := range f.GetMetric() {
				d := distribution(mt.GetHistogram())
				toolAt(label(mt, "plugin"), label(mt, "tool")).Duration = &d
			}
		case "mcpd_tool_result_bytes":
			for _, mt := range f.GetMetric() {
				d := distribution(mt.GetHistogram())
				toolAt(label(mt, "plugin"), label(mt, "tool")).ResultBytes = &d
			}
		case "mcpd_upstream_request_duration_seconds":
			for _, mt := range f.GetMetric() {
				out.Upstream = append(out.Upstream, UpstreamStats{
					Plugin:   label(mt, "plugin"),
					Outcome:  label(mt, "outcome"),
					Duration: distribution(mt.GetHistogram()),
				})
			}
		case "mcpd_plugin_cache_events_total":
			for _, mt := range f.GetMetric() {
				key := [2]string{label(mt, "plugin"), label(mt, "kind")}
				c, ok := caches[key]
				if !ok {
					c = &CacheStats{Plugin: key[0], Kind: key[1]}
					caches[key] = c
				}
				n := uint64(mt.GetCounter().GetValue())
				switch label(mt, "event") {
				case "hit":
					c.Hit += n
				case "miss":
					c.Miss += n
				case "shared":
					c.Shared += n
				}
			}
		}
	}

	for _, t := range tools {
		out.Tools = append(out.Tools, *t)
	}
	for _, c := range caches {
		out.Cache = append(out.Cache, *c)
	}
	// Sorted so the console renders the same order twice running. A map's
	// order would reshuffle the table on every poll.
	sort.Slice(out.Tools, func(i, j int) bool {
		if out.Tools[i].Plugin != out.Tools[j].Plugin {
			return out.Tools[i].Plugin < out.Tools[j].Plugin
		}
		return out.Tools[i].Tool < out.Tools[j].Tool
	})
	sort.Slice(out.Upstream, func(i, j int) bool {
		if out.Upstream[i].Plugin != out.Upstream[j].Plugin {
			return out.Upstream[i].Plugin < out.Upstream[j].Plugin
		}
		return out.Upstream[i].Outcome < out.Upstream[j].Outcome
	})
	sort.Slice(out.Cache, func(i, j int) bool {
		if out.Cache[i].Plugin != out.Cache[j].Plugin {
			return out.Cache[i].Plugin < out.Cache[j].Plugin
		}
		return out.Cache[i].Kind < out.Cache[j].Kind
	})
	return out
}

// label reads one label off a metric, empty if it carries no such label.
func label(m *dto.Metric, name string) string {
	for _, p := range m.GetLabel() {
		if p.GetName() == name {
			return p.GetValue()
		}
	}
	return ""
}

// distribution converts one Prometheus histogram into the console's shape.
//
// The cumulative-to-per-bucket subtraction is the whole job. The overflow
// bucket is what the total count minus the last cumulative count leaves, and
// it is carried explicitly because it is the interesting one: for result sizes
// it is the answers that went past what may be sent.
func distribution(h *dto.Histogram) Distribution {
	d := Distribution{Count: h.GetSampleCount(), Sum: h.GetSampleSum(), Buckets: []Bucket{}}

	buckets := h.GetBucket()
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].GetUpperBound() < buckets[j].GetUpperBound()
	})

	var prev uint64
	for _, b := range buckets {
		bound := b.GetUpperBound()
		if math.IsInf(bound, 1) {
			// client_golang does not normally emit +Inf here, but a histogram
			// configured with one would otherwise produce a bar with a bound
			// the overflow entry already covers.
			continue
		}
		cumulative := b.GetCumulativeCount()
		le := bound
		d.Buckets = append(d.Buckets, Bucket{LE: &le, Count: cumulative - prev})
		prev = cumulative
	}
	if d.Count > prev {
		d.Buckets = append(d.Buckets, Bucket{LE: nil, Count: d.Count - prev})
	}

	d.P50 = quantile(buckets, d.Count, 0.50)
	d.P95 = quantile(buckets, d.Count, 0.95)
	return d
}

// quantile estimates one from cumulative buckets, interpolating linearly
// inside the bucket the rank lands in.
//
// The same estimate Prometheus makes, and it inherits the same limits: a value
// past the last boundary cannot be placed, so it reports that boundary rather
// than an invented number, and a distribution with nothing in it reports zero
// rather than a quantile of no observations.
func quantile(buckets []*dto.Bucket, count uint64, q float64) float64 {
	if count == 0 || len(buckets) == 0 {
		return 0
	}
	rank := q * float64(count)
	var prevCount uint64
	var prevBound float64
	for _, b := range buckets {
		bound := b.GetUpperBound()
		if math.IsInf(bound, 1) {
			continue
		}
		cumulative := b.GetCumulativeCount()
		if float64(cumulative) >= rank {
			within := float64(cumulative - prevCount)
			if within <= 0 {
				return bound
			}
			return prevBound + (bound-prevBound)*((rank-float64(prevCount))/within)
		}
		prevCount, prevBound = cumulative, bound
	}
	// Past the last boundary: report it rather than extrapolating into a range
	// the buckets say nothing about.
	return prevBound
}
