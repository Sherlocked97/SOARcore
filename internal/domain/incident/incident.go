// Package incident is the domain layer for the Incident entity. It owns
// the typed Incident struct, the status state machine, and the validation
// rules that cannot be expressed in SQL constraints (status transitions,
// attribute schema validation).
//
// Layout:
//   - incident.go  — types, constants, the state machine.
//   - service.go   — orchestrates persistence + audit + outbox writes.
//
// Two strict rules in this layer:
//
//  1. SQL never appears here. The service calls into internal/persistence
//     (the only place SQL lives).
//  2. Every write opens a transaction, then writes (a) the entity row,
//     (b) the audit row, (c) the outbox row in that one transaction.
//     This is the at-least-once-events guarantee from ADR-001.
package incident

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// SchemaVersion is the current entity schema version. Stamped on every
// row written by the service. Bump (and add a migration) when the row
// shape changes.
const SchemaVersion = 1

// Severity is a typed enum. Using a distinct string-typed enum makes
// signatures self-documenting and lets tests use named values.
type Severity string

const (
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// validSeverities is the source-of-truth set used by IsValid.
var validSeverities = map[Severity]struct{}{
	SeverityLow: {}, SeverityMedium: {}, SeverityHigh: {}, SeverityCritical: {},
}

// IsValid reports whether s is a known severity. Useful when validating
// input from the API.
func (s Severity) IsValid() bool { _, ok := validSeverities[s]; return ok }

// Status enumerates the lifecycle states of an incident. The DB also
// carries an enum of the same names, but the *transitions* between them
// live below.
type Status string

const (
	StatusNew        Status = "new"
	StatusTriaged    Status = "triaged"
	StatusInProgress Status = "in_progress"
	StatusContained  Status = "contained"
	StatusResolved   Status = "resolved"
	StatusClosed     Status = "closed"
)

var validStatuses = map[Status]struct{}{
	StatusNew: {}, StatusTriaged: {}, StatusInProgress: {},
	StatusContained: {}, StatusResolved: {}, StatusClosed: {},
}

// IsValid reports whether s is a known status.
func (s Status) IsValid() bool { _, ok := validStatuses[s]; return ok }

// allowedTransitions encodes the state machine documented in
// context/data_model.md:
//
//	new → triaged → in_progress → contained → resolved → closed
//	                       ↑                       ↓
//	                       └──── reopen ───────────┘  (resolved/closed → in_progress)
//
// A transition from X to Y is valid iff allowedTransitions[X][Y] is set.
// We deliberately keep this in code (not the DB) so violations show up
// as typed Go errors mappable to a 422 by the API layer.
var allowedTransitions = map[Status]map[Status]struct{}{
	StatusNew:        {StatusTriaged: {}},
	StatusTriaged:    {StatusInProgress: {}},
	StatusInProgress: {StatusContained: {}, StatusResolved: {}},
	StatusContained:  {StatusResolved: {}},
	StatusResolved:   {StatusClosed: {}, StatusInProgress: {}}, // reopen
	StatusClosed:     {StatusInProgress: {}},                   // reopen
}

// ErrInvalidTransition is returned by CanTransition when the proposed
// transition is not in allowedTransitions. The API layer maps this to
// 422 Unprocessable Entity.
var ErrInvalidTransition = errors.New("invalid status transition")

// CanTransition reports whether `from → to` is a legal status change.
// Returns nil for valid transitions, ErrInvalidTransition otherwise.
//
// A "transition" to the same state is treated as valid (a no-op) because
// idempotent updates are common (e.g. a connector re-asserting state).
func CanTransition(from, to Status) error {
	if from == to {
		return nil
	}
	allowed, ok := allowedTransitions[from]
	if !ok {
		return ErrInvalidTransition
	}
	if _, ok := allowed[to]; !ok {
		return ErrInvalidTransition
	}
	return nil
}

// Incident is the domain-shaped struct. It is nearly identical to
// persistence.IncidentRow but uses richer types (Severity, Status,
// map[string]any for attributes) so the service and API layers don't
// fight stringly-typed enums.
type Incident struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	SchemaVersion     int
	ExternalID        *string
	Title             string
	Description       *string
	Severity          Severity
	Status            Status
	AssigneeID        *uuid.UUID
	SourceConnectorID *string
	Attributes        map[string]any
	Tags              []string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ClosedAt          *time.Time
}
