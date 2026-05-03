# 06 — Adding an Entity (end-to-end)

This walkthrough adds a **fictional second entity, `Observable`** so
you can see every layer that needs an edit. Use Incident as the
reference shape (it's the most complete one).

The work is a vertical slice: schema → table → repo → domain → API →
events → tests. Don't skip layers; the system depends on every layer
contributing to the invariant "every state change emits an event in
the same transaction."

## 1. Define the entity in the data model

Add the new entity to `context/data_model.md`:

- Fields, types, constraints.
- The status state machine (if any).
- The events it emits (`observable.created`, etc.).
- Any indexes the API needs.

This is also where you decide which fields are first-class columns and
which live in `attributes` (JSONB). Heuristic: filter/index → column,
display-only or schema-evolving → `attributes`.

## 2. Add a JSON Schema for `attributes`

`internal/schemaregistry/schemas/observable_v1.json` describes the
Draft 2020-12 schema. Then register it in
`internal/schemaregistry/registry.go` (`init()` block, alongside
`incident_v1`).

## 3. Migration

`migrations/0002_observable.up.sql` (and `.down.sql`):

- `CREATE TYPE observable_status AS ENUM (...)` if you have a state
  machine.
- `CREATE TABLE observable (...)` with `id`, `tenant_id`,
  `schema_version`, your typed columns, `attributes JSONB`,
  `tags TEXT[]`, `created_at`, `updated_at`.
- Indexes per the data model.

See `docs/05-adding-a-migration.md` for the rules.

## 4. Persistence repo

`internal/persistence/observables.go`:

- `ObservableRow` struct mirroring the table.
- `ObservableRepo` with `Insert`, `Update`, `GetByID`, `List`.
- Methods accept `Querier` so they work in or out of a transaction.

Copy the structure of `incidents.go` line-by-line; the only thing that
changes is which columns exist.

## 5. Domain

`internal/domain/observable/`:

- `observable.go` — types, `Status` enum + `allowedTransitions` map +
  `CanTransition`.
- `service.go` — `Service` with `Create`, `Get`, `List`, `Patch`. Each
  write opens a tx via `pool.WithTx` and inserts entity + audit +
  outbox.

`service.go` is mostly the same shape as the Incident service; the
only differences are which fields are validated, which event types
are emitted, and the state-machine specific to your entity.

## 6. Events

In `internal/events/envelope.go`, add the new event-type constants:

```go
const (
    EventTypeObservableCreated = "observable.created"
    EventTypeObservableUpdated = "observable.updated"
)
```

You don't need to touch the publisher / subscriber — they're
type-agnostic. Routing on the bus uses the event type as the key, so
new types automatically route through the existing exchange.

## 7. API

`internal/api/observables.go`:

- `createObservableRequest` / `patchObservableRequest` /
  `observableResponse` types.
- `handleCreateObservable`, `handleGetObservable`, `handleListObservables`,
  `handlePatchObservable`.

In `internal/api/server.go`, add a route group:

```go
r.Route("/observables", func(r chi.Router) {
    r.Post("/", s.handleCreateObservable)
    r.Get("/", s.handleListObservables)
    r.Get("/{id}", s.handleGetObservable)
    r.Patch("/{id}", s.handlePatchObservable)
})
```

If your entity introduces new error sentinels (e.g.
`observable.ErrInvalidIOC`), add them to the `switch` in `writeError`.

## 8. Wire it up in `main`

In `cmd/core/main.go`:

```go
observableRepo := persistence.NewObservableRepo()
observableService := observable.NewService(pool, observableRepo, outboxRepo, auditRecorder, schema, authorizer)
```

Pass `observableService` into `api.NewServer(...)`.

## 9. Tests

- `internal/domain/observable/observable_test.go` — state machine
  truth table.
- `internal/api/observables_test.go` (if you add HTTP-level tests).
- Update `scripts/smoke.sh` to exercise the new entity, or add a
  second smoke script.

## 10. Optionally: a connector capability

If a connector should produce or consume the new entity, declare
`"produces": ["observable"]` or `"consumes": ["observable"]` in its
registration call. The runtime validates that the type is known to the
schema registry — that's why step 2 came before step 4.

## Done — checklist

- [ ] data_model.md entry
- [ ] JSON Schema + registered
- [ ] up + down migration
- [ ] persistence repo
- [ ] domain (types, state machine, service)
- [ ] event-type constants
- [ ] API routes + handlers
- [ ] error mapping
- [ ] main.go wiring
- [ ] tests
- [ ] smoke step (optional but recommended)
