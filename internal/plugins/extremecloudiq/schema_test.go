package extremecloudiq

import (
	"slices"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// The schema a model reads is derived from the handler's parameter type, and
// the window fields arrive on it by embedding. Embedding is the right shape --
// it is what stops the four windowed tools drifting apart in what they accept
// -- but it only works if the generator inlines it, and one that nested it
// instead would produce tools with no way to name a window and nothing saying
// so.
func TestSchema_InlinesTheWindowFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema func() (*jsonschema.Schema, error)
	}{
		{"alerts", func() (*jsonschema.Schema, error) { return jsonschema.For[AlertsInput](nil) }},
		{"audit logs", func() (*jsonschema.Schema, error) { return jsonschema.For[AuditLogsInput](nil) }},
		{"device health", func() (*jsonschema.Schema, error) { return jsonschema.For[DeviceHealthInput](nil) }},
		{"estate summary", func() (*jsonschema.Schema, error) { return jsonschema.For[EstateSummaryInput](nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := tc.schema()
			if err != nil {
				t.Fatalf("deriving the schema: %v", err)
			}
			for _, field := range []string{"range_seconds", "from", "to"} {
				prop, ok := s.Properties[field]
				if !ok {
					t.Fatalf("%s is not in the schema; the embedded window was "+
						"nested rather than inlined", field)
				}
				// A field with no description is one a model has to guess at,
				// and the point of these three is that only one way of naming
				// a window may be used at a time.
				if prop.Description == "" {
					t.Errorf("%s has no description", field)
				}
			}
		})
	}
}

// A record is a map on purpose: the fields depend on the view a caller asked
// for, they change with the release, and a struct per collection would be a
// fourth description of the same thing. That only pays off if it derives as a
// generic object rather than as something a model cannot read.
func TestSchema_KeepsRecordCollectionsGeneric(t *testing.T) {
	s, err := jsonschema.For[DevicesOutput](nil)
	if err != nil {
		t.Fatalf("deriving the schema: %v", err)
	}
	devices, ok := s.Properties["devices"]
	if !ok {
		t.Fatal("devices is not in the output schema")
	}
	// A slice is derived as ["null","array"], since a nil one encodes as null.
	if !slices.Contains(devices.Types, "array") && devices.Type != "array" {
		t.Errorf("devices is %v/%q, want an array", devices.Types, devices.Type)
	}
	// Generic, which is the whole point: the fields depend on the view, so a
	// schema enumerating them would be wrong for every view but one.
	if devices.Items == nil || devices.Items.AdditionalProperties == nil {
		t.Error("a device row is not described as an open object, so a model " +
			"would be told the fields it actually gets do not exist")
	}
	// The numbers a model reasons with have to be described, since they are
	// the difference between a truncated answer and a small estate.
	for _, field := range []string{"returned", "total"} {
		if _, ok := s.Properties[field]; !ok {
			t.Errorf("%s is not in the output schema", field)
		}
	}
}
