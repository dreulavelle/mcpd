package plugins

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spoked/mcpd/internal/operations"
)

// Approval can happen in two places, and which one is right depends on the
// change.
//
// Leaving the conversation to approve every routine change is friction that gets
// worked around, so mcpd asks in the conversation when it can: the MCP spec's
// elicitation lets a server put a question to the user through the client,
// and the answer comes back as a real user action rather than a model
// decision.
//
// Enforcement stays here regardless. mcpd will not execute a mutation without
// an approval recorded, so a client that cannot elicit gets the two-step flow
// instead of an unguarded write. The agent has no path that skips this.

// elicitationSupported reports whether the calling client can put a question
// to its user.
//
// Capabilities are read per request, which is what makes this work without a
// session: under the 2026-07-28 protocol they travel in the request's _meta.
func elicitationSupported(req *mcp.CallToolRequest) bool {
	if req == nil {
		return false
	}
	caps := req.ClientCapabilities()
	if caps == nil || caps.Elicitation == nil {
		return false
	}
	// Form elicitation is assumed when neither sub-capability is named, per
	// the spec. A URL-only client cannot render the confirm/decline this
	// needs, so it falls back to the two-step flow.
	return caps.Elicitation.Form != nil ||
		(caps.Elicitation.Form == nil && caps.Elicitation.URL == nil)
}

// elicitDecision is what the user said.
type elicitDecision int

const (
	// decisionUnavailable means the client could not ask.
	decisionUnavailable elicitDecision = iota
	// decisionApproved means the user confirmed.
	decisionApproved
	// decisionDeclined means the user refused, or dismissed the question.
	decisionDeclined
)

// askUser puts the change to the user through the client.
//
// The message is the whole of what a person has to decide on, so it carries
// the impact and the field-level diff rather than a tool name and a payload.
// Someone shown "call device.reboot" has not been given enough to judge;
// someone shown "Lobby-East will go offline for a few minutes and 12 clients
// will disconnect" has.
func askUser(ctx context.Context, req *mcp.CallToolRequest, op *operations.Operation) (elicitDecision, error) {
	if !elicitationSupported(req) {
		return decisionUnavailable, nil
	}
	session := req.Session
	if session == nil {
		return decisionUnavailable, nil
	}

	result, err := session.Elicit(ctx, &mcp.ElicitParams{
		Mode:    "form",
		Message: approvalMessage(op),
		// No fields are requested. The decision is the whole answer, and the
		// client's accept/decline is how it is given -- asking the user to
		// retype anything would invite them to change what they are approving.
		RequestedSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	})
	if err != nil {
		// A client that advertised elicitation and then failed is not one to
		// guess about. Falling back to the two-step flow is the safe reading.
		return decisionUnavailable, err
	}

	if result.Action == "accept" {
		return decisionApproved, nil
	}
	return decisionDeclined, nil
}

// approvalMessage renders the change for a human.
func approvalMessage(op *operations.Operation) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Approve this change to %s?\n\n", op.Plugin)

	if op.Impact != "" {
		fmt.Fprintf(&b, "%s\n\n", op.Impact)
	}

	if len(op.Changes) > 0 {
		b.WriteString("What changes:\n")
		for _, c := range op.Changes {
			fmt.Fprintf(&b, "  %s: %v → %v\n", c.Field, display(c.From), display(c.To))
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "Risk: %s\n", op.Risk)
	b.WriteString(assuranceWarning(op))
	b.WriteString("\nNothing has happened yet. Approving applies it now; " +
		"declining leaves everything as it is.")
	return b.String()
}

// assuranceWarning tells the person in the dialog what approving does not buy
// them.
//
// This is the low-friction path and the only one where somebody decides
// without seeing the note the model reads, so it is the path where a gated
// call would otherwise be indistinguishable from a fully proved change --
// which is the whole reason the two are named separately. Phrased for a
// person rather than a model: no vocabulary, just the two things mcpd cannot
// promise.
func assuranceWarning(op *operations.Operation) string {
	if op.Assurance() == operations.AssuranceReviewedChange {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nBefore you decide:\n")
	if !op.DriftChecked() {
		b.WriteString("  - If something else changes this target between now and " +
			"when it is applied, mcpd will not notice and will apply it anyway.\n")
	}
	if !op.Verifiable {
		b.WriteString("  - mcpd cannot read the target back afterwards to confirm " +
			"the change took effect. Success will mean the request was accepted, " +
			"nothing more.\n")
	}
	return b.String()
}

func display(v any) string {
	if v == nil {
		return "unset"
	}
	if s, ok := v.(string); ok {
		if s == "" {
			return "empty"
		}
		return s
	}
	return fmt.Sprint(v)
}
