package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Sherlocked97/soarcore/internal/auth"
	"github.com/Sherlocked97/soarcore/internal/domain/incident"
)

// createIncidentRequest is the JSON body for POST /v1/incidents. We keep
// the struct here (in the api package) rather than reusing
// domain.CreateInput because the wire shape is the API's concern; the
// domain type might evolve independently.
type createIncidentRequest struct {
	ExternalID        *string                `json:"external_id"`
	Title             string                 `json:"title"`
	Description       *string                `json:"description"`
	Severity          incident.Severity      `json:"severity"`
	AssigneeID        *uuid.UUID             `json:"assignee_id"`
	SourceConnectorID *string                `json:"source_connector_id"`
	Attributes        map[string]any         `json:"attributes"`
	Tags              []string               `json:"tags"`
	CorrelationID     *uuid.UUID             `json:"correlation_id"`
}

// patchIncidentRequest is the JSON body for PATCH /v1/incidents/:id.
// Pointer fields are how we distinguish "absent" from "set". For the
// Attributes map we use a separate `attributesPresent` bool because the
// JSON decoder produces nil for both `"attributes": null` and missing.
//
// json.RawMessage lets us peek at the raw body for the presence trick.
type patchIncidentRequest struct {
	Title         *string                `json:"title"`
	Description   *string                `json:"description"`
	Severity      *incident.Severity     `json:"severity"`
	Status        *incident.Status       `json:"status"`
	AssigneeID    *uuid.UUID             `json:"assignee_id"`
	Attributes    json.RawMessage        `json:"attributes"`
	Tags          *[]string              `json:"tags"`
	CorrelationID *uuid.UUID             `json:"correlation_id"`
}

// incidentResponse is the JSON shape we return for incidents. Using a
// dedicated response struct (rather than reusing the domain Incident)
// gives us control over JSON keys and forward-compatible additions.
type incidentResponse struct {
	ID                uuid.UUID      `json:"id"`
	TenantID          uuid.UUID      `json:"tenant_id"`
	SchemaVersion     int            `json:"schema_version"`
	ExternalID        *string        `json:"external_id,omitempty"`
	Title             string         `json:"title"`
	Description       *string        `json:"description,omitempty"`
	Severity          string         `json:"severity"`
	Status            string         `json:"status"`
	AssigneeID        *uuid.UUID     `json:"assignee_id,omitempty"`
	SourceConnectorID *string        `json:"source_connector_id,omitempty"`
	Attributes        map[string]any `json:"attributes,omitempty"`
	Tags              []string       `json:"tags"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	ClosedAt          *time.Time     `json:"closed_at,omitempty"`
}

// toResponse maps the domain type to the wire type. Centralizing this
// keeps every handler returning the same shape.
func toResponse(i incident.Incident) incidentResponse {
	tags := i.Tags
	if tags == nil {
		// Avoid `null` on the wire; clients prefer `[]` for empty.
		tags = []string{}
	}
	return incidentResponse{
		ID: i.ID, TenantID: i.TenantID, SchemaVersion: i.SchemaVersion,
		ExternalID: i.ExternalID, Title: i.Title, Description: i.Description,
		Severity: string(i.Severity), Status: string(i.Status),
		AssigneeID: i.AssigneeID, SourceConnectorID: i.SourceConnectorID,
		Attributes: i.Attributes, Tags: tags,
		CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt, ClosedAt: i.ClosedAt,
	}
}

// principalAndTenant is a small helper that pulls both context values
// at once and returns 500 if either is missing — which would only
// happen if the auth middleware wasn't installed.
func (s *Server) principalAndTenant(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "principal missing"})
		return auth.Principal{}, uuid.Nil, false
	}
	t, ok := auth.TenantFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "tenant missing"})
		return auth.Principal{}, uuid.Nil, false
	}
	return p, t, true
}

// handleCreateIncident handles POST /v1/incidents.
func (s *Server) handleCreateIncident(w http.ResponseWriter, r *http.Request) {
	principal, tenant, ok := s.principalAndTenant(w, r)
	if !ok {
		return
	}

	var req createIncidentRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // strict — typo'd field names become 400, not silent drops.
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid JSON: " + err.Error()})
		return
	}

	in := incident.CreateInput{
		ExternalID: req.ExternalID, Title: req.Title, Description: req.Description,
		Severity: req.Severity, AssigneeID: req.AssigneeID,
		SourceConnectorID: req.SourceConnectorID,
		Attributes:        req.Attributes,
		Tags:              req.Tags,
	}
	corrID := uuid.Nil
	if req.CorrelationID != nil {
		corrID = *req.CorrelationID
	}
	created, err := s.incidentService.Create(r.Context(), principal, tenant, in, corrID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toResponse(*created))
}

// handleGetIncident handles GET /v1/incidents/:id.
func (s *Server) handleGetIncident(w http.ResponseWriter, r *http.Request) {
	_, tenant, ok := s.principalAndTenant(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid id"})
		return
	}
	got, err := s.incidentService.Get(r.Context(), tenant, id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(*got))
}

// handlePatchIncident handles PATCH /v1/incidents/:id.
func (s *Server) handlePatchIncident(w http.ResponseWriter, r *http.Request) {
	principal, tenant, ok := s.principalAndTenant(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid id"})
		return
	}

	var req patchIncidentRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid JSON: " + err.Error()})
		return
	}

	patch := incident.PatchInput{
		Title: req.Title, Description: req.Description,
		Severity: req.Severity, Status: req.Status,
		AssigneeID: req.AssigneeID,
	}
	if req.Tags != nil {
		patch.Tags = *req.Tags
	}
	if len(req.Attributes) > 0 && string(req.Attributes) != "null" {
		var m map[string]any
		if err := json.Unmarshal(req.Attributes, &m); err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid attributes: " + err.Error()})
			return
		}
		// nil-vs-empty: an explicit {} should be a non-nil empty map so
		// the domain treats it as "set to {}" rather than "no change".
		if m == nil {
			m = map[string]any{}
		}
		patch.Attributes = m
	}

	corrID := uuid.Nil
	if req.CorrelationID != nil {
		corrID = *req.CorrelationID
	}
	updated, err := s.incidentService.Patch(r.Context(), principal, tenant, id, patch, corrID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResponse(*updated))
}

// handleListIncidents handles GET /v1/incidents?status=...&since=...&until=...
func (s *Server) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	_, tenant, ok := s.principalAndTenant(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()

	var statuses []incident.Status
	if v := q.Get("status"); v != "" {
		// status= can be repeated or comma-separated. Split on commas
		// so curl --data-urlencode "status=new,triaged" works.
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				statuses = append(statuses, incident.Status(p))
			}
		}
	}
	parseTime := func(raw string) (time.Time, bool) {
		if raw == "" {
			return time.Time{}, true
		}
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	}
	since, ok := parseTime(q.Get("since"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid 'since'; want RFC3339"})
		return
	}
	until, ok := parseTime(q.Get("until"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid 'until'; want RFC3339"})
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	out, err := s.incidentService.List(r.Context(), tenant, statuses, since, until, limit, offset)
	if err != nil {
		writeError(w, err)
		return
	}
	resp := make([]incidentResponse, 0, len(out))
	for _, i := range out {
		resp = append(resp, toResponse(i))
	}
	writeJSON(w, http.StatusOK, resp)
}
