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
	disagreed := false

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
			name: "a re-read that disagreed is not a confirmation",
			op: &operations.Operation{
				State: operations.StateSucceeded, Verifiable: true,
				Preconditions:   json.RawMessage(`{"a":1}`),
				OutcomeVerified: &disagreed,
			},
			wantHas:    []string{"did not match", "a human has to look"},
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

// The elicitation dialog is the low-friction path and the only one where a
// person decides without seeing the note the model reads. A gated call
// presented there as an ordinary yes/no is indistinguishable from a fully
// proved change, which is the whole thing the vocabulary split exists to
// prevent.
func TestApprovalMessage_WarnsAboutAGatedCall(t *testing.T) {
	base := func() *operations.Operation {
		return &operations.Operation{
			ID: "op_1", Plugin: "echo", Action: "label.set",
			State: operations.StatePendingApproval, Risk: operations.RiskLow,
			Impact:  "Changes the label.",
			Changes: []operations.Change{{Field: "label", From: "a", To: "b"}},
		}
	}

	reviewed := base()
	reviewed.Verifiable = true
	reviewed.Preconditions = json.RawMessage(`{"label":"a"}`)
	if msg := approvalMessage(reviewed); strings.Contains(msg, "Before you decide") {
		t.Errorf("a fully proved change needs no caveat, got:\n%s", msg)
	}

	unverifiable := base()
	unverifiable.Preconditions = json.RawMessage(`{"label":"a"}`)
	msg := approvalMessage(unverifiable)
	if !strings.Contains(msg, "Before you decide") ||
		!strings.Contains(msg, "cannot read the target back") {
		t.Errorf("the person approving must be told the outcome cannot be confirmed, got:\n%s", msg)
	}
	if strings.Contains(msg, "will not notice") {
		t.Errorf("this one does declare preconditions; it must not claim otherwise:\n%s", msg)
	}

	undrifted := base()
	undrifted.Verifiable = true
	msg = approvalMessage(undrifted)
	if !strings.Contains(msg, "will not notice") {
		t.Errorf("the person approving must be told drift will not be caught, got:\n%s", msg)
	}
	if strings.Contains(msg, "cannot read the target back") {
		t.Errorf("this one can be verified; it must not claim otherwise:\n%s", msg)
	}

	// The caveat goes before the closing sentence, which is the last thing
	// read and has to stay the last thing read.
	if !strings.HasSuffix(strings.TrimSpace(msg),
		"declining leaves everything as it is.") {
		t.Errorf("the closing sentence must remain last:\n%s", msg)
	}
}
