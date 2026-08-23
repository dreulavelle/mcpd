package plugins

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// defaultMutationRateLimit bounds proposals of one mutation by one caller, in
// requests per second, when the mutation declares nothing.
//
// Unbounded is not a defensible zero value for a write. A read tool that
// declares no limit costs an upstream a request; a mutation that declares none
// costs an upstream a *change*, and under a standing rule nobody is asked
// first. One a second is far above any workflow a person drives and far below
// what a model in a retry loop produces, which is the gap this has to sit in.
const defaultMutationRateLimit = 1.0

// maxTrackedProposers bounds how many per-caller budgets are held.
//
// The key includes a principal, and although principals are authenticated and
// therefore few, "few" is a property of the deployment rather than of this
// code. Eviction is exact rather than approximate -- see evictIdleLocked.
const maxTrackedProposers = 1024

// toolLimiter bounds how often one tool may be called.
//
// Per tool rather than per plugin: the expensive call is usually one endpoint
// rather than a whole integration, and bounding the plugin to protect it would
// slow every cheap call beside it. Global rather than per caller, because what
// it protects is an upstream's quota, and the upstream does not care which
// agent spent it.
//
// A nil limiter is the unbounded case, so a tool that declares no limit costs
// nothing at call time rather than paying for a limiter that never refuses.
type toolLimiter struct {
	limiter   *rate.Limiter
	perSecond float64
}

func newToolLimiter(perSecond float64) toolLimiter {
	if perSecond <= 0 {
		return toolLimiter{}
	}
	// Burst of one. A burst allowance would let the first few calls ignore the
	// limit entirely, which is the shape a model retrying in a loop produces.
	return toolLimiter{limiter: rate.NewLimiter(rate.Limit(perSecond), 1), perSecond: perSecond}
}

// allow reports whether the call may run now, and refuses promptly when it may
// not.
//
// It used to wait. Waiting looks like the polite thing to do and is the wrong
// thing here: the caller is a model with a deadline, so a queued call arrives
// at the front having spent most of the budget it needed to do the work, and
// every caller behind it holds a goroutine and a context for as long as the
// queue is. Refusing immediately turns a hidden stall into a fact the model
// can act on -- which is what the error says, in words and with a number.
func (t toolLimiter) allow(now time.Time) error {
	if t.limiter == nil {
		return nil
	}
	r := t.limiter.ReserveN(now, 1)
	if !r.OK() {
		// Burst is one and the reservation is one, so this cannot happen. If
		// it ever does, refusing is the safe reading.
		return fmt.Errorf("this tool is rate limited and cannot be called now")
	}
	if delay := r.DelayFrom(now); delay > 0 {
		// Hand the turn back: this call is not taking it.
		r.CancelAt(now)
		return fmt.Errorf("this tool is rate limited to %s and has no turn "+
			"available; try again in %s. Nothing was called upstream",
			describeRate(t.perSecond), describeDelay(delay))
	}
	return nil
}

// mutationLimiter bounds how often one caller may propose one mutation.
//
// Per caller, unlike the tool limiter above, and the difference is deliberate.
// A read tool's limit protects an upstream's quota, which is a shared resource
// nobody has a claim on. A mutation's limit exists because a standing rule can
// authorise a class of change with nobody being asked, so the thing that has
// to be bounded is *one agent's* ability to land writes in a loop. A single
// global budget would let that agent spend it and leave the operator's own
// corrective change refused -- and the corrective change is the one that stops
// the runaway.
//
// What protects the upstream itself is where it has always been: the plugin's
// own client, which knows what its API can take. This is backpressure on the
// gate, not on the wire.
type mutationLimiter struct {
	perSecond float64

	mu       sync.Mutex
	byCaller map[string]*rate.Limiter
}

func newMutationLimiter(perSecond float64) *mutationLimiter {
	if perSecond <= 0 {
		perSecond = defaultMutationRateLimit
	}
	return &mutationLimiter{perSecond: perSecond, byCaller: map[string]*rate.Limiter{}}
}

// allow reports whether this caller may propose now.
//
// Refusal happens before anything else the propose tool does: before the plan,
// which reads upstream, and before the operation is recorded, so a refused
// call leaves no row, spends no idempotency key, and reaches nothing outside
// this process. That matters more here than for a read: a refusal that
// consumed the idempotency of the operation it refused would make the retry
// the caller is being told to make return the wrong answer.
func (m *mutationLimiter) allow(caller string, now time.Time) error {
	m.mu.Lock()
	limiter, known := m.byCaller[caller]
	if !known {
		if len(m.byCaller) >= maxTrackedProposers {
			m.evictIdleLocked(now)
		}
		limiter = rate.NewLimiter(rate.Limit(m.perSecond), 1)
		m.byCaller[caller] = limiter
	}
	m.mu.Unlock()

	r := limiter.ReserveN(now, 1)
	if !r.OK() {
		return fmt.Errorf("this change is rate limited and cannot be proposed now")
	}
	if delay := r.DelayFrom(now); delay > 0 {
		r.CancelAt(now)
		return fmt.Errorf("proposals of this change are rate limited to %s per "+
			"caller; try again in %s. Nothing was proposed, nothing was read "+
			"upstream, and nothing changed",
			describeRate(m.perSecond), describeDelay(delay))
	}
	return nil
}

// evictIdleLocked drops budgets that are indistinguishable from a fresh one.
//
// A limiter back at full burst has forgotten everything it ever refused, so
// dropping it and building a new one on the next call are the same thing. That
// makes eviction exact: nothing is forgiven early, and there is no idle
// threshold to pick wrongly. If every tracked caller is genuinely mid-budget
// the map is allowed to grow past the cap rather than hand somebody a reset,
// which would be a way to buy turns by making noise.
func (m *mutationLimiter) evictIdleLocked(now time.Time) {
	for caller, limiter := range m.byCaller {
		if limiter.TokensAt(now) >= float64(limiter.Burst()) {
			delete(m.byCaller, caller)
		}
	}
}

// tracked reports how many per-caller budgets are held, for a test that has
// something to say about the bound.
func (m *mutationLimiter) tracked() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byCaller)
}

// describeRate renders a limit the way a sentence needs it.
func describeRate(perSecond float64) string {
	switch {
	case perSecond >= 1:
		return fmt.Sprintf("%g per second", perSecond)
	default:
		// Below one a second, "0.2 per second" is arithmetic the reader
		// should not have to do.
		return fmt.Sprintf("one every %s", describeDelay(
			time.Duration(float64(time.Second)/perSecond)))
	}
}

// describeDelay rounds a wait to something worth reading. Sub-millisecond
// precision in a message telling somebody to wait is noise.
func describeDelay(d time.Duration) string {
	if d < time.Millisecond {
		return "1ms"
	}
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return d.Round(100 * time.Millisecond).String()
}
