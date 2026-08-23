package operations

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Auto-approval is the answer to a gate that asks about everything.
//
// Requiring a human decision for a trivial write is the same mistake as
// requiring one to leave the conversation: it is friction, and friction is
// what people route around. Risk was already computed, stored and displayed on
// every operation and consulted by nothing, so every mutation was equally
// consequential as far as the gate was concerned.
//
// What changes is who authorises, never whether an authorisation exists. An
// auto-approved operation is an ordinary operation: the row is written, the
// payload is frozen and hashed, plan/apply/observe runs, drift is checked,
// the outcome is verified where the mutation can prove one, and the audit
// chain carries every transition. The only thing it skips is the interruption.
//
// So the property this project rests on survives with one word changed:
// nothing writes without a *recorded authorisation* it can prove. The
// authorisation is a standing rule an administrator wrote instead of a click
// somebody made, and the record names it -- because "auto-approved" without
// saying by what is exactly the unprovable approval the gate exists to
// prevent.

// RuleAny is the wildcard in a rule's scope. It is spelled out rather than
// represented by an empty string in storage, so a rule written by hand with a
// field left off reads as what it is rather than as an accident.
const RuleAny = "*"

// PolicyActor is the actor recorded on an approval nobody made. It is a
// system identity for the same reason the reaper's is: attributing the
// decision to the principal who proposed the change would say a person
// approved their own write, which is the one thing that did not happen.
const PolicyActor = SystemActor + ":policy"

// maxRules bounds a stored rule set. Resolution is linear over it and it is
// evaluated on every proposal; a set past this size is a configuration
// mistake rather than a deployment.
const maxRules = 200

var (
	// ruleIDPattern is a slug, because the id is what an audit entry names and
	// what an operator reads back. A generated identifier would be stable and
	// unreadable; a slug says which rule authorised the change.
	ruleIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// scopePattern bounds a plugin or action selector. It is deliberately
	// looser than either name's own rule: those are enforced where the names
	// are registered, and a rule naming something that does not exist simply
	// never matches. What is enforced here is that the value cannot carry
	// anything that breaks a log line or renders as something it is not.
	scopePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)
)

// AutoApprovalRule is a standing authorisation: an administrator saying in
// advance that a class of change does not need to be asked about.
//
// Scope is three independent selectors, each of which may be RuleAny. Plugin
// and action say what may be authorised; principal says whose proposals it
// covers, so a service token and a person can be given different latitude for
// the same write.
type AutoApprovalRule struct {
	// ID names the rule in the audit trail and on the operation it
	// authorised.
	ID string `json:"id"`
	// Plugin, Action and Principal scope the rule. RuleAny matches anything.
	Plugin    string `json:"plugin"`
	Action    string `json:"action"`
	Principal string `json:"principal"`
	// MaxRisk is the highest risk this rule authorises without asking.
	//
	// Empty authorises nothing, which is not a disabled rule -- it is a
	// deliberate "always ask", and it is how a specific action is carved out
	// of a broader rule. Because a more specific rule wins outright, a rule
	// naming one action with no ceiling stops that action auto-approving
	// however permissive the plugin-wide rule beside it is.
	MaxRisk RiskLevel `json:"max_risk"`
	// Note is the operator's own reason, carried into the audit detail so the
	// record says why as well as which.
	Note string `json:"note,omitempty"`
}

// Scope renders the rule's selectors for a log line or an audit entry.
func (r AutoApprovalRule) Scope() string {
	return fmt.Sprintf("%s/%s for %s", r.Plugin, r.Action, r.Principal)
}

// specificity ranks a rule against others that also match.
//
// The weights encode which selector wins an argument. Plugin outranks action
// because an action name means nothing without the plugin it belongs to, and
// both outrank principal because a rule that carves an action out is a
// statement about the change itself -- "rebooting a device is never
// automatic" -- and must not be defeated by a broad grant to whoever happens
// to be asking.
func (r AutoApprovalRule) specificity() int {
	score := 0
	if r.Plugin != RuleAny {
		score += 4
	}
	if r.Action != RuleAny {
		score += 2
	}
	if r.Principal != RuleAny {
		score++
	}
	return score
}

// matches reports whether the rule's scope covers a proposal.
func (r AutoApprovalRule) matches(req AutoApprovalRequest) bool {
	return selectorMatches(r.Plugin, req.Plugin) &&
		selectorMatches(r.Action, req.Action) &&
		selectorMatches(r.Principal, req.Principal)
}

func selectorMatches(selector, value string) bool {
	return selector == RuleAny || selector == value
}

