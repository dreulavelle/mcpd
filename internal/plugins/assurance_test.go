package plugins

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spoked/mcpd/internal/operations"
)

// The note is the sentence a model repeats to a person, so it must not claim
// a check that did not happen. Every succeeded operation used to be described
// as "confirmed by re-reading the target", whether or not anything was read.
func TestNoteFor_DoesNotClaimUnperformedVerification(t *testing.T) {
	verified := true

	tests := []struct {
		name       string
		op         *operations.Operation
		wantHas    []string
		wantNotHas []string
	}{
		{
			name: "verified success says so",
			op: &operations.Operation{
				State: operations.StateSucceeded, Verifiable: true,
				Preconditions:   json.RawMessage(`{"a":1}`),
				OutcomeVerified: &verified,
			},
			wantHas: []string{"Applied and confirmed by re-reading"},
		},
		{
			name: "unchecked success says the result is not proved",
			op: &operations.Operation{
				State: operations.StateSucceeded, Verifiable: false,
				Preconditions: json.RawMessage(`{"a":1}`),
			},
			wantHas:    []string{"cannot be confirmed", "nothing here"},
			wantNotHas: []string{"Applied and confirmed"},
		},
		{
			name: "a pending gated call warns before the decision, not after",
			op: &operations.Operation{
				State: operations.StatePendingApproval, Verifiable: false,
			},
			wantHas: []string{
				"NOTHING HAS CHANGED YET",
				"gated call rather than a reviewed change",
				"declares no preconditions",
				"cannot be confirmed",
			},
		},
		{
			name: "a pending reviewed change carries no caveat",
			op: &operations.Operation{
				State: operations.StatePendingApproval, Verifiable: true,
				Preconditions: json.RawMessage(`{"a":1}`),
			},
			wantNotHas: []string{"gated call"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			note := noteFor(tc.op)
			for _, want := range tc.wantHas {
				if !strings.Contains(note, want) {
					t.Errorf("note %q does not mention %q", note, want)
				}
			}
			for _, unwanted := range tc.wantNotHas {
				if strings.Contains(note, unwanted) {
					t.Errorf("note %q must not claim %q", note, unwanted)
				}
			}
		})
	}
}

// The distinction reaches the caller as a field, not only as prose, so a
// dashboard can render it without parsing a sentence.
func TestOperationView_CarriesAssurance(t *testing.T) {
	op := &operations.Operation{
		ID: "op_1", State: operations.StateSucceeded, Plugin: "echo",
		Action: "label.set", Risk: operations.RiskLow,
	}
	if got := viewOf(op).Assurance; got != string(operations.AssuranceGatedCall) {
		t.Fatalf("assurance = %q, want gated_call", got)
	}

	op.Verifiable = true
	op.Preconditions = json.RawMessage(`{"label":"a"}`)
	if got := viewOf(op).Assurance; got != string(operations.AssuranceReviewedChange) {
		t.Fatalf("assurance = %q, want reviewed_change", got)
	}
}
