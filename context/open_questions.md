# Open Questions

## Format
- **[Q-XXX]** Question — Owner area — Blocking: [yes/no]

## Open
- [Q-004] What CLA mechanism should be in place before accepting external contributions? Options: manual `CLA.md` + signed-off-by, automated (CLA Assistant GitHub App), or DCO. A CLA preserves the option to relicense (e.g., to BUSL-1.1) if the project ever ships as a SaaS — without one, the license choice is permanent. — OSS / repo hygiene — Blocking: yes (before merging first external PR)
- [Q-006] When and whether to introduce a read-side search projection (e.g. OpenSearch), and which technology — Architecture — Blocking: no.
  Trigger condition: a concrete query class (full-text incident search, fuzzy observable matching, large aggregations) proves SQL+JSONB insufficient on representative data. The event bus and outbox pattern (ADR-001, ADR-002) keep this purely additive — no rewrite of the write path required. Capture representative queries here as they emerge so the eventual ADR has evidence, not speculation.
- [Q-008] Curation policy for first-party bundled connectors (ADR-007) — Architecture + OSS / repo hygiene — Blocking: yes (before the first bundled connector ships).
  Must define: (a) inclusion criteria — which specific SIEM/EDR/ticketing vendors are in the v1 bundle and why; (b) maintenance SLA — response time for vendor-API breakage; (c) security review process for bundled connectors; (d) deprecation rules when a vendor changes contracts; (e) promotion path for community connectors graduating to bundled status. Ties directly to the OSS governance discussion under Q-004.
- [Q-010] When to bring a dedicated frontend lead onto the project — Architecture — Blocking: yes (before meaningful UI implementation begins under `/ui`).
  ADR-005 deferred this; ADR-006 pulls it forward by promoting the UI to first-party co-product. Open sub-questions: tech stack (separate ADR scoped to `/ui`), scope (does the frontend lead also own UX, or is UX a separate role?), and how the frontend role interacts with the product owner identified in Q-009.
- [Q-011] Is the bundled ticketing connector (ADR-007) authoritative for incident ownership, or a downstream sync target? — Product (with Architecture input) — Blocking: yes (before bundled ticketing connector ships).
  Product recommendation in `context/product_spec.md` §3.6: downstream sync target only, on the grounds that ownership carries response-action authorization and a ticketing system that does not enforce the platform's authz model would leak that boundary. Architecture must confirm the authz-model implication before this is closed.
- [Q-012] Co-watchers / collaborators on incidents beyond the exactly-one-owner rule (`context/product_spec.md` §3.2) — Product — Blocking: no (deferred beyond v1 unless a concrete user need surfaces).
  v1 commits to exactly one current owner per incident with full ownership history. Watchers, shared-ownership, or "subscribed analyst" concepts are explicitly out of scope for v1. Reopen if shift-lead or MSSP-tenant feedback shows the single-owner rule blocks routine collaboration.
- [Q-013] Auto-promotion rules for AI consumers (alert → incident) — Product (with AI advisor input) — Blocking: no (default-off in v1; v1 of the AI surface is enrichment + suggestion, not auto-execution).
  Must define before auto-promotion ships: (a) what conditions an operator can configure to permit AI-driven promotion; (b) what guardrails are mandatory regardless of configuration (e.g. severity floor, observable allowlist); (c) what audit signal distinguishes AI-promoted incidents from analyst-promoted ones for downstream review.
- [Q-014] Suppression-rule semantics at alert ingest — Product — Blocking: yes (before alert ingest goes live).
  Must define: (a) who configures suppression rules (per-tenant role / persona); (b) how an analyst overrides an individual suppression to surface a specific alert into the triage queue; (c) what audit trail covers both the rule itself and individual overrides; (d) whether suppression is alert-pattern-based, observable-based, or both.

## Resolved
- [Q-001] Primary datastore — Architecture — Resolved 2026-04-26 by **ADR-001: Postgres** as the primary system-of-record. Rationale and rejection of OpenSearch-as-primary documented in full in the ADR. Read-side projection deferred to Q-006.
- [Q-002] Event bus / pub-sub — Architecture — Resolved 2026-04-26 by **ADR-002: RabbitMQ** for v1. Kafka-compatible bus reserved as a superseding ADR if telemetry-firehose use cases enter core scope.
- [Q-003] Connector contract — Architecture — Resolved 2026-04-26 by **ADR-003: wire-protocol contract, no SDK requirement**. Public API for commands; event bus for notifications; capability declaration at registration. SDKs are conveniences, not contracts.
- [Q-005] License selection — OSS / repo hygiene — Resolved 2026-04-26: **Apache-2.0**.
  Rationale: maximize early-stage adoption; defer SaaS-defense licensing
  (BUSL-1.1) until a commercial offering exists. Relicensing later remains
  possible *only if* a CLA is in place for all contributors — see Q-004.
- [Q-007] First-party frontend — Architecture — Resolved 2026-05-03 by **ADR-006**: first-party analyst UI as co-product, located at `/ui` for v1. Supersedes ADR-005's "reference UI" framing. Tech-stack choice still deferred to a UI-scoped ADR; frontend-lead timing tracked in Q-010.
- [Q-009] Product/UX requirements ownership — Maintainer — Resolved 2026-05-03: a dedicated product role now owns `context/product_spec.md`, the Alert entity and shift/on-call product semantics, and the product roadmap. Sequencing remains maintainer-owned for this solo pre-v1 OSS project.
