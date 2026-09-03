package supportinfo

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

/*
Call quality, which 3CX measures per call and files in the event log.

Every finished call of any length gets a report: a MOS score each way, jitter,
packet loss, the codec, what kind of endpoint each party was on, and a sentence
in 3CX's own words about why the call scored the way it did. It is by far the
most direct evidence of what a caller actually heard, and it is buried in a CSV
column as escaped JSON.

Two things about MOS that have to be right or the whole section lies.

Zero is not a score. A call too short to measure, or one where no RTCP came
back, is filed with MOS 0 — and averaging those in reports a healthy system as
catastrophic. They are counted separately as unrated, because "we could not
measure 90% of calls" is itself a finding.

And direction matters. MOSFromPBX is what the party heard; MOSToPBX is what the
PBX heard from them. One-way audio shows up as one of those being fine and the
other being nothing, which is invisible in an average of the two.
*/

// Quality is what callers actually experienced.
type Quality struct {
	// Calls is how many quality reports were in the log.
	Calls int `json:"calls"`
	// RatedLegs is how many call legs carried a usable score. A call has two
	// legs, so this can exceed Calls and routinely does; the gap between it
	// and twice Calls is the legs too short to measure or with no RTCP back.
	RatedLegs int `json:"rated_legs"`
	// Poor is rated legs below the 3.6 usually taken as the floor for a call
	// nobody would complain about.
	Poor      int     `json:"poor"`
	MedianMOS float64 `json:"median_mos,omitempty"`
	WorstMOS  float64 `json:"worst_mos,omitempty"`
	// LossPercent and JitterMS are medians across rated legs.
	LossPercent float64 `json:"loss_percent,omitempty"`
	JitterMS    float64 `json:"jitter_ms,omitempty"`

	Codecs    []Counted `json:"codecs,omitempty"`
	Endpoints []Counted `json:"endpoints,omitempty"`
	// Reasons is 3CX's own explanation of why calls scored badly, counted.
	Reasons []Counted `json:"reasons,omitempty"`
	// Worst is the handful of individual calls worth opening.
	Worst []QualityCall `json:"worst,omitempty"`
}

// Counted is a thing and how often it was seen.
type Counted struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// QualityCall is one call worth looking at.
type QualityCall struct {
	Number   string  `json:"number,omitempty"`
	Duration string  `json:"duration,omitempty"`
	MOS      float64 `json:"mos,omitempty"`
	Codec    string  `json:"codec,omitempty"`
	Endpoint string  `json:"endpoint,omitempty"`
	Agent    string  `json:"agent,omitempty"`
	Address  string  `json:"address,omitempty"`
	Jitter   int     `json:"jitter_ms,omitempty"`
	LossPct  float64 `json:"loss_percent,omitempty"`
	Reason   string  `json:"reason,omitempty"`
}

// rawQuality mirrors the JSON 3CX writes into the event message.
type rawQuality struct {
	Reason      string    `json:"Reason"`
	MOS         float64   `json:"MOS"`
	Transcoding bool      `json:"Transcoding"`
	Party1      *rawParty `json:"Party1"`
	Party2      *rawParty `json:"Party2"`
}

type rawParty struct {
	Duration     string  `json:"Duration"`
	Number       string  `json:"Number"`
	Codec        string  `json:"Codec"`
	RxJitter     int     `json:"RxJitter"`
	RxLost       int     `json:"RxLost"`
	RxPackets    int     `json:"RxPackets"`
	TxJitter     int     `json:"TxJitter"`
	TxLost       int     `json:"TxLost"`
	TxPackets    int     `json:"TxPackets"`
	MOSFromPBX   float64 `json:"MOSFromPBX"`
	MOSToPBX     float64 `json:"MOSToPBX"`
	UserAgent    string  `json:"UserAgent"`
	EndpointType string  `json:"EndpointType"`
	AddressStr   string  `json:"AddressStr"`
}

// qualityTotals accumulates call quality as the event log streams past.
type qualityTotals struct {
	calls     int
	scores    []float64
	losses    []float64
	jitters   []float64
	poor      int
	codecs    map[string]int
	endpoints map[string]int
	reasons   map[string]int
	worst     []QualityCall
}

// poorMOS is the floor below which somebody notices. 4.0+ is toll quality,
// 3.6 is the usual line for "acceptable", and below 3 people complain.
const poorMOS = 3.6

func newQualityTotals() *qualityTotals {
	return &qualityTotals{
		codecs: map[string]int{}, endpoints: map[string]int{}, reasons: map[string]int{},
	}
}

