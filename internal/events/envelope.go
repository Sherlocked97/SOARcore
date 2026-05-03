// Package events defines the wire envelope for everything that travels
// across the message bus, plus the AMQP publisher and subscriber. The
// envelope shape is normative — it is the contract between the core and
// every connector, today and forever (until a superseding ADR rolls the
// envelope schema version).
//
// Why this package contains both the type and the bus client
// ----------------------------------------------------------
// Two reasons:
//
//  1. The envelope is a value object that must be importable everywhere
//     (the domain produces them, the relay publishes them, connectors
//     consume them). Putting it next to the publisher keeps a single
//     cohesive surface.
//  2. The bus client (Publisher / Subscriber) is the *only* thing in the
//     codebase that imports the AMQP library. The bus client must not
//     leak past the events package; centralizing both here makes that
//     boundary easy to enforce.
//
// To keep the envelope itself dependency-free (so the reference connector
// can import it without dragging in AMQP), this file deliberately contains
// only the type and helpers. The AMQP code lives in publisher.go /
// subscriber.go.
package events

import (
	"time"

	"github.com/google/uuid"
)

// SchemaVersion is the current envelope schema version. Bumping this is a
// bus-level breaking change; consumers declare which envelope versions
// they accept at registration time.
const SchemaVersion = 1

// Event types we emit today. Adding a new event type means: (1) a new
// constant here, (2) a payload type for it, (3) the domain layer enqueues
// it, (4) consumers that care declare it in their capabilities.
const (
	EventTypeIncidentCreated       = "incident.created"
	EventTypeIncidentUpdated       = "incident.updated"
	EventTypeIncidentStatusChanged = "incident.status_changed"
	EventTypeIncidentClosed        = "incident.closed"
)

// ActorType enumerates who can cause a state change. Stored as a string
// (not an int) so wire dumps are human-readable.
type ActorType string

const (
	ActorUser      ActorType = "user"
	ActorConnector ActorType = "connector"
	ActorSystem    ActorType = "system"
)

// Actor identifies the principal that caused an event. The struct tags
// (in backticks) tell encoding/json which JSON keys to use — Go field
// names are CamelCase, but on the wire we want snake_case.
type Actor struct {
	Type ActorType `json:"type"`
	ID   string    `json:"id"`
}

// Entity identifies the object the event is about, plus the schema
// version it was written under. Consumers can use SchemaVersion to decide
// whether they understand this row.
type Entity struct {
	Type          string    `json:"type"`
	ID            uuid.UUID `json:"id"`
	SchemaVersion int       `json:"schema_version"`
}

// Envelope is the canonical message shape on the bus. Every field on this
// struct corresponds 1:1 with a row in the "Event envelope" table in
// context/data_model.md.
//
// Pointer-typed fields (*uuid.UUID) represent optional values: a nil
// pointer marshals to a missing JSON key (with the omitempty tag), which
// is what we want for fields like prior_event_id that may not exist.
type Envelope struct {
	EventID            uuid.UUID  `json:"event_id"`
	EventType          string     `json:"event_type"`
	EventSchemaVersion int        `json:"event_schema_version"`
	OccurredAt         time.Time  `json:"occurred_at"`
	TenantID           uuid.UUID  `json:"tenant_id"`
	Actor              Actor      `json:"actor"`
	Entity             Entity     `json:"entity"`
	Payload            any        `json:"payload"`
	CorrelationID      uuid.UUID  `json:"correlation_id"`
	PriorEventID       *uuid.UUID `json:"prior_event_id,omitempty"`
}

// NewEnvelope is a small constructor that fills in the boilerplate
// (event_id, occurred_at, schema version) so callers only specify what
// matters semantically. It's a convenience, not a contract — domain code
// is welcome to build envelopes by hand if it needs full control.
func NewEnvelope(
	eventType string,
	tenantID uuid.UUID,
	actor Actor,
	entity Entity,
	payload any,
	correlationID uuid.UUID,
) Envelope {
	return Envelope{
		EventID:            uuid.New(),
		EventType:          eventType,
		EventSchemaVersion: SchemaVersion,
		OccurredAt:         time.Now().UTC(),
		TenantID:           tenantID,
		Actor:              actor,
		Entity:             entity,
		Payload:            payload,
		CorrelationID:      correlationID,
	}
}
