package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Sherlocked97/soarcore/internal/audit"
	"github.com/Sherlocked97/soarcore/internal/auth"
	"github.com/Sherlocked97/soarcore/internal/events"
	"github.com/Sherlocked97/soarcore/internal/persistence"
	"github.com/Sherlocked97/soarcore/internal/schemaregistry"
)

// Service orchestrates writes to the incident, audit, and outbox tables
// in a single transaction. It is the only place that knows the rule
// "every write produces an outbox row".
type Service struct {
	pool       *persistence.Pool
	incidents  *persistence.IncidentRepo
	outbox     *persistence.OutboxRepo
	audit      *audit.Recorder
	schema     *schemaregistry.Registry
	authorizer auth.Authorizer
}

// NewService wires up the dependencies. Caller passes already-constructed
// repos so this layer is easy to substitute in tests.
func NewService(
	pool *persistence.Pool,
	incidents *persistence.IncidentRepo,
	outbox *persistence.OutboxRepo,
	auditR *audit.Recorder,
	schema *schemaregistry.Registry,
	authorizer auth.Authorizer,
) *Service {
	return &Service{
		pool: pool, incidents: incidents, outbox: outbox,
		audit: auditR, schema: schema, authorizer: authorizer,
	}
}

// CreateInput is the shape the API layer hands to Create. It mirrors the
// JSON request body, with the validation already done at the API layer
// (e.g. severity is a typed Severity, not a string).
type CreateInput struct {
	ExternalID        *string
	Title             string
	Description       *string
	Severity          Severity
	AssigneeID        *uuid.UUID
	SourceConnectorID *string
	Attributes        map[string]any
	Tags              []string
}

// ErrInvalidInput is returned when input fails domain validation. The
// API layer maps it to 400.
var ErrInvalidInput = errors.New("invalid input")

// Create persists a new Incident, writes the audit row, and enqueues an
// incident.created envelope — all in one transaction.
//
// The correlation_id ties the entity, audit, and event together; it is
// generated here unless the caller has one to supply (e.g. propagating
// from upstream).
func (s *Service) Create(
	ctx context.Context,
	principal auth.Principal,
	tenantID uuid.UUID,
	in CreateInput,
	correlationID uuid.UUID,
) (*Incident, error) {
	// Authz first — the audit row records denial separately if denied.
	if err := s.authorizer.Allow(ctx, "incident.create", "incident"); err != nil {
		// We *do* want to persist the denial. We use a fresh tx so the
		// denial audit row commits even though no entity was written.
		_ = s.pool.WithTx(ctx, func(tx pgx.Tx) error {
			return s.audit.Record(ctx, tx, tenantID, principal, "incident.create",
				nil, audit.ResultDenied, correlationID, nil)
		})
		return nil, err
	}

	// Domain-level input validation.
	if err := validateCreateInput(in); err != nil {
		return nil, err
	}

	// Validate the attributes blob against the registered schema.
	attrBytes, err := marshalAttributes(in.Attributes)
	if err != nil {
		return nil, fmt.Errorf("%w: attributes: %s", ErrInvalidInput, err)
	}
	if err := s.schema.Validate(schemaregistry.EntityIncident, SchemaVersion, attrBytes); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidInput, err)
	}

	if correlationID == uuid.Nil {
		correlationID = uuid.New()
	}

	now := time.Now().UTC()
	inc := Incident{
		ID:                uuid.New(),
		TenantID:          tenantID,
		SchemaVersion:     SchemaVersion,
		ExternalID:        in.ExternalID,
		Title:             in.Title,
		Description:       in.Description,
		Severity:          in.Severity,
		Status:            StatusNew, // every incident starts in `new`
		AssigneeID:        in.AssigneeID,
		SourceConnectorID: in.SourceConnectorID,
		Attributes:        in.Attributes,
		Tags:              in.Tags,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	// Build the event envelope *before* opening the transaction so the
	// payload is final and the tx is short.
	envelope := events.NewEnvelope(
		events.EventTypeIncidentCreated,
		tenantID,
		actorOf(principal),
		events.Entity{Type: "incident", ID: inc.ID, SchemaVersion: SchemaVersion},
		map[string]any{"incident": incidentJSON(inc)},
		correlationID,
	)
	envelopeBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("marshal envelope: %w", err)
	}

	// One transaction for the row + audit + outbox.
	err = s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		row := toRow(inc, attrBytes)
		if err := s.incidents.Insert(ctx, tx, row); err != nil {
			return err
		}
		if err := s.audit.Record(ctx, tx, tenantID, principal, "incident.create",
			&audit.Target{Type: "incident", ID: inc.ID},
			audit.ResultSuccess, correlationID, nil); err != nil {
			return err
		}
		return s.outbox.Enqueue(ctx, tx, persistence.OutboxRow{
			EventID:   envelope.EventID,
			EventType: envelope.EventType,
			TenantID:  tenantID,
			Payload:   envelopeBytes,
			CreatedAt: now,
		})
	})
	if err != nil {
		return nil, err
	}
	return &inc, nil
}

