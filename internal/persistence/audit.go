package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AuditRow mirrors a row in the audit table. Append-only by convention —
// there is no Update or Delete method on AuditRepo.
type AuditRow struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	OccurredAt    time.Time
	Actor         []byte // raw JSONB (e.g. {"type":"user","id":"..."})
	Action        string
	Target        []byte // raw JSONB; may be nil
	Result        string // "success" | "denied" | "error"
	CorrelationID *uuid.UUID
	Metadata      []byte // raw JSONB; may be nil
}

// AuditRepo writes rows to the audit table. Like IncidentRepo it accepts
// a Querier so the same call works in or out of a transaction — but the
// invariant from ADR-001 is that audit.Insert is *always* called inside
// the same transaction as the entity write that produced it.
type AuditRepo struct{}

// NewAuditRepo returns a stateless repo.
func NewAuditRepo() *AuditRepo { return &AuditRepo{} }

// Insert appends one audit row. The caller is responsible for setting ID
// and OccurredAt — typically the audit recorder in internal/audit.
func (r *AuditRepo) Insert(ctx context.Context, q Querier, row AuditRow) error {
	const sql = `
		INSERT INTO audit (
			id, tenant_id, occurred_at, actor, action, target, result, correlation_id, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7::audit_result, $8, $9
		)
	`
	_, err := q.Exec(ctx, sql,
		row.ID, row.TenantID, row.OccurredAt, row.Actor, row.Action,
		row.Target, row.Result, row.CorrelationID, row.Metadata,
	)
	if err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}

// CountForTenant is a tiny helper used by the smoke test to assert "an
// audit row exists for this incident". Production callers should use
// proper queries — this one exists for verification.
func (r *AuditRepo) CountForTenant(ctx context.Context, q Querier, tenantID uuid.UUID) (int, error) {
	const sql = `SELECT COUNT(*) FROM audit WHERE tenant_id = $1`
	var n int
	if err := q.QueryRow(ctx, sql, tenantID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count audit: %w", err)
	}
	return n, nil
}
