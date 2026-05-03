// Command reference-connector is the proof-of-life implementation of
// ADR-003: a separate process that talks to the core only over the
// public HTTP API and the message bus. No shared in-process imports
// beyond the wire envelope (internal/events/envelope.go).
//
// What it does
// ------------
//  1. Registers itself with the core (POST /v1/connectors), declaring
//     it consumes "incident" entities and speaks event-schema v1.
//  2. Subscribes to the "soar.events" exchange with routing pattern
//     "incident.*" on a durable queue named after its connector ID.
//  3. On every "incident.created" envelope it receives, it PATCHes the
//     incident to append the tag "enriched-by-reference-connector" —
//     using its connector principal so the audit + outbox attribute
//     the action correctly.
//  4. Sends a heartbeat to POST /v1/connectors/:id/heartbeat at a
//     configurable interval.
//
// Why this exists
// ---------------
// It is the thinnest possible thing that exercises the whole wire
// contract end to end. If a future connector author wants a starting
// point, this file is it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/Sherlocked97/soarcore/internal/config"
	"github.com/Sherlocked97/soarcore/internal/events"
)

// principalHeader is the X-Principal-Id this connector sends with every
// API call. Audit rows on patches will record this value, so the human
// reading the audit log knows the reference connector did the work.
const principalHeader = "connector:reference-connector"