// AutoApprovalRequest is what the policy is asked about. It is the proposal as
// it stands after risk has been raised by everything entitled to raise it,
// never the plugin's declared risk on its own.
type AutoApprovalRequest struct {
	Plugin    string
	Action    string
	Principal string
	// Risk is the final classification: the mutation's declaration, raised by
	// the plan and by any operator override. Risk may be raised and never
	// lowered, and the ceiling in a rule is compared against the raised
	// value -- so a plan that reclassifies a change upward puts it back in
	// front of a person even though the proposal qualified without it.
	Risk RiskLevel
	// Reversible is the mutation's declaration that an inverse exists. A
	// mutation that cannot be undone is never auto-approved, whatever a rule
	// says: the argument for a standing authorisation is that a mistake is
	// cheap to correct, and it does not hold where there is no correction.
	Reversible bool
}

// AutoApprovalDecision is the outcome, and it is explainable by construction:
// whichever way it went, Reason says why in words an operator can act on and
// Rule names the rule that decided, if one did.
type AutoApprovalDecision struct {
	// AutoApprove reports whether the change may proceed without being put to
	// a person.
	AutoApprove bool
	// Rule is the rule that decided. It is set whenever one matched, which
	// includes the case where the matching rule was the reason a person is
	// being asked.
	Rule *AutoApprovalRule
	// Reason is the explanation, recorded in the audit trail when the
	// decision was to proceed.
	Reason string
}

// RuleID returns the deciding rule's identifier, or "" when none matched.
func (d AutoApprovalDecision) RuleID() string {
	if d.Rule == nil {
		return ""
	}
	return d.Rule.ID
}

// AutoApprovalPolicy is the configured rule set.
//
// The zero value asks about everything. That is not a placeholder: nothing
// about an upgrade may loosen a running deployment, so a host that has never
// been configured must behave exactly as it did before this existed.
type AutoApprovalPolicy struct {
	Rules []AutoApprovalRule
}

// Evaluate decides whether a proposal is asked about.
//
// Three refusals come before any rule is consulted, because they are
// properties of the change rather than of the configuration and no rule may
// override them.
func (p AutoApprovalPolicy) Evaluate(req AutoApprovalRequest) AutoApprovalDecision {
	if !req.Reversible {
		return AutoApprovalDecision{
			Reason: "the mutation declares no way back, so nothing authorises it in advance",
		}
	}
	if !req.Risk.Valid() {
		// An unrecognised classification is exactly the case to put in front
		// of someone. Treating it as harmless is the wrong way to fail.
		return AutoApprovalDecision{
			Reason: fmt.Sprintf("risk %q is not a classification this host recognises", req.Risk),
		}
	}

	rule := p.resolve(req)
	if rule == nil {
		return AutoApprovalDecision{
			Reason: "no rule covers this change, so it is put to a person",
		}
	}
	if !rule.MaxRisk.Valid() || !rule.MaxRisk.AtLeast(req.Risk) {
		return AutoApprovalDecision{
			Rule: rule,
			Reason: fmt.Sprintf("rule %s authorises up to %s and this change is %s",
				rule.ID, orNone(rule.MaxRisk), req.Risk),
		}
	}
	return AutoApprovalDecision{
		AutoApprove: true,
		Rule:        rule,
		Reason: fmt.Sprintf("rule %s (%s) authorises %s changes up to %s",
			rule.ID, rule.Scope(), req.Risk, rule.MaxRisk),
	}
}

// resolve picks the one rule that decides, and it must be deterministic:
// "which rule applied" is a question an operator has to be able to answer from
// the configuration alone, without knowing what order it happened to be
// stored in.
//
// Most specific wins. A tie can only survive validation as a bug -- duplicate
// scopes are refused when the set is stored -- so it is broken towards the
// stricter rule, and then by id, rather than left to map iteration.
func (p AutoApprovalPolicy) resolve(req AutoApprovalRequest) *AutoApprovalRule {
	var best *AutoApprovalRule
	for i := range p.Rules {
		candidate := &p.Rules[i]
		if !candidate.matches(req) {
			continue
		}
		if best == nil || moreSpecific(*candidate, *best) {
			best = candidate
		}
	}
	return best
}

// moreSpecific reports whether a should beat b.
func moreSpecific(a, b AutoApprovalRule) bool {
	if sa, sb := a.specificity(), b.specificity(); sa != sb {
		return sa > sb
	}
	if a.MaxRisk.rank() != b.MaxRisk.rank() {
		return a.MaxRisk.rank() < b.MaxRisk.rank()
	}
	return a.ID < b.ID
}

func orNone(r RiskLevel) string {
	if r == "" {
		return "nothing"
	}
	return r.String()
}

