package operations

// InlineApprovalPolicy decides which changes may be approved from a
// conversation rather than the dashboard.
//
// It is a ceiling rather than a switch, because the two places are not
// equivalent. Approving a routine change inline is what stops the gate being
// worked around: an operator who has to open a dashboard for every trivial
// edit eventually stops using the gate at all. But a consequential change
// deserves the dashboard, where the full before-and-after is visible, the
// audit trail is at hand, and -- where identities are real -- a second person
// can be required. A one-line confirmation in a chat window gives none of
// those.
type InlineApprovalPolicy struct {
	// MaxRisk is the highest risk approvable in a conversation. The zero
	// value permits nothing, so a deployment that never configures this keeps
	// every approval at the dashboard.
	MaxRisk RiskLevel
}

// Allows reports whether a risk level may be approved inline.
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
