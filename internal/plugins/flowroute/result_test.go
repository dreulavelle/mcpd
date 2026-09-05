package flowroute

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestE164(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr string
	}{
		{in: "12065550100", want: "12065550100"},
		{in: "2065550100", want: "12065550100"},
		{in: "+1 (206) 555-0100", want: "12065550100"},
		{in: "+1-206-555-0100", want: "12065550100"},
		// Longer than the North American plan: passed through as its digits
		// rather than guessed at, so a mistake reaches the API as a 404 naming
		// the number instead of a lookup of somebody else's subscriber.
		{in: "+44 20 7946 0958", want: "442079460958"},
		{in: "", wantErr: "no digits"},
		{in: "acme", wantErr: "no digits"},
		{in: "555-0100", wantErr: "only 7 digits"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := e164(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want an error saying %q, got %v (%q)", tc.wantErr, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("e164(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("e164(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDisplay(t *testing.T) {
	t.Parallel()
	if got := display("12065550100"); got != "+1 206 555 0100" {
		t.Fatalf("display = %q", got)
	}
	if got := display("442079460958"); got != "+442079460958" {
		t.Fatalf("display = %q", got)
	}
	if got := display(""); got != "" {
		t.Fatalf("display = %q", got)
	}
}

// Flowroute sends null for an alias, a note or an edge strategy that was never
// set. A row full of *string would make every one of them a dereference.
func TestBlankReadsNullAsEmpty(t *testing.T) {
	t.Parallel()
	var row struct {
		Alias blank `json:"alias"`
		Note  blank `json:"note"`
	}
	if err := json.Unmarshal([]byte(`{"alias":null,"note":"  hello  "}`), &row); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if row.Alias.String() != "" {
		t.Errorf("null should read as empty, got %q", row.Alias)
	}
	if row.Note.String() != "hello" {
		t.Errorf("note is %q", row.Note)
	}
}

// JSON:API says an id is a string, and Flowroute sends one everywhere except
// an edge strategy, whose id arrives as a bare number. A string-only field
// failed that one response outright, which is what the live account found.
func TestEntityIDAcceptsAStringOrANumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		body string
		want string
	}{
		{`{"id":"12065550100"}`, "12065550100"},
		{`{"id":1}`, "1"},
		{`{"id":null}`, ""},
	}
	for _, tc := range cases {
		var row struct {
			ID entityID `json:"id"`
		}
		if err := json.Unmarshal([]byte(tc.body), &row); err != nil {
			t.Fatalf("unmarshal %s: %v", tc.body, err)
		}
		if row.ID.String() != tc.want {
			t.Fatalf("%s gave id %q, want %q", tc.body, row.ID, tc.want)
		}
	}
	var row struct {
		ID entityID `json:"id"`
	}
	if err := json.Unmarshal([]byte(`{"id":{"nope":1}}`), &row); err == nil {
		t.Fatal("an id that is neither a string nor a number should fail")
	}
}

// The same lesson entityID learned, applied to every other nullable field: a
// field that is a string in every response anybody has seen is not a promise
// about the response nobody has.
func TestBlankAcceptsANumber(t *testing.T) {
	t.Parallel()
	var row struct {
		EdgeStrategyID blank `json:"edge_strategy_id"`
		AddressTypeNum blank `json:"address_type_number"`
	}
	if err := json.Unmarshal([]byte(`{"edge_strategy_id":2,"address_type_number":"4"}`), &row); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if row.EdgeStrategyID.String() != "2" {
		t.Errorf("a numeric edge strategy id read as %q", row.EdgeStrategyID)
	}
	if row.AddressTypeNum.String() != "4" {
		t.Errorf("address type number read as %q", row.AddressTypeNum)
	}
	var bad struct {
		Alias blank `json:"alias"`
	}
	if err := json.Unmarshal([]byte(`{"alias":{"nope":1}}`), &bad); err == nil {
		t.Fatal("a field that is neither a string nor a number should fail")
	}
}