// NormaliseRules validates a rule set and returns it in canonical form.
//
// It is the one place a rule set is checked, so the same rules apply whether
// the set arrives from the dashboard, the API, or a value already in the
// database. The returned slice is sorted, which makes the stored JSON stable
// and a settings-history diff readable.
func NormaliseRules(rules []AutoApprovalRule) ([]AutoApprovalRule, error) {
	if len(rules) > maxRules {
		return nil, fmt.Errorf("operations: %d rules is past the %d this host will hold",
			len(rules), maxRules)
	}

	out := make([]AutoApprovalRule, 0, len(rules))
	ids := make(map[string]bool, len(rules))
	scopes := make(map[string]string, len(rules))

	for _, r := range rules {
		r.ID = strings.TrimSpace(r.ID)
		r.Plugin = normaliseSelector(r.Plugin)
		r.Action = normaliseSelector(r.Action)
		r.Principal = normaliseSelector(r.Principal)
		r.MaxRisk = RiskLevel(strings.TrimSpace(string(r.MaxRisk)))
		r.Note = strings.TrimSpace(r.Note)

		if err := validateRule(r); err != nil {
			return nil, err
		}
		if ids[r.ID] {
			return nil, fmt.Errorf("operations: two rules are both called %q", r.ID)
		}
		ids[r.ID] = true

		// Two rules with the same scope are the ambiguity resolution exists to
		// avoid. Refusing them is better than picking one, because the one
		// picked would be correct only by accident.
		scope := r.Plugin + "\x00" + r.Action + "\x00" + r.Principal
		if other, ok := scopes[scope]; ok {
			return nil, fmt.Errorf(
				"operations: rules %q and %q both cover %s; one scope, one rule",
				other, r.ID, r.Scope())
		}
		scopes[scope] = r.ID

		out = append(out, r)
	}

	slices.SortFunc(out, func(a, b AutoApprovalRule) int {
		if a.specificity() != b.specificity() {
			// Most specific first, so a rendered list reads in the order
			// resolution considers it.
			return b.specificity() - a.specificity()
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func validateRule(r AutoApprovalRule) error {
	if !ruleIDPattern.MatchString(r.ID) {
		return fmt.Errorf("operations: rule id %q must match %s", r.ID, ruleIDPattern)
	}
	for label, value := range map[string]string{
		"plugin": r.Plugin, "action": r.Action, "principal": r.Principal,
	} {
		if value == RuleAny {
			continue
		}
		if !scopePattern.MatchString(value) {
			return fmt.Errorf("operations: rule %s has a %s of %q, which is not a name "+
				"this host would ever match; use %q for any", r.ID, label, value, RuleAny)
		}
	}
	switch {
	case r.MaxRisk == "":
		// A rule that authorises nothing. Legitimate, and the point of it is
		// to beat a broader rule.
	case r.MaxRisk == RiskCritical:
		// A level named critical that an operator can opt out of is not a
		// level. Nothing stops a deployment classifying its own writes lower;
		// what this refuses is a standing rule that quietly covers the top of
		// the scale.
		return fmt.Errorf("operations: rule %s would authorise critical changes; "+
			"a critical change is one a person has to see", r.ID)
	case !r.MaxRisk.Valid():
		return fmt.Errorf("operations: rule %s has max_risk %q, which is not a risk level",
			r.ID, r.MaxRisk)
	}
	if len(r.Note) > 256 {
		return fmt.Errorf("operations: rule %s has a note longer than 256 characters", r.ID)
	}
	if err := printable(r.Note); err != nil {
		return fmt.Errorf("operations: rule %s: %w", r.ID, err)
	}
	return nil
}

// normaliseSelector treats an omitted selector as the wildcard, so a rule
// written without a principal means what it looks like it means.
func normaliseSelector(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return RuleAny
	}
	return v
}

// printable refuses the characters that make a stored string lie about
// itself: a newline that breaks a log line in two, a bidirectional override
// that renders text as something it is not.
func printable(s string) error {
	for _, r := range s {
		switch {
		case unicode.IsControl(r):
			return fmt.Errorf("a note cannot contain control characters")
		case unicode.Is(unicode.Cf, r):
			return fmt.Errorf("a note cannot contain invisible formatting characters")
		case r == utf8.RuneError:
			return fmt.Errorf("a note must be valid UTF-8")
		}
	}
	return nil
}

// RuleCeilings lists the risk levels a rule may authorise up to, least severe
// first. It is what the dashboard offers, so the choices on the form and the
// values validateRule accepts cannot drift apart.
//
// Critical is absent by design: see validateRule.
func RuleCeilings() []RiskLevel { return []RiskLevel{RiskLow, RiskMedium, RiskHigh} }
