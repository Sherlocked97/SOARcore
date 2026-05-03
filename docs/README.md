# SOARcore — Contributor Documentation

Welcome. This directory is for **contributors**: people reading,
running, or modifying the SOARcore code. End-user / operator
documentation is a separate, future deliverable.

If you have never written Go before, start at **03-go-primer.md** and
work forward. If you know Go but not this codebase, read **01** then
**04**. If you just want the thing running, jump to **02**.

## Suggested reading order

| #  | File                          | What it covers                                                                |
|----|-------------------------------|-------------------------------------------------------------------------------|
| 01 | `01-architecture-overview.md` | The modular monolith: what each module does and where the contracts live.    |
| 02 | `02-running-locally.md`       | `docker compose up` → first incident → smoke test.                            |
| 03 | `03-go-primer.md`             | Go for first-timers: only the bits this codebase uses.                        |
| 04 | `04-codebase-tour.md`         | File-by-file walkthrough in build order.                                      |
| 05 | `05-adding-a-migration.md`    | Schema changes, expand-then-contract, golang-migrate basics.                  |
| 06 | `06-adding-an-entity.md`      | End-to-end: defining a new entity beyond Incident.                            |
| 07 | `07-writing-a-connector.md`   | The wire contract by example — read alongside `cmd/reference-connector/`.    |
| 08 | `08-glossary.md`              | Terms you'll see: outbox, ADR, JSONB, AMQP topic, distroless, etc.           |

## Where authoritative information lives

- **Architecture decisions** — `context/decisions.md` (ADRs).
- **Data model & event envelope** — `context/data_model.md`.
- **Open questions** — `context/open_questions.md`.

`docs/` explains the *current implementation*. When the implementation
and an ADR disagree, the ADR wins; please open an issue.
