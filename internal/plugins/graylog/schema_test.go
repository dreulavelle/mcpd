package graylog

import (
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// The schema a model reads is derived from the handler's parameter type, and
// the time range fields arrive on it by embedding. Embedding is the right
// shape -- it is what stops the three searching tools drifting apart in what
// they accept -- but it only works if the schema generator inlines it, and a
// generator that nested it instead would produce tools with no way to name a
// window and nothing saying so.
func TestSchema_InlinesTheTimeRangeFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema func() (*jsonschema.Schema, error)
	}{
		{"search", func() (*jsonschema.Schema, error) { return jsonschema.For[searchArgs](nil) }},
		{"aggregate", func() (*jsonschema.Schema, error) { return jsonschema.For[aggregateArgs](nil) }},
		{"events", func() (*jsonschema.Schema, error) { return jsonschema.For[eventsArgs](nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := tc.schema()
			if err != nil {
				t.Fatalf("deriving the schema: %v", err)
			}
			for _, field := range []string{"range_seconds", "from", "to", "keyword"} {
				prop, ok := s.Properties[field]
				if !ok {
					t.Fatalf("%s is not in the schema; the embedded time range "+
						"was nested rather than inlined", field)
				}
				// A field with no description is one a model has to guess at,
				// and the whole point of these four is that only one of them
				// may be used at a time.
				if prop.Description == "" {
					t.Errorf("%s has no description", field)
				}
			}
		})
	}
}

// The aggregation arguments are the most structured thing this integration
// takes, and they are anonymous nested structs. If those do not survive
// derivation the tool is unusable in the exact case it exists for.
func TestSchema_DescribesGroupByAndMetrics(t *testing.T) {
	s, err := jsonschema.For[aggregateArgs](nil)
	if err != nil {
		t.Fatalf("deriving the schema: %v", err)
	}
	for _, field := range []string{"group_by", "metrics"} {
		prop, ok := s.Properties[field]
		if !ok {
			t.Fatalf("%s is not in the schema", field)
		}
		if prop.Items == nil || len(prop.Items.Properties) == 0 {
			t.Fatalf("%s has no item shape, so a model cannot fill it in", field)
		}
	}
	if _, ok := s.Properties["metrics"].Items.Properties["function"]; !ok {
		t.Error("a metric's function is not described")
	}
	if _, ok := s.Properties["group_by"].Items.Properties["timeunit"]; !ok {
		t.Error("a grouping's time unit is not described, so no series over time")
	}
}
