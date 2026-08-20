package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"time"
)

// PendingEvent is an outbox row awaiting publication.
type PendingEvent struct {
	Seq           int64
	EventID       string
	Subject       string
	OperationID   string
	CorrelationID string
	Payload       json.RawMessage
	Attempts      int
}

// OutboxReader is the slice of the outbox this package needs.
//
// It is declared here rather than imported from storage so that messaging does
// not depend on the domain packages that queue events into it. The storage
// implementation satisfies it structurally.
type OutboxReader interface {
	Pending(ctx context.Context, now time.Time, limit int) ([]PendingEvent, error)
	MarkPublished(ctx context.Context, eventID string, at time.Time) error
	MarkFailed(ctx context.Context, eventID string, nextAttempt time.Time, cause string) error
}

// PublisherConfig tunes the outbox drain.
type PublisherConfig struct {
	// BatchSize bounds how many events are drained per pass.
	BatchSize int
	// PollInterval is the fallback tick. The drain is normally woken by
	// Notify, so this only has to catch an event whose notification was
	// missed -- for instance one committed just before a restart.
	PollInterval time.Duration
	// BaseBackoff is the first retry delay after a publish failure.
	BaseBackoff time.Duration
	// MaxBackoff caps the retry delay.
	//
	// If a JetStream bus is ever introduced, this must stay well below the
	// stream's duplicate window: a republish arriving after that window is no
	// longer recognised as a duplicate and lands twice.
	MaxBackoff time.Duration
}

func (c *PublisherConfig) withDefaults() {
	if c.BatchSize <= 0 {
		c.BatchSize = 128
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = time.Minute
	}
}

// Publisher drains the outbox onto the bus.
//
// It is the only component that turns a committed state change into a
// delivered event, and it is restart-safe by construction: the drain query is
// the entire recovery procedure. There is no separate recovery path that could
// be wrong, because an unpublished row looks identical whether it was written
// a millisecond ago or before the last crash.
type Publisher struct {
	repo   OutboxReader
	bus    Bus
	log    *slog.Logger
	cfg    PublisherConfig
	now    func() time.Time
	notify chan struct{}

	stopOnce sync.Once
	done     chan struct{}
}

// NewPublisher builds the drain worker.
func NewPublisher(repo OutboxReader, bus Bus, log *slog.Logger, cfg PublisherConfig, now func() time.Time) *Publisher {
	cfg.withDefaults()
	if now == nil {
		now = time.Now
	}
	return &Publisher{
		repo: repo,
		bus:  bus,
		log:  log,
		cfg:  cfg,
		now:  now,
		// Buffered depth one: many commits collapsing into one wake-up is
		// exactly right, since the drain reads a batch rather than a single
		// event.
		notify: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

// Notify wakes the drain. It never blocks, so a caller inside a transaction
// commit path cannot be stalled by a busy publisher.
func (p *Publisher) Notify() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// Run drains until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context) error {
	defer close(p.done)

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	// Drain once at startup to pick up anything committed before the last
	// shutdown.
	p.drain(ctx)

	for {
		select {
		case <-ctx.Done():
			// Best-effort final drain so a clean shutdown does not leave
			// events sitting until the next start.
			p.finalDrain()
			return nil
		case <-ticker.C:
			p.drain(ctx)
		case <-p.notify:
			p.drain(ctx)
		}
	}
}

// finalDrain runs one last pass with a short, independent deadline.
func (p *Publisher) finalDrain() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p.drain(ctx)
}

// drain publishes one batch.
func (p *Publisher) drain(ctx context.Context) {
	pending, err := p.repo.Pending(ctx, p.now(), p.cfg.BatchSize)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			p.log.Error("failed to read the outbox", "error", err)
		}
		return
	}

	for _, ev := range pending {
		if ctx.Err() != nil {
			return
		}
		p.publishOne(ctx, ev)
	}
}

// publishOne delivers a single event and records the outcome.
func (p *Publisher) publishOne(ctx context.Context, ev PendingEvent) {
	event := Event{
		ID:            ev.EventID,
		Subject:       ev.Subject,
		OperationID:   ev.OperationID,
		CorrelationID: ev.CorrelationID,
		OccurredAt:    p.now(),
		Payload:       ev.Payload,
	}

	if err := p.bus.Publish(ctx, event); err != nil {
		delay := p.backoff(ev.Attempts)
		p.log.Warn("event publication failed; will retry",
			"event_id", ev.EventID, "subject", ev.Subject,
			"attempts", ev.Attempts+1, "retry_in", delay, "error", err)

		if mErr := p.repo.MarkFailed(ctx, ev.EventID, p.now().Add(delay), err.Error()); mErr != nil {
			p.log.Error("failed to record a publication failure",
				"event_id", ev.EventID, "error", mErr)
		}
		return
	}

	// Marking published is deliberately its own transaction. Folding it into
	// the business transaction would hold the single writer connection open
	// across delivery, turning consumer latency into database contention and a
	// stalled consumer into a total write outage.
	//
	// The cost is at-least-once rather than exactly-once delivery, which is
	// acceptable precisely because no consumer carries authority.
	if err := p.repo.MarkPublished(ctx, ev.EventID, p.now()); err != nil {
		p.log.Error("event was published but could not be marked; it will be redelivered",
			"event_id", ev.EventID, "error", err)
	}
}

// backoff returns the retry delay for an attempt count, with jitter.
//
// Jitter matters even single-threaded: without it a batch that fails together
// retries together, reproducing the same burst that failed.
func (p *Publisher) backoff(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	// Cap the exponent before shifting, so a long-stuck event cannot overflow
	// into a negative duration.
	if attempts > 20 {
		attempts = 20
	}
	delay := float64(p.cfg.BaseBackoff) * math.Pow(2, float64(attempts))
	if delay > float64(p.cfg.MaxBackoff) {
		delay = float64(p.cfg.MaxBackoff)
	}
	// Full jitter over [delay/2, delay].
	jittered := delay/2 + rand.Float64()*(delay/2)
	return time.Duration(jittered)
}

// Wait blocks until Run has returned.
func (p *Publisher) Wait(ctx context.Context) error {
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
