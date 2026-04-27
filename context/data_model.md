# Data Model

> The data model is the product. Hand-waving is failure. Every field has
> a name, type, cardinality, nullability, and an indexing implication.

## Schema evolution rules

These rules apply to every entity and every event in this document.

1. **Each row stores `schema_version`.** A row carries the version of
   the entity schema it was written under. Readers must tolerate any
   version they declare support for.
2. **Migrations are expand-then-contract.** Add new columns/fields as
   nullable, dual-write, backfill, then drop the old shape in a later
   release. No destructive single-step migrations.
3. **Events are versioned at the envelope.** Every envelope carries
   `event_schema_version`. The bus rejects nothing; consumers filter
   on declared compatibility. Connectors declare their accepted event
   versions at registration (ADR-003).
4. **Public API is versioned at the major.** Surfaces appear under
   `/v1/...`. Breaking changes roll the major and run the two surfaces
   in parallel during a deprecation window.
5. **Schema registry is the source of truth.** Entity definitions and
   their per-version JSON Schemas for the `attributes` flex zone live
   in the schema registry module — not hand-rolled in handlers.

## Entities

### Incident — v1

The first entity. Concrete enough to compile against; small enough to
extend.

| Field | Type | Null | Notes / index |
|---|---|---|---|
| `id` | UUID | no | primary key |
| `tenant_id` | UUID | no | indexed; partitioning key candidate |
| `schema_version` | int | no | row stamps schema it was written under |
| `external_id` | text | yes | unique with `(tenant_id, source_connector_id)` when set; for idempotent ingest |
| `title` | text | no | |
| `description` | text | yes | |
| `severity` | enum(`low`,`medium`,`high`,`critical`) | no | |
| `status` | enum(`new`,`triaged`,`in_progress`,`contained`,`resolved`,`closed`) | no | indexed |
| `assignee_id` | UUID | yes | FK → principal |
| `source_connector_id` | text | yes | which connector created this row |
| `attributes` | JSONB | yes | extension fields; validated against versioned JSON Schema in the registry |
| `tags` | text[] | yes | GIN index |
| `created_at` | timestamptz | no | indexed |
| `updated_at` | timestamptz | no | indexed |
| `closed_at` | timestamptz | yes | |

**Status state machine** (enforced in the `domain` module, not in DB
constraints — keep DB simple):

```
new → triaged → in_progress → contained → resolved → closed
                       ↑                       ↓
                       └───────── reopen ──────┘   (resolved/closed → in_progress)
```

Any other transition is rejected with `422`. Transitions emit
`incident.status_changed` in addition to `incident.updated`.

**Indexing implications**:
- Multi-column indexes: `(tenant_id, status, created_at desc)` for
  default list views; `(tenant_id, updated_at desc)` for activity feed.
- GIN on `tags`.
- Partial unique index on `(tenant_id, source_connector_id, external_id)`
  WHERE `external_id IS NOT NULL` — supports idempotent re-ingest from a
  given connector without forbidding null `external_id`.

## Event envelope

Every event on the bus uses this envelope. The payload varies by
`event_type`.

| Field | Type | Notes |
|---|---|---|
| `event_id` | UUID | unique per event |
| `event_type` | text | dotted, e.g. `incident.created`, `incident.status_changed` |
| `event_schema_version` | int | bus-level versioning per `event_type` |
| `occurred_at` | timestamptz | when the state change happened in the core |
| `tenant_id` | UUID | tenant scope of the event |
| `actor` | object | `{ type: "user"\|"connector"\|"system", id: text }` |
| `entity` | object | `{ type: text, id: UUID, schema_version: int }` |
| `payload` | JSON | per `event_type` (see below) |
| `correlation_id` | UUID | causal-chain tracing across actions |
| `prior_event_id` | UUID, nullable | optional ordered chain for audit replay |

Topic naming: `{entity_type}.{action}` (e.g. `incident.created`).
Connectors subscribe by entity-type prefix or specific action.

## Events emitted by the Incident domain

All payloads below are at `event_schema_version = 1`.

### `incident.created` (v1)

Payload: full Incident snapshot at creation.

```json
{
  "incident": { /* every Incident field */ }
}
```

### `incident.updated` (v1)

Payload: changed fields plus the post-update snapshot.

```json
{
  "changed_fields": ["tags", "description"],
  "incident": { /* full post-update snapshot */ }
}
```

### `incident.status_changed` (v1)

Emitted *in addition to* `incident.updated` because status transitions
are first-class for downstream consumers (notifications, automations).

```json
{
  "from": "triaged",
  "to": "in_progress",
  "incident_id": "..."
}
```

### `incident.closed` (v1)

Emitted on the terminal `→ closed` transition, in addition to
`incident.status_changed` and `incident.updated`. Convenience for
consumers that only care about closure.

```json
{
  "incident_id": "...",
  "closed_at": "2026-04-26T10:11:12Z",
  "final_status": "closed"
}
```

## Audit log

Audit is a sibling table, not derivable from the event stream — it
records *intent and authorization* even when the action fails or no
event is emitted.

| Field | Type | Null | Notes |
|---|---|---|---|
| `id` | UUID | no | PK |
| `tenant_id` | UUID | no | indexed |
| `occurred_at` | timestamptz | no | indexed |
| `actor` | JSONB | no | `{ type, id }` as in the event envelope |
| `action` | text | no | dotted, e.g. `incident.create`, `incident.status_change` |
| `target` | JSONB | yes | `{ type, id }` of the affected entity, when applicable |
| `result` | enum(`success`,`denied`,`error`) | no | |
| `correlation_id` | UUID | yes | links audit row to the event(s) it accompanies |
| `metadata` | JSONB | yes | request hash, source IP, etc. — never payload bodies |

Audit rows are append-only. There is no UPDATE path. Retention and
export are operational concerns, not core schema concerns.

## Outbox

Implementation detail of the durable event-emission pattern, but
documented here because it is on the write path.

| Field | Type | Null | Notes |
|---|---|---|---|
| `id` | bigserial | no | PK |
| `event_id` | UUID | no | matches the eventual envelope |
| `event_type` | text | no | |
| `tenant_id` | UUID | no | |
| `payload` | JSONB | no | full envelope, ready to publish |
| `created_at` | timestamptz | no | indexed |
| `published_at` | timestamptz | yes | set by the relay on successful publish |

Domain writes insert into the outbox in the same transaction as the
state change. A background relay drains unpublished rows to RabbitMQ.
At-least-once semantics; consumers must be idempotent on `event_id`.