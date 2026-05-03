# SOARcore

> Open-source, modular SOAR platform built around a stable data layer.
> Currently in pre-implementation phase — architecture accepted, code
> not yet published.

## What This Is

SOARcore is a community-pluggable SOAR platform. The core defines the data model,
API contracts, and event interfaces. Connectors, response actions, and integrations
are built by the community on top of it. A first-party analyst UI ships alongside
the core as a co-equal product.

Inspired by what OpenCTI achieved for Threat Intelligence — applied to SOAR.

## Status

Architecture accepted (ADRs 001–007). Implementation is underway locally; the
code is not yet published, pending reconciliation with the latest ADRs.

Design notes live under [`context/`](./context/):

- [`decisions.md`](./context/decisions.md) — Architectural Decision Records
- [`open_questions.md`](./context/open_questions.md) — Unresolved design questions
- [`data_model.md`](./context/data_model.md) — Evolving schema definitions
- [`product_spec.md`](./context/product_spec.md) — Product/UX requirements (stub)

## Contributing

Not yet. The project is not accepting external contributions until contributor
licensing (CLA) is in place. Please open an issue to start a conversation.

## License

[Apache 2.0](./LICENSE) © 2026 Sherlocked97
