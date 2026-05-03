// Command core is the SOARcore modular monolith binary. One process, all
// modules. The shape — and most of the comments — are written for a Go
// newcomer who wants to read this top-to-bottom and understand how a
// service starts up, wires its dependencies, and shuts down cleanly.
//
// Run order:
//  1. Read config from env vars.
//  2. Open structured logger (JSON, stdout).
//  3. Wait for Postgres to accept connections.
//  4. Run database migrations.
//  5. Wait for RabbitMQ.
//  6. Build the dependency graph (repos → services → server).
//  7. Start the outbox relay as a background goroutine.
//  8. Start the HTTP server.
//  9. On SIGINT/SIGTERM, gracefully drain in-flight requests and stop.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Sherlocked97/soarcore/internal/api"
	"github.com/Sherlocked97/soarcore/internal/audit"
	"github.com/Sherlocked97/soarcore/internal/auth"
	"github.com/Sherlocked97/soarcore/internal/config"
	"github.com/Sherlocked97/soarcore/internal/connectorruntime"
	"github.com/Sherlocked97/soarcore/internal/domain/incident"
	"github.com/Sherlocked97/soarcore/internal/events"
	"github.com/Sherlocked97/soarcore/internal/outbox"
	"github.com/Sherlocked97/soarcore/internal/persistence"
	"github.com/Sherlocked97/soarcore/internal/schemaregistry"
	"github.com/Sherlocked97/soarcore/migrations"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.LoadCore()
	if err != nil {
		logger.Error("config load", "err", err)
		os.Exit(1)
	}
	logger.Info("starting core",
		"http_addr", cfg.HTTPAddr,
		"default_tenant", cfg.DefaultTenantID,
	)

	// Top-level context: cancelled on SIGINT/SIGTERM. Every long-running
	// component (relay, HTTP server) accepts a context derived from this
	// one so they all stop together.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Wait for the database, then run migrations. Doing migrations
	// in-process keeps "docker compose up" → "service is ready" simple
	// for v1; production deployments will likely move migrations to a
	// separate one-shot job.
	pool, err := persistence.WaitForDB(ctx, cfg.PostgresDSN, cfg.StartupAttempts, cfg.StartupDelay, logger)
	if err != nil {
		logger.Error("postgres unreachable", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := persistence.RunMigrations(migrations.FS, ".", cfg.PostgresDSN, logger); err != nil {
		logger.Error("migrations failed", "err", err)
		os.Exit(1)
	}

	// Wait for the broker, then create a single Publisher used by the
	// outbox relay. Publisher is safe for concurrent use.
	publisher, err := events.WaitForBroker(cfg.AMQPURL, cfg.StartupAttempts, cfg.StartupDelay, logger)
	if err != nil {
		logger.Error("amqp unreachable", "err", err)
		os.Exit(1)
	}
	defer publisher.Close()

	// Build the dependency graph. Repos are stateless, so we make one
	// of each. Services accept the repos as constructor args — Go's
	// idiomatic dependency injection is "pass it in".
	schema, err := schemaregistry.New()
	if err != nil {
		logger.Error("schema registry", "err", err)
		os.Exit(1)
	}

	incidentRepo := persistence.NewIncidentRepo()
	auditRepo := persistence.NewAuditRepo()
	outboxRepo := persistence.NewOutboxRepo()
	connectorRepo := persistence.NewConnectorRepo()

	auditRecorder := audit.NewRecorder(auditRepo)

	// The v1 authorizer is the stub — it always allows. Replacing it is
	// a one-line change here, and downstream call sites already pass
	// principals so they don't need to change.
	authorizer := auth.StubAuthorizer{}

	incidentService := incident.NewService(pool, incidentRepo, outboxRepo, auditRecorder, schema, authorizer)
	connectorService := connectorruntime.NewService(connectorRepo, schema)

	// Start the outbox relay. `go` launches a goroutine — Go's
	// lightweight thread. The relay reads ctx.Done() to know when to
	// stop, so the deferred stop() above will eventually unwind it.
	relay := outbox.New(pool, outboxRepo, publisher, logger)
	go func() {
		if err := relay.Run(ctx); err != nil {
			logger.Warn("relay terminated", "err", err)
		}
	}()

	// Build the HTTP handler.
	server := api.NewServer(logger, pool, incidentService, connectorService, cfg.DefaultTenantID)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Run the HTTP server in a goroutine so main() can wait on the
	// signal context. ListenAndServe returns only on error or after
	// Shutdown is called.
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http listening", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	// Wait for either: (a) a shutdown signal, or (b) the server crashed.
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverErr:
		if err != nil {
			logger.Error("http server crashed", "err", err)
			os.Exit(1)
		}
	}

	// Give in-flight requests up to 10s to finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown error", "err", err)
	}
	logger.Info("core stopped")
}
