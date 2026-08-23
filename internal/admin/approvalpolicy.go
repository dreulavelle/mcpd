package admin

import (
	"encoding/json"
	"net/http"

	"github.com/spoked/mcpd/internal/auth"
	"github.com/spoked/mcpd/internal/operations"
	"github.com/spoked/mcpd/internal/settings"
)

// The approval policy is the set of standing rules that decide which changes
// are authorised in advance and which are put to a person.
//
// It gets its own endpoints rather than a field in the generic settings form.
// A rule is three selectors, a ceiling and a note, and the whole set has to be
// judged together -- "no two rules cover the same thing" is not a property any
// one rule has. A text box holding JSON would be validated by nothing, which
// is the wrong shape for the setting that decides when a human is skipped.
//
// The values still live in the settings store, so a change is recorded in
// settings_history against the administrator who made it and is readable
// wherever settings are.

// approvalPolicyResponse is what the dashboard renders from.
type approvalPolicyResponse struct {
	Rules []operations.AutoApprovalRule `json:"rules"`
	// Wildcard is the selector meaning "anything", named here so the UI does
	// not have to hardcode it.
	Wildcard string `json:"wildcard"`
	// Ceilings lists the risk levels a rule may authorise up to, in order.
	// The empty ceiling is not in it: it is a rule that authorises nothing,
	// which the UI should offer as a distinct choice rather than as a level.
	Ceilings []string `json:"ceilings"`
	// Default states what happens where no rule matches, so the page can say
	// it rather than leaving an empty list to imply it.
	Default string `json:"default"`
}

func (s *Server) approvalPolicyView(rules []operations.AutoApprovalRule) approvalPolicyResponse {
	ceilings := operations.RuleCeilings()
	out := make([]string, len(ceilings))
	for i, c := range ceilings {
		out[i] = c.String()
	}
	if rules == nil {
		rules = []operations.AutoApprovalRule{}
	}
	return approvalPolicyResponse{
		Rules:    rules,
		Wildcard: operations.RuleAny,
		Ceilings: out,
		Default:  "Every change is put to a person unless a rule authorises it.",
	}
}

// storedRules reads the rule set as it stands.
//
// A set that cannot be read or does not validate is reported as an error
// rather than silently rendered as empty: an administrator looking at this
// page needs to know the difference between "no rules" and "the rules are
// unreadable", because the host is asking about everything in both cases and
// only one of them is what they configured.
func (s *Server) storedRules(r *http.Request) ([]operations.AutoApprovalRule, error) {
	var rules []operations.AutoApprovalRule
	ok, err := s.opts.Settings.GetJSON(r.Context(), settings.KeyApprovalAutoRules, &rules)
	if err != nil {
		return nil, err
	}
	if !ok || len(rules) == 0 {
		return nil, nil
	}
	return operations.NormaliseRules(rules)
}

func (s *Server) handleGetApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	if s.opts.Settings == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "settings are unavailable")
		return
	}
	rules, err := s.storedRules(r)
	if err != nil {
		s.opts.Log.Error("the stored approval rules could not be read", "error", err)
		s.writeJSON(w, r, http.StatusConflict, map[string]string{
			"error": "unreadable_rules",
			"detail": "the stored rules are not valid, so every change is being put to " +
				"a person: " + err.Error(),
		})
		return
	}
	s.writeJSON(w, r, http.StatusOK, s.approvalPolicyView(rules))
}

type putApprovalPolicyRequest struct {
	// Rules replaces the whole set. Whole-set replacement is the only honest
	// unit: whether a rule is legal depends on the others beside it, and a
	// per-rule endpoint could accept two that together are ambiguous.
	Rules []operations.AutoApprovalRule `json:"rules"`
}

func (s *Server) handlePutApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	if s.opts.Settings == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "settings are unavailable")
		return
	}

	var req putApprovalPolicyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "the request could not be read")
		return
	}

	// Validated and canonicalised before anything is stored, so what is written
	// is what the host will read back and a bad rule changes nothing.
	rules, err := operations.NormaliseRules(req.Rules)
	if err != nil {
		s.writeJSON(w, r, http.StatusBadRequest, map[string]string{
			"error":  "invalid_rules",
			"detail": err.Error(),
		})
		return
	}
	if rules == nil {
		rules = []operations.AutoApprovalRule{}
	}

	actor := auth.FromContext(r.Context()).ID
	if err := s.opts.Settings.SetJSON(r.Context(), actor,
		settings.KeyApprovalAutoRules, rules); err != nil {
		s.opts.Log.Error("could not store the approval rules", "actor", actor, "error", err)
		s.writeJSON(w, r, http.StatusConflict, map[string]string{
			"error":  "rules_not_applied",
			"detail": err.Error(),
		})
		return
	}

	s.opts.Log.Info("approval rules changed", "actor", actor, "rules", len(rules))
	s.writeJSON(w, r, http.StatusOK, s.approvalPolicyView(rules))
}

// evaluateRequest asks what the policy would do with a change.
type evaluateRequest struct {
	Plugin    string `json:"plugin"`
	Action    string `json:"action"`
	Principal string `json:"principal"`
	Risk      string `json:"risk"`
	// Reversible defaults to false, which is the answer for a mutation that
	// says nothing. A caller asking about a real mutation should send what the
	// mutation declares.
	Reversible bool `json:"reversible"`
}

type evaluateResponse struct {
	AutoApprove bool                         `json:"auto_approve"`
	Rule        *operations.AutoApprovalRule `json:"rule,omitempty"`
	Reason      string                       `json:"reason"`
}

// handleEvaluateApprovalPolicy answers "which rule would apply, and why".
//
// Resolution is deterministic and the winning rule is recorded on every
// operation it authorises, but an operator editing rules needs the answer
// before a change is proposed rather than after. Reading requires no more than
// reading the rules does: it computes over configuration and touches nothing.
func (s *Server) handleEvaluateApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	if s.opts.Settings == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "settings are unavailable")
		return
	}

	var req evaluateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "the request could not be read")
		return
	}

	rules, err := s.storedRules(r)
	if err != nil {
		s.writeJSON(w, r, http.StatusConflict, map[string]string{
			"error":  "unreadable_rules",
			"detail": err.Error(),
		})
		return
	}

	decision := operations.AutoApprovalPolicy{Rules: rules}.Evaluate(operations.AutoApprovalRequest{
		Plugin:     req.Plugin,
		Action:     req.Action,
		Principal:  req.Principal,
		Risk:       operations.RiskLevel(req.Risk),
		Reversible: req.Reversible,
	})
	s.writeJSON(w, r, http.StatusOK, evaluateResponse{
		AutoApprove: decision.AutoApprove,
		Rule:        decision.Rule,
		Reason:      decision.Reason,
	})
}
