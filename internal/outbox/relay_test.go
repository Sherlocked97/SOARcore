// Tests for the outbox relay. White-box: this file lives in the same
// package as relay.go so it can substitute the unexported interface
// types (querierSource, outboxRepo, publisher) with stubs. The relay's
// drainOnce method is the seam — its public Run wraps drainOnce in a
// ticker loop, which is not interesting to test in isolation.
//
//	go test -run TestDrainOnce -v ./internal/outbox
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Sherlocked97/soarcore/internal/events"
	"github.com/Sherlocked97/soarcore/internal/persistence"
)

// stubQuerierSource satisfies querierSource. The Querier itself is
// never called by the relay (the repo stub does the real work), so
// returning nil is fine.
type stubQuerierSource struct{}

func (stubQuerierSource) Pgx() persistence.Querier { return nil }

// stubRepo satisfies outboxRepo with in-memory state. Captures the IDs
// that get marked-published so tests can assert the relay's behavior.
type stubRepo struct {
	mu       sync.Mutex
	rows     []persistence.OutboxRow
	marked   []int64
	fetchErr error
	markErr  error
}

func (s *stubRepo) FetchUnpublished(ctx context.Context, _ persistence.Querier, limit int) ([]persistence.OutboxRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fetchErr != nil {
		return nil, s.fetchErr
	}
	// Return a copy so the test can mutate s.rows safely.
	out := make([]persistence.OutboxRow, len(s.rows))
	copy(out, s.rows)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *stubRepo) MarkPublished(ctx context.Context, _ persistence.Querier, id int64, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markErr != nil {
		return s.markErr
	}
	s.marked = append(s.marked, id)
	// Drop the marked row from rows so a second drain finds nothing.
	kept := s.rows[:0]
	for _, r := range s.rows {
		if r.ID != id {
			kept = append(kept, r)
		}
	}
	s.rows = kept
	return nil
}

// stubPublisher satisfies publisher. failOn is set to a specific
// event_id for tests that simulate a transient publish failure.
type stubPublisher struct {
	mu        sync.Mutex
	published []events.Envelope
	failOn    map[uuid.UUID]bool
	failErr   error
}

func (p *stubPublisher) Publish(_ context.Context, env events.Envelope) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failOn[env.EventID] {
		return p.failErr
	}
	p.published = append(p.published, env)
	return nil
}

// silentLogger discards all output so test runs don't drown in JSON.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// makeRow builds a fully-formed outbox row whose payload deserialises
// into a valid Envelope.
func makeRow(id int64, eventType string) persistence.OutboxRow {
	env := events.NewEnvelope(
		eventType,
		uuid.New(),
		events.Actor{Type: events.ActorUser, ID: "tester"},
		events.Entity{Type: "incident", ID: uuid.New(), SchemaVersion: 1},
		map[string]any{"hello": "world"},
		uuid.New(),
	)
	body, _ := json.Marshal(env)
	return persistence.OutboxRow{
		ID:        id,
		EventID:   env.EventID,
		EventType: env.EventType,
		TenantID:  env.TenantID,
		Payload:   body,
		CreatedAt: env.OccurredAt,
	}
}

// TestDrainOnce_HappyPath: every fetched row is published exactly once
// and marked published, leaving the repo empty after one drain.
func TestDrainOnce_HappyPath(t *testing.T) {
	repo := &stubRepo{rows: []persistence.OutboxRow{
		makeRow(1, events.EventTypeIncidentCreated),
		makeRow(2, events.EventTypeIncidentUpdated),
	}}
	pub := &stubPublisher{}
	r := newRelay(stubQuerierSource{}, repo, pub, silentLogger())

	if err := r.drainOnce(context.Background()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if got, want := len(pub.published), 2; got != want {
		t.Fatalf("publish count: got %d, want %d", got, want)
	}
	if got, want := len(repo.marked), 2; got != want {
		t.Fatalf("marked count: got %d, want %d", got, want)
	}
	if len(repo.rows) != 0 {
		t.Fatalf("expected repo drained, still has %d rows", len(repo.rows))
	}
}

// TestDrainOnce_PublishFailureLeavesRowUnmarked: when Publish returns
// an error the relay must NOT mark that row, and must not move on to
// later rows in the same batch (ordering matters within a tenant).
func TestDrainOnce_PublishFailureLeavesRowUnmarked(t *testing.T) {
	row1 := makeRow(1, events.EventTypeIncidentCreated)
	row2 := makeRow(2, events.EventTypeIncidentUpdated)
	repo := &stubRepo{rows: []persistence.OutboxRow{row1, row2}}
	pub := &stubPublisher{
		failOn:  map[uuid.UUID]bool{row1.EventID: true},
		failErr: errors.New("broker down"),
	}
	r := newRelay(stubQuerierSource{}, repo, pub, silentLogger())

	if err := r.drainOnce(context.Background()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if len(pub.published) != 0 {
		t.Fatalf("expected zero successful publishes, got %d", len(pub.published))
	}
	if len(repo.marked) != 0 {
		t.Fatalf("expected zero marks, got %d", len(repo.marked))
	}
	// Both rows still pending so the next tick can retry.
	if len(repo.rows) != 2 {
		t.Fatalf("expected both rows pending, got %d", len(repo.rows))
	}

	// On the next drain (after the broker recovers), both rows go
	// through and are marked. This exercises the at-least-once retry.
	pub.failOn = nil
	if err := r.drainOnce(context.Background()); err != nil {
		t.Fatalf("retry drain: %v", err)
	}
	if len(pub.published) != 2 {
		t.Fatalf("expected 2 publishes after recovery, got %d", len(pub.published))
	}
	if len(repo.marked) != 2 {
		t.Fatalf("expected 2 marks after recovery, got %d", len(repo.marked))
	}
}

// TestDrainOnce_NoRowsIsNoop: an empty fetch is not an error and does
// not reach the publisher.
func TestDrainOnce_NoRowsIsNoop(t *testing.T) {
	repo := &stubRepo{}
	pub := &stubPublisher{}
	r := newRelay(stubQuerierSource{}, repo, pub, silentLogger())
	if err := r.drainOnce(context.Background()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}
	if len(pub.published) != 0 || len(repo.marked) != 0 {
		t.Fatalf("publisher/repo should not be touched")
	}
}

// TestDrainOnce_FetchErrorPropagates: a DB error from FetchUnpublished
// surfaces as the function's return value (the Run loop catches and
// logs it, but drainOnce itself returns the error).
func TestDrainOnce_FetchErrorPropagates(t *testing.T) {
	repo := &stubRepo{fetchErr: errors.New("db down")}
	pub := &stubPublisher{}
	r := newRelay(stubQuerierSource{}, repo, pub, silentLogger())
	if err := r.drainOnce(context.Background()); err == nil {
		t.Fatalf("expected error, got nil")
	}
}
