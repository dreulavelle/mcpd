package operations

import (
	"bytes"
	"encoding/json"
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
	// Empty authorises nothing, which is not a disabled rule. It is an
	// exclusion -- a deliberate "always ask" -- and an exclusion beats every
	// grant that matches beside it, whatever their scopes are. That is what an
	// operator writing "never" means, and it is the direction to be wrong in:
	// an exclusion that wins too often asks a person unnecessarily, while one
	// that loses reboots hardware with nobody looking.
	MaxRisk RiskLevel `json:"max_risk"`
	// Note is the operator's own reason, carried into the audit detail so the
	// record says why as well as which.
	Note string `json:"note,omitempty"`
}

// ruleWire is the decode target. It exists so AutoApprovalRule can carry a
// strict UnmarshalJSON without that method recursing into itself.
type ruleWire struct {
	ID        string    `json:"id"`
	Plugin    string    `json:"plugin"`
	Action    string    `json:"action"`
	Principal string    `json:"principal"`
	MaxRisk   RiskLevel `json:"max_risk"`
	Note      string    `json:"note"`
}

// UnmarshalJSON decodes a rule strictly, and it has to live on the type rather
// than at each call site.
//
// An absent selector means "anything", which is a convenience worth having and
// is also what makes strictness load-bearing. Written the obvious way,
//
//	{"id": "x", "principle": "svc:agent", "max_risk": "low"}
//
// is accepted: "principle" is not a field, encoding/json discards it silently,
// and the real principal defaults to every principal. An operator writing a
// deliberately narrow rule gets a global one, with nothing saying so. For a
// feature whose entire job is bounding who may write without being asked,
// silently widening on a typo is the worst failure available.
//
// A json.Decoder's DisallowUnknownFields does not reach inside a custom
// UnmarshalJSON, so putting the check here rather than in the HTTP handler is
// not belt-and-braces -- it is the only place that covers every way a rule
// arrives: the API, the settings store on startup, a restore, a future
// importer.
func (r *AutoApprovalRule) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		return fmt.Errorf("operations: a rule cannot be null")
	}

	// Decoded twice, and the first pass earns its keep: decoding straight into
	// the struct cannot tell an absent field from one explicitly set to null,
	// and {"plugin": null} widens exactly the way the typo above does. Null is
	// refused for every field rather than only the dangerous ones, because a
	// rule that is uniformly "omit it or give it a value" is one an operator
	// can hold in their head.
	var present map[string]json.RawMessage
	if err := json.Unmarshal(data, &present); err != nil {
		return fmt.Errorf("operations: reading a rule: %w", err)
	}
	for field, value := range present {
		if string(bytes.TrimSpace(value)) == "null" {
			return fmt.Errorf("operations: a rule's %s is null; "+
				"omit the field or give it a value", field)
		}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w ruleWire
	if err := dec.Decode(&w); err != nil {
		return fmt.Errorf("operations: reading a rule: %w", err)
	}

	// A selector written as an empty string is not the wildcard. Refusing it
	// keeps "" from becoming a third spelling of "anything" beside absence and
	// "*", which is one spelling too many for a value that decides who may
	// write unattended.
	for _, sel := range []struct{ name, value string }{
		{"plugin", w.Plugin}, {"action", w.Action}, {"principal", w.Principal},
	} {
		if _, ok := present[sel.name]; ok && strings.TrimSpace(sel.value) == "" {
			return fmt.Errorf("operations: a rule's %s cannot be empty; "+
				"omit it or use %q for any", sel.name, RuleAny)
		}
	}

	*r = AutoApprovalRule(w)
	return nil
}

// DecodeRules reads a stored rule set, strictly, and returns it canonicalised.
//
// One function so the settings store and anything else reading the stored
// value get the same judgement the API applies on the way in. A set that does
// not survive this is not a set this host will act on.
func DecodeRules(raw []byte) ([]AutoApprovalRule, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var rules []AutoApprovalRule
	if err := dec.Decode(&rules); err != nil {
		return nil, fmt.Errorf("operations: reading the rule set: %w", err)
	}
	return NormaliseRules(rules)
}

// Scope renders the rule's selectors for a log line or an audit entry.
func (r AutoApprovalRule) Scope() string {
	return fmt.Sprintf("%s/%s for %s", r.Plugin, r.Action, r.Principal)
}

