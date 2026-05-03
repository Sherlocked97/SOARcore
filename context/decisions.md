# Architectural Decision Records

ADRs are append-only. Supersede; do not edit history.

## Template

**ADR-XXX: [Title]**
- Status: Proposed | Accepted | Superseded by ADR-YYY
- Date: YYYY-MM-DD
- Context: forces driving the decision; alternatives considered
- Decision: the chosen direction, stated as a positive claim
- Consequences: what becomes easier, what becomes harder, new obligations
- Open follow-ups: links to questions in open_questions.md

---

## ADR-001: Postgres as the primary system-of-record

- **Status**: Accepted
- **Date**: 2026-04-26
- **Context**:
  The core data layer must satisfy four hard properties:
  1. Multi-row transactional integrity - atomic updates across an
     Incident, its Observables, the audit row, and the event-outbox row.
  2. Explicit schema-evolution support - versioning non-negotiable.
  3. Self-hostable container AND managed offering across multiple
     clouds.
  4. AI/LLM-consumable query surface.

  Alternatives considered: ElasticSearch / OpenSearch, MongoDB,
  CockroachDB.

  *Why not OpenSearch as primary* (documented in full so this is not
  re-litigated):

  1. **Not transactional.** No multi-document ACID (Atomicity,
     Consistency, Isolation, Durability) transactions. SOAR state
     transitions ("assign incident, mark observable IOC, write audit,
     emit event") must be atomic. Postgres does this in one transaction;
     OpenSearch cannot.
  2. **Eventually consistent reads.** Default 1-second refresh interval
     makes read-after-write semantics fragile - a footgun for incident
     state machines.
  3. **Schema evolution is hostile.** Many mapping changes require full
     reindex. Postgres `JSONB` + plain DDL + migration tooling is
     strictly better against the "schema evolution as first-class"
     principle.
  4. **Audit log fit.** Append-only, ordered, durable, queryable-by-time
     is trivial in Postgres and awkward in OpenSearch.
  5. **License pragmatics.** Elasticsearch is SSPL/Elastic License v2 -
     not OSI-approved. OpenSearch (Apache-2.0) is the OSI-clean fork.
     Worth naming the right tool when comparing.

  Postgres and OpenSearch solve different problems and are not
  interchangeable for this role. Postgres provides the transactional
  guarantees, schema-evolution support, and append-only audit fit listed
  above. OpenSearch is the right tool for full-text search, aggregation,
  fuzzy matching, and analytics, and likely belongs in the architecture
  later on the read side, fed by events from the bus. That is captured
  as Q-006.

  *MSSP / large-enterprise scale path.* Postgres scales here without
  changing the contract:
  - Native partitioning + `tenant_id` discriminator → per-tenant data
    isolation and pruning.
  - Citus extension → horizontal sharding when single-node hits
    ceilings. Available self-hosted (container) and managed (Azure
    Cosmos for PostgreSQL - formerly Hyperscale Citus). Same wire
    protocol.
  - Read replicas → query offload.
  - Outbox pattern → events on the bus → read-side projections (e.g.
    OpenSearch) added as new subscribers without rearchitecting writes.

- **Decision**:
  Postgres is the primary system-of-record. Relational columns carry
  typed core attributes; `JSONB` columns carry heterogeneous extension
  fields under a versioned schema. No graph DB hard dependency. Citus
  + native partitioning is the documented horizontal scale path.

- **Consequences**:
  - *Easier*: transactions, migrations, multi-cloud managed offerings,
    SQL as the LLM-readable query surface, mature tooling.
  - *Harder*: full-text search and complex aggregations at firehose
    scale. Mitigated by future read-side projection (Q-006).
  - *Obligations*:
    - Outbox pattern in the write path - events durably emitted in the
      same transaction as state changes.
    - Multi-tenancy primitives (`tenant_id` everywhere,
      partitioning-ready) baked in from day one even while v1 is
      single-tenant.

- **Open follow-ups**: This ADR resolves the choice of primary
  datastore. A separate question is opened on whether and when to
  introduce a read-side search projection (e.g. OpenSearch) once a
  concrete query class proves SQL plus JSONB insufficient.

---

## ADR-002: RabbitMQ as event bus for v1

- **Status**: Accepted
- **Date**: 2026-04-26
- **Context**:
  The platform is event-driven by default: every state change in the
  core emits a subscribable event so that consumers (UI projections,
  connectors, automations, AI pipelines) can react without polling.
  The event bus must therefore exist both as a self-hostable container
  and as a managed offering across multiple clouds, so deployment can
  switch between modes without architectural divergence.

  Alternatives considered: Kafka / Redpanda, NATS JetStream, RabbitMQ.

- **Decision**:
  RabbitMQ for v1. Lighter operational footprint than Kafka. Sufficient
  durability for state-change events. Self-hostable container; managed
  offerings include CloudAMQP, Amazon MQ for RabbitMQ, and STACKIT.

- **Consequences**:
  - *Easier*: low operational burden, well-understood routing semantics,
    managed availability across required clouds, fast time-to-first-event
    for contributors running locally.
  - *Harder*: high-volume telemetry firehoses (millions/sec). Acceptable -
    raw telemetry ingestion is not core scope; that is a connector
    concern. If a future use case forces it, supersede with a
    Kafka-compatible ADR rather than retrofitting RabbitMQ.
  - *Obligations*:
    - Define and document the event envelope schema before producers
      and consumers are built.
    - Versioning rule: every envelope carries `event_schema_version`;
      consumers declare which versions they accept.

- **Open follow-ups**: This ADR resolves the choice of event bus for
  v1.

---

## ADR-003: Wire-protocol connector contract - no SDK requirement

- **Status**: Accepted
- **Date**: 2026-04-26
- **Context**:
  The platform integrates with external systems through an
  out-of-process connector ecosystem authored by the community.
  Defining the contract those connectors must conform to is
  architecturally significant: it determines which languages
  contributors may use, how tightly the platform is coupled to its
  integration code, and how stable the connector author experience can
  remain across versions. The reference model (OpenCTI) demonstrates a
  clear failure mode: when a language-specific SDK *is* the contract,
  the platform locks itself into one ecosystem and the SDK becomes a
  DX liability. Avoiding this trap is one of the motivating goals for
  the platform.

- **Decision**:
  The connector contract is wire-level. SDKs may exist as conveniences
  in any language, but the wire is authoritative.

  1. **Identity & lifecycle**: connectors register via the public API
     (`POST /v1/connectors`, periodic heartbeat, deregister).
  2. **Inbound (connector → core)**: connectors call the public API for
     all commands (create/update entities, attach observables). Same
     surface every other client uses. No privileged side door.
  3. **Outbound (core → connector)**: connectors subscribe to event-bus
     topics matching their declared interests; envelopes are versioned.
  4. **Capability declaration**: at registration the connector declares
     the entity types it produces/consumes and its schema-version
     compatibility - used for routing and rejection of mismatched
     versions.

- **Consequences**:
  - *Easier*: language pluralism, zero core lock-in, identical surface
    for test/mock connectors, every connector behavior reproducible
    with curl + a queue subscriber.
  - *Harder*: no in-core hooks for connector internals. By design - the
    OpenCTI departure.
  - *Obligations*:
    - Stable, versioned public API and event envelope before inviting
      external connector authors. Connector author documentation and
      onboarding DX live outside the scope of this ADR.

- **Open follow-ups**: This ADR resolves the shape of the connector
  contract.

---

## ADR-004: Go as the core runtime

- **Status**: Accepted
- **Date**: 2026-04-26
- **Context**:
  Runtime/language for the core is architecturally significant - it
  shapes the API surface, deployment model, and contributor base, and
  is hard to reverse once code lands.

  Alternatives considered: Go, TypeScript/Node, Python, Rust.

- **Decision**:
  Go. Rationale: single-binary deployment (no runtime install on
  operator machines), strong stdlib for service code, type system
  adequate for schema-driven domain modeling, mature ecosystem for
  Postgres + RabbitMQ + OpenAPI/gRPC, strong operator UX in
  self-hosted mode.

- **Consequences**:
  - *Easier*: shipping the self-hosted container, ops adoption,
    runtime performance under MSSP load, low cold-start in restricted
    environments.
  - *Harder*: rapid prototyping vs Python; smaller pool of candidate
    contributors than Python or TypeScript. Connector authors are
    unaffected - the wire contract (ADR-003) means they may use any
    language.
  - *Obligations*:
    - Codify a project layout convention before significant code lands.
      The specific layout (e.g. standard Go project layout vs DDD-style
      bounded contexts) is an implementation-level decision and is not
      recorded here.

- **Open follow-ups**: This ADR fixes the runtime for the core. No
  further follow-up needed.

---

## ADR-005: First-party reference UI lives in `/ui` for v1, with a migration path to a separate repo

- **Status**: Superseded by ADR-006 (2026-05-03)
- **Date**: 2026-04-27
- **Context**:
  Principle #1 of the project states the core is a data model and API
  layer, not a UI. A frontend will eventually exist regardless;
  deferring *how* it relates to core indefinitely creates ambiguity for
  contributors, blocks any frontend implementation work, and leaves
  an unresolved architectural question (Q-007).

  Three buckets considered:
  1. **External reference UI** - separate repo (or `/examples`),
     consumes the public API like any third-party client, no core
     dependency.
  2. **Optional bundled module** - lives in this monorepo at `/ui`,
     separate build, core does not depend on it.
  3. **First-party UI in core** - bundled into the core build.

  *Why not Bucket 3.* Directly contradicts principle #1. Bundling a UI
  into the core build re-creates the architectural posture the project
  has explicitly decided against and is precisely the OpenCTI failure
  mode the project departs from.

  *Why Bucket 2 over Bucket 1 for v1.* During the pre-v1 phase, the
  public API surface is still fluid. Co-evolving API and reference UI
  in a single git history makes it cheap to land an endpoint and its
  first consumer in the same PR, which surfaces API gaps faster than
  the two-repo equivalent. With zero contributors and zero users, the
  triage-pollution and gravity arguments against an in-tree UI are
  speculative; the iteration cost of two repos is paid every day.
  Bucket 2 is therefore the right v1 trade. Bucket 1 remains the
  right end state - this ADR commits to migrating there once named
  triggers fire.

  *Symmetry with AI.* "AI is a consumer of the data layer, never
  embedded in core" still holds: under this ADR the UI is a consumer
  too. It is colocated in the repo for v1 ergonomics, not embedded in
  the core build. The constraint is preserved at the build/dependency
  level even though the repo boundary is relaxed.

- **Decision**:
  The reference UI lives at `/ui` in this repository for v1. It is not
  part of the core build, depends only on the public API surface, and
  is migrated to a separate repository once any of the named triggers
  fire.

  1. **Location**: `/ui` directory at the repo root.
  2. **Build separation**: the UI has its own build pipeline. Core
     `make`/CI targets do not depend on UI build success. UI failures
     do not block core releases.
  3. **Dependency direction**: enforced one-way. `/ui` may consume the
     public `/v1/...` API and the public event-bus surface. Nothing
     under `/ui` may import core internals (`internal/...`); nothing
     under `internal/...` or `cmd/...` may import from `/ui`. This is
     enforced by a CI check, not convention. The check is a core
     engineering responsibility.
  4. **Privileges**: none. ADR-003's "no privileged side door"
     applies. The reference UI uses the same public surface as any
     other client - including connectors.
  5. **Status**: reference implementation, not a blessed UI. A note
     to that effect lives in `ui/README.md` and the root README links
     to it.
  6. **Tech stack**: deferred. Framework choice belongs in a separate
     ADR scoped to `/ui`; it does not constrain core ADRs.
  7. **Frontend ownership**: deferred. A dedicated frontend
     contributor is brought on only when reference-UI work begins in
     earnest.
  8. **Principle #1 of the project**: clarified, not removed. The
     principle still binds the *core*; a one-line addendum names the
     reference UI as a separate consumer so contributors do not read
     `/ui` as a contradiction.

  **Migration triggers** (any one moves `/ui` to its own repo via a
  superseding ADR):
  - UI build or CI begins gating core releases in practice.
  - A second UI implementation is contributed and the in-tree UI gets
    treated as "the" UI by users or contributors.
  - UI release cadence diverges materially from core.
  - The v1 public API is declared stable; co-evolution in one git
    history no longer earns its keep.
  - UI-shaped issues against the core repo become a triage burden.

- **Consequences**:
  - *Easier*:
    - Single git history during pre-v1: API endpoint + first consumer
      land in one PR; API gaps surface immediately.
    - One repo for new contributors to clone for the full demo flow.
    - One CI to learn early.
  - *Harder*:
    - Reviewer attention drifts toward UI-shaped work because it is
      visually compelling. Mitigated by separate CODEOWNERS for `/ui`
      and explicit issue labels.
    - Issue triage receives UI bugs filed against the core repo.
      Mitigated by `area:ui` labels and a clear scoping line in
      CONTRIBUTING.
    - The `/ui` <-> `internal/` boundary is no longer enforced by the
      repo boundary. The CI import check is the only safeguard;
      treat its absence as a release blocker.
    - Migration to a separate repo at the trigger point is a real
      cost (history split, CI re-plumbing, contributor URL changes).
      Accepted as a deferred cost in exchange for v1 iteration speed.
  - *Obligations*:
    - The import-boundary CI check is added (core engineering) before
      any non-trivial code lands under `/ui`.
    - `ui/README.md` exists and states "reference implementation, not
      the blessed UI."
    - Migration triggers reviewed at every quarterly architecture
      review (or whenever a contributor flags one as fired).
    - The public API and event envelope obligation from ADR-003 -
      stable enough for an out-of-tree consumer - applies to the UI
      *as if it were already out-of-tree*. The UI is forbidden from
      using its colocation as an excuse to depend on internals.

---

## ADR-006: First-party analyst UI as co-product, located at `/ui` for v1

- **Status**: Accepted
- **Date**: 2026-05-03
- **Supersedes**: ADR-005
- **Context**:
  ADR-005 framed the UI as a *reference implementation* — present to
  prove the public API is usable, but explicitly "not the blessed UI."
  Stakeholder conversations during pre-implementation surfaced a clear
  signal: a SOAR product without a first-class analyst UI will not be
  adopted, regardless of how clean the API is. The OpenCTI-style
  "API-first, bring your own UI" stance underestimates how much of
  SOAR's value sits in the analyst experience itself.

  This ADR therefore promotes the UI from "reference" to
  "first-party co-product." It does *not* move the UI into the core
  build, does *not* grant it privileged access, and does *not* change
  its location. The architectural posture from ADR-005 — separate
  build, one-way dependency, public API only — is preserved verbatim.
  Only the *status* and the *adoption commitment* change.

  *Why not bundle the UI into the core build now that it is
  co-product.* No. Co-product means co-equal *deliverable*, not
  co-located *build*. Bundling would re-create the OpenCTI failure
  mode (UI assumptions leaking into core internals) for zero gain —
  the UI can ship in the same release without sharing a build graph.

  *Why not move the UI to its own repo now.* The pre-v1 iteration
  argument from ADR-005 still holds: API surface is fluid, co-evolving
  in one git history is cheap, contributor count is zero. ADR-005's
  migration triggers carry forward (with one revision below).

- **Decision**:
  The first-party analyst UI is a co-equal product alongside the data
  layer. It lives at `/ui` in this repository for v1.

  Inherited unchanged from ADR-005:
  1. **Location**: `/ui` directory at the repo root.
  2. **Build separation**: own build pipeline; core CI does not depend
     on UI build success.
  3. **Dependency direction**: enforced one-way by CI. `/ui` consumes
     only the public `/v1/...` API and event-bus surface. No imports
     from `internal/...` or into `internal/...`. The check is a core
     engineering responsibility.
  4. **Privileges**: none. Same public surface as any other client.
  5. **Tech stack**: deferred to a UI-scoped ADR. Does not constrain
     core ADRs.

  Changed by this ADR:
  6. **Status**: first-party co-product, not reference implementation.
     `ui/README.md` updated to drop the "reference implementation, not
     the blessed UI" line.
  7. **Frontend ownership**: pulled forward from "deferred until UI
     work begins" to "needed soon." Tracked as Q-010.
  8. **Migration triggers**: ADR-005's trigger *"a second UI
     implementation is contributed and the in-tree UI gets treated as
     'the' UI"* is **removed** — that is now intentional, not a
     trigger. The remaining triggers carry forward:
     - UI build or CI begins gating core releases in practice.
     - UI release cadence diverges materially from core.
     - The v1 public API is declared stable; co-evolution in one git
       history no longer earns its keep.
     - UI-shaped issues against the core repo become a triage burden.

- **Consequences**:
  - *Easier*: clear adoption story for stakeholders; analyst-facing
    work has an explicit home; frontend ownership is no longer in
    limbo.
  - *Harder*: the OpenCTI-style "bring your own UI" positioning as a
    differentiator is weakened. Accepted trade — adoption beats
    purity at this stage.
  - *Obligations*:
    - All ADR-005 obligations carry forward (CI import check,
      `ui/README.md`, public-API discipline).
    - `ui/README.md` is updated to reflect first-party status.
    - Q-010 (frontend ownership timing) must be answered before
      meaningful UI implementation begins.
    - Principle #1 of the project is rewritten (done in this change
      set) to name the UI as co-product.

