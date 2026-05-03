# 04 — Codebase Tour (file-by-file, in build order)

Read this with the source open. Each section names the file, says
what it does, and points to the most interesting bits.

## Project layout

```
SOARcore/
├── cmd/                  runnable programs
│   ├── core/             the modular monolith
│   ├── reference-connector/  proves the wire contract
│   └── smoke-consumer/   tiny AMQP consumer used by smoke.sh
├── internal/             package private to this module
│   ├── api/              HTTP layer (chi router, handlers, errors)
│   ├── audit/            single source for writing audit rows
│   ├── auth/             middleware + Authorizer interface (stub for v1)
│   ├── config/           env-var loader
│   ├── connectorruntime/ in-core connector identity & lifecycle
│   ├── domain/incident/  the Incident entity + state machine + service
│   ├── events/           wire envelope + AMQP publisher/subscriber
│   ├── outbox/           background relay (outbox table → bus)
│   ├── persistence/      pgx pool, repos, migration runner
│   └── schemaregistry/   JSON Schema validators per entity+version
├── migrations/           SQL files + embed.go
├── deploy/               Dockerfiles + docker-compose.yml
├── scripts/smoke.sh      end-to-end verification
├── docs/                 you are here
└── context/              decisions.md, data_model.md, open_questions.md
```

## 1. Schema registry — `internal/schemaregistry/`

`registry.go` loads `schemas/incident_v1.json` via `go:embed` and
compiles a `*jsonschema.Schema` per entity + version. The domain layer
calls `Registry.Validate("incident", 1, attributesBytes)` before any
write.

> Why JSON Schema? It's the boring choice for "validate a JSONB blob
> against a versioned shape". `santhosh-tekuri/jsonschema/v6` is pure
> Go, supports Draft 2020-12, and has no cgo.

## 2. Persistence — `internal/persistence/`

The **only** package that imports `pgx` or writes SQL. Read in this
order:

- `pool.go` — wraps `*pgxpool.Pool`. Owns `WithTx`, the primitive every
  service uses to run "entity + audit + outbox" in one transaction.
  Defines the `Querier` interface so a repo method can be called with
  either the pool or a `pgx.Tx`.
- `migrate.go` — runs `golang-migrate` against the embedded SQL.
  Idempotent on every startup.
- `incidents.go`, `audit.go`, `outbox.go`, `connectors.go` — one file
  per table. Repos are stateless structs (`type IncidentRepo struct{}`);
  state lives in the rows you pass in.

## 3. Domain — `internal/domain/incident/`

- `incident.go` — types (`Incident`, `Severity`, `Status`) and the
  status state machine (`allowedTransitions` map, `CanTransition`).
  *This is the only place that knows valid transitions.*
- `service.go` — the orchestration layer. Every write goes
  `Authorize → Validate input → Validate attributes → Open tx → Insert
  row → Insert audit → Insert outbox → Commit`. The same `service.go`
  also does Get + List (no tx needed there).

## 4. Events — `internal/events/`

- `envelope.go` — the canonical `Envelope` struct. Importable from
  anywhere, including the connector binary. **Has no AMQP imports.**
- `publisher.go` — wraps `rabbitmq/amqp091-go` for sending. Used only
  by the relay.
- `subscriber.go` — wraps the same library for receiving. Used by
  the reference connector and by `cmd/smoke-consumer`.

## 5. Outbox relay — `internal/outbox/relay.go`

A goroutine started in `main`. Polls every 250ms for unpublished rows,
publishes each one, marks it published. Errors during publish leave
the row unmarked → next tick retries. The unit tests in
`relay_test.go` cover the happy path, a transient publish failure, and
a fetch error — read them as documentation of the contract.

## 6. API — `internal/api/`

- `server.go` — chi router, middleware stack, `writeJSON`,
  `writeError` (the central error→HTTP-status mapping).
- `incidents.go` — `POST/GET/PATCH /v1/incidents`. The handler is
  three steps every time: parse request → call service → write
  response.
- `connectors.go` — `POST /v1/connectors` and the heartbeat endpoint.

## 7. Auth — `internal/auth/auth.go`

The middleware extracts `X-Principal-Id` (`type:id`) and `X-Tenant-Id`
from headers and stuffs them into the request context. The
`Authorizer` interface has one method, `Allow`. `StubAuthorizer`
returns `nil` for everything in v1 — swapping in a real implementation
is one line in `cmd/core/main.go`.

## 8. Audit — `internal/audit/audit.go`

`Recorder.Record` is the single function every code path uses to
write an audit row. Lives in the same transaction as the entity write
when there is one.

## 9. Connector runtime — `internal/connectorruntime/runtime.go`

In-core surface of ADR-003: persists the connector identity and
capabilities, validates declared types/versions against the schema
registry. The connector itself is in `cmd/reference-connector/`.

## 10. Config — `internal/config/config.go`

Two structs, `Core` and `Connector`, both populated from env vars with
sensible defaults so docker compose just works. No flags, no files.

## 11. Entry points — `cmd/`

- `cmd/core/main.go` — wires every module together, in the order
  comments at the top describe.
- `cmd/reference-connector/main.go` — registers, subscribes, enriches
  on `incident.created`, heartbeats periodically.
- `cmd/smoke-consumer/main.go` — one-shot consumer used by
  `scripts/smoke.sh` to assert envelopes on the bus.

## Where to go next

- Adding a migration: **05-adding-a-migration.md**
- Adding a second entity: **06-adding-an-entity.md**
- Building a connector: **07-writing-a-connector.md**
