# Open Questions

## Format
- **[Q-XXX]** Question — Owner persona — Blocking: [yes/no]

## Open
- [Q-001] What is the primary datastore for the core data layer? — Architect — Blocking: yes
- [Q-002] What event bus / pub-sub mechanism? — Architect — Blocking: yes
- [Q-003] What does a "connector contract" look like? — Architect — Blocking: yes
- [Q-004] What CLA mechanism should be in place before accepting external contributions? Options: manual `CLA.md` + signed-off-by, automated (CLA Assistant GitHub App), or DCO. A CLA preserves the option to relicense (e.g., to BUSL-1.1) if the project ever ships as a SaaS — without one, the license choice is permanent. — OSS Mentor — Blocking: yes (before merging first external PR)

## Resolved
- [Q-005] License selection — OSS Mentor — Resolved 2026-04-26: **Apache-2.0**.
  Rationale: maximize early-stage adoption; defer SaaS-defense licensing
  (BUSL-1.1) until a commercial offering exists. Relicensing later remains
  possible *only if* a CLA is in place for all contributors — see Q-004.
