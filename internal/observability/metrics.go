package observability

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the host's Prometheus surface.
//
// Every series here answers a question somebody actually asks while operating
// this host, and nothing is here because it was easy to count. The list, and
// the question each one answers:
//
//   - tool_calls_total: which integration is being used, and how often a call
//     fails, is refused for want of a capability, or is turned away by a rate
//     limit. It is the first thing to look at when an assistant reports that
//     an integration "does not work".
//   - tool_call_duration_seconds: which tool is slow. A model with a deadline
//     abandons a slow tool and reports a failure that looks like an outage.
//   - tool_result_bytes: how large an answer is, measured in the units
//     plugins.MaxResultBytes budgets against. A tool whose results sit against
//     the ceiling is one whose answers are being cut, and this says which tool
//     and how often before anybody reports a reply that stopped mid-sentence.
//     Note that the wire cost is twice this: the specification has a result
//     carried as structured content and again as text.
//   - mutation_proposals_total: how often a class of change is being asked
//     for, and how often the per-caller limit is refusing it -- the number
//     that says whether a standing rule is being leaned on harder than the
//     operator expected.
//   - operations / operations_authorized_by_rule: how many changes are in each
//     state right now, and how many of them nobody was asked about. Read from
//     the database at scrape time, so it is exact and survives a restart.
//   - upstream_request_duration_seconds: whether a plugin's own upstream is
//     the slow part, as opposed to this host.
//   - catalog_requests_total / catalog_fetch_duration_seconds: whether
//     browsing the public catalogues is being served from cache, and which
//     source is failing when a page comes back short.
//   - plugin_cache_events_total: whether a plugin's read cache is earning its
//     memory. A cache with no hits is a bug, not a feature.
//   - outbox_pending / outbox_published_total: whether committed state changes
//     are reaching their consumers. A backlog means an approval is not waking
//     the executor.
//
// A nil *Metrics is usable and records nothing, so a test or a deployment with
// the endpoint switched off costs one branch per call site rather than a set
// of collectors nobody scrapes.
type Metrics struct {
	registry *prometheus.Registry

	toolCalls      *prometheus.CounterVec
	toolDuration   *prometheus.HistogramVec
	toolResultSize *prometheus.HistogramVec

	proposals *prometheus.CounterVec

	upstream *prometheus.HistogramVec

	catalogRequests *prometheus.CounterVec
	catalogDuration *prometheus.HistogramVec

	pluginCache *prometheus.CounterVec

	outboxPublished *prometheus.CounterVec
}

// Outcomes recorded against a tool call. Kept as constants because a typo in a
// label value produces a second series rather than an error.
const (
	// OutcomeOK is a call that returned a result.
	OutcomeOK = "ok"
	// OutcomeError is a call the plugin or its upstream failed.
	OutcomeError = "error"
	// OutcomeDenied is a call refused by the authorization gate.
	OutcomeDenied = "denied"
	// OutcomeRateLimited is a call refused by a rate limit. Separate from
	// denied because the two call for different actions: one is a grant to
	// change, the other is a limit to raise or a caller to slow down.
	OutcomeRateLimited = "rate_limited"
)

// The values a cache event carries -- hit, miss, shared -- are named in
// internal/plugins, which owns the interface a plugin reports them through.
// They are deliberately not copied here: two sets of constants that have to
// agree are one set too many, and this package only passes them along.

// latencyBuckets spans a fast local call to a slow upstream one.
//
// The default buckets top out at ten seconds, which is inside the range these
// calls routinely occupy: a cnMaestro device walk against a large estate takes
// longer than that, and a bucket set whose last boundary is below the
// interesting values reports every one of them identically.
var latencyBuckets = []float64{
	0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60,
}

