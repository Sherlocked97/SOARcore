// Package outbox holds the background relay that drains the outbox table
// to RabbitMQ. The relay is the at-least-once boundary in the system:
// rows in the outbox are guaranteed durable (they were written in the
// same transaction as the entity), and the relay is responsible for
// publishing them at least once. Consumers must therefore be idempotent
// on event_id.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Sherlocked97/soarcore/internal/events"
	"github.com/Sherlocked97/soarcore/internal/persistence"
)

// outboxRepo is the slice of *persistence.OutboxRepo this package
// actually uses. Defining the interface here (instead of importing the
// concrete type everywhere) keeps the relay unit-testable with a stub.
type outboxRepo interface {
	FetchUnpublished(ctx context.Context, q persistence.Querier, limit int) ([]persistence.OutboxRow, error)
	MarkPublished(ctx context.Context, q persistence.Querier, id int64, at time.Time) error
}

// publisher is the slice of *events.Publisher this package needs.
type publisher interface {
	Publish(ctx context.Context, env events.Envelope) error
}

// querierSource is the slice of *persistence.Pool the relay uses — just
// enough to get a Querier for the read/mark queries.
type querierSource interface {
	Pgx() persistence.Querier
}

// Relay drains the outbox table to a Publisher. It is started as a
// background goroutine from main() and runs until its context is
// cancelled.
type Relay struct {
	pool      querierSource
	repo      outboxRepo
	publisher publisher
	logger    *slog.Logger
	interval  time.Duration
	batch     int
}

// New constructs a Relay. Defaults: 250ms polling interval, 100-row batches.
func New(pool *persistence.Pool, repo *persistence.OutboxRepo, publisher *events.Publisher, logger *slog.Logger) *Relay {
	return newRelay(pool, repo, publisher, logger)
}

// newRelay is the internal constructor that accepts the interface-typed
// dependencies. Tests inside this package use it; New is the public
// face that the rest of the codebase wires up with concrete types.
func newRelay(pool querierSource, repo outboxRepo, pub publisher, logger *slog.Logger) *Relay {
	return &Relay{
		pool: pool, repo: repo, publisher: pub, logger: logger,
		interval: 250 * time.Millisecond,
		batch:    100,
	}
}

// Run blocks until ctx is cancelled. Each iteration: fetch a batch of
// unpublished rows, publish each, mark each published. Errors during
// publish leave the row unmarked so the next iteration retries it —
// this is the at-least-once mechanism.
//
// Run is intended to be called as `go relay.Run(ctx)` from main(). The
// returned error is non-nil only on terminal failures (e.g. ctx
// cancelled with a non-nil cause).
func (r *Relay) Run(ctx context.Context) error {
	r.logger.Info("outbox relay starting", "interval", r.interval, "batch", r.batch)
	// time.NewTicker fires repeatedly on a channel — the standard Go
	// pattern for periodic work. defer ticker.Stop() releases the
	// underlying resources when the function returns.
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("outbox relay stopping", "reason", ctx.Err())
			return nil
		case <-ticker.C:
			if err := r.drainOnce(ctx); err != nil {
				// drainOnce returns errors only for unexpected
				// problems (DB unavailable). Per-row publish
				// failures are swallowed and logged inside drainOnce.
				r.logger.Warn("outbox drain error", "err", err)
			}
		}
	}
}

// drainOnce processes one batch of unpublished rows. Exposed (lowercase)
// for testing via white-box tests.
func (r *Relay) drainOnce(ctx context.Context) error {
	rows, err := r.repo.FetchUnpublished(ctx, r.pool.Pgx(), r.batch)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	for _, row := range rows {
		var env events.Envelope
		if err := json.Unmarshal(row.Payload, &env); err != nil {
			// A malformed payload should never happen — the domain
			// builds envelopes itself. Log and skip; we deliberately
			// do *not* mark this row published, so it stays visible
			// for ops to inspect.
			r.logger.Error("malformed outbox payload, skipping",
				"outbox_id", row.ID, "event_id", row.EventID, "err", err)
			continue
		}
		if err := r.publisher.Publish(ctx, env); err != nil {
			// Bus is down or transient error. Stop processing this
			// batch — order matters within a tenant — and try again
			// next tick.
			r.logger.Warn("publish failed, will retry",
				"outbox_id", row.ID, "event_id", row.EventID, "err", err)
			return nil
		}
		if err := r.repo.MarkPublished(ctx, r.pool.Pgx(), row.ID, time.Now().UTC()); err != nil {
			// Mark-published failed but publish succeeded → the
			// next iteration will republish, and consumers must
			// dedupe on event_id. This is acceptable per ADR-001.
			r.logger.Warn("mark published failed",
				"outbox_id", row.ID, "event_id", row.EventID, "err", err)
		}
	}
	return nil
}
