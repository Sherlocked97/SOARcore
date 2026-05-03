#!/usr/bin/env bash
# scripts/smoke.sh — the end-to-end verification for the v1 vertical
# slice. Wraps the eight steps from the build plan into a single script.
# Pass criterion: this script exits 0.
#
# Prereqs:
#   * docker compose is up (`make up`)
#   * curl, psql, jq are on $PATH
#   * the smoke-consumer binary is built into ./bin/smoke-consumer
#     (the Makefile builds it before running this script)
#
# Read top-to-bottom: each numbered comment maps to one row of the
# verification table in the build plan.
#
# Why some consumers are launched in the BACKGROUND before the action:
#   AMQP topic exchanges only deliver to queues that are bound at the
#   moment a message is published. If we launched a consumer *after*
#   the relay had already published, the new queue would never receive
#   the message. So for steps 4 and 6 we start the consumer first,
#   give the broker ~1.5s to register the binding, then perform the
#   action that produces the event.

set -euo pipefail

# ---- config ----------------------------------------------------------------
CORE="${CORE:-http://localhost:8080}"
PG_DSN="${PG_DSN:-postgres://soar:soar@localhost:5432/soar?sslmode=disable}"
AMQP_URL="${AMQP_URL:-amqp://soar:soar@localhost:5672/}"
TENANT_ID="${TENANT_ID:-00000000-0000-0000-0000-000000000001}"
PRINCIPAL_USER="${PRINCIPAL_USER:-user:11111111-1111-1111-1111-111111111111}"
SMOKE_CONSUMER="${SMOKE_CONSUMER:-./bin/smoke-consumer}"

if [[ -t 1 ]]; then
    GREEN=$'\e[32m'; RED=$'\e[31m'; BOLD=$'\e[1m'; RESET=$'\e[0m'
else
    GREEN=""; RED=""; BOLD=""; RESET=""
fi
say() { echo "${BOLD}→${RESET} $*"; }
ok()  { echo "  ${GREEN}✓${RESET} $*"; }
die() { echo "  ${RED}✗${RESET} $*" >&2; exit 1; }

psql_one() { psql "$PG_DSN" -At -c "$1"; }

# unique run id so background consumer queues don't collide with
# concurrent runs.
RUN_ID="$$-$(date +%s%N)"

# ---- step 0 ----------------------------------------------------------------
say "step 0: waiting for ${CORE}/healthz"
for i in $(seq 1 60); do
    if curl -sf -o /dev/null "$CORE/healthz"; then
        ok "core is healthy"; break
    fi
    sleep 1
    [[ "$i" == "60" ]] && die "core /healthz never returned 200"
done

# ---- pre-step: launch background consumers ---------------------------------
# We start two ephemeral consumers *before* creating the incident, so
# their queues are bound when the relay publishes. Each writes the
# matched envelope to a temp file, then exits.
say "pre-step: launch background consumers for incident.created + incident.updated"

CREATED_OUT=$(mktemp -t smoke-created.XXXXXX.json)
UPDATED_OUT=$(mktemp -t smoke-updated.XXXXXX.json)
trap 'rm -f "$CREATED_OUT" "$UPDATED_OUT"' EXIT

# 30s gives plenty of slack for relay polling (~250ms) + reference
# connector reaction (~few hundred ms).
"$SMOKE_CONSUMER" --url="$AMQP_URL" --pattern="incident.*" \
    --event-type="incident.created" \
    --queue="created-${RUN_ID}" \
    --timeout=30s > "$CREATED_OUT" 2>/tmp/smoke-created.err &
CREATED_PID=$!

"$SMOKE_CONSUMER" --url="$AMQP_URL" --pattern="incident.*" \
    --event-type="incident.updated" \
    --queue="updated-${RUN_ID}" \
    --timeout=30s > "$UPDATED_OUT" 2>/tmp/smoke-updated.err &
UPDATED_PID=$!

# Give both consumers ~2s to dial AMQP, declare their queues, and bind
# to the exchange. Without this, the create can race the binding.
sleep 2
ok "consumers running (pids $CREATED_PID, $UPDATED_PID)"

# ---- step 1 ----------------------------------------------------------------
say "step 1: create a valid incident"
CREATE_BODY='{
    "title": "smoke-test incident",
    "description": "created by scripts/smoke.sh",
    "severity": "high",
    "tags": ["smoke"],
    "attributes": {"source": "smoke"}
}'
CREATE_RESP=$(curl -sf -X POST "$CORE/v1/incidents" \
    -H "Content-Type: application/json" \
    -H "X-Principal-Id: $PRINCIPAL_USER" \
    -H "X-Tenant-Id: $TENANT_ID" \
    -d "$CREATE_BODY")
INCIDENT_ID=$(echo "$CREATE_RESP" | jq -r '.id')
[[ "$INCIDENT_ID" =~ ^[0-9a-f-]{36}$ ]] || die "create returned no id: $CREATE_RESP"
ok "created id=$INCIDENT_ID"

# ---- step 2 ----------------------------------------------------------------
say "step 2: incident row in DB"
ROW_COUNT=$(psql_one "SELECT count(*) FROM incident WHERE id = '$INCIDENT_ID' AND tenant_id = '$TENANT_ID' AND schema_version = 1")
[[ "$ROW_COUNT" == "1" ]] || die "expected 1 incident row, got $ROW_COUNT"
ok "incident row present"

# ---- step 3 ----------------------------------------------------------------
say "step 3: audit row for incident.create"
AUDIT_COUNT=$(psql_one "SELECT count(*) FROM audit WHERE action = 'incident.create' AND target ->> 'id' = '$INCIDENT_ID' AND result = 'success'")
[[ "$AUDIT_COUNT" == "1" ]] || die "expected 1 audit row, got $AUDIT_COUNT"
ok "audit row present"

