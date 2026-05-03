# SOARcore

> Open-source, modular SOAR platform built around a stable data layer.

## What This Is

SOARcore is a community-pluggable SOAR platform. The core defines the data model,
API contracts, and event interfaces. Connectors, response actions, and integrations
are built by the community on top of it. A first-party analyst UI ships alongside
the core as a co-equal product.

Inspired by what OpenCTI achieved for Threat Intelligence — applied to SOAR.

## Status

Early implementation. Architecture accepted (ADRs 001–007); the first entity
(`Incident`) is wired end to end: HTTP API, Postgres persistence, transactional
outbox, RabbitMQ event publishing, and a reference connector that consumes events
and calls back into the API. The first-party analyst UI (`/ui`, ADR-006) ships
a minimal status dashboard today; the analyst workbench itself is not yet built.

## Running Locally

Prereqs: Docker + the `docker compose` plugin, Go 1.23+, `make`, `curl`, `jq`,
and the `psql` client.

```bash
git clone https://github.com/Sherlocked97/SOARcore.git && cd SOARcore
make up      # postgres, rabbitmq, core, reference-connector, ui
make build   # build the local Go binaries
make smoke   # end-to-end smoke test against the running stack
make down    # tear it all down (drops volumes)
```

Once the stack is up:

- core API: <http://localhost:8080>
- UI status dashboard: <http://localhost:8081>

`make help` lists every target. See [`docs/02-running-locally.md`](./docs/02-running-locally.md)
for the walkthrough, a hand-rolled first request, and troubleshooting.

## Documentation

Contributor docs live under [`docs/`](./docs/) — architecture overview, codebase
tour, Go primer, and how to add a migration / entity / connector.

Authoritative design notes:

- [`context/decisions.md`](./context/decisions.md) — Architectural Decision Records
- [`context/data_model.md`](./context/data_model.md) — Schema and event envelope
- [`context/product_spec.md`](./context/product_spec.md) — Product/UX requirements
- [`context/open_questions.md`](./context/open_questions.md) — Unresolved design questions

## Contributing

Not yet. The project is not accepting external contributions until contributor
licensing (CLA) is in place. Please open an issue to start a conversation.

## License

[Apache 2.0](./LICENSE) © 2026 Sherlocked97
