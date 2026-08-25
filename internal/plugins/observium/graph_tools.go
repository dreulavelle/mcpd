package observium

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Observium keeps its time series in RRD and serves them only as rendered
// images, through graph.php. There is no endpoint that returns the numbers.
//
// That constrains what this tool can honestly be. It returns links, says they
// are images, and tells the model it cannot read them -- because the failure
// mode otherwise is a model describing a trend it never saw, which is worse
// than having no trend tool at all.
//
// # Why the URLs carry no credential
//
// graph.php authenticates with HTTP basic auth or an existing Observium
// session. The links here are for a person to open in a browser, where their
// own session answers for them. Embedding this plugin's credential in a URL
// would put it in a chat transcript, a model's context, and whatever logs sit
// between -- so the links are plain, and someone not signed in to Observium is
// asked to sign in, which is correct.
//
// # Why the type list is short
//
// Observium's documentation confirms a handful of graph type strings and does
// not publish the rest. Guessing at the others would produce links that render
// an error page, which is a worse answer than a shorter list -- so the
// confirmed ones are offered by name and anything else is passed through
// verbatim for a caller who has read one off Observium's own UI.
const graphPath = "/graph.php"

// graphTypes are the type strings Observium documents, grouped by what
// identifies the entity. Nothing is inferred: an entry here is one the vendor
// names.
var graphTypes = map[string][]graphType{
	"device": {
		{Type: "device_bits", Title: "Total traffic"},
	},
	"port": {
		{Type: "port_bits", Title: "Traffic (bits/sec)"},
	},
	"storage": {
		{Type: "storage_usage", Title: "Storage usage"},
	},
}

type graphType struct {
	Type  string `json:"type"`
	Title string `json:"title"`
}

// deviceKeyed is the set of entity kinds graph.php identifies with device=
// rather than id=. Everything else -- ports, sensors, storage, processors --
// uses id=, which is the entity's own primary key.
var deviceKeyed = map[string]bool{"device": true}

type graphArgs struct {
	Kind string `json:"entity_kind" jsonschema:"what the id refers to: device, port, sensor, storage, processor, or mempool"`
	ID   int    `json:"entity_id" jsonschema:"the entity's numeric id, from the tool that listed it"`
	Type string `json:"graph_type,omitempty" jsonschema:"a specific Observium graph type; omit for the documented default for this kind"`
	From string `json:"from,omitempty" jsonschema:"window start: a unix timestamp, or Observium's relative form such as -1d, -7d, -1m. Default -1d"`
	To   string `json:"to,omitempty" jsonschema:"window end: a unix timestamp or 'now'. Default now"`
	W    int    `json:"width,omitempty" jsonschema:"image width in pixels, default 800"`
	H    int    `json:"height,omitempty" jsonschema:"image height in pixels, default 300"`
}

type graphLink struct {
	Title string `json:"title"`
	Type  string `json:"graph_type"`
	URL   string `json:"url"`
}

type graphResult struct {
	Graphs []graphLink `json:"graphs"`
	Note   string      `json:"note"`
}

func (p *Plugin) graphURLs(_ context.Context, in graphArgs) (graphResult, error) {
	if !p.configured {
		return graphResult{}, fmt.Errorf("observium: not configured yet — set " +
			"its connection details on the Plugins page")
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if kind == "" {
		return graphResult{}, fmt.Errorf(
			"observium: say what entity_id refers to — entity_kind is one of " +
				"device, port, sensor, storage, processor, mempool")
	}
	if in.ID <= 0 {
		return graphResult{}, fmt.Errorf("observium: entity_id is required and " +
			"must be the numeric id from the tool that listed the entity")
	}

	wanted := graphTypes[kind]
	if custom := strings.TrimSpace(in.Type); custom != "" {
		wanted = []graphType{{Type: custom, Title: custom}}
	}
	if len(wanted) == 0 {
		return graphResult{}, fmt.Errorf(
			"observium: no graph type is documented for %q, so building a link "+
				"would mean guessing one. Open the entity in Observium, copy the "+
				"type= value out of a graph's own URL, and pass it as graph_type",
			kind)
	}

	from, to := strings.TrimSpace(in.From), strings.TrimSpace(in.To)
	if from == "" {
		from = "-1d"
	}
	if to == "" {
		to = "now"
	}
	width, height := in.W, in.H
	if width <= 0 {
		width = 800
	}
	if height <= 0 {
		height = 300
	}

	idParam := "id"
	if deviceKeyed[kind] {
		idParam = "device"
	}

	out := graphResult{Note: "These are links to PNG images. You cannot see " +
		"what they contain, so do not describe the trend, name a peak, or " +
		"state a value from them — offer the links for the person to open. " +
		"Observium does not serve its time series as data; this is the only " +
		"form it publishes them in."}

	for _, g := range wanted {
		q := url.Values{}
		q.Set("type", g.Type)
		q.Set(idParam, strconv.Itoa(in.ID))
		q.Set("from", from)
		q.Set("to", to)
		q.Set("width", strconv.Itoa(width))
		q.Set("height", strconv.Itoa(height))

		out.Graphs = append(out.Graphs, graphLink{
			Title: g.Title,
			Type:  g.Type,
			URL:   p.client.Root() + graphPath + "?" + q.Encode(),
		})
	}
	return out, nil
}