# ---- step 4 ----------------------------------------------------------------
say "step 4: incident.created envelope on the bus"
if ! wait "$CREATED_PID"; then
    cat /tmp/smoke-created.err >&2 || true
    die "incident.created consumer did not capture an envelope in time"
fi
jq -e ".entity.id == \"$INCIDENT_ID\"" "$CREATED_OUT" >/dev/null \
    || die "envelope entity.id mismatch (got $(jq -r .entity.id "$CREATED_OUT"))"
jq -e '.event_type == "incident.created"' "$CREATED_OUT" >/dev/null \
    || die "event_type mismatch"
ok "incident.created envelope observed"

# ---- step 5 ----------------------------------------------------------------
say "step 5: reference connector enriches the incident"
ENRICHED=""
for i in $(seq 1 30); do
    GOT=$(curl -sf "$CORE/v1/incidents/$INCIDENT_ID" \
        -H "X-Principal-Id: $PRINCIPAL_USER" \
        -H "X-Tenant-Id: $TENANT_ID")
    if echo "$GOT" | jq -e '.tags | index("enriched-by-reference-connector")' >/dev/null; then
        ENRICHED="$GOT"; break
    fi
    sleep 1
done
[[ -n "$ENRICHED" ]] || die "connector did not enrich within 30s"
ok "incident enriched with new tag"

# ---- step 6 ----------------------------------------------------------------
say "step 6: second audit row + incident.updated event"
AUDIT_COUNT=$(psql_one "SELECT count(*) FROM audit WHERE target ->> 'id' = '$INCIDENT_ID'")
(( AUDIT_COUNT >= 2 )) || die "expected at least 2 audit rows, got $AUDIT_COUNT"

if ! wait "$UPDATED_PID"; then
    cat /tmp/smoke-updated.err >&2 || true
    die "incident.updated consumer did not capture an envelope in time"
fi
jq -e ".entity.id == \"$INCIDENT_ID\"" "$UPDATED_OUT" >/dev/null \
    || die "incident.updated entity.id mismatch"
jq -e '.payload.changed_fields | index("tags")' "$UPDATED_OUT" >/dev/null \
    || die "incident.updated did not include changed_fields[tags]"
ok "second audit row + incident.updated observed"

# ---- step 7 ----------------------------------------------------------------
say "step 7: malformed payload is rejected"
BEFORE_INC=$(psql_one "SELECT count(*) FROM incident")
BEFORE_AUDIT=$(psql_one "SELECT count(*) FROM audit")
BEFORE_OUTBOX=$(psql_one "SELECT count(*) FROM outbox")

STATUS=$(curl -s -o /tmp/bad.json -w "%{http_code}" -X POST "$CORE/v1/incidents" \
    -H "Content-Type: application/json" \
    -H "X-Principal-Id: $PRINCIPAL_USER" \
    -H "X-Tenant-Id: $TENANT_ID" \
    -d '{"severity": "high"}')
[[ "$STATUS" == "400" ]] || die "expected 400, got $STATUS: $(cat /tmp/bad.json)"

AFTER_INC=$(psql_one "SELECT count(*) FROM incident")
AFTER_AUDIT=$(psql_one "SELECT count(*) FROM audit")
AFTER_OUTBOX=$(psql_one "SELECT count(*) FROM outbox")
[[ "$BEFORE_INC" == "$AFTER_INC" ]] || die "incident table changed on bad request"
[[ "$BEFORE_AUDIT" == "$AFTER_AUDIT" ]] || die "audit table changed on bad request"
[[ "$BEFORE_OUTBOX" == "$AFTER_OUTBOX" ]] || die "outbox table changed on bad request"
ok "malformed request rejected with no side effects"

# ---- step 8 ----------------------------------------------------------------
# For step 8 we want to assert that NO incident.status_changed event
# was published. We start a consumer first (pre-binding), then send
# the bad PATCH, then confirm the consumer times out without ever
# receiving anything.
say "step 8: disallowed status transition is rejected, no status_changed event"

NOEVENT_OUT=$(mktemp -t smoke-noevent.XXXXXX.json)
trap 'rm -f "$CREATED_OUT" "$UPDATED_OUT" "$NOEVENT_OUT"' EXIT

"$SMOKE_CONSUMER" --url="$AMQP_URL" --pattern="incident.*" \
    --event-type="incident.status_changed" \
    --entity-id="$INCIDENT_ID" \
    --queue="noevent-${RUN_ID}" \
    --timeout=4s > "$NOEVENT_OUT" 2>/tmp/smoke-noevent.err &
NOEVENT_PID=$!
sleep 2

STATUS=$(curl -s -o /tmp/bad.json -w "%{http_code}" -X PATCH "$CORE/v1/incidents/$INCIDENT_ID" \
    -H "Content-Type: application/json" \
    -H "X-Principal-Id: $PRINCIPAL_USER" \
    -H "X-Tenant-Id: $TENANT_ID" \
    -d '{"status": "resolved"}')
[[ "$STATUS" == "422" ]] || die "expected 422, got $STATUS: $(cat /tmp/bad.json)"

# The consumer's exit status: 0 means it captured something (BAD), 1
# means it timed out (GOOD).
if wait "$NOEVENT_PID"; then
    die "unexpected status_changed event captured: $(cat "$NOEVENT_OUT")"
fi
ok "disallowed transition rejected, no status_changed published"

echo
echo "${GREEN}${BOLD}smoke OK${RESET} — all 8 steps passed"
