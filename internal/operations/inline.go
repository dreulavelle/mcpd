package operations

// InlineApprovalPolicy decides which changes a client's own yes/no prompt may
// settle on its own.
//
// It is a ceiling on the *shortcut*, not on where the decision happens. Every
// approval happens in the conversation: sending someone to a dashboard to
// approve a tool call breaks the thing they were doing, and a gate that costs
// a context switch is one people arrange not to need. What the ceiling changes
// is how much the assistant has to do first. Below it, a single confirmation
// is enough. Above it the prompt is withheld and the assistant must show the
// change in full and be told explicitly before calling the approve tool --
// because a one-line confirmation is too thin a thing to hang a consequential
// change on, not because somewhere else would be a better place to answer it.
//
// The dashboard remains where an operator reviews history, writes standing
// rules and reads the audit trail. It is not a step in this flow.
type InlineApprovalPolicy struct {
	// MaxRisk is the highest risk a client's confirmation may settle by
	// itself. The zero value permits nothing, so a deployment that never
	// configures this makes the assistant show every change in full and be
	// told explicitly -- still in the conversation, and still without anyone
	// leaving it.
	MaxRisk RiskLevel
}

// Allows reports whether a risk level may be settled by a client's own
// confirmation prompt.
func (p InlineApprovalPolicy) Allows(risk RiskLevel) bool {
	if !p.MaxRisk.Valid() {
		return false
	}
	// An unrecognised risk is never inline-approvable: an unknown
	// classification is exactly the case to put in front of someone.
	if !risk.Valid() {
		return false
	}
	return p.MaxRisk.AtLeast(risk)
}

// AllowsInline implements the plugins package's InlinePolicy.
func (p InlineApprovalPolicy) AllowsInline(risk RiskLevel) bool { return p.Allows(risk) }
