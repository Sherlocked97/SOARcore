# SOARcore — Analyst UI

This directory holds the first-party analyst UI for SOARcore. It is a
**co-equal product** alongside the data layer and public API
(see [ADR-006](../context/decisions.md#adr-006-first-party-analyst-ui-as-co-product-located-at-ui-for-v1)).

## What ships today

A single-page **status dashboard**, intentionally minimal. It shows:

- core API health (polled every 10s)
- incident counts by status
- the most recent incidents

It exists so the project has a usable, visible surface before the
analyst workbench lands — not as a stand-in for it. The workbench
itself (triage queue, alert→incident promotion, audit timeline, shift
handover) is described in [`context/product_spec.md`](../context/product_spec.md)
and is not implemented yet.

## Stack

Vanilla HTML, CSS, and JavaScript served by nginx. No framework, no
build step, no bundler. This is **deliberate**: a UI-scoped
tech-stack ADR is owed before any framework choice lands
([Q-010](../context/open_questions.md)). Shipping a static page now
keeps that decision open and gives Q-010 a real artifact to react to
rather than a blank canvas.

When the framework decision is made, everything in this directory is
expected to be rewritten. Treat the current code as a placeholder
with a defined replacement path.

## Boundary

The UI consumes only the **public** core surface:

- `/healthz`
- `/v1/...`

Nothing under `ui/` may import from `internal/...`, and nothing under
`internal/` or `cmd/` may reach into `ui/`. There is no CI check
enforcing this yet — that check is owed before non-trivial UI code
lands and is a core engineering responsibility (ADR-006 §3).

The auth stub still applies: the core requires an `X-Principal-Id`
header (`type:id`) on every `/v1/...` call. The UI's nginx config
injects `user:soar-ui` so audit rows distinguish UI-driven calls from
connector or smoke-test calls. When the stub authorizer is replaced,
the UI sends real credentials like any other client.

## Running it

The UI is wired into `deploy/docker-compose.yml` and starts with the
rest of the stack:

```bash
make up
# core API:        http://localhost:8080
# UI status page:  http://localhost:8081
```

Open <http://localhost:8081> in a browser. Run `make smoke` in
another terminal and reload — you should see a freshly created
incident appear in the table.

The UI image builds from this directory only; core CI does not depend
on UI build success and vice versa (ADR-006 §2).

## What's deliberately *not* here

- a framework, component library, state management library, or build
  tool — all deferred until the UI tech-stack ADR
- TypeScript — same reason
- tests — there is nothing here yet that warrants them; component and
  end-to-end tests arrive with the framework choice
- `internationalization` infrastructure — the page ships English-only
  but with no hardcoded user-facing copy that would resist later
  extraction (only headings and labels in markup)

## What's owed before significant UI work begins

1. **Q-010** resolved — frontend ownership and tech-stack ADR.
2. **Import-boundary CI check** — core engineering, per ADR-006 §3.
3. **Public API gaps surfaced as ADR proposals**, not worked around
   in the UI layer (ADR-006 §5).
