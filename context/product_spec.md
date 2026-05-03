# Product Specification

> **Status**: stub. Captures the product/UX requirements raised during
> the principles revision that produced ADR-006 and ADR-007. A
> dedicated product owner should expand this file as the spec grows
> — see Q-009.
>
> Architectural decisions live in `context/decisions.md`. This file is
> for *product-level* requirements: what the product does for its
> users, independent of how it is built.

## Scope

Product requirements that shape the first-party analyst UI (ADR-006)
and the data model surface that supports it. These are not principles
and not ADRs — they are commitments to capabilities.

---

## 1. Analyst Workbench

The first-party UI must provide a working analyst bench: a single
surface where an analyst triages incoming alerts, opens incidents,
pivots to related entities (observables, assets, prior incidents),
records actions, and hands off cleanly. The bench is the primary
adoption surface — if it does not feel native to a SOC analyst, the
platform fails its first user test regardless of API quality.

Implications:
- Data model must expose join paths an analyst expects (incident →
  alerts → observables → prior incidents on the same observables)
  without N+1 query patterns.
- Action recording is first-class: every analyst action emits an
  event and writes to the audit log (ADR-001 obligation already
  satisfied at the data layer; UI must surface it).

## 2. Incident & Alert Management

The product distinguishes *alerts* (raw, high-volume, machine-emitted
signals) from *incidents* (analyst- or rule-promoted cases that carry
state, ownership, and lifecycle). Both are first-class entities. The
UI and API must support: alert triage queues, alert-to-incident
promotion (1:1, 1:N, N:1), incident state machine, ownership
assignment, severity and priority fields independent of each other,
and bulk operations on alerts.

Implications:
- `Incident` v1 (`context/data_model.md`) is defined; an `Alert`
  entity and the alert-to-incident relationship are not yet defined
  and must be added before UI implementation begins.
- AI SOC consumers (e.g. Qevlar) feed primarily into the alert
  surface, not the incident surface — they generate enriched alerts
  that analysts (or rules) promote to incidents.

## 3. Shift Handover & Ownership

The product accommodates 24/7 SOC operations: multiple shifts,
rotating ownership, and clean handover. An incident always has a
current owner (individual or team), a handover history, and a clear
"what happened during the previous shift" view. Shift definitions
themselves are configurable per tenant — the product does not
impose a fixed shift model.

Implications:
- Ownership is modeled as a time-bounded assignment, not a single
  field. The data model must support querying "who owned this
  incident at time T."
- Shift definitions, on-call rotations, and handover notes are
  product-level concepts that need data-model entities. None exist
  yet.
- Notification and assignment routing during handover is event-bus
  driven (ADR-002) — the product spec names the requirement; the
  routing implementation is connector or plugin scope.

---

## Open product questions

- Alert entity schema and alert-to-incident promotion semantics —
  needs definition in `context/data_model.md` before UI work begins.
- Shift / on-call data model — same.
- Whether the bundled ticketing connector (ADR-007) is the
  authoritative ownership system or a downstream sync target.