// specificity orders one grant against another. It is never consulted
// between a grant and an exclusion, because an exclusion always wins.
//
// The weights encode which selector wins an argument between two rules that
// both authorise something. Plugin outranks action because an action name
// means nothing without the plugin it belongs to, and both outrank principal
// because what is being changed bounds the answer more tightly than who is
// asking.
//
// This used to decide exclusions too, and it got the answer wrong in exactly
// the case the feature exists to serve. An exclusion is naturally written
// narrowly -- one action, or one service token -- and a grant naturally
// broadly, so scoring handed almost every argument to the grant:
// {plugin:*, action:device.reboot} scores 2 and lost to
// {plugin:cnmaestro, action:*} at 4, which auto-approved a device reboot.
// No weighting fixes that, because the exclusion's narrowness is the point of
// it. Exclusions are not a more specific kind of grant, and ordering them
// against each other was the mistake.
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
	// Bypass is the window that authorised this, when one did. It is set
	// instead of Rule, never beside it: a change authorised because somebody
	// had switched the asking off did not match a rule, and recording it as
	// though it had would put a standing authorisation in the trail that
	// nobody wrote.
	Bypass *Bypass
}

// Excluded reports that a rule refused this outright.
//
// An exclusion is somebody writing "never" about a specific action, and it is
// the one decline a bypass must not override. Distinguished from a grant that
// merely did not reach far enough, which a bypass may.
func (d AutoApprovalDecision) Excluded() bool {
	return d.Rule != nil && d.Rule.authorisesNothing()
}

// RuleID returns the deciding rule's identifier, or "" when none matched.
func (d AutoApprovalDecision) RuleID() string {
	if d.Rule == nil {
		return ""
	}
	return d.Rule.ID
}

// Authority names what authorised a change that is going ahead unasked.
//
// A rule where one decided, and the host's default where none did. Never
// empty for an approval: a change recorded as authorised by nothing is a
// change the trail cannot explain.
func (d AutoApprovalDecision) Authority() string {
	if d.Bypass != nil {
		return BypassAuthority(d.Bypass.ID)
	}
	if d.Rule != nil {
		return d.Rule.ID
	}
	return DefaultAuthority
}

// DefaultAuthority is recorded as the authority when a change was authorised
// by the host's own default rather than by a rule somebody wrote.
//
// A separate value rather than an empty one, because empty already means
// something here: nobody authorised this in advance. The audit trail has to be
// able to say which of the two happened, and "no rule" and "the default" are
// different answers to "why did this run without being asked about".
const DefaultAuthority = "policy:default"

// AutoApprovalPolicy is the configured rule set, and what happens to a change
// no rule covers.
type AutoApprovalPolicy struct {
	Rules []AutoApprovalRule

	// Unmatched is the highest risk authorised when no rule matches.
	//
	// Empty asks about everything, which is what this host did before the
	// field existed and what the zero value must keep meaning: nothing about
	// an upgrade may loosen a running deployment.
	//
	// It is the lowest-precedence thing in the model. An exclusion still wins
	// outright, and a matching grant still decides, so a carve-out written for
	// one dangerous action keeps working whatever this is set to.
	Unmatched RiskLevel
}

// authorisesNothing reports a rule that cannot grant anything, and is
// therefore an exclusion.
//
// Three spellings reach the same place. An empty ceiling is the deliberate
// one, written to carve something out. A critical ceiling is refused when a
// rule set is stored -- a level an operator can opt out of is not a level --
// and a ceiling this build does not recognise is refused there too. Neither of
// those should exist, but Evaluate must not depend on having been handed a
// validated set: AutoApprovalPolicy and its Rules are exported, and the
// function that decides whether a human is skipped is the wrong one to make
// conditional on a caller remembering to call NormaliseRules first.
//
// Reaching Evaluate they are all the same fact -- this rule authorises
// nothing -- and the safe reading of a malformed rule is the same as the safe
// reading of a deliberate exclusion.
func (r AutoApprovalRule) authorisesNothing() bool {
	return !r.MaxRisk.Valid() || r.MaxRisk == RiskCritical
}

