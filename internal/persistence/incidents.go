package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// IncidentRow mirrors a row in the incident table. It is a transport type
// — the domain layer maps it to its richer Incident type. We keep this
// type unexported in spirit (the domain re-exports the shape it cares
// about) but exported in code so internal/domain can read it.
type IncidentRow struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	SchemaVersion     int
	ExternalID        *string // nullable
	Title             string
	Description       *string // nullable
	Severity          string  // enum value as text
	Status            string  // enum value as text
	AssigneeID        *uuid.UUID
	SourceConnectorID *string
	Attributes        []byte // raw JSONB
	Tags              []string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ClosedAt          *time.Time
}

// ErrNotFound is returned when a row lookup finds zero rows. The domain
// layer maps this to a 404 in the API.
var ErrNotFound = errors.New("not found")

// IncidentRepo is the SQL gateway for the incident table. Methods accept
// a Querier so they work both inside a transaction (Querier == pgx.Tx)
// and outside one (Querier == the pool).
type IncidentRepo struct{}

// NewIncidentRepo returns a stateless repo. It's a struct rather than
// free functions so the domain can mock it in tests by satisfying an
// interface in the domain package.
func NewIncidentRepo() *IncidentRepo { return &IncidentRepo{} }

// Insert writes a new incident row. Caller supplies all fields (the
// domain layer is responsible for assigning IDs, timestamps, and
// stamping schema_version).
func (r *IncidentRepo) Insert(ctx context.Context, q Querier, row IncidentRow) error {
	const sql = `
		INSERT INTO incident (
			id, tenant_id, schema_version, external_id, title, description,
			severity, status, assignee_id, source_connector_id, attributes,
			tags, created_at, updated_at, closed_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7::incident_severity, $8::incident_status, $9, $10, $11,
			$12, $13, $14, $15
		)
	`
	_, err := q.Exec(ctx, sql,
		row.ID, row.TenantID, row.SchemaVersion, row.ExternalID, row.Title, row.Description,
		row.Severity, row.Status, row.AssigneeID, row.SourceConnectorID, row.Attributes,
		row.Tags, row.CreatedAt, row.UpdatedAt, row.ClosedAt,
	)
	if err != nil {
		return fmt.Errorf("insert incident: %w", err)
	}
	return nil
}

// GetByID returns a single incident scoped to a tenant. Tenant scoping at
// the SQL level means an attacker who guesses an ID from another tenant
// still gets ErrNotFound.
func (r *IncidentRepo) GetByID(ctx context.Context, q Querier, tenantID, id uuid.UUID) (IncidentRow, error) {
	const sql = `
		SELECT id, tenant_id, schema_version, external_id, title, description,
		       severity::text, status::text, assignee_id, source_connector_id,
		       attributes, tags, created_at, updated_at, closed_at
		FROM incident
		WHERE tenant_id = $1 AND id = $2
	`
	var row IncidentRow
	err := q.QueryRow(ctx, sql, tenantID, id).Scan(
		&row.ID, &row.TenantID, &row.SchemaVersion, &row.ExternalID, &row.Title, &row.Description,
		&row.Severity, &row.Status, &row.AssigneeID, &row.SourceConnectorID,
		&row.Attributes, &row.Tags, &row.CreatedAt, &row.UpdatedAt, &row.ClosedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IncidentRow{}, ErrNotFound
		}
		return IncidentRow{}, fmt.Errorf("get incident: %w", err)
	}
	return row, nil
}

// Update applies a partial update. Fields that are nil pointers are left
// alone; non-nil pointers are written. Tags and attributes are treated
// specially because their zero values are valid (empty array, empty obj).
//
// We pass a struct of pointers so the caller can express "don't touch"
// vs "set to null" — a common Go pattern for partial updates.
type IncidentUpdate struct {
	Title       *string
	Description *string  // *string, double-deref needed if we ever want SET NULL
	Severity    *string
	Status      *string
	AssigneeID  *uuid.UUID
	Attributes  []byte // nil = no change; explicit empty []byte("{}") = clear to {}
	Tags        []string // nil = no change; explicit []string{} = clear
	ClosedAt    *time.Time
	UpdatedAt   time.Time // always set
}