// enrichTag is the tag this connector appends on incident.created.
const enrichTag = "enriched-by-reference-connector"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.LoadConnector()
	if err != nil {
		logger.Error("config load", "err", err)
		os.Exit(1)
	}
	logger.Info("starting reference-connector",
		"id", cfg.ConnectorID,
		"core", cfg.CoreAPIBase,
		"tenant", cfg.TenantID,
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := &coreClient{
		base:      cfg.CoreAPIBase,
		tenant:    cfg.TenantID.String(),
		principal: principalHeader,
		http:      &http.Client{Timeout: 10 * time.Second},
		logger:    logger,
	}

	// Wait for core /healthz before registering. The compose file gives
	// us a depends_on with a healthcheck, but staying defensive makes
	// the connector deployable outside compose too.
	if err := client.waitForCore(ctx, cfg.StartupAttempts, cfg.StartupDelay); err != nil {
		logger.Error("core unreachable", "err", err)
		os.Exit(1)
	}

	if err := client.register(ctx, cfg.ConnectorID); err != nil {
		logger.Error("register", "err", err)
		os.Exit(1)
	}
	logger.Info("registered with core")

	// Connect to the bus.
	sub, err := waitForSubscriber(cfg.AMQPURL, cfg.StartupAttempts, cfg.StartupDelay, logger)
	if err != nil {
		logger.Error("amqp unreachable", "err", err)
		os.Exit(1)
	}
	defer sub.Close()

	// Periodic heartbeat.
	go heartbeatLoop(ctx, client, cfg.ConnectorID, cfg.HeartbeatPeriod, logger)

	// Subscribe in the foreground; this returns when ctx is cancelled.
	queueName := "ref-connector." + cfg.ConnectorID
	logger.Info("subscribing", "queue", queueName, "pattern", "incident.*")
	err = sub.Subscribe(ctx, queueName, "incident.*", func(env events.Envelope) error {
		return handleEnvelope(ctx, client, logger, env)
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("subscribe terminated", "err", err)
		os.Exit(1)
	}
	logger.Info("reference-connector stopped")
}

// handleEnvelope is the per-message dispatcher. We only act on
// incident.created today; everything else is logged and ignored. A
// future iteration might enrich on incident.updated too, but the v1
// smoke test is satisfied by created alone.
func handleEnvelope(ctx context.Context, client *coreClient, logger *slog.Logger, env events.Envelope) error {
	logger.Info("event received",
		"event_id", env.EventID,
		"event_type", env.EventType,
		"entity_id", env.Entity.ID,
	)
	if env.EventType != events.EventTypeIncidentCreated {
		return nil
	}
	// Ignore events caused by ourselves to avoid an infinite loop.
	if env.Actor.Type == events.ActorConnector && env.Actor.ID == "reference-connector" {
		return nil
	}

	// Pull the prior tags out of the payload so we don't clobber them.
	prior := priorTags(env.Payload)
	next := append([]string{}, prior...)
	if !contains(next, enrichTag) {
		next = append(next, enrichTag)
	}

	if err := client.patchIncidentTags(ctx, env.Entity.ID.String(), next, env.CorrelationID); err != nil {
		logger.Warn("patch failed", "incident_id", env.Entity.ID, "err", err)
		return err
	}
	logger.Info("incident enriched", "incident_id", env.Entity.ID, "tags", next)
	return nil
}

// priorTags extracts incident.tags from the event payload. The payload
// is `any` because envelopes carry arbitrary JSON; we navigate the
// generic map shape that matches our domain's incidentJSON output.
func priorTags(payload any) []string {
	m, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	inc, ok := m["incident"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := inc["tags"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		if s, ok := t.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// heartbeatLoop ticks every cfg.HeartbeatPeriod and POSTs to
// /v1/connectors/:id/heartbeat. Failures are logged but do not exit —
// the connector keeps consuming events even if heartbeat updates
// briefly fail (e.g. core restarting).
func heartbeatLoop(ctx context.Context, client *coreClient, id string, period time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := client.heartbeat(ctx, id); err != nil {
				logger.Warn("heartbeat failed", "err", err)
			}
		}
	}
}

// waitForSubscriber retries the AMQP dial until the broker is up. It
// mirrors events.WaitForBroker but for the subscriber side, since the
// publisher helper isn't applicable here (we don't publish).
func waitForSubscriber(url string, attempts int, delay time.Duration, logger *slog.Logger) (*events.Subscriber, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		s, err := events.NewSubscriber(url, logger)
		if err == nil {
			return s, nil
		}
		lastErr = err
		logger.Info("waiting for AMQP broker", "attempt", i+1, "of", attempts, "err", err)
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("amqp broker unreachable after %d attempts: %w", attempts, lastErr)
}

// coreClient is a tiny HTTP client for the connector. We deliberately
// avoid any shared abstraction across connectors — every connector
// author is free to use whatever HTTP library they prefer; this one
// uses stdlib net/http.
type coreClient struct {
	base      string
	tenant    string
	principal string
	http      *http.Client
	logger    *slog.Logger
}

func (c *coreClient) waitForCore(ctx context.Context, attempts int, delay time.Duration) error {
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/healthz", nil)
		if err != nil {
			return err
		}
		resp, err := c.http.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		c.logger.Info("waiting for core /healthz", "attempt", i+1, "of", attempts)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return fmt.Errorf("core /healthz did not become ready")
}

func (c *coreClient) register(ctx context.Context, id string) error {
	body := map[string]any{
		"id": id,
		"capabilities": map[string]any{
			"produces":              []string{},
			"consumes":              []string{"incident"},
			"event_schema_versions": []int{1},
		},
	}
	return c.do(ctx, http.MethodPost, "/v1/connectors", body, nil, http.StatusCreated)
}

func (c *coreClient) heartbeat(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/connectors/"+id+"/heartbeat", nil, nil, http.StatusOK)
}

func (c *coreClient) patchIncidentTags(ctx context.Context, id string, tags []string, correlationID uuid.UUID) error {
	body := map[string]any{
		"tags": tags,
	}
	// Forward the correlation_id so the resulting incident.updated event
	// stays linked to the original incident.created.
	if correlationID != uuid.Nil {
		body["correlation_id"] = correlationID.String()
	}
	return c.do(ctx, http.MethodPatch, "/v1/incidents/"+id, body, nil, http.StatusOK)
}

// do is the request helper. It sets the auth-stub headers, marshals the
// body, executes the call, and asserts the expected status code.
func (c *coreClient) do(ctx context.Context, method, path string, body any, out any, wantStatus int) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal body: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Principal-Id", c.principal)
	req.Header.Set("X-Tenant-Id", c.tenant)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

