package operations

import (
	"fmt"
	"time"
)

// Bypass is a window in which this host authorises changes it would otherwise
// put to a person, and which closes on its own.
//
// It is a weaker authority than a rule, not a stronger one. A rule is somebody
// deciding, in advance and in the open, that a class of change does not need a
// person; a bypass is somebody saying "not for the next hour". So everything a
// rule cannot do, this cannot do either -- and one thing more, it cannot
// override an exclusion, because a rule that authorises nothing is somebody
// writing "never" about a specific action and an evening's convenience must not
// quietly cancel it.
type Bypass struct {
	ID string
	// Plugin is the instance this covers, or empty for every one.
	Plugin string
	// Ceiling is the highest risk it authorises. Never critical.
	Ceiling   RiskLevel
	ExpiresAt time.Time
	Reason    string
	CreatedBy string
	CreatedAt time.Time
}

// MaxBypassMinutes bounds a window.
//
// Eight hours, and no way to say more. The number matters less than the
// absence of an option beside it: a window that can be extended indefinitely
// is a rule with worse bookkeeping, and the reason to have this rather than
// widening a rule is precisely that it ends.
const MaxBypassMinutes = 480

// MinBypassMinutes keeps a window long enough to be worth opening.
const MinBypassMinutes = 1

// Active reports whether the window is open at the given moment.
func (b *Bypass) Active(now time.Time) bool {
	return b != nil && now.Before(b.ExpiresAt)
}

// Remaining is how much of the window is left, floored at zero.
func (b *Bypass) Remaining(now time.Time) time.Duration {
	if !b.Active(now) {
		return 0
	}
	return b.ExpiresAt.Sub(now)
}

// Covers reports whether this window authorises one change.
//
// Every refusal here mirrors one the rule set already makes, which is the
// property worth keeping: a bypass changes who is asked, never what may be
// authorised.
func (b *Bypass) Covers(req AutoApprovalRequest, now time.Time) (bool, string) {
	switch {
	case !b.Active(now):
		return false, "the bypass has expired"
	case !req.Reversible:
		// The same refusal Evaluate makes first, for the same reason: the
		// argument for authorising anything in advance is that a mistake is
		// cheap to correct.
		return false, "the mutation declares no way back"
	case !req.Risk.Valid():
		return false, fmt.Sprintf("risk %q is not a classification this host recognises", req.Risk)
	case b.Ceiling == RiskCritical || !b.Ceiling.Valid():
		// Defence in depth. Opening one is refused at the API and at the
		// store; this is the last place that would let it through.
		return false, "a bypass cannot authorise critical changes"
	case b.Plugin != "" && b.Plugin != req.Plugin:
		return false, fmt.Sprintf("the bypass covers %s and this change is on %s", b.Plugin, req.Plugin)
	case !b.Ceiling.AtLeast(req.Risk):
		return false, fmt.Sprintf("the bypass authorises up to %s and this change is %s",
			b.Ceiling, req.Risk)
	}
	return true, fmt.Sprintf("a bypass opened by %s authorises %s changes up to %s until %s",
		b.CreatedBy, req.Risk, b.Ceiling, b.ExpiresAt.UTC().Format(time.RFC3339))
}

// BypassAuthority is how a bypass is recorded as the authority for a change.
//
// Prefixed so it can never be mistaken for a rule id in the audit trail. "A
// rule authorised this" and "somebody had switched the asking off" are
// different facts about how a change came to run, and somebody reading back
// through what happened needs to be able to tell them apart.
func BypassAuthority(id string) string { return "bypass:" + id }
