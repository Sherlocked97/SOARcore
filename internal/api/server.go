// Package api is the HTTP surface of the core service. It owns the
// router, the request-decoding helpers, the error-mapping table, and
// the per-resource handlers (incidents.go, connectors.go).
//
// A handler in this package does three things and only three things:
//
//  1. Parse the request (JSON body, path params, query strings, headers).
//  2. Call into the domain or runtime layer to do the work.
//  3. Format the response (JSON + status code + headers).
//
// Business rules live in the domain layer. Persistence rules live in
// the persistence layer. Anything that looks like business logic in this
// package is a bug.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/Sherlocked97/soarcore/internal/auth"
	"github.com/Sherlocked97/soarcore/internal/connectorruntime"
	"github.com/Sherlocked97/soarcore/internal/domain/incident"
	"github.com/Sherlocked97/soarcore/internal/persistence"
)

// Server holds the HTTP server's dependencies. It is built once at
// startup and the resulting *Server is passed to http.ListenAndServe.
type Server struct {
	logger          *slog.Logger
	pool            *persistence.Pool
	incidentService *incident.Service
	connectorSvc    *connectorruntime.Service
	defaultTenant   uuid.UUID
}

// NewServer wires up dependencies. Wiring is in cmd/core/main.go.
func NewServer(
	logger *slog.Logger,
	pool *persistence.Pool,
	incidentService *incident.Service,
	connectorSvc *connectorruntime.Service,
	defaultTenant uuid.UUID,
) *Server {
	return &Server{
		logger:          logger,
		pool:            pool,
		incidentService: incidentService,
		connectorSvc:    connectorSvc,
		defaultTenant:   defaultTenant,
	}
}

// Router returns the chi router configured with our middleware stack and
// route table. The returned http.Handler is what main() passes to
// http.ListenAndServe (or a *http.Server for graceful shutdown).
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	// chi's standard middleware stack: recover from panics, request ID,
	// real IP, and a 60s timeout to keep slow handlers from holding
	// goroutines. Order matters — Recoverer must be early so it sees
	// panics from later middleware.
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(60 * time.Second))
	r.Use(s.requestLogger)

	// /healthz is exempt from auth — used by docker-compose healthchecks.
	r.Get("/healthz", s.handleHealthz)

	// Everything under /v1 requires an X-Principal-Id header (per the
	// auth stub) and is scoped to a tenant.
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Middleware(s.defaultTenant))
		r.Route("/incidents", func(r chi.Router) {
			r.Post("/", s.handleCreateIncident)
			r.Get("/", s.handleListIncidents)
			r.Get("/{id}", s.handleGetIncident)
			r.Patch("/{id}", s.handlePatchIncident)
		})
		r.Route("/connectors", func(r chi.Router) {
			r.Post("/", s.handleRegisterConnector)
			r.Post("/{id}/heartbeat", s.handleConnectorHeartbeat)
		})
	})
	return r
}

// requestLogger logs every request with method, path, status, and
// duration — enough for ops without leaking bodies.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", chimw.GetReqID(r.Context()),
		)
	})
}

// handleHealthz reports liveness with a quick DB ping. Returns 200 if
// the pool can talk to Postgres, 503 otherwise.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var one int
	if err := s.pool.Pgx().QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// writeJSON serializes v as JSON and writes it with the given status. We
// use it everywhere instead of inline json.Marshal so the status/header
// dance is in one place.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// json.NewEncoder writes directly to the ResponseWriter — no
	// intermediate buffer, no second copy.
	_ = json.NewEncoder(w).Encode(v)
}

// errorBody is the canonical error response shape. Keeping it small —
// "error" only — means UI/connector authors can rely on a fixed key.
type errorBody struct {
	Error string `json:"error"`
}

// writeError maps a domain or persistence error to an HTTP response.
// Centralizing this avoids handlers each making their own (incompatible)
// status-code decisions.
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, incident.ErrInvalidInput):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
	case errors.Is(err, incident.ErrInvalidTransition):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: err.Error()})
	case errors.Is(err, persistence.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: err.Error()})
	case errors.Is(err, auth.ErrDenied):
		writeJSON(w, http.StatusForbidden, errorBody{Error: err.Error()})
	case errors.Is(err, connectorruntime.ErrUnknownEntityType),
		errors.Is(err, connectorruntime.ErrUnsupportedEventSchema):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}
