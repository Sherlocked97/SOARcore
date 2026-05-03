# Product Specification

> **Status**: in active development. Captures the product/UX
> requirements that shape what the platform does for its users —
> SOC analysts, shift leads, and AI SOC consumers — independent of
> how those requirements are implemented.
>
> Architectural decisions live in `context/decisions.md`. This file
> captures *product-level* commitments: capabilities the platform
> commits to, the user need each commitment serves, and the failure
> mode each commitment guards against.

## Scope

Product requirements that shape the first-party analyst UI (ADR-006),
the data-model surface that supports it, and the API surface that AI
consumers and connectors interact with. These are not principles and
not ADRs — they are commitments to capabilities tied to user needs.

Each capability commitment names:

- **The user need** it serves (a concrete analyst or AI-consumer
  action, not an abstraction).
- **The failure mode** if shipped wrong.
- **Constraints** on the experience: latency, error visibility,
  recovery path, where applicable.

Where a commitment implies a data-model entity or an API affordance,
the implication is named under the relevant section so the architecture
and frontend roles can pick it up — without this spec proposing
schema or UI components itself.

---

## 1. Analyst Workbench

The first-party UI must provide a working analyst bench: a single
surface where an analyst triages incoming alerts, opens incidents,
pivots to related entities (observables, assets, prior incidents),
records actions, and hands off cleanly. The bench is the primary
adoption surface — if it does not feel native to a SOC analyst, the
platform fails its first user test regardless of API quality.

### 1.1 Triage queue

**Capability**: an analyst sees the outstanding alert backlog as a
single sorted list, with default sort by severity descending then
arrival time descending, and with user-configurable filters and
saved views.

- **User need**: an analyst arriving at the start of a shift must
  identify outstanding work within seconds, not minutes.
- **Failure mode**: a slow, unsorted, or unfiltered queue means the
  analyst either ignores it (and misses real signals) or burns time
  scrolling — the workbench fails its first user test.
- **Constraints**: first paint in under 1 second for the default
  view at up to 10,000 matching alerts. Filter changes apply within
  1 second.

### 1.2 Pivot to related entities

