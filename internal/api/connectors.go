package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Sherlocked97/soarcore/internal/connectorruntime"
)

// registerConnectorRequest is the JSON body for POST /v1/connectors.
// The id is supplied by the connector itself so that re-registrations
// (e.g. after a restart) reuse the same record.
type registerConnectorRequest struct {
	ID           string                       `json:"id"`
	Capabilities connectorruntime.Capabilities `json:"capabilities"`
}

// connectorResponse is what we return on register / heartbeat.
type connectorResponse struct {
	ID                string                        `json:"id"`
	TenantID          string                        `json:"tenant_id"`
	Capabilities      connectorruntime.Capabilities `json:"capabilities"`
	Status            string                        `json:"status"`
	LastHeartbeatAt   *time.Time                    `json:"last_heartbeat_at,omitempty"`
	RegisteredAt      time.Time                     `json:"registered_at"`
}

// handleRegisterConnector handles POST /v1/connectors.
func (s *Server) handleRegisterConnector(w http.ResponseWriter, r *http.Request) {
	_, tenant, ok := s.principalAndTenant(w, r)
	if !ok {
		return
	}
	var req registerConnectorRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid JSON: " + err.Error()})
		return
	}
	reg, err := s.connectorSvc.Register(r.Context(), s.pool.Pgx(), req.ID, tenant, req.Capabilities)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, connectorResponse{
		ID: reg.ID, TenantID: reg.TenantID.String(),
		Capabilities: reg.Capabilities,
		Status:       reg.Status, RegisteredAt: reg.RegisteredAt,
	})
}

// handleConnectorHeartbeat handles POST /v1/connectors/:id/heartbeat.
func (s *Server) handleConnectorHeartbeat(w http.ResponseWriter, r *http.Request) {
	_, _, ok := s.principalAndTenant(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.connectorSvc.Heartbeat(r.Context(), s.pool.Pgx(), id); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
