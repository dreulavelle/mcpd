package plugins

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

// The bug this defends: wait() called limiter.Wait(ctx), which blocked until a
// turn came free. Callers piled up holding goroutines and deadlines, and the
// one that eventually got a turn had spent most of the budget it needed to do
// the work. Refusing is the whole fix.
func TestToolLimiter_RefusesRatherThanQueues(t *testing.T) {
	l := newToolLimiter(1)
	now := time.Now()

	if err := l.allow(now); err != nil {
		t.Fatalf("the first call has a turn: %v", err)
	}

	start := time.Now()
	err := l.allow(now)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("the second call inside the same second must be refused")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("refusal took %s; it must not wait", elapsed)
	}
	// The caller is a model. It can act on "wait this long" and cannot act on
	// "context deadline exceeded".
	for _, want := range []string{"rate limited", "try again in", "per second"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
}

// A refused call must not have spent the turn it was refused: the very next
// tick has to work, or a rejected caller has quietly pushed everybody back.
func TestToolLimiter_ARefusalDoesNotSpendTheTurn(t *testing.T) {
	l := newToolLimiter(1)
	now := time.Now()

	if err := l.allow(now); err != nil {
		t.Fatalf("first: %v", err)
	}
	for range 5 {
		if err := l.allow(now); err == nil {
			t.Fatal("a call with no turn available must be refused")
		}
	}
	// One second later exactly one turn has accrued, which the five refusals
	// must not have eaten.
	if err := l.allow(now.Add(time.Second)); err != nil {
		t.Errorf("the next turn was consumed by refusals: %v", err)
	}
}

func TestToolLimiter_UnboundedAlwaysAllows(t *testing.T) {
	l := newToolLimiter(0)
	if l.limiter != nil {
		t.Error("no limit must mean no limiter")
	}
	for range 100 {
		if err := l.allow(time.Now()); err != nil {
			t.Fatalf("an unbounded limiter must never refuse: %v", err)
		}
	}
}

// Per caller, not global. A runaway agent must not be able to spend the budget
// the operator needs to make the change that stops it.
func TestMutationLimiter_IsPerCaller(t *testing.T) {
	l := newMutationLimiter(1)
	now := time.Now()

	if err := l.allow("svc:agent", now); err != nil {
		t.Fatalf("the agent's first proposal: %v", err)
	}
	if err := l.allow("svc:agent", now); err == nil {
		t.Fatal("the agent's second proposal in the same second must be refused")
	}
	if err := l.allow("user:operator", now); err != nil {
		t.Errorf("one caller's runaway must not refuse another's proposal: %v", err)
	}
}

// The refusal has to say enough for a model to do the right thing, and has to
// be explicit that nothing happened -- because the caller's next question is
// whether to retry, and retrying a change that half-landed is the failure this
// whole subsystem exists to avoid.
func TestMutationLimiter_RefusalIsLegible(t *testing.T) {
	l := newMutationLimiter(1)
	now := time.Now()
	_ = l.allow("svc:agent", now)

	err := l.allow("svc:agent", now)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"rate limited", "try again in", "Nothing was proposed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
}

// A mutation that declares nothing is still bounded. Unbounded is not a
// defensible zero value for a write, and every mutation in the tree today
// declares nothing.
func TestMutationLimiter_DefaultsToABoundRatherThanNone(t *testing.T) {
	l := newMutationLimiter(0)
	now := time.Now()
	if err := l.allow("svc:agent", now); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := l.allow("svc:agent", now); err == nil {
		t.Fatal("a mutation declaring no rate limit must still be bounded")
	}
}

// The per-caller map is bounded, and eviction only ever drops a budget that is
// indistinguishable from a fresh one -- so nothing is forgiven early and
// nobody can buy turns by filling the map.
func TestMutationLimiter_EvictsOnlyWhatItCannotBeWrongAbout(t *testing.T) {
	l := newMutationLimiter(1)
	now := time.Now()

	for i := range maxTrackedProposers {
		if err := l.allow(callerName(i), now); err != nil {
			t.Fatal(err)
		}
	}
	if l.tracked() != maxTrackedProposers {
		t.Fatalf("tracked %d callers, want %d", l.tracked(), maxTrackedProposers)
	}

	// An hour on, every one of those budgets is back at full burst, so
	// dropping it and rebuilding it on the next call are the same thing.
	if err := l.allow("svc:new", now.Add(time.Hour)); err != nil {
		t.Fatalf("a new caller: %v", err)
	}
	if l.tracked() > maxTrackedProposers {
		t.Errorf("the map holds %d budgets, past the cap of %d",
			l.tracked(), maxTrackedProposers)
	}
}

// The bound is allowed to be exceeded rather than reset, because handing every
// tracked caller a fresh budget is exactly what a runaway agent would want.
func TestMutationLimiter_DoesNotResetWhenEverybodyIsMidBudget(t *testing.T) {
	l := newMutationLimiter(1)
	now := time.Now()
	for i := range maxTrackedProposers {
		if err := l.allow(callerName(i), now); err != nil {
			t.Fatal(err)
		}
	}
	// A new caller arrives with nothing evictable.
	if err := l.allow("svc:new", now); err != nil {
		t.Fatalf("a new caller's first proposal must still be allowed: %v", err)
	}
	// And nobody already tracked was forgiven.
	if err := l.allow(callerName(0), now); err == nil {
		t.Error("an existing caller's budget was reset by a new caller arriving")
	}
}

func callerName(i int) string { return "svc:agent-" + strconv.Itoa(i) }

// Concurrent callers of one mutation must not race, and the total admitted in
// a window must not exceed what the limit permits for each of them.
func TestMutationLimiter_ConcurrentCallersAreCountedSeparately(t *testing.T) {
	l := newMutationLimiter(1)
	now := time.Now()

	const callers, each = 8, 10
	var wg sync.WaitGroup
	admitted := make([]int, callers)
	for c := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				if l.allow(callerName(c), now) == nil {
					admitted[c]++
				}
			}
		}()
	}
	wg.Wait()

	for c, n := range admitted {
		if n != 1 {
			t.Errorf("caller %d was admitted %d times in one instant, want 1", c, n)
		}
	}
}

// BenchmarkToolLimiter contrasts refusing with the queueing it replaced. The
// numbers are not comparable as throughput -- the point is that one of them
// returns and the other holds the caller.
func BenchmarkToolLimiter(b *testing.B) {
	b.Run("reject", func(b *testing.B) {
		l := newToolLimiter(1)
		now := time.Now()
		b.ResetTimer()
		for range b.N {
			_ = l.allow(now)
		}
	})

	b.Run("queue", func(b *testing.B) {
		// The shape this replaced. A generous limit keeps the benchmark from
		// running for hours; even so, every call pays a wait.
		l := rate.NewLimiter(rate.Limit(100000), 1)
		ctx := context.Background()
		b.ResetTimer()
		for range b.N {
			_ = l.Wait(ctx)
		}
	})
}
