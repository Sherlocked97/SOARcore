package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Subscriber consumes envelopes from the bus. It is used by connectors
// (out-of-process) and by the smoke-consumer command. The core service
// itself does not subscribe — it only publishes.
//
// Subscriber owns its connection and channel and is not safe for
// concurrent use; create one per consumer.
type Subscriber struct {
	url     string
	logger  *slog.Logger
	conn    *amqp.Connection
	channel *amqp.Channel
}

// NewSubscriber dials the broker and prepares for consumption. The shared
// exchange is declared (idempotent) so that a subscriber can come up
// before the publisher does without an error.
func NewSubscriber(url string, logger *slog.Logger) (*Subscriber, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("amqp dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("amqp channel: %w", err)
	}
	err = ch.ExchangeDeclare(ExchangeName, "topic", true, false, false, false, nil)
	if err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("declare exchange: %w", err)
	}
	return &Subscriber{url: url, logger: logger, conn: conn, channel: ch}, nil
}

// Subscribe binds a durable queue named `queueName` to the exchange with
// the given routing pattern (e.g. "incident.*") and starts delivering
// envelopes via the returned channel. Closing the returned channel
// signals that the subscription has ended (broker disconnected, ctx
// cancelled, etc.).
//
// Each delivered envelope is automatically Ack'd before being passed to
// the handler, which keeps v1 simple. A future iteration may move to
// manual Ack so handler failures can leave messages on the queue for
// redelivery.
func (s *Subscriber) Subscribe(
	ctx context.Context,
	queueName string,
	routingPattern string,
	handler func(Envelope) error,
) error {
	return s.subscribe(ctx, queueName, routingPattern, true /*durable*/, false /*autoDelete*/, handler)
}

// SubscribeEphemeral is like Subscribe but the queue is non-durable and
// auto-deletes once no consumers are attached. Tests and one-shot
// inspectors (e.g. cmd/smoke-consumer) use this so they don't leave
// orphan queues on the broker. Production consumers should use
// Subscribe so messages survive a broker or consumer restart.
func (s *Subscriber) SubscribeEphemeral(
	ctx context.Context,
	queueName string,
	routingPattern string,
	handler func(Envelope) error,
) error {
	return s.subscribe(ctx, queueName, routingPattern, false /*durable*/, true /*autoDelete*/, handler)
}

func (s *Subscriber) subscribe(
	ctx context.Context,
	queueName string,
	routingPattern string,
	durable bool,
	autoDelete bool,
	handler func(Envelope) error,
) error {
	q, err := s.channel.QueueDeclare(
		queueName,
		durable,
		autoDelete,
		false, // exclusive — kept off so a debugger can peek at the queue
		false, // no-wait
		nil,   // arguments
	)
	if err != nil {
		return fmt.Errorf("declare queue: %w", err)
	}
	// Bind the queue to the exchange with the routing pattern.
	if err := s.channel.QueueBind(q.Name, routingPattern, ExchangeName, false, nil); err != nil {
		return fmt.Errorf("bind queue: %w", err)
	}

	// Start consuming. autoAck=true means the broker considers a message
	// delivered as soon as it leaves the queue — fine for v1 because the
	// handler doesn't need redelivery semantics yet.
	msgs, err := s.channel.Consume(q.Name, "", true, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume: %w", err)
	}

	// Read from the deliveries channel until ctx is cancelled or the
	// channel is closed by the broker. select{} is the idiomatic Go way
	// to wait on multiple channels simultaneously.
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-msgs:
			if !ok {
				// The deliveries channel is closed — the broker
				// has cancelled our consumer or the connection died.
				return fmt.Errorf("amqp deliveries channel closed")
			}
			var env Envelope
			if err := json.Unmarshal(d.Body, &env); err != nil {
				s.logger.Warn("received malformed envelope, dropping", "err", err)
				continue
			}
			if err := handler(env); err != nil {
				// Log and continue. v1 does not redeliver; the
				// at-least-once contract is between the *outbox*
				// and the bus, not between the bus and consumers.
				s.logger.Warn("handler returned error", "event_id", env.EventID, "err", err)
			}
		}
	}
}

// Close releases the underlying AMQP resources.
func (s *Subscriber) Close() error {
	if s.channel != nil {
		_ = s.channel.Close()
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	return nil
}
