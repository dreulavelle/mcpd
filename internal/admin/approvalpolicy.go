package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

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
	// Unmatched is the same fact as a value the page can put in a control:
	// the ceiling authorised when nothing else decides, or "none".
	Unmatched string `json:"unmatched"`
	// Warnings names rules that match nothing this host currently serves.
	//
	// Never a refusal. A rule may legitimately name a plugin an operator is
	// about to add, and refusing it would make the order of two configuration
	// steps matter. But a typo in an *exclusion* is worth saying out loud: it
	// fails closed in the sense that the exclusion never authorises anything,
	// and open in the sense that matters, because the exclusion it was
	// supposed to be never fires and a broader grant decides instead.
	Warnings []string `json:"warnings,omitempty"`
}

func (s *Server) approvalPolicyView(rules []operations.AutoApprovalRule, unmatched string) approvalPolicyResponse {
	ceilings := operations.RuleCeilings()
	out := make([]string, len(ceilings))
	for i, c := range ceilings {
		out[i] = c.String()
	}
	if rules == nil {
		rules = []operations.AutoApprovalRule{}
	}
	return approvalPolicyResponse{
		Rules:     rules,
		Wildcard:  operations.RuleAny,
		Ceilings:  out,
		Default:   defaultSentence(unmatched),
		Unmatched: unmatched,
		Warnings:  s.unmatchedRules(rules),
	}
}

// unmatchedRules reports rules naming a plugin or action this host does not
// serve.
//
// The case worth catching is an exclusion with a typo in it. Under deny-wins a
// misspelled exclusion is not dangerous in itself -- it authorises nothing, so
// it can only ever refuse -- but it silently stops protecting the thing it was
// written for, and a plugin-wide grant beside it then decides. The operator
// believes reboots are excluded and they are not.
//
// A wildcard selector matches by definition and is never reported. Nor is
// anything reported when the host has no plugins mounted, which is what a test
// harness and a half-configured host both look like; warning that every rule
// matches nothing would be noise in the one case and alarming in the other.
func (s *Server) unmatchedRules(rules []operations.AutoApprovalRule) []string {
	if s.opts.Manager == nil {
		return nil
	}
	actions := map[string]map[string]bool{}
	for _, m := range s.opts.Manager.All() {
		if m.Registry == nil {
			continue
		}
		known := map[string]bool{}
		for _, action := range m.Registry.MutationActions() {
			known[action] = true
		}
		actions[m.Descriptor.Name] = known
	}
	if len(actions) == 0 {
		return nil
	}

	var out []string
	for _, r := range rules {
		switch {
		case r.Plugin != operations.RuleAny && actions[r.Plugin] == nil:
			out = append(out, fmt.Sprintf(
				"rule %q names plugin %q, which is not mounted here, so it matches nothing",
				r.ID, r.Plugin))
		case r.Action != operations.RuleAny && !anyPluginHas(actions, r.Plugin, r.Action):
			out = append(out, fmt.Sprintf(
				"rule %q names action %q, which no mounted plugin registers, "+
					"so it matches nothing", r.ID, r.Action))
		}
	}
	sort.Strings(out)
	return out
}

// anyPluginHas reports whether the action is registered by the named plugin,
// or by any of them when the rule's plugin selector is the wildcard.
func anyPluginHas(actions map[string]map[string]bool, plugin, action string) bool {
	if plugin != operations.RuleAny {
		return actions[plugin][action]
	}
	for _, known := range actions {
		if known[action] {
			return true
		}
	}
	return false
}

// storedRules reads the rule set as it stands.
//
// A set that cannot be read or does not validate is reported as an error
// rather than silently rendered as empty: an administrator looking at this
// page needs to know the difference between "no rules" and "the rules are
// unreadable", because the host is asking about everything in both cases and
// only one of them is what they configured.
// unmatchedDefault reads the ceiling that applies when no rule matches.
//
// Unreadable settings report the strict answer rather than the permissive one:
// a page that says changes are being held while they are going ahead is the
// one mistake this sentence must not make.
func (s *Server) unmatchedDefault(r *http.Request) string {
	raw, ok, err := s.opts.Settings.Get(r.Context(), settings.KeyApprovalUnmatched)
	if err != nil || !ok {
		if f, found := settings.FieldFor(settings.KeyApprovalUnmatched); found && err == nil {
			if d, isString := f.Default.(string); isString {
				return d
			}
		}
		return settings.RiskNone
	}
	return raw
}

func (s *Server) storedRules(r *http.Request) ([]operations.AutoApprovalRule, error) {
	raw, ok, err := s.opts.Settings.Get(r.Context(), settings.KeyApprovalAutoRules)
	if err != nil || !ok {
		return nil, err
	}
	// The same judgement the write path applies, so what this page shows is
	// what the host will act on rather than a more forgiving reading of it.
	return operations.DecodeRules([]byte(raw))
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
	s.writeJSON(w, r, http.StatusOK, s.approvalPolicyView(rules, s.unmatchedDefault(r)))
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

	// Strict at the wrapper as well as inside each rule. The rules themselves
	// refuse an unknown field in AutoApprovalRule.UnmarshalJSON, which is
	// where it has to live to cover every way a rule arrives; this catches a
	// misspelling of "rules" itself, which would otherwise read as an empty
	// set and quietly delete the policy.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()

	var req putApprovalPolicyRequest
	if err := dec.Decode(&req); err != nil {
		// The decoder's own message names the offending field, which is the
		// whole value of being strict -- swallowing it would leave an operator
		// staring at a rule that looks right.
		s.writeJSON(w, r, http.StatusBadRequest, map[string]string{
			"error":  "invalid_rules",
			"detail": err.Error(),
		})
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
	s.writeJSON(w, r, http.StatusOK, s.approvalPolicyView(rules, s.unmatchedDefault(r)))
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

	// Strict here too. A typo in a what-if question gives a confident answer
	// about a change nobody asked about, which is worse than an error.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()

	var req evaluateRequest
	if err := dec.Decode(&req); err != nil {
		s.writeError(w, r, http.StatusBadRequest, err.Error())
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

// defaultSentence says what a change meets when no rule covers it.
//
// Read from the setting rather than stated as a constant. The page describes
// the policy this host is running; a sentence that describes the policy it
// used to run is worse than no sentence, because an operator has no reason to
// doubt it.
func defaultSentence(unmatched string) string {
	switch unmatched {
	case "", settings.RiskNone:
		return "Every change is put to a person here unless a rule authorises it."
	case "high":
		return "A change no rule covers goes ahead, on the understanding that the " +
			"assistant asked. A change that cannot be undone is still put to a person."
	default:
		return "A change no rule covers goes ahead up to " + unmatched +
			" risk, on the understanding that the assistant asked. Anything higher, " +
			"and anything that cannot be undone, is put to a person."
	}
}
