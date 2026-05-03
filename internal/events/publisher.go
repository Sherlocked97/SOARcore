package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ExchangeName is the single topic exchange we publish all envelopes to.
// Consumers create their own queues bound to this exchange with a routing
// pattern (e.g. "incident.*") that matches the event types they care about.
const ExchangeName = "soar.events"

// Publisher publishes envelopes to RabbitMQ. It owns a long-lived
// connection and a channel; callers should reuse a single Publisher for
// the lifetime of the process.
//
// The zero value is *not* usable — call NewPublisher to construct one.
type Publisher struct {
	url    string
	logger *slog.Logger

	// mu guards the conn/channel fields so reconnects don't race with
	// publishes. sync.Mutex is the simplest concurrency primitive and is
	// sufficient here because publishing is not on a hot path.
	mu      sync.Mutex
	conn    *amqp.Connection
	channel *amqp.Channel
}

// NewPublisher creates a Publisher and immediately attempts to connect.
// If the initial connection fails, NewPublisher returns the error so the
// caller can decide whether to retry or abort startup.
func NewPublisher(url string, logger *slog.Logger) (*Publisher, error) {
	p := &Publisher{url: url, logger: logger}
	if err := p.connect(); err != nil {
		return nil, err
	}
	return p, nil
}

// connect (re)establishes the AMQP connection and channel and declares the
// shared exchange. It is idempotent — declaring an exchange that already
// exists with the same parameters is a no-op.
func (p *Publisher) connect() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	conn, err := amqp.Dial(p.url)
	if err != nil {
		return fmt.Errorf("amqp dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		// Best-effort cleanup of the connection if channel-open fails.
		_ = conn.Close()
		return fmt.Errorf("amqp channel: %w", err)
	}
	// Declare the exchange we publish to. "topic" routing matches event
	// types like "incident.created" against patterns like "incident.*".
	// The exchange is durable so it survives broker restarts.
	err = ch.ExchangeDeclare(
		ExchangeName, // name
		"topic",      // kind
		true,         // durable
		false,        // auto-delete
		false,        // internal
		false,        // no-wait
		nil,          // arguments
	)
	if err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return fmt.Errorf("declare exchange: %w", err)
	}
	p.conn = conn
	p.channel = ch
	return nil
}

// Publish sends a single envelope. The routing key is the event_type, so
// consumers can subscribe to "incident.*" or specific types like
// "incident.status_changed".
//
// Publish is safe to call from many goroutines concurrently — the mutex
// serializes access to the underlying channel. If the channel is closed
// (broker restart, network blip), Publish attempts a single reconnect
// before failing. The outbox relay treats a publish failure as "leave the
// row unpublished, retry later", so transient errors are absorbed.
func (p *Publisher) Publish(ctx context.Context, env Envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		// A marshal failure is a programmer error (bad payload type),
		// not a transient network problem — return immediately.
		return fmt.Errorf("marshal envelope: %w", err)
	}

	publish := func() error {
		p.mu.Lock()
		ch := p.channel
		p.mu.Unlock()
		if ch == nil {
			return fmt.Errorf("amqp channel is nil")
		}
		// PublishWithContext respects ctx for cancellation/deadlines —
		// important so a stuck broker can't hang the relay forever.
		return ch.PublishWithContext(ctx,
			ExchangeName,   // exchange
			env.EventType,  // routing key
			false,          // mandatory
			false,          // immediate
			amqp.Publishing{
				ContentType:  "application/json",
				DeliveryMode: amqp.Persistent, // survive broker restart
				MessageId:    env.EventID.String(),
				Timestamp:    env.OccurredAt,
				Body:         body,
			},
		)
	}

	if err := publish(); err != nil {
		// One reconnect attempt. If that fails too, surface the error.
		p.logger.Warn("publish failed, attempting reconnect", "err", err)
		if rerr := p.connect(); rerr != nil {
			return fmt.Errorf("reconnect after publish failure: %w", rerr)
		}
		if err := publish(); err != nil {
			return fmt.Errorf("publish after reconnect: %w", err)
		}
	}
	return nil
}

// Close releases the underlying AMQP resources. It is safe to call
// multiple times; subsequent calls are no-ops.
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.channel != nil {
		_ = p.channel.Close()
		p.channel = nil
	}
	if p.conn != nil {
		_ = p.conn.Close()
		p.conn = nil
	}
	return nil
}

// WaitForBroker is a small startup helper used by main(). It retries the
// initial connection up to `attempts` times with a fixed delay, so that
// docker-compose race conditions (core service starts before RabbitMQ is
// fully up) don't crash-loop the binary.
func WaitForBroker(url string, attempts int, delay time.Duration, logger *slog.Logger) (*Publisher, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		p, err := NewPublisher(url, logger)
		if err == nil {
			return p, nil
		}
		lastErr = err
		logger.Info("waiting for AMQP broker", "attempt", i+1, "of", attempts, "err", err)
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("amqp broker unreachable after %d attempts: %w", attempts, lastErr)
}
