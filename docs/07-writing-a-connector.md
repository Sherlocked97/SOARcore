# 07 — Writing a Connector

A connector is **any process** that talks to SOARcore over the public
HTTP API and the message bus. Read this alongside
`cmd/reference-connector/main.go`; that file is the smallest possible
working example.

The contract has three pieces:

1. **HTTP API** for register / heartbeat / read / write.
2. **Bus subscription** for state-change events.
3. **The wire envelope** (`internal/events/envelope.go`) that travels
   on the bus.

Per ADR-003 there is **no connector SDK**. You can write a connector
in any language. The reference connector is in Go because it lives in
this repo, but its outline is identical in Python, Node, or Rust.

## Registration

```
POST /v1/connectors
Content-Type: application/json
X-Principal-Id: connector:my-connector
X-Tenant-Id:    <tenant uuid>

{
  "id": "my-connector",
  "capabilities": {
    "produces":              ["incident"],
    "consumes":              ["incident"],
    "event_schema_versions": [1]
  }
}
```

The core validates that every entity type is known to its schema
registry and that every event-schema version is one it speaks.
Re-registering with the same id is idempotent — a restarting connector
just calls register again.

## Heartbeat

```
POST /v1/connectors/{id}/heartbeat
X-Principal-Id: connector:my-connector
```

Send this every 15-30 seconds. The core flips the connector to `stale`
if it stops hearing from you for too long (timeout policy is a future
ADR — for v1 there's a column but no enforcement loop yet).

## Subscribing to events

Bus: RabbitMQ topic exchange `soar.events`. Routing keys are the event
types (`incident.created`, `incident.updated`, `incident.status_changed`,
`incident.closed`).

Bind a **durable queue** named after your connector id, with the
routing pattern that matches the events you care about:

```
queue   = my-connector
exchange = soar.events
binding = incident.*
```

The reference connector uses the AMQP 0-9-1 client; any AMQP 0-9-1
client works.

## Reading the envelope

```jsonc
{
  "event_id": "uuid",
  "event_type": "incident.created",
  "event_schema_version": 1,
  "occurred_at": "2026-...Z",
  "tenant_id": "uuid",
  "actor":  { "type": "user|connector|system", "id": "..." },
  "entity": { "type": "incident", "id": "uuid", "schema_version": 1 },
  "payload": { ... event-type specific shape ... },
  "correlation_id": "uuid",
  "prior_event_id": "uuid|null"
}
```

Two rules:

1. **Idempotent on `event_id`.** The relay is at-least-once;
   duplicates are possible. Track which `event_id`s you've handled
   (a small KV or DB table) and skip repeats.
2. **Forward `correlation_id`** on any API call you make in response.
   That's how the audit log + downstream events stay tied to the
   original cause.

## Writing back

To enrich an incident on `incident.created`:

```
PATCH /v1/incidents/{id}
Content-Type: application/json
X-Principal-Id: connector:my-connector
X-Tenant-Id:    <tenant>

{ "tags": [...prior, "my-tag"], "correlation_id": "<from envelope>" }
```

The PATCH semantics: any field you supply is replaced; any field you
omit is left alone. Tags being non-nil and empty (`"tags": []`) clears
the list; `tags` absent leaves the list untouched.

## Authentication & authorization (today)

V1 ships with a stub — every request is allowed as long as
`X-Principal-Id` is supplied in the form `type:id`. The shape is real
and won't change when we add real auth; you'll just additionally get a
token to send.

## A mental model for connector authors

1. Subscribe → wait for events.
2. For each event you care about: do work outside (TI lookup, ticket
   open, etc.), then PATCH or POST back to express the result.
3. If you generate net-new entities (e.g. ingest from a SIEM into
   `incident`), your connector POSTs to `/v1/incidents` directly.
4. Heartbeat in a parallel goroutine/thread.
5. Exit on SIGINT cleanly; on restart, register again.

The reference connector is ~250 lines including comments. Yours can
be similarly small.