// resultSizeBuckets spans a terse answer to one well past what may be sent.
//
// The boundary that matters is 40,000: it is plugins.MaxResultBytes, the
// ceiling a plugin builds against, and a count above it is a result the client
// will cut rather than the plugin. The boundaries either side of it exist so
// "close to the ceiling" and "far past it" are different readings rather than
// one bucket labelled +Inf. 20,000 is the share a two-collection composite
// gets, which is the other place answers bunch up.
//
// Exported so plugins can assert the boundary still matches its own constant;
// this package cannot import that one without a cycle.
var ResultSizeBuckets = []float64{
	512, 2048, 8192, 20_000, ResultBudgetBytes, 60_000, 100_000, 200_000,
}

// ResultBudgetBytes is the ceiling a plugin builds an answer against.
//
// It is plugins.MaxResultBytes, restated here because that package imports
// this one and the reverse would be a cycle. Restated rather than derived from
// the bucket list by position: an index into a slice is arithmetic that goes
// quietly wrong when a boundary is added, and this number is read by the
// console to draw the line an operator judges every tool against. A test in
// the plugins package fails if the two ever disagree.
const ResultBudgetBytes = 40_000

// NewMetrics builds the collectors on a registry of its own.
//
// Its own rather than the default one: the default registry is global state
// anything in the process can write to, including a dependency that registers
// a collector on import. What this host exposes should be what this host
// decided to expose.
func NewMetrics() *Metrics {
	m := &Metrics{registry: prometheus.NewRegistry()}

	m.toolCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mcpd_tool_calls_total",
		Help: "Tool calls by plugin, tool and outcome.",
	}, []string{"plugin", "tool", "outcome"})

	m.toolDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mcpd_tool_call_duration_seconds",
		Help:    "Time a tool call took, including the plugin's own upstream work.",
		Buckets: latencyBuckets,
	}, []string{"plugin", "tool"})

	// Bytes rather than tokens. A token count depends on a tokeniser this
	// host does not have and would differ per model; bytes are exact, and the
	// arithmetic from one to the other is written down once in
	// plugins.MaxResultBytes rather than approximated per series.
	m.toolResultSize = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mcpd_tool_result_bytes",
		Help:    "Size of a successful tool result, as the plugin built it.",
		Buckets: ResultSizeBuckets,
	}, []string{"plugin", "tool"})

	m.proposals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mcpd_mutation_proposals_total",
		Help: "Mutation proposals by plugin, action and outcome.",
	}, []string{"plugin", "action", "outcome"})

	m.upstream = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mcpd_upstream_request_duration_seconds",
		Help:    "Time one request to a plugin's upstream API took.",
		Buckets: latencyBuckets,
	}, []string{"plugin", "outcome"})

	m.catalogRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mcpd_catalog_requests_total",
		Help: "Catalogue reads by source and how they were answered.",
	}, []string{"source", "result"})

	m.catalogDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "mcpd_catalog_fetch_duration_seconds",
		Help:    "Time one fetch from a public catalogue took.",
		Buckets: latencyBuckets,
	}, []string{"source"})

	// kind rather than tool: a plugin classifies its own reads, and a label
	// carrying an identifier -- a device address, a network name -- is a new
	// time series per thing read.
	m.pluginCache = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mcpd_plugin_cache_events_total",
		Help: "A plugin read cache's hits, misses and shared fetches.",
	}, []string{"plugin", "kind", "event"})

	m.outboxPublished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mcpd_outbox_published_total",
		Help: "Outbox events by publication result.",
	}, []string{"result"})

	m.registry.MustRegister(
		m.toolCalls, m.toolDuration, m.toolResultSize, m.proposals, m.upstream,
		m.catalogRequests, m.catalogDuration, m.pluginCache, m.outboxPublished,
	)
	return m
}

// Handler serves the exposition format.
func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "metrics are not enabled", http.StatusNotFound)
		})
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		// A collector that fails at scrape time -- the database being busy,
		// say -- should cost the affected series rather than the whole
		// response, and should say so in the response rather than in a log
		// nobody is reading at the time.
		ErrorHandling: promhttp.ContinueOnError,
	})
}

// Sample is one value with the label values it belongs to, in the order the
// gauge's label names were declared.
type Sample struct {
	Labels []string
	Value  float64
}

