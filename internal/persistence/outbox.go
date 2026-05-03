package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// OutboxRow mirrors a row in the outbox table.
type OutboxRow struct {
	ID          int64
	EventID     uuid.UUID
	EventType   string
	TenantID    uuid.UUID
	Payload     []byte // raw envelope JSONB
	CreatedAt   time.Time
	PublishedAt *time.Time
}

// OutboxRepo is the SQL gateway for the outbox table. The relay reads
// from it; the domain writes to it inside the same transaction as the
// entity write.
type OutboxRepo struct{}

// NewOutboxRepo returns a stateless repo.
func NewOutboxRepo() *OutboxRepo { return &OutboxRepo{} }

// Enqueue stages a single envelope for publishing. The transaction-wide
// guarantee that the event becomes durable iff the entity write commits
// is the whole point of this table — never call Enqueue outside a
// transaction with the entity write.
func (r *OutboxRepo) Enqueue(ctx context.Context, q Querier, row OutboxRow) error {
	const sql = `
		INSERT INTO outbox (event_id, event_type, tenant_id, payload, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	_, err := q.Exec(ctx, sql, row.EventID, row.EventType, row.TenantID, row.Payload, row.CreatedAt)
	if err != nil {
		return fmt.Errorf("enqueue outbox: %w", err)
	}
	return nil
}

// FetchUnpublished returns up to `limit` envelopes that have not yet been
// published, oldest first. The relay calls this in a tight loop.
//
// FOR UPDATE SKIP LOCKED would let multiple relay workers run in
// parallel; v1 has only one relay so we keep the simpler form.
func (r *OutboxRepo) FetchUnpublished(ctx context.Context, q Querier, limit int) ([]OutboxRow, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	const sql = `
		SELECT id, event_id, event_type, tenant_id, payload, created_at, published_at
		FROM outbox
		WHERE published_at IS NULL
		ORDER BY created_at ASC
		LIMIT $1
	`
	rows, err := q.Query(ctx, sql, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch unpublished outbox: %w", err)
	}
	defer rows.Close()

	var out []OutboxRow
	for rows.Next() {
		var row OutboxRow
		err := rows.Scan(&row.ID, &row.EventID, &row.EventType, &row.TenantID, &row.Payload, &row.CreatedAt, &row.PublishedAt)
		if err != nil {
			return nil, fmt.Errorf("scan outbox row: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// MarkPublished sets published_at on a row. The relay calls this after a
// successful AMQP publish.
func (r *OutboxRepo) MarkPublished(ctx context.Context, q Querier, id int64, at time.Time) error {
	const sql = `UPDATE outbox SET published_at = $1 WHERE id = $2`
	_, err := q.Exec(ctx, sql, at, id)
	if err != nil {
		return fmt.Errorf("mark outbox published: %w", err)
	}
	return nil
}