// readQuality takes the JSON out of one call-quality event.
func (q *qualityTotals) readQuality(message string) {
	var raw rawQuality
	if err := json.Unmarshal([]byte(strings.TrimSpace(message)), &raw); err != nil {
		return
	}
	q.calls++

	for _, line := range strings.Split(raw.Reason, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			q.reasons[line]++
		}
	}

	for _, p := range []*rawParty{raw.Party1, raw.Party2} {
		if p == nil {
			continue
		}
		if p.Codec != "" {
			q.codecs[p.Codec]++
		}
		if p.EndpointType != "" {
			q.endpoints[p.EndpointType]++
		}

		// What this party heard. Zero means unmeasured, not silent.
		heard := p.MOSFromPBX
		if heard <= 0 {
			continue
		}
		loss := lossPercent(p.RxLost, p.RxPackets)
		q.scores = append(q.scores, heard)
		q.losses = append(q.losses, loss)
		q.jitters = append(q.jitters, float64(p.RxJitter))
		if heard >= poorMOS {
			continue
		}
		q.poor++
		q.worst = append(q.worst, QualityCall{
			Number:   p.Number,
			Duration: p.Duration,
			MOS:      round(heard, 2),
			Codec:    p.Codec,
			Endpoint: p.EndpointType,
			Agent:    p.UserAgent,
			Address:  p.AddressStr,
			Jitter:   p.RxJitter,
			LossPct:  round(loss, 1),
			Reason:   firstLine(raw.Reason),
		})
	}
}

func lossPercent(lost, received int) float64 {
	total := lost + received
	if total == 0 {
		return 0
	}
	return float64(lost) * 100 / float64(total)
}

func (q *qualityTotals) result() *Quality {
	if q.calls == 0 {
		return nil
	}
	out := &Quality{
		Calls:     q.calls,
		RatedLegs: len(q.scores),
		Poor:      q.poor,
		Codecs:    counted(q.codecs, 6),
		Endpoints: counted(q.endpoints, 6),
		Reasons:   counted(q.reasons, 6),
	}
	if len(q.scores) > 0 {
		out.MedianMOS = round(median(q.scores), 2)
		out.WorstMOS = round(minOf(q.scores), 2)
		out.LossPercent = round(median(q.losses), 2)
		out.JitterMS = round(median(q.jitters), 1)
	}

	sort.SliceStable(q.worst, func(i, j int) bool { return q.worst[i].MOS < q.worst[j].MOS })
	if len(q.worst) > 10 {
		q.worst = q.worst[:10]
	}
	out.Worst = q.worst
	return out
}

func (q *qualityTotals) findings(source string) []Finding {
	result := q.result()
	if result == nil {
		return nil
	}
	var out []Finding

	if result.RatedLegs > 0 && result.Poor > 0 {
		share := float64(result.Poor) * 100 / float64(result.RatedLegs)
		severity := Warning
		if share >= 25 {
			severity = Critical
		}
		out = append(out, Finding{
			Severity: severity,
			Title: fmt.Sprintf("%s of %s scored below usable quality",
				plural(result.Poor, "call leg", "call legs"), plural(result.RatedLegs, "measured leg", "measured legs")),
			Detail: fmt.Sprintf("That is %.0f%% of the calls the system could measure, with a median score of "+
				"%.2f and a worst of %.2f. Below about %.1f is where people start saying the line was breaking up. "+
				"Median jitter %.0f ms and %.1f%% packet loss on those legs.",
				share, result.MedianMOS, result.WorstMOS, poorMOS, result.JitterMS, result.LossPercent),
			Occurrences: result.Poor,
			Source:      source,
			Evidence:    reasonLines(result.Reasons),
		})
	}

	// Unmeasurable calls are their own signal: if the PBX cannot score most
	// calls it is usually because RTCP is not coming back, which is the same
	// network problem that causes the audio complaints.
	// Two legs to a call, so that is the number of scores a fully measured
	// capture would have produced.
	legs := result.Calls * 2
	if unrated := legs - result.RatedLegs; result.Calls >= 20 && result.RatedLegs < legs/2 {
		out = append(out, Finding{
			Severity: Warning,
			Title:    "Most calls could not be scored for quality",
			Detail: fmt.Sprintf("%d of the call legs in this capture carried no usable quality score. Very short "+
				"calls explain some of it, but the usual cause is RTCP not making it back to the phone system — "+
				"which is the same firewall or NAT problem that produces one-way audio.", unrated),
			Occurrences: unrated,
			Source:      source,
			Evidence:    reasonLines(result.Reasons),
		})
	}
	return out
}

func reasonLines(reasons []Counted) []string {
	out := make([]string, 0, maxEvidence)
	for i, r := range reasons {
		if i == maxEvidence {
			break
		}
		out = append(out, fmt.Sprintf("%s (%d)", r.Name, r.Count))
	}
	return out
}

// counted turns a tally into the most common few.
func counted(counts map[string]int, limit int) []Counted {
	out := make([]Counted, 0, len(counts))
	for name, n := range counts {
		out = append(out, Counted{Name: name, Count: n})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func minOf(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	low := values[0]
	for _, v := range values {
		if v < low {
			low = v
		}
	}
	return low
}
