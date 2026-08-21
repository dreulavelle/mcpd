package plugins

import (
	"context"
	"fmt"

	"golang.org/x/time/rate"
)

// toolLimiter bounds how often one tool may be called.
//
// Per tool rather than per plugin: the expensive call is usually one endpoint
// rather than a whole integration, and bounding the plugin to protect it would
// slow every cheap call beside it.
//
// A nil limiter is the unbounded case, so a tool that declares no limit costs
// nothing at call time rather than paying for a limiter that never refuses.
type toolLimiter struct {
	limiter *rate.Limiter
}

func newToolLimiter(perSecond float64) toolLimiter {
	if perSecond <= 0 {
		return toolLimiter{}
	}
	// Burst of one. A burst allowance would let the first few calls ignore the
	// limit entirely, which is the shape a model retrying in a loop produces.
	return toolLimiter{limiter: rate.NewLimiter(rate.Limit(perSecond), 1)}
}

// wait blocks until the tool may run, or the context ends.
func (t toolLimiter) wait(ctx context.Context) error {
	if t.limiter == nil {
		return nil
	}
	if err := t.limiter.Wait(ctx); err != nil {
		// The caller is a model, and "rate limited" is something it can act on
		// by waiting or asking for less. A context error is not.
		return fmt.Errorf("this tool is rate limited and the call did not get a "+
			"turn in time: %w", err)
	}
	return nil
}
