package echoplugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/plugins"
)

// TestBuildPayloadHitsTheRequestedSizeExactly is the guarantee the tool exists
// for.
//
// The point of a chosen size is to land in a chosen bucket. An answer that is
// approximately 40,000 bytes falls either side of the boundary depending on how
// long the word "medium" is, and a histogram exercised by a tool that cannot
// say which bucket it aimed at proves nothing.
func TestBuildPayloadHitsTheRequestedSizeExactly(t *testing.T) {
	for name, target := range payloadSizes {
		t.Run(name, func(t *testing.T) {
			out, err := buildPayload(name, target)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) != target {
				t.Fatalf("encoded to %d bytes, asked for %d", len(encoded), target)
			}
			if out.Bytes != target {
				t.Errorf("reported %d bytes, encoded %d", out.Bytes, len(encoded))
			}
		})
	}
}

// TestOverSaysItIsOver keeps the deliberate failure legible.
//
// A reply the client will cut is the one result a caller must not mistake for
// a complete one, so the tool says so in the payload rather than leaving it to
// be inferred from a size.
func TestOverSaysItIsOver(t *testing.T) {
	over := payloadSizes["over"]
	if over <= plugins.MaxResultBytes {
		t.Fatalf("the over size (%d) does not exceed the budget (%d)",
			over, plugins.MaxResultBytes)
	}
	out, err := buildPayload("over", over)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Note, "ceiling") {
		t.Errorf("an over-budget payload said %q, which does not warn", out.Note)
	}

	// And the one at the ceiling must not warn, or the warning means nothing.
	at, err := buildPayload("budget", payloadSizes["budget"])
	if err != nil {
		t.Fatal(err)
	}
	if at.Note != "" {
		t.Errorf("a payload inside the budget warned: %q", at.Note)
	}
}

// TestBudgetSizeTracksTheHostConstant stops the two drifting.
//
// The "budget" size is only meaningful while it is the actual ceiling. Left
// behind after a change to MaxResultBytes it would still answer, still be
// named budget, and quietly measure the wrong boundary.
func TestBudgetSizeTracksTheHostConstant(t *testing.T) {
	if payloadSizes["budget"] != plugins.MaxResultBytes {
		t.Fatalf("the budget size is %d, MaxResultBytes is %d",
			payloadSizes["budget"], plugins.MaxResultBytes)
	}
}

func TestPayloadSizeRefusesWhatItDoesNotKnow(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"a known size", "small", true},
		{"case and spacing are forgiven", "  Medium ", true},
		{"an invented size", "enormous", false},
		{"empty", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := payloadSize(tc.in)
			if tc.want && err != nil {
				t.Fatalf("payloadSize(%q) = %v, want it accepted", tc.in, err)
			}
			if !tc.want && err == nil {
				t.Fatalf("payloadSize(%q) was accepted", tc.in)
			}
		})
	}
}

// TestBenchmarksAreOffUnlessAskedFor is the gate.
//
// A tool whose purpose is to generate load must not appear to an assistant
// that is merely checking a connection works.
func TestBenchmarksAreOffUnlessAskedFor(t *testing.T) {
	var off Config
	if off.BenchmarksEnabled {
		t.Fatal("the zero Config enables benchmarks")
	}
	var decoded Config
	if err := decode(map[string]any{}, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.BenchmarksEnabled {
		t.Fatal("settings with nothing set enabled benchmarks")
	}
	if err := decode(map[string]any{"benchmarks_enabled": true}, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.BenchmarksEnabled {
		t.Fatal("asking for benchmarks did not enable them")
	}
}
