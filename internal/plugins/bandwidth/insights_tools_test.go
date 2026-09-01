package bandwidth

import "testing"

// A null payload means no records, not "look somewhere else".
//
// The first live call to Insights returned {"links":[{href,rel}],"data":null},
// and a fallback that accepted any top-level array turned the envelope's own
// page link into a call record: one result, two fields, neither of them a call.
// Not empty and not right is worse than either.
func TestJSONRecords_DoesNotMistakeTheEnvelopeForThePayload(t *testing.T) {
	body := map[string]any{
		"links": []any{map[string]any{"href": "/api/v1/voice/calls", "rel": "self"}},
		"data":  nil,
	}
	if got := jsonRecords(body); len(got) != 0 {
		t.Errorf("null data means no calls, got %d record(s): %v", len(got), got)
	}

	// And the payload is still found when it is there.
	body["data"] = []any{map[string]any{"callId": "c-1", "callResult": "completed"}}
	got := jsonRecords(body)
	if len(got) != 1 || got[0]["callId"] != "c-1" {
		t.Errorf("the data array should be returned, got %v", got)
	}

	// An unrecognised payload key is still findable, but links never are.
	other := map[string]any{
		"links":       []any{map[string]any{"href": "x", "rel": "self"}},
		"voiceEvents": []any{map[string]any{"callId": "c-2"}},
	}
	got = jsonRecords(other)
	if len(got) != 1 || got[0]["callId"] != "c-2" {
		t.Errorf("an unrecognised payload key should still be found, got %v", got)
	}
}
