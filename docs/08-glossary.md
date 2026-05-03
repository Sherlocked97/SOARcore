# 08 — Glossary

Terms you'll see in this codebase or its docs, in alphabetical order.

**ADR (Architecture Decision Record)** — A short Markdown document
that captures a single architectural choice, its alternatives, and
the reasons. Stored in `context/decisions.md`.

**AMQP** — *Advanced Message Queuing Protocol*, version 0.9.1, the
wire protocol RabbitMQ speaks. SOARcore uses an AMQP **topic
exchange** (`soar.events`) — messages are routed to bound queues
based on a pattern match between the routing key and the binding.

**Audit row** — A record of *intent and authorization*: who tried to
do what, with what result. Lives in the `audit` table. Written in
the same transaction as the entity write that caused it (or alone, if
authorization denied the action).

**At-least-once delivery** — A guarantee that every event makes it to
the bus eventually, possibly more than once. The outbox is the
mechanism. Consumers must dedupe on `event_id`.

**Connector** — An out-of-process program that integrates an external
system (TI source, ticketing, EDR, …) with SOARcore via the public
HTTP API + the bus. ADR-003.

**CGO_ENABLED=0** — A `go build` setting that disables C interop. The
binary becomes fully static — no `libc` dependency — and runs on
distroless's `static-debian12` image. Pgx is pure Go, so we keep it
off.

**Distroless** — A family of minimal container images by Google:
`gcr.io/distroless/static-debian12:nonroot` has no shell, no package
manager, runs as a non-root user by default. We use it as the runtime
base for `cmd/core` and `cmd/reference-connector`.

**Embed (`//go:embed`)** — A Go compiler directive that bakes files
into the binary as an `embed.FS`. Used for the JSON Schema files
and the SQL migrations.

**Event envelope** — The canonical JSON shape every event uses on the
bus. Defined in `internal/events/envelope.go`.
See `context/data_model.md` for the field list.

**Exchange (RabbitMQ)** — The "router" half of AMQP. Producers publish
to an exchange; the exchange decides which queues get the message.
We use exactly one exchange (`soar.events`).

**JSONB** — Postgres's binary-encoded JSON column type. Supports
indexed key lookups and operators (`->`, `->>`, `@>`). We store
flexible per-entity attributes in a JSONB column.

**Modular monolith** — One process at deploy time, multiple
internal modules with clean contracts at code time. Future split into
services is allowed but not the goal. ADR-001 chose this for v1.

**Outbox** — A database table that stages events for publication. The
relay drains it. Writing the outbox row in the *same transaction* as
the entity write is the trick that makes "publish or die together"
hold without a distributed transaction.

**Principal** — The actor making an API call: a user, a connector, or
the system itself. Encoded as `type:id` in the `X-Principal-Id`
header.

**pgx / pgxpool** — The Postgres driver and connection pool we use.
Native, pure-Go, supports JSONB / `text[]` / UUID without third-party
helpers.

**Relay** — The background goroutine in `internal/outbox/relay.go`.
Reads unpublished outbox rows, publishes each one to RabbitMQ, marks
the row published.

**Schema registry** — `internal/schemaregistry/`. Maps `(entity,
version)` to a compiled JSON Schema validator. The domain layer calls
it before any write that touches `attributes`.

**Schema version** — An integer stamped on every entity row at write
time. When the JSONB shape changes, we bump the version, register a
new validator, and migrate code paths to handle both — never rewrite
old rows.

**Tenant** — The data-isolation unit. Every entity row carries a
`tenant_id`. v1 is single-tenant in practice, but the column is
present from day one so multi-tenancy doesn't require a schema
change.

**Wire contract** — The combination of HTTP API + event envelope +
bus topology. The contract a connector author needs to honor. ADR-003
fixes its shape.
