// Command smoke-consumer is a tiny one-shot AMQP consumer used by
// scripts/smoke.sh to assert that an envelope of an expected event_type
// (and optionally for a specific entity_id) reaches the bus.
//
// Why this exists
// ---------------
// The smoke script needs to verify "the core published the event we
// expected". curl can't talk AMQP. We could shell out to an external
// helper (amqp-tools), but that adds a non-Go dependency for a
// one-screen task. Embedding a tiny consumer keeps the smoke harness
// self-contained.
//
// Usage
// -----
//
//	smoke-consumer \
//	    --url=amqp://soar:soar@localhost:5672/ \
//	    --pattern=incident.* \
//	    --event-type=incident.created \
//	    [--entity-id=<uuid>] \
//	    [--timeout=10s]
//
// Exits 0 if a matching envelope is observed within --timeout, prints
// the envelope as JSON on stdout. Exits 1 on timeout or AMQP error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/Sherlocked97/soarcore/internal/events"
)

func main() {
	url := flag.String("url", "amqp://soar:soar@localhost:5672/", "AMQP broker URL")
	pattern := flag.String("pattern", "incident.*", "routing pattern to subscribe to")
	wantType := flag.String("event-type", "", "event_type to wait for (required)")
	entityID := flag.String("entity-id", "", "if set, also require this entity_id (UUID)")
	queueSuffix := flag.String("queue", "smoke", "queue name suffix; queue is named smoke-consumer.<suffix>")
	timeout := flag.Duration("timeout", 10*time.Second, "max time to wait for a matching envelope")
	flag.Parse()

	if *wantType == "" {
		fmt.Fprintln(os.Stderr, "smoke-consumer: --event-type is required")
		os.Exit(2)
	}

	var wantEntity uuid.UUID
	if *entityID != "" {
		parsed, err := uuid.Parse(*entityID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "smoke-consumer: invalid --entity-id: %v\n", err)
			os.Exit(2)
		}
		wantEntity = parsed
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sub, err := events.NewSubscriber(*url, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "smoke-consumer: connect: %v\n", err)
		os.Exit(1)
	}
	defer sub.Close()

	// Use a short, ephemeral-ish queue name so re-running the smoke
	// suite doesn't pile up bindings.
	queueName := "smoke-consumer." + *queueSuffix + "." + time.Now().UTC().Format("20060102T150405.000")

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// matchedC is a channel because the Subscribe handler runs on the
	// AMQP goroutine and we need to surface the matched envelope back
	// to main. Buffered size 1 so the handler doesn't block if main is
	// already cancelling the ctx after a match.
	matchedC := make(chan events.Envelope, 1)

	go func() {
		err := sub.SubscribeEphemeral(ctx, queueName, *pattern, func(env events.Envelope) error {
			if env.EventType != *wantType {
				return nil
			}
			if wantEntity != uuid.Nil && env.Entity.ID != wantEntity {
				return nil
			}
			select {
			case matchedC <- env:
			default:
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(os.Stderr, "smoke-consumer: subscribe: %v\n", err)
		}
	}()

	select {
	case env := <-matchedC:
		out, err := json.Marshal(env)
		if err != nil {
			fmt.Fprintf(os.Stderr, "smoke-consumer: marshal: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(out))
		os.Exit(0)
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "smoke-consumer: timeout waiting for %s (entity %v)\n", *wantType, wantEntity)
		os.Exit(1)
	}
}
