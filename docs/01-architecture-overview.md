# 01 — Architecture Overview

SOARcore is a **modular monolith** written in Go. One Linux process
(`cmd/core`) hosts every module today; out-of-process **connectors**
talk to it over HTTP and a message bus.

```
                         ┌─────────────────────────┐
   curl / connectors →   │  cmd/core (HTTP :8080)   │
                         │ ┌──────────────────────┐ │
                         │ │ api  →  domain  →     │ │
                         │ │            persistence│ │  ←  PostgreSQL
                         │ │       ↘  audit         │ │
                         │ │       ↘  outbox table  │ │
                         │ └────────┬─────────────┘ │
                         │   relay (goroutine)      │
                         └────────┬─────────────────┘
                                  │ AMQP publish
                                  ▼
                              RabbitMQ  ←─ subscribed by → cmd/reference-connector
```

## Module map (what's in `internal/`)

| Module                | Job                                                                  |
|-----------------------|----------------------------------------------------------------------|
| `schemaregistry`      | JSON Schema validators per entity + version. Embeds `incident_v1.json`. |
| `persistence`         | The **only** place that imports `pgx` or writes SQL. Repos + pool.   |
| `domain/incident`     | The Incident entity: typed enums, status state machine, service.     |
| `events`              | The wire envelope + the AMQP publisher / subscriber.                  |
| `outbox`              | The background relay that drains the outbox table to the bus.         |
| `api`                 | HTTP router (chi), handlers, error mapping.                           |
| `auth`                | Middleware that extracts Principal/Tenant; pluggable Authorizer.      |
| `audit`               | The single place audit rows are written.                              |
| `connectorruntime`    | In-core surface for connector identity & capabilities.                |
| `config`              | 12-factor env-var loader. No flags, no config files.                  |

## The four hard rules

1. **SQL only in `internal/persistence`.** If you `grep` for `pgx.` in
   any other package, that's a bug.
2. **AMQP only in `internal/events`.** Same rule, different driver.
3. **Every state change is one transaction:** entity row + audit row +
   outbox row, atomically. The relay then publishes the outbox row at
   least once. Consumers must dedupe on `event_id`.
4. **Connectors are out-of-process.** They use the public HTTP API and
   the bus. They share **only** `internal/events/envelope.go` (the wire
   shape). No other in-process imports.

## How a write flows

1. Client `POST /v1/incidents` (with `X-Principal-Id` & `X-Tenant-Id`).
2. `internal/auth.Middleware` extracts the principal and tenant.
3. `internal/api.handleCreateIncident` decodes JSON, calls the domain
   service.
4. `incident.Service.Create` (in `internal/domain/incident`)
   - calls `Authorizer.Allow` (stub today — always nil)
   - validates input + the JSONB attributes against the schema
     registry
   - opens a transaction via `persistence.Pool.WithTx`
   - inserts the incident row, the audit row, and the outbox row
   - commits.
5. The outbox **relay** (a goroutine started in `main`) wakes every
   250ms, finds the new outbox row, publishes it to RabbitMQ on the
   `soar.events` exchange with routing key `incident.created`, then
   marks the row published.
6. Subscribers (e.g. `cmd/reference-connector`) receive the envelope
   and react.

## Where to read next

- **02-running-locally.md** if you want a working stack right now.
- **04-codebase-tour.md** if you want a file-level walk-through.
- `context/decisions.md` for the *why* behind these choices (ADRs).
