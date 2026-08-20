package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// InProcessBus delivers events to handlers in the same process.
//
// On a single node this is the whole messaging layer. Durability is already
// provided by the outbox table, which is written in the same transaction as
// the state change it describes; the bus only has to wake a consumer. Running
// a broker to carry that signal from a process to itself would add an on-disk
// store, a second corruption story, and a network hop to every notification.
type InProcessBus struct {
	log *slog.Logger

	mu     sync.RWMutex
	subs   []subscription
	closed bool

	// wg tracks in-flight deliveries so Close can wait for them rather than
	// abandoning handlers mid-transaction.
	wg sync.WaitGroup
}

type subscription struct {
	durable string
	pattern string
	handler Handler
}

// NewInProcessBus returns a bus with no subscribers.
func NewInProcessBus(log *slog.Logger) *InProcessBus {
	return &InProcessBus{log: log}
}

// Subscribe registers a handler.
func (b *InProcessBus) Subscribe(durable, pattern string, h Handler) error {
	if h == nil {
		return fmt.Errorf("messaging: subscription %q has a nil handler", durable)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return fmt.Errorf("messaging: bus is closed")
	}
	for _, s := range b.subs {
		if s.durable == durable {
			return fmt.Errorf("messaging: duplicate durable subscription %q", durable)
		}
	}
	b.subs = append(b.subs, subscription{durable: durable, pattern: pattern, handler: h})
	return nil
}

// Publish delivers an event to every matching handler, synchronously.
//
// Synchronous delivery is deliberate. The publisher is the outbox drain, which
// only marks an event published after Publish returns, so a handler that fails
// leaves the event pending and it is retried. Delivering asynchronously would
// mean acknowledging events that were never processed.
//
// A handler error is logged and does not stop the others: one failing consumer
// must not deny an event to the rest.
func (b *InProcessBus) Publish(ctx context.Context, e Event) error {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return fmt.Errorf("messaging: bus is closed")
	}
	matched := make([]subscription, 0, len(b.subs))
	for _, s := range b.subs {
		if MatchSubject(s.pattern, e.Subject) {
			matched = append(matched, s)
		}
	}
	b.wg.Add(1)
	b.mu.RUnlock()
	defer b.wg.Done()

	var firstErr error
	for _, s := range matched {
		if err := b.deliver(ctx, s, e); err != nil {
			b.log.Error("event handler failed",
				"durable", s.durable, "subject", e.Subject,
				"event_id", e.ID, "operation_id", e.OperationID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// deliver invokes one handler, converting a panic into an error so that a bug
// in one consumer cannot take down the drain loop.
func (b *InProcessBus) deliver(ctx context.Context, s subscription, e Event) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = fmt.Errorf("messaging: handler %q panicked: %v", s.durable, v)
		}
	}()
	return s.handler(ctx, e)
}

// Close stops accepting publishes and waits for in-flight deliveries.
func (b *InProcessBus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()

	b.wg.Wait()
	return nil
}