**Capability**: from any alert or incident, the analyst reaches every
other entity that shares an observable in one click. Before
committing to the pivot, the analyst sees the count ("seen in N prior
incidents") so they can decide whether the pivot is worth it.

- **User need**: an analyst's core triage skill is recognition —
  "I've seen this IP before." The product's job is to make that
  lookup near-zero cost.
- **Failure mode**: slow pivots, awkward pagination, or a hidden
  prior-incident count turns recognition into speculation.
- **Surfaces to architecture**: the data model must expose join
  paths the workbench depends on, without N+1 query patterns:
  - alert → observables → other alerts on same observable → prior
    incidents on same observable
  - incident → source alerts → enrichment annotations
  - incident → ownership history → audit events scoped per
    ownership window
  - observable → all alerts and incidents that referenced it,
    time-ordered

### 1.3 Action recording and audit timeline

**Capability**: every state-changing analyst action is recorded as
an audit event and (where applicable) a domain event. The workbench
surfaces the audit timeline of an incident as a first-class view,
filterable by actor class (human / AI consumer / connector / system)
and by action class.

- **User need**: shift handover requires the incoming analyst to
  reconstruct what happened during the prior shift, including who
  did what and why.
- **Failure mode**: a buried or unfilterable timeline forces the
  incoming analyst to page the outgoing one — and the outgoing one
  is asleep. The night-shift handover collapses.
- **Constraint**: ADR-001's audit obligation is satisfied at the
  data layer. The workbench's job is to surface it without becoming
  an archaeology exercise.

### 1.4 Bulk operations on alerts

**Capability**: an analyst dismisses, promotes, or tags N alerts in
a single action. Bulk actions return per-alert success/failure
feedback in the response.

- **User need**: alert volumes routinely produce hundreds of
  look-alikes after a campaign; per-alert action does not scale, and
  campaign correlation is the operationally load-bearing pattern.
- **Failure mode**: a bulk dismissal that silently drops 3 of 50
  alerts ("succeeded with errors") leaves alerts in an indeterminate
  state and erodes trust in the surface.
- **Constraint**: the response is per-alert, not aggregate. The UI
  shows which alerts succeeded and which did not, with the reason
  for each failure.

### 1.5 Shift handover surface

**Capability**: at shift change, the outgoing analyst sees their
open incidents grouped for handover and is required to write a
handover note per incident before the handover completes. The
incoming analyst sees inherited incidents with the
night-shift-readiness view defined in §3.

- **User need**: 24/7 SOC operations.
- **Detailed semantics**: §3.

### 1.6 AI consumer parity

**Capability**: every workbench affordance an analyst uses is
reachable via the public API. The UI is one consumer of the API; AI
SOC tools are another, with the same surface (per ADR-006 and
ADR-003).

- **User need**: AI SOC tools are first-class consumers of the alert
  surface (§2). If a capability exists for a human but not an API
  consumer, the symmetry breaks and AI integrations fragment.
- **Failure mode**: a workbench affordance that depends on a
  UI-private endpoint creates a privileged side door — a posture
  ADR-003 explicitly forbids.

---

## 2. Incident & Alert Management

The product distinguishes *alerts* (raw, high-volume, machine-emitted
signals) from *incidents* (analyst- or rule-promoted cases that carry
state, ownership, and lifecycle). Both are first-class entities.
They are not the same thing under different states; the alert is not
"the incident before promotion."

### 2.1 What an alert is

An alert is a discrete, machine-emitted signal that something
potentially noteworthy happened. It carries:

- a source — which connector or system produced it
- an original timestamp from the source
- a severity as the source asserted it (the product preserves the
  source assertion; analyst-assigned severity lives at the incident
  level)
- a short description
- a reference to the raw payload (the payload itself is not the
  alert; payload storage is a connector or storage concern)
- a set of observables (IPs, hashes, hostnames, user IDs, …)
  extracted by the connector at ingest time

An alert *is not*:

- an incident (incidents are §2.3 onward)
- a vehicle for analyst state (analyst state lives on the incident)
- a vehicle for AI conclusions (AI conclusions are enrichment
  annotations on the alert, not a different alert — see §2.7)

### 2.2 Alert lifecycle

Product-level states (not schema):

```
                ┌──────────────┐
   ingest ─────▶│      new     │
                └──────┬───────┘
                       │
              ┌────────┼────────┐
              ▼        ▼        ▼
        ┌──────────┐ ┌──────┐ ┌──────────┐
        │ triaged  │ │ dis- │ │ promoted │
        └────┬─────┘ │missed│ └──────────┘
             │       └──────┘   (terminal,
             ▼                   linked to
        ┌────────────┐           ≥1 incident)
        │ promoted   │
        │    or      │
        │ dismissed  │
        └────────────┘
```

A side-channel state, `suppressed`, is rule-driven at ingest and is
not analyst-reachable except through manual override:

- `new → suppressed` is automatic when an ingest rule (deduplication
  window, suppression list) matches.
- `suppressed → new` is either automatic at window expiry or
  analyst-triggered through manual override.

Suppressed alerts are queryable. They do not appear in the default
triage queue.

State transitions emit `alert.state_changed`; promotion and
dismissal emit additional dedicated events (§2.3, §2.6).

### 2.3 Alert-to-incident promotion

Promotion is the operationally load-bearing transition. The product
supports three cardinalities:

```
1:1   alert ─────────▶ incident
      Default for high-severity, unambiguous alerts.

1:N   alert ─────────▶ incident_a
                  └──▶ incident_b
      One alert spawns multiple incidents — rare but real
      (a composite alert covering separate impact tracks).

N:1   alert_1 ──┐
      alert_2 ──┼──▶ incident
      alert_3 ──┘
      Campaign correlation. The most operationally
      important pattern.
```

Promotion is:

- **An explicit action**: alert state alone does not promote.
  Promotion is a separate verb. An alert moving to `triaged` is not
  promotion.
- **Atomic**: promotion either succeeds end-to-end (incident
  created, all selected alerts linked, audit and outbox written) or
  fails with no partial state. There is no "incident exists but
  only some alerts are linked" outcome. This is a transactional
  commitment.
- **Authorized**: the analyst (or rule) performing the promotion is
  the *initial* owner of the resulting incident. Reassignment is
  immediate and routine — promotion is not a long-term ownership
  claim.

Promotion emits `alert.promoted` (one event per source alert,
correlation-linked) plus `incident.created`.

### 2.4 What carries over from alert to incident

| Carries over | Rule |
|---|---|
| Observables | Union across all source alerts, deduplicated by canonical form |
| Severity | Highest severity among source alerts becomes the initial incident severity; analyst may adjust at promotion time or after |
| Tags | Union, deduplicated |
| Raw payload references | All source alerts' references attached |
| Source connector | If all source alerts share one source, that becomes the incident's source; if mixed, the incident-level source is null and source is recorded per source alert |
| Enrichment annotations | Preserved per source alert; not merged across alerts (merging would lose provenance) |

| Does NOT carry over | Why |
|---|---|
| Alert lifecycle state | Alerts retain their own state (`promoted`); the incident has its own lifecycle |
| Source-system disposition | A SIEM closing an alert on its end does not close the incident here — the alert is the alert; the incident is its own thing |

**Surfaces to architecture**: an alert ↔ incident link table is
required (many-to-many to support N:1 and 1:N). The link must
carry a snapshot of the source alert at promotion time, not just an
FK, so that subsequent edits to the alert (e.g. late-arriving
enrichment) do not retroactively rewrite the incident's history.

### 2.5 Reverse direction — un-promotion

Once an alert is promoted to an incident, the link cannot be
removed. Un-promotion is not a verb the product exposes.

An analyst who realizes a wrong promotion closes the incident with
a terminal status and a reason — not by unwinding the link. The
alert remains linked; the incident is closed. This rule exists
because:

- **24/7 reality**: a shift that promotes wrongly and goes off-shift
  cannot have its work silently unwound by the next shift; the
  audit history would become ambiguous about whether work was real.
- **AI consumer auditability**: if an AI consumer suggests a
  promotion the analyst confirms, an "undo" would erase the
  suggestion trail.

The terminal state used for wrongly-promoted incidents is part of
the incident state machine (architecture's seat). The product
requirement here is that *some* terminal state with a reason
exists, distinct from a normal close.

### 2.6 Dismissal

Dismissal is alert-level only — incidents close, alerts dismiss. A
dismissal requires a reason from a controlled vocabulary:

- `false_positive` — the alert is not real
- `benign_true_positive` — the alert is real but expected or
  authorized
- `duplicate` — covered by another alert or incident already
- `not_actionable` — real but the platform has no action available
- `expired` — superseded by time

Free-text comment is optional; the reason code is required.

Dismissed alerts remain queryable. The workbench must support a
"what the prior shift dismissed and why" view — dismissal patterns
are how operators tune the alert pipeline.

Dismissal emits `alert.dismissed`.

### 2.7 AI consumer view

AI SOC consumers feed primarily into the alert surface, not the
incident surface. Their first-class needs at the alert surface:

- **Read alerts**, filtered by criteria.
- **Write enrichment annotations** to an alert (additional context,
  classification confidence, suggested observables). Enrichment is
  metadata on the alert, not a different entity.
- **Suggest a promotion** — produces a queue entry an analyst
  reviews. Does *not* execute a promotion, unless an explicit
  auto-promotion rule the operator configured permits it.
  **Auto-promotion is a configuration, not a default.**
- **Read prior incidents on shared observables** — the same join
  the human analyst depends on.

**Failure mode if AI auto-promotion defaults on**: the alert
surface becomes the noise floor it was supposed to filter.
Dismissal patterns stop reflecting analyst judgment and start
reflecting model miscalibration.

The AI-pipeline shape itself (prompts, agent behavior, retrieval
strategy) is out of scope for this spec.
The product-level need this spec commits to is *the API surface
that makes AI consumers first-class without making them
privileged*.

### 2.8 Failure modes if §2 is shipped wrong

- **Alerts and incidents conflated**: the workbench loses the
  high-volume triage queue and analysts drown in incidents.
- **Promotion non-atomic**: incidents exist with partial alert
  links, audit history disagrees with state, recovery is manual.
- **Dismissal lacks a reason code**: dismissal patterns can't be
  audited and tuning the alert pipeline is blind.
- **AI auto-promotion as default**: see §2.7.

### 2.9 Latency and visibility commitments

- **Alert ingest visibility**: an alert reaching the bus appears in
  the analyst queue at P95 < 2 seconds, P99 < 5 seconds, from
  connector publish to queue visibility. Operators watching for a
  campaign in real time depend on this.
- **Promotion**: atomic, as noted above.
- **Bulk operations**: per-item feedback, as noted in §1.4.

---

## 3. Shift Handover & Ownership

The product accommodates 24/7 SOC operations: multiple shifts,
rotating ownership, and clean handover. Every UX commitment in this
section must work for an analyst on a night shift inheriting an
in-flight incident from someone they cannot reach.

### 3.1 Shifts

Shifts are tenant-configurable. The product does **not** impose a
fixed shift model. The platform must support:

- **Named shifts** (e.g. "EU-day", "AMER-day", "follow-the-sun-3").
- **Shift assignments**: an analyst is on shift S during a time
  interval [t1, t2).
- **Shift overlaps**: handover windows where two shifts both have
  active analysts.

**Surfaces to architecture**: shift, shift-assignment, and
on-call-rotation entities are required. None exist yet.

### 3.2 Ownership

An incident has **exactly one** current owner — individual or team
— at any given time. Never zero, never two. Ownership is not a
single field: it is a sequence of assignments over time.

Each ownership row carries:

- the owner (individual or team)
- the time window the assignment was active
- a reason from a controlled vocabulary: `created`, `assigned`,
  `shift_handover`, `escalation`, `claimed`, `auto_assigned`

The current owner is the row whose end time is null. Prior rows
describe the ownership history.

- **User need**: a night-shift analyst inheriting an incident must
  be able to answer "who owned this two hours ago, and what did
  they do?" The answer must come from the platform, not from paging
  the prior owner.
- **Failure mode**: a single `assignee_id` field with no history
  erases that answer. The night-shift handover collapses.
- **Surfaces to architecture**: the current schema's `assignee_id`
  field on Incident (`context/data_model.md`) is insufficient as the
  sole ownership representation. An ownership-history mechanism is
  required. Whether `assignee_id` remains as a denormalized
  current-owner pointer alongside a history table is architecture's
  call.

### 3.3 Handover

A handover is a *bulk ownership transition* scoped to a shift
change. It produces:

- updated ownership rows for incidents transitioning between owners
- a **handover note per incident**, written by the outgoing owner —
  required, free text. The outgoing owner cannot complete handover
  without writing one.
- a handover summary capturing what the outgoing shift did and what
  the incoming shift inherits

- **User need**: handover is the spec's hardest UX constraint. The
  incoming shift must be operational without paging the outgoing
  shift.
- **Failure mode if handover note is optional**: outgoing shift
  leaves without context; incoming shift pages someone offline.
  Exactly the failure the section is meant to prevent.
- **Failure mode if handover auto-reassigns at clock-time**:
  anything mid-flight that is owner-bound (a containment workflow,
  an external ticket update in progress) breaks silently, and there
  is no human signal that the work was reviewed.

### 3.4 What the incoming shift sees

For each inherited incident, the incoming analyst sees, without
having to reach the outgoing analyst:

- current state and full ownership history
- the outgoing shift's handover note for this incident
- the prior shift's audit events scoped to this incident (default:
  last ~20, configurable)
- observables that changed during the prior shift
- any pending automation actions (e.g., a containment workflow
  mid-flight) and their state

**Surfaces to frontend**: this is a single composed view. The
information is not new (each piece is reachable individually), but
the composition is the product commitment. An incoming analyst
opening an inherited incident expects the inherited-context view by
default, not the standard incident detail.

### 3.5 Reassignment vs. handover

| | Reassignment | Handover |
|---|---|---|
| When | Any time | Scheduled at shift change |
| Scope | Single incident | Batch (all incidents owned by the outgoing shift) |
| Required input | Reason from vocabulary | Reason + handover note per incident |
| Audit | Single event | Multiple events under one correlation_id |

Both produce ownership rows. They are distinct verbs because their
required input differs.

### 3.6 Bundled ticketing connector and ownership authority

**Recommendation, pending architecture concurrence**: the bundled
ticketing connector (ADR-007) is a **downstream sync target**, not
the authoritative ownership system.

Reason: ownership in the platform carries response-action
authorization. If a ticketing system that does not enforce the
platform's authorization model owns assignment, the platform's
authorization story breaks — anyone with edit rights in the
ticketing tool can change who is authorized to run response actions
in the platform.

The ticketing connector subscribes to ownership events and reflects
them outward. It does not declare ownership.

Tracked as Q-011 (open) until architecture confirms the
authorization-model implication.

### 3.7 Failure modes if §3 is shipped wrong

- Single-owner field, no history → night-shift handover collapses.
- Handover note optional → outgoing shift leaves without context.
- Clock-time auto-reassignment → mid-flight work breaks silently.
- Ticketing-as-authoritative-ownership → authorization model leaks.

### 3.8 Latency commitments

- The handover view (inherited incidents page) loads in under 2
  seconds for up to 200 incidents.
- The ownership-history query for any single incident returns in
  under 500 ms.

---

## 4. Product Roadmap

Capability ordering, not dates. Each step explains *why this step
ships before the next* — the dependency is on user value validated
by the prior step, not on engineering convenience.

| Order | Capability | Why this order |
|---|---|---|
| 1 | Alert ingest + triage queue (§1.1, §2.1, §2.2) | The triage queue is the workbench's first user test. There is nothing to triage without alert ingest, and there is no test of the workbench's value without the queue. |
| 2 | Alert-to-incident promotion, 1:1 only (§2.3, §2.4) | The simplest promotion case validates the alert↔incident link mechanics and the atomicity commitment. N:1 and 1:N are riskier; defer until 1:1 is solid. |
| 3 | Incident lifecycle + audit timeline (§1.3) | Once promotion exists, analysts spend time in incidents. The audit timeline is the prerequisite for any handover commitment (§3) and for AI-consumer auditability. |
| 4 | Bulk alert operations, including N:1 promotion (§1.4, §2.3) | Triage at campaign scale fails without bulk action. N:1 is the operationally load-bearing pattern; it ships only after 1:1 has proved the link mechanics. |
| 5 | Pivot / observable cross-entity views (§1.2) | Pivots are higher-value once there are alerts, incidents, and history to pivot to. Shipping pivots before there is data inverts the value test. |
| 6 | Time-bounded ownership + reassignment (§3.2, §3.5) | Ownership history is the prerequisite for any handover surface. Reassignment is the simpler verb and ships first. |
| 7 | Shift handover surface (§3.3, §3.4) | The hardest UX commitment. Ships only after ownership history is solid — handover is a special case of ownership transition. |
| 8 | AI consumer enrichment surface (§2.7) | Layered onto a stable alert API. Auto-promotion configuration deferred to a later iteration; v1 of this step is enrichment + suggestion, not auto-execution. |

What this order *deliberately* defers, with reasons:

- **Co-watchers / collaborators on incidents**: incompatible with
  the exactly-one-owner rule (§3.2). Tracked as Q-012.
- **Auto-promotion rules for AI consumers**: requires Q-013 to
  resolve guardrails first.
- **Ticketing-connector ownership authority**: Q-011 must resolve.
- **Suppression-rule analyst override surface**: alert ingest goes
  live at step 1, but the analyst override path is not a step-1
  capability — Q-014 must resolve.

---

## Open product questions

- **Q-011**: Is the bundled ticketing connector authoritative for
  ownership, or a downstream sync target? Recommendation in §3.6.
  Mirrored to `context/open_questions.md`.
- **Q-012**: Co-watchers / collaborators on incidents — deferred
  beyond v1 unless a strong user need surfaces. Mirrored to
  `context/open_questions.md`.
- **Q-013**: Auto-promotion rules for AI consumers — when, with
  what guardrails, what audit signal. Default-off in v1. Mirrored
  to `context/open_questions.md`.
- **Q-014**: Suppression-rule semantics — who configures
  suppression rules at ingest, how analysts override individual
  suppressions, what audit trail covers the override. Mirrored to
  `context/open_questions.md`.
