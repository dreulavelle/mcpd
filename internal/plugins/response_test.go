package plugins

import (
	"math"
	"slices"
	"testing"

	"github.com/spoked/mcpd/internal/observability"
)

// TestResultSizeBucketsCoverTheBudget stops the histogram and the budget
// drifting apart.
//
// A boundary exactly at MaxResultBytes is what makes "past the ceiling" a
// count somebody can read rather than a number to interpolate between two
// buckets. Move the budget without moving the boundary and the series keeps
// reporting, having quietly stopped answering the question it exists for.
//
// The assertion lives here rather than in observability because that package
// cannot import this one without a cycle, which is also why the buckets are
// exported.
func TestResultSizeBucketsCoverTheBudget(t *testing.T) {
	// The console draws its ceiling from this number, so a disagreement is a
	// line in the wrong place on a chart people judge every tool against.
	if observability.ResultBudgetBytes != MaxResultBytes {
		t.Fatalf("observability.ResultBudgetBytes = %d, MaxResultBytes = %d",
			observability.ResultBudgetBytes, MaxResultBytes)
	}
	if !slices.Contains(observability.ResultSizeBuckets, float64(MaxResultBytes)) {
		t.Fatalf("no result-size bucket at MaxResultBytes (%d); buckets are %v",
			MaxResultBytes, observability.ResultSizeBuckets)
	}
	// A ceiling with nothing above it cannot show how far past a result went,
	// which is the difference between "trim a field" and "this tool is wrong".
	over := 0
	for _, b := range observability.ResultSizeBuckets {
		if b > float64(MaxResultBytes) {
			over++
		}
	}
	if over < 2 {
		t.Fatalf("only %d bucket(s) above the budget; overshoot cannot be told apart", over)
	}
}

// TestMarshalledSizeReportsBytes checks the measurement is the encoded length
// rather than anything derived from the Go value.
func TestMarshalledSizeReportsBytes(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want int
	}{
		{"empty object", struct{}{}, 2},
		{"one field", struct {
			A string `json:"a"`
		}{"hi"}, len(`{"a":"hi"}`)},
		{"null", nil, len("null")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := marshalledSize(tc.in); got != tc.want {
				t.Fatalf("marshalledSize = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestMarshalledSizeRefusesToGuess is the case that must not become a zero.
//
// Zero is a real measurement -- a tool that answered with an empty object is
// close to it -- so a value that could not be encoded reports -1 and is left
// out of the histogram rather than being counted as the smallest answer there
// is.
func TestMarshalledSizeRefusesToGuess(t *testing.T) {
	if got := marshalledSize(math.NaN()); got != -1 {
		t.Fatalf("marshalledSize of an unencodable value = %d, want -1", got)
	}
}
