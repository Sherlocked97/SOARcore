package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ConnectorRow mirrors a row in the connector table.
type ConnectorRow struct {
	ID                string
	TenantID          uuid.UUID
	Capabilities      []byte // raw JSONB
	Status            string
	LastHeartbeatAt   *time.Time
	RegisteredAt      time.Time
	DeregisteredAt    *time.Time
}

// ConnectorRepo is the SQL gateway for the connector table.
type ConnectorRepo struct{}

// NewConnectorRepo returns a stateless repo.
func NewConnectorRepo() *ConnectorRepo { return &ConnectorRepo{} }

// Upsert inserts or updates a connector registration. We use ON CONFLICT
// so a re-registering connector (e.g. after a restart) doesn't fail.
func (r *ConnectorRepo) Upsert(ctx context.Context, q Querier, row ConnectorRow) error {
	const sql = `
		INSERT INTO connector (id, tenant_id, capabilities, status, registered_at)
		VALUES ($1, $2, $3, $4::connector_status, $5)
		ON CONFLICT (id) DO UPDATE SET
			tenant_id = EXCLUDED.tenant_id,
			capabilities = EXCLUDED.capabilities,
			status = EXCLUDED.status,
			registered_at = EXCLUDED.registered_at,
			deregistered_at = NULL
	`
	_, err := q.Exec(ctx, sql, row.ID, row.TenantID, row.Capabilities, row.Status, row.RegisteredAt)
	if err != nil {
		return fmt.Errorf("upsert connector: %w", err)
	}
	return nil
}

// Heartbeat updates last_heartbeat_at and ensures the connector is
// marked active.
func (r *ConnectorRepo) Heartbeat(ctx context.Context, q Querier, id string, at time.Time) error {
	const sql = `
		UPDATE connector
		SET last_heartbeat_at = $1, status = 'active'
		WHERE id = $2
	`
	tag, err := q.Exec(ctx, sql, at, id)
	if err != nil {
		return fmt.Errorf("heartbeat connector: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetByID looks up a connector. Used to validate that a heartbeat target
// actually exists.
func (r *ConnectorRepo) GetByID(ctx context.Context, q Querier, id string) (ConnectorRow, error) {
	const sql = `
		SELECT id, tenant_id, capabilities, status::text, last_heartbeat_at, registered_at, deregistered_at
		FROM connector
		WHERE id = $1
	`
	var row ConnectorRow
	err := q.QueryRow(ctx, sql, id).Scan(
		&row.ID, &row.TenantID, &row.Capabilities, &row.Status,
		&row.LastHeartbeatAt, &row.RegisteredAt, &row.DeregisteredAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConnectorRow{}, ErrNotFound
		}
		return ConnectorRow{}, fmt.Errorf("get connector: %w", err)
	}
	return row, nil
}