// AddGauge registers a value read when a scrape arrives rather than pushed.
//
// Some numbers have an authority that is not this process's memory. How many
// operations are in each state is answered by SQLite, exactly, and a counter
// incremented in Go would drift from it on every restart and every prune. Read
// is called with a bounded context; returning nil reports nothing for this
// scrape, which is the right answer when the source could not be reached.
func (m *Metrics) AddGauge(name, help string, labelNames []string, read func(context.Context) []Sample) {
	if m == nil || read == nil {
		return
	}
	m.registry.MustRegister(&gaugeSource{
		desc: prometheus.NewDesc(name, help, labelNames, nil),
		read: read,
	})
}

// gaugeSource adapts a read function to a Prometheus collector.
type gaugeSource struct {
	desc *prometheus.Desc
	read func(context.Context) []Sample
}

func (g *gaugeSource) Describe(ch chan<- *prometheus.Desc) { ch <- g.desc }

func (g *gaugeSource) Collect(ch chan<- prometheus.Metric) {
	// Bounded here rather than by the caller, because the caller is a scrape
	// and a scrape that hangs takes the monitoring with it.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, s := range g.read(ctx) {
		ch <- prometheus.MustNewConstMetric(g.desc, prometheus.GaugeValue, s.Value, s.Labels...)
	}
}

// ToolCall records one tool call.
func (m *Metrics) ToolCall(plugin, tool, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.toolCalls.WithLabelValues(plugin, tool, outcome).Inc()
	// Only successes and plugin failures are timed. A call refused by the gate
	// or by a rate limit took microseconds and would drag every quantile
	// towards zero, hiding the latency the histogram exists to show.
	if outcome == OutcomeOK || outcome == OutcomeError {
		m.toolDuration.WithLabelValues(plugin, tool).Observe(d.Seconds())
	}
}

// ToolResultSize records how large one successful result was.
//
// The size arrives as a function rather than a number because measuring it
// costs a marshal of the whole answer, and a host with no metrics endpoint
// should not pay for a series nobody scrapes. A nil *Metrics never calls it.
func (m *Metrics) ToolResultSize(plugin, tool string, size func() int) {
	if m == nil || size == nil {
		return
	}
	// Negative means the caller could not measure it. Not recorded, because a
	// zero would read as a tool that answered with nothing.
	if n := size(); n >= 0 {
		m.toolResultSize.WithLabelValues(plugin, tool).Observe(float64(n))
	}
}

// MutationProposal records one proposal, whether or not it was recorded.
func (m *Metrics) MutationProposal(plugin, action, outcome string) {
	if m == nil {
		return
	}
	m.proposals.WithLabelValues(plugin, action, outcome).Inc()
}

// UpstreamRequest records one request a plugin made to its own upstream.
func (m *Metrics) UpstreamRequest(plugin, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.upstream.WithLabelValues(plugin, outcome).Observe(d.Seconds())
}

// CatalogRequest records how one catalogue read was answered: from a fresh
// entry, from a stale one, by a fetch, or not at all.
func (m *Metrics) CatalogRequest(source, result string) {
	if m == nil {
		return
	}
	m.catalogRequests.WithLabelValues(source, result).Inc()
}

// CatalogFetch records one round trip to a public catalogue.
func (m *Metrics) CatalogFetch(source string, d time.Duration) {
	if m == nil {
		return
	}
	m.catalogDuration.WithLabelValues(source).Observe(d.Seconds())
}

// CacheEvent records what a plugin's own read cache did. It implements the
// narrow interface plugins are handed, so a plugin can report cache outcomes
// and nothing else.
func (m *Metrics) CacheEvent(plugin, kind, event string) {
	if m == nil {
		return
	}
	m.pluginCache.WithLabelValues(plugin, kind, event).Inc()
}

// OutboxPublished records the result of publishing one event.
func (m *Metrics) OutboxPublished(result string) {
	if m == nil {
		return
	}
	m.outboxPublished.WithLabelValues(result).Inc()
}
