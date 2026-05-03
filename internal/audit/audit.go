// Package audit centralizes how audit rows are written. Every
// state-changing path in the service goes through Recorder.Record so we
// have a single place to enforce the shape, the actor formatting, and
// the rule that audit rows live in the same transaction as the entity
// write that produced them (when there is one).
//
// Audit is *intent and authorization*. It is sibling, not derivative,
// of the event stream — a denied action emits no event but still leaves
// an audit row.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Sherlocked97/soarcore/internal/auth"
	"github.com/Sherlocked97/soarcore/internal/persistence"
)

// Recorder writes audit rows.
type Recorder struct {
	repo *persistence.AuditRepo
}

// NewRecorder returns a Recorder bound to a repository.
func NewRecorder(repo *persistence.AuditRepo) *Recorder { return &Recorder{repo: repo} }

// Result is one of the three audit outcomes.
type Result string

const (
	ResultSuccess Result = "success"
	ResultDenied  Result = "denied"
	ResultError   Result = "error"
)

// Target identifies the affected entity, when there is one.
type Target struct {
	Type string    `json:"type"`
	ID   uuid.UUID `json:"id"`
}

// Record appends a single audit row using the supplied Querier — pass
// the *pgx.Tx when in a transaction, or pool.Pgx() when not. The
// correlation_id is optional; passing uuid.Nil omits it.
func (r *Recorder) Record(
	ctx context.Context,
	q persistence.Querier,
	tenantID uuid.UUID,
	principal auth.Principal,
	action string,
	target *Target,
	result Result,
	correlationID uuid.UUID,
	metadata map[string]any,
) error {
	actorJSON, err := json.Marshal(map[string]string{
		"type": string(principal.Type),
		"id":   principal.ID,
	})
	if err != nil {
		// Marshalling a tiny map cannot fail in practice, but we still
		// surface the error for completeness.
		return fmt.Errorf("marshal actor: %w", err)
	}

	var targetJSON []byte
	if target != nil {
		targetJSON, err = json.Marshal(target)
		if err != nil {
			return fmt.Errorf("marshal target: %w", err)
		}
	}

	var metaJSON []byte
	if metadata != nil {
		metaJSON, err = json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("marshal metadata: %w", err)
		}
	}

	var corrPtr *uuid.UUID
	if correlationID != uuid.Nil {
		c := correlationID
		corrPtr = &c
	}

	row := persistence.AuditRow{
		ID:            uuid.New(),
		TenantID:      tenantID,
		OccurredAt:    time.Now().UTC(),
		Actor:         actorJSON,
		Action:        action,
		Target:        targetJSON,
		Result:        string(result),
		CorrelationID: corrPtr,
		Metadata:      metaJSON,
	}
	return r.repo.Insert(ctx, q, row)
}