// Evaluate decides whether a proposal is asked about.
//
// Two refusals come before any rule is consulted, because they are properties
// of the change rather than of the configuration and no rule may override
// them. Then exclusions, then grants -- in that order, and the order is the
// whole of the model.
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

	excluded, grant := p.match(req)

	// An exclusion wins outright, before specificity is consulted at all.
	//
	// This is what every access-control system does and what an operator
	// writing "never" means, and it is the only ordering that fails in the
	// right direction: an exclusion that wins too often costs somebody a
	// question they did not need to answer, while one that loses costs a
	// change nobody reviewed.
	if excluded != nil {
		return AutoApprovalDecision{
			Rule:   excluded,
			Reason: exclusionReason(*excluded),
		}
	}
	if grant == nil {
		switch {
		case !p.Unmatched.Valid() || p.Unmatched == RiskCritical:
			return AutoApprovalDecision{
				Reason: "no rule covers this change, so it is put to a person",
			}
		case !p.Unmatched.AtLeast(req.Risk):
			return AutoApprovalDecision{
				Reason: fmt.Sprintf(
					"no rule covers this change, and this host authorises up to %s "+
						"by default while this change is %s", p.Unmatched, req.Risk),
			}
		default:
			return AutoApprovalDecision{
				AutoApprove: true,
				Reason: fmt.Sprintf(
					"no rule covers this change, and this host authorises %s changes "+
						"up to %s by default", req.Risk, p.Unmatched),
			}
		}
	}
	if !grant.MaxRisk.AtLeast(req.Risk) {
		return AutoApprovalDecision{
			Rule: grant,
			Reason: fmt.Sprintf("rule %s authorises up to %s and this change is %s",
				grant.ID, grant.MaxRisk, req.Risk),
		}
	}
	return AutoApprovalDecision{
		AutoApprove: true,
		Rule:        grant,
		Reason: fmt.Sprintf("rule %s (%s) authorises %s changes up to %s",
			grant.ID, grant.Scope(), req.Risk, grant.MaxRisk),
	}
}

// match returns the exclusion and the grant that decide, either of which may
// be nil.
//
// Both are resolved rather than short-circuiting on the first exclusion found,
// because the decision has to be deterministic: "which rule applied" is a
// question an operator answers from the configuration alone, without knowing
// what order the set happened to be stored in.
//
// Within each kind, most specific wins. A tie can only survive validation as a
// bug -- duplicate scopes are refused when the set is stored -- so it is
// broken towards the stricter rule and then by id rather than left to
// iteration order.
func (p AutoApprovalPolicy) match(req AutoApprovalRequest) (excluded, grant *AutoApprovalRule) {
	for i := range p.Rules {
		candidate := &p.Rules[i]
		if !candidate.matches(req) {
			continue
		}
		if candidate.authorisesNothing() {
			if excluded == nil || moreSpecific(*candidate, *excluded) {
				excluded = candidate
			}
			continue
		}
		if grant == nil || moreSpecific(*candidate, *grant) {
			grant = candidate
		}
	}
	return excluded, grant
}

// exclusionReason says which of the three spellings this was, because the
// deliberate one and the malformed ones need different things done about them.
func exclusionReason(r AutoApprovalRule) string {
	switch {
	case r.MaxRisk == "":
		return fmt.Sprintf("rule %s (%s) excludes this from automatic authorisation",
			r.ID, r.Scope())
	case r.MaxRisk == RiskCritical:
		return fmt.Sprintf("rule %s (%s) claims to authorise critical changes, "+
			"which no rule may; it authorises nothing", r.ID, r.Scope())
	default:
		return fmt.Sprintf("rule %s (%s) has a ceiling of %q, which is not a risk "+
			"level; it authorises nothing", r.ID, r.Scope(), r.MaxRisk)
	}
}

// moreSpecific reports whether a should beat b. It is only ever asked about
// two rules of the same kind.
func moreSpecific(a, b AutoApprovalRule) bool {
	if sa, sb := a.specificity(), b.specificity(); sa != sb {
		return sa > sb
	}
	if a.MaxRisk.rank() != b.MaxRisk.rank() {
		return a.MaxRisk.rank() < b.MaxRisk.rank()
	}
	return a.ID < b.ID
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