- **Open follow-ups**: Q-010 (frontend ownership timing).

---

## ADR-007: First-party bundled connectors for SIEM, EDR, ticketing

- **Status**: Accepted
- **Date**: 2026-05-03
- **Context**:
  ADR-003 fixed the connector contract as wire-protocol; the original
  Principle #2 declared "no integrations, connectors, or response
  actions are bundled in the core." That stance optimized for
  architectural purity but reproduced an OpenCTI-adjacent failure
  mode: an "empty platform on first install" experience that pushes
  adoption cost onto every new user.

  Stakeholder feedback (same input that drove ADR-006) made the
  trade-off explicit: SIEM, EDR, and ticketing integrations are table
  stakes for a SOAR. Forcing every adopter to source community
  connectors before the product does anything useful is a credibility
  problem, not a modularity win.

  *Why this does not contradict ADR-003.* It does not. Bundled
  connectors are out-of-process, register via `POST /v1/connectors`,
  use the public API for all commands, subscribe to event-bus topics,
  and are subject to the same capability-declaration and version-
  rejection rules as any community connector. The wire contract
  remains authoritative. What changes is *who maintains a small
  starter set* and *what ships in the same release as the core*.

- **Decision**:
  The core release ships with a curated set of first-party bundled
  connectors covering three categories: SIEM, EDR, and ticketing.

  1. **Architecture**: identical to community connectors. Out-of-
     process. Wire contract. No privileged side door. No imports
     into `internal/...`.
  2. **Distribution**: shipped as separate container images alongside
     the core release. Optional to deploy — core runs without them.
  3. **Maintenance**: owned by the core team. Versioned in lockstep
     with the public API and event envelope so that a core release
     guarantees a working bundled-connector set.
  4. **Categories in scope for v1**: SIEM, EDR, ticketing. Specific
     vendor selection (e.g. which SIEM, which EDR) is *not* fixed by
     this ADR — see Q-008 for the curation policy.
  5. **Categories out of scope for v1**: everything else. Threat
     intel, identity, cloud-posture, response actions beyond
     ticketing — community-built.
  6. **Promotion path**: a community connector may graduate to
     bundled status; criteria live in Q-008.

- **Consequences**:
  - *Easier*: day-one usefulness for new adopters; clear "what does
    SOARcore do out of the box" answer for stakeholders; a working
    end-to-end demo path that does not require sourcing third-party
    code.
  - *Harder*: the core team now carries vendor-API churn for the
    bundled set; security review surface grows; a deprecation
    discipline is required when a vendor changes API contracts. The
    "no integrations in core" simplicity is gone — accepted trade.
  - *Obligations*:
    - Q-008 must resolve a curation policy before the first bundled
      connector ships: inclusion criteria, maintenance SLA, security
      review process, deprecation rules, promotion path from
      community.
    - Bundled connectors must run in CI against the public API
      surface — no special hooks, no shared internals.
    - `README` and adopter documentation must distinguish "bundled"
      vs "community" connectors so the boundary stays legible.

- **Open follow-ups**: Q-008 (curation policy for the bundle).