// PatchInput is the shape the API hands to Patch. nil pointers mean "do
// not touch"; non-nil pointers replace the field. Tags being non-nil and
// empty means "clear all tags".
type PatchInput struct {
	Title       *string
	Description *string
	Severity    *Severity
	Status      *Status
	AssigneeID  *uuid.UUID
	Attributes  map[string]any // nil = no change; explicit empty map = set to {}
	Tags        []string       // nil = no change; explicit empty slice = clear
}

// HasAttributes reports whether attributes were supplied (vs left absent).
// We use a separate flag because nil and empty maps are not distinguishable
// by length alone — the JSON decoder produces `nil` for absent and
// `map[string]any{}` for `{}`.
func (p PatchInput) hasAttributes() bool { return p.Attributes != nil }

// Patch applies a partial update, writes the audit row(s), and emits the
// appropriate event(s) — all in one transaction.
//
// If the status is changing, two events are emitted: incident.updated
// AND incident.status_changed. If the new status is `closed`, a third
// event (incident.closed) is also emitted, per data_model.md.
func (s *Service) Patch(
	ctx context.Context,
	principal auth.Principal,
	tenantID uuid.UUID,
	id uuid.UUID,
	p PatchInput,
	correlationID uuid.UUID,
) (*Incident, error) {
	if err := s.authorizer.Allow(ctx, "incident.update", "incident"); err != nil {
		_ = s.pool.WithTx(ctx, func(tx pgx.Tx) error {
			return s.audit.Record(ctx, tx, tenantID, principal, "incident.update",
				&audit.Target{Type: "incident", ID: id},
				audit.ResultDenied, correlationID, nil)
		})
		return nil, err
	}

	if correlationID == uuid.Nil {
		correlationID = uuid.New()
	}

	// We need the prior state to compute changed_fields and to validate
	// the status transition. Read inside the tx for consistency.
	var (
		updated         Incident
		changedFields   []string
		statusChanged   bool
		fromStatus      Status
		toStatus        Status
	)

	err := s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		prior, err := s.incidents.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		priorInc := fromRow(prior)

		// Compute the patch.
		upd := persistence.IncidentUpdate{UpdatedAt: time.Now().UTC()}
		if p.Title != nil && *p.Title != priorInc.Title {
			upd.Title = p.Title
			changedFields = append(changedFields, "title")
		}
		if p.Description != nil {
			upd.Description = p.Description
			changedFields = append(changedFields, "description")
		}
		if p.Severity != nil {
			if !p.Severity.IsValid() {
				return fmt.Errorf("%w: invalid severity %q", ErrInvalidInput, *p.Severity)
			}
			s := string(*p.Severity)
			upd.Severity = &s
			changedFields = append(changedFields, "severity")
		}
		if p.Status != nil {
			if !p.Status.IsValid() {
				return fmt.Errorf("%w: invalid status %q", ErrInvalidInput, *p.Status)
			}
			if err := CanTransition(priorInc.Status, *p.Status); err != nil {
				return err
			}
			statusStr := string(*p.Status)
			upd.Status = &statusStr
			changedFields = append(changedFields, "status")
			statusChanged = priorInc.Status != *p.Status
			fromStatus = priorInc.Status
			toStatus = *p.Status
			if *p.Status == StatusClosed {
				closedAt := time.Now().UTC()
				upd.ClosedAt = &closedAt
			}
		}
		if p.AssigneeID != nil {
			upd.AssigneeID = p.AssigneeID
			changedFields = append(changedFields, "assignee_id")
		}
		if p.hasAttributes() {
			attrBytes, err := marshalAttributes(p.Attributes)
			if err != nil {
				return fmt.Errorf("%w: attributes: %s", ErrInvalidInput, err)
			}
			if err := s.schema.Validate(schemaregistry.EntityIncident, SchemaVersion, attrBytes); err != nil {
				return fmt.Errorf("%w: %s", ErrInvalidInput, err)
			}
			upd.Attributes = attrBytes
			changedFields = append(changedFields, "attributes")
		}
		if p.Tags != nil {
			// Only mark as changed if the tag set differs.
			if !stringSliceEqual(p.Tags, priorInc.Tags) {
				upd.Tags = p.Tags
				changedFields = append(changedFields, "tags")
			}
		}

		// If nothing actually changed, skip the SQL UPDATE — we still
		// don't write an audit row because no action was taken.
		if len(changedFields) == 0 {
			updated = priorInc
			return nil
		}

		if err := s.incidents.Update(ctx, tx, tenantID, id, upd); err != nil {
			return err
		}

		// Re-read so the post-update snapshot is precise (DB-side defaults
		// like updated_at would otherwise drift).
		after, err := s.incidents.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		updated = fromRow(after)

		// Audit: one row per logical action. Status change → its own
		// audit action; otherwise just incident.update.
		auditAction := "incident.update"
		if statusChanged {
			auditAction = "incident.status_change"
		}
		if err := s.audit.Record(ctx, tx, tenantID, principal, auditAction,
			&audit.Target{Type: "incident", ID: id},
			audit.ResultSuccess, correlationID, nil); err != nil {
			return err
		}

		// Outbox: incident.updated always, plus incident.status_changed
		// (and incident.closed when applicable).
		if err := s.enqueueEvent(ctx, tx, events.EventTypeIncidentUpdated, tenantID, principal, updated, correlationID,
			map[string]any{
				"changed_fields": changedFields,
				"incident":       incidentJSON(updated),
			}); err != nil {
			return err
		}
		if statusChanged {
			if err := s.enqueueEvent(ctx, tx, events.EventTypeIncidentStatusChanged, tenantID, principal, updated, correlationID,
				map[string]any{
					"from":        string(fromStatus),
					"to":          string(toStatus),
					"incident_id": updated.ID,
				}); err != nil {
				return err
			}
			if toStatus == StatusClosed {
				if err := s.enqueueEvent(ctx, tx, events.EventTypeIncidentClosed, tenantID, principal, updated, correlationID,
					map[string]any{
						"incident_id":   updated.ID,
						"closed_at":     updated.ClosedAt,
						"final_status": string(toStatus),
					}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// Get fetches a single incident by ID, scoped to the tenant.
func (s *Service) Get(ctx context.Context, tenantID, id uuid.UUID) (*Incident, error) {
	row, err := s.incidents.GetByID(ctx, s.pool.Pgx(), tenantID, id)
	if err != nil {
		return nil, err
	}
	inc := fromRow(row)
	return &inc, nil
}

// List returns incidents for the tenant filtered by status + time range.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID, statuses []Status, since, until time.Time, limit, offset int) ([]Incident, error) {
	statusStrings := make([]string, 0, len(statuses))
	for _, s := range statuses {
		statusStrings = append(statusStrings, string(s))
	}
	rows, err := s.incidents.List(ctx, s.pool.Pgx(), persistence.ListFilter{
		TenantID: tenantID, Statuses: statusStrings, Since: since, Until: until,
		Limit: limit, Offset: offset,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Incident, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, nil
}

// enqueueEvent builds an envelope and inserts it into the outbox in the
// supplied transaction. Centralized so we don't repeat the "build + json
// marshal + insert" boilerplate in every emit site above.
func (s *Service) enqueueEvent(
	ctx context.Context, q persistence.Querier,
	eventType string,
	tenantID uuid.UUID, principal auth.Principal,
	inc Incident, correlationID uuid.UUID,
	payload map[string]any,
) error {
	env := events.NewEnvelope(
		eventType, tenantID,
		actorOf(principal),
		events.Entity{Type: "incident", ID: inc.ID, SchemaVersion: SchemaVersion},
		payload, correlationID,
	)
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	return s.outbox.Enqueue(ctx, q, persistence.OutboxRow{
		EventID:   env.EventID,
		EventType: env.EventType,
		TenantID:  tenantID,
		Payload:   body,
		CreatedAt: env.OccurredAt,
	})
}

// validateCreateInput enforces input rules that aren't expressible in
// the API layer's JSON decoding alone.
func validateCreateInput(in CreateInput) error {
	if in.Title == "" {
		return fmt.Errorf("%w: title is required", ErrInvalidInput)
	}
	if !in.Severity.IsValid() {
		return fmt.Errorf("%w: invalid severity %q", ErrInvalidInput, in.Severity)
	}
	return nil
}

// marshalAttributes renders the map as JSON bytes for storage. nil maps
// become "null" — we replace that with "{}" so the column is queryable
// as an object.
func marshalAttributes(attrs map[string]any) ([]byte, error) {
	if attrs == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(attrs)
}

// stringSliceEqual reports element-by-element equality. Used to skip
// no-op tag updates.
func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// actorOf converts an auth.Principal to the events.Actor shape — they
// carry the same information but live in separate packages so domain
// can compile without depending on the HTTP layer.
func actorOf(p auth.Principal) events.Actor {
	return events.Actor{Type: events.ActorType(p.Type), ID: p.ID}
}

// toRow converts the domain Incident + serialized attributes to the
// persistence row shape.
func toRow(inc Incident, attrBytes []byte) persistence.IncidentRow {
	return persistence.IncidentRow{
		ID: inc.ID, TenantID: inc.TenantID, SchemaVersion: inc.SchemaVersion,
		ExternalID: inc.ExternalID, Title: inc.Title, Description: inc.Description,
		Severity: string(inc.Severity), Status: string(inc.Status),
		AssigneeID: inc.AssigneeID, SourceConnectorID: inc.SourceConnectorID,
		Attributes: attrBytes, Tags: inc.Tags,
		CreatedAt: inc.CreatedAt, UpdatedAt: inc.UpdatedAt, ClosedAt: inc.ClosedAt,
	}
}

// fromRow converts the persistence row back to the domain shape.
func fromRow(r persistence.IncidentRow) Incident {
	var attrs map[string]any
	if len(r.Attributes) > 0 {
		// Best-effort decode; if the row contains malformed JSON,
		// default to an empty map. The schema validator should have
		// rejected this on insert/update, so we do not return an error.
		_ = json.Unmarshal(r.Attributes, &attrs)
	}
	return Incident{
		ID: r.ID, TenantID: r.TenantID, SchemaVersion: r.SchemaVersion,
		ExternalID: r.ExternalID, Title: r.Title, Description: r.Description,
		Severity: Severity(r.Severity), Status: Status(r.Status),
		AssigneeID: r.AssigneeID, SourceConnectorID: r.SourceConnectorID,
		Attributes: attrs, Tags: r.Tags,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt,
	}
}

// incidentJSON returns the snapshot used in event payloads. Putting this
// in one place keeps "what the wire sees" consistent across event types.
func incidentJSON(inc Incident) map[string]any {
	out := map[string]any{
		"id":                 inc.ID,
		"tenant_id":          inc.TenantID,
		"schema_version":     inc.SchemaVersion,
		"title":              inc.Title,
		"severity":           string(inc.Severity),
		"status":             string(inc.Status),
		"tags":               inc.Tags,
		"created_at":         inc.CreatedAt,
		"updated_at":         inc.UpdatedAt,
	}
	if inc.ExternalID != nil {
		out["external_id"] = *inc.ExternalID
	}
	if inc.Description != nil {
		out["description"] = *inc.Description
	}
	if inc.AssigneeID != nil {
		out["assignee_id"] = *inc.AssigneeID
	}
	if inc.SourceConnectorID != nil {
		out["source_connector_id"] = *inc.SourceConnectorID
	}
	if inc.Attributes != nil {
		out["attributes"] = inc.Attributes
	}
	if inc.ClosedAt != nil {
		out["closed_at"] = *inc.ClosedAt
	}
	return out
}