// Update writes the partial update. It uses COALESCE-based SQL so we can
// keep the statement static and let Postgres pick "old vs new" per
// column. Nil pointers translate to SQL NULL, which COALESCE replaces
// with the existing column value.
func (r *IncidentRepo) Update(ctx context.Context, q Querier, tenantID, id uuid.UUID, u IncidentUpdate) error {
	// Tags and Attributes need explicit "untouched vs cleared" semantics.
	// We model "leave unchanged" as nil and "set to empty" as a non-nil
	// empty value; the SQL distinguishes via the boolean flags below.
	tagsTouched := u.Tags != nil
	attrsTouched := u.Attributes != nil

	const sql = `
		UPDATE incident SET
			title = COALESCE($3, title),
			description = COALESCE($4, description),
			severity = COALESCE($5::incident_severity, severity),
			status = COALESCE($6::incident_status, status),
			assignee_id = COALESCE($7, assignee_id),
			tags = CASE WHEN $8::bool THEN $9 ELSE tags END,
			attributes = CASE WHEN $10::bool THEN $11 ELSE attributes END,
			closed_at = COALESCE($12, closed_at),
			updated_at = $13
		WHERE tenant_id = $1 AND id = $2
	`
	tag, err := q.Exec(ctx, sql,
		tenantID, id,
		u.Title, u.Description, u.Severity, u.Status, u.AssigneeID,
		tagsTouched, u.Tags,
		attrsTouched, u.Attributes,
		u.ClosedAt, u.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("update incident: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFilter expresses the supported list query: tenant + status range +
// time range. v1 supports exactly this one query (per the build plan).
type ListFilter struct {
	TenantID uuid.UUID
	Statuses []string  // empty = any
	Since    time.Time // zero = no lower bound
	Until    time.Time // zero = no upper bound
	Limit    int       // 0 = default
	Offset   int
}

// List returns incidents matching the filter, ordered by created_at desc.
func (r *IncidentRepo) List(ctx context.Context, q Querier, f ListFilter) ([]IncidentRow, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}

	// We build the WHERE clause dynamically because the optional bounds
	// would otherwise require COALESCE games. This is the simplest form
	// that still uses prepared parameters everywhere — no string
	// concatenation of user input.
	args := []any{f.TenantID}
	where := "tenant_id = $1"
	if len(f.Statuses) > 0 {
		args = append(args, f.Statuses)
		where += fmt.Sprintf(" AND status::text = ANY($%d)", len(args))
	}
	if !f.Since.IsZero() {
		args = append(args, f.Since)
		where += fmt.Sprintf(" AND created_at >= $%d", len(args))
	}
	if !f.Until.IsZero() {
		args = append(args, f.Until)
		where += fmt.Sprintf(" AND created_at < $%d", len(args))
	}
	args = append(args, f.Limit, f.Offset)
	limitArg := len(args) - 1
	offsetArg := len(args)

	sql := fmt.Sprintf(`
		SELECT id, tenant_id, schema_version, external_id, title, description,
		       severity::text, status::text, assignee_id, source_connector_id,
		       attributes, tags, created_at, updated_at, closed_at
		FROM incident
		WHERE %s
		ORDER BY created_at DESC
		LIMIT $%d OFFSET $%d
	`, where, limitArg, offsetArg)

	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close() // ensure the Rows handle is released even on errors below

	var out []IncidentRow
	for rows.Next() {
		var row IncidentRow
		err := rows.Scan(
			&row.ID, &row.TenantID, &row.SchemaVersion, &row.ExternalID, &row.Title, &row.Description,
			&row.Severity, &row.Status, &row.AssigneeID, &row.SourceConnectorID,
			&row.Attributes, &row.Tags, &row.CreatedAt, &row.UpdatedAt, &row.ClosedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return out, nil
}
