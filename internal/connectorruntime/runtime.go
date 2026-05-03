// Package connectorruntime implements the in-core surface of ADR-003:
// connector identity, capability declaration, and lifecycle (heartbeat,
// deregistration). Connectors themselves are out-of-process — this
// package only manages their *records* in the database.
//
// The wire contract is: connectors POST to /v1/connectors to register,
// PATCH /v1/connectors/:id/heartbeat periodically, and DELETE
// /v1/connectors/:id to deregister. All three call into the methods on
// Service below.
package connectorruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Sherlocked97/soarcore/internal/persistence"
	"github.com/Sherlocked97/soarcore/internal/schemaregistry"
)

// Capabilities is what a connector declares at registration. Producing /
// consuming entity types and which event-schema versions it understands.
type Capabilities struct {
	Produces             []string `json:"produces"`
	Consumes             []string `json:"consumes"`
	EventSchemaVersions  []int    `json:"event_schema_versions"`
}

// Registration is the record the API persists. It mirrors the row but
// with the JSONB capabilities decoded.
type Registration struct {
	ID                string
	TenantID          uuid.UUID
	Capabilities      Capabilities
	Status            string
	LastHeartbeatAt   *time.Time
	RegisteredAt      time.Time
}

// ErrUnknownEntityType is returned when a connector declares producing
// or consuming an entity type the core's schema registry doesn't know
// about. This catches typos at registration time rather than letting
// them produce silent dead-letter routing later.
var ErrUnknownEntityType = errors.New("unknown entity type")

// ErrUnsupportedEventSchema is returned when a connector declares an
// event_schema_version the core doesn't speak.
var ErrUnsupportedEventSchema = errors.New("unsupported event schema version")

// Service implements the runtime.
type Service struct {
	repo   *persistence.ConnectorRepo
	schema *schemaregistry.Registry
}

// NewService returns a wired-up runtime service.
func NewService(repo *persistence.ConnectorRepo, schema *schemaregistry.Registry) *Service {
	return &Service{repo: repo, schema: schema}
}

// Register upserts a connector identity. ID is supplied by the caller
// (the connector itself) so a connector that restarts re-uses its ID.
func (s *Service) Register(ctx context.Context, q persistence.Querier, id string, tenantID uuid.UUID, caps Capabilities) (*Registration, error) {
	if id == "" {
		return nil, fmt.Errorf("connector id is required")
	}
	// Validate declared types/versions are known.
	for _, t := range append(caps.Produces, caps.Consumes...) {
		if !s.schema.Has(schemaregistry.EntityType(t), 1) {
			return nil, fmt.Errorf("%w: %s", ErrUnknownEntityType, t)
		}
	}
	for _, v := range caps.EventSchemaVersions {
		// v1 is the only one we ship today.
		if v != 1 {
			return nil, fmt.Errorf("%w: v%d", ErrUnsupportedEventSchema, v)
		}
	}
	capsJSON, err := json.Marshal(caps)
	if err != nil {
		return nil, fmt.Errorf("marshal capabilities: %w", err)
	}
	now := time.Now().UTC()
	row := persistence.ConnectorRow{
		ID:           id,
		TenantID:     tenantID,
		Capabilities: capsJSON,
		Status:       "active",
		RegisteredAt: now,
	}
	if err := s.repo.Upsert(ctx, q, row); err != nil {
		return nil, err
	}
	return &Registration{
		ID: id, TenantID: tenantID, Capabilities: caps,
		Status: "active", RegisteredAt: now,
	}, nil
}

// Heartbeat marks a connector as alive.
func (s *Service) Heartbeat(ctx context.Context, q persistence.Querier, id string) error {
	return s.repo.Heartbeat(ctx, q, id, time.Now().UTC())
}
