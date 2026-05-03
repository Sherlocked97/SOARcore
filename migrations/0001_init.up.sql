-- Migration 0001 — initial schema for SOARcore v1.
--
-- This migration creates the four tables that the v1 vertical slice writes:
--   * incident   — the first business entity (see context/data_model.md).
--   * audit      — append-only record of who-did-what.
--   * outbox     — durable event-emission staging table; drained by the relay.
--   * connector  — connector identity & capability registry (ADR-003).
--
-- Conventions enforced here, derived from ADR-001 and the data model:
--   * `tenant_id` is non-null on every entity table from day one. v1 is
--     single-tenant, but the column lets us add multi-tenancy later by
--     partitioning on it without touching call sites.
--   * `schema_version` stamps the row with the entity-schema version it was
--     written under. Migrations are expand-then-contract — we never rewrite
--     a row's schema_version retroactively in a destructive single step.
--   * Status is stored as a Postgres enum so the DB rejects garbage, but the
--     legal *transitions* are enforced in the Go domain layer, not in the DB.
--   * Outbox rows are written in the same transaction as their entity write;
--     the relay reads `published_at IS NULL` to find work.
--
-- Reversibility: a paired 0001_init.down.sql drops every object created here.

-- ---------------------------------------------------------------------------
-- Enums
-- ---------------------------------------------------------------------------

-- Severity for an Incident. Ordered from least to most severe.
CREATE TYPE incident_severity AS ENUM ('low', 'medium', 'high', 'critical');

-- Status enum. The DB only knows the *set* of valid states; the state machine
-- (which transitions are allowed) lives in internal/domain/incident.
CREATE TYPE incident_status AS ENUM (
    'new',
    'triaged',
    'in_progress',
    'contained',
    'resolved',
    'closed'
);

-- Audit row outcome. `denied` rows exist when authz refused the action; in
-- v1 the stub authorizer always allows, so denied rows only appear in tests.
CREATE TYPE audit_result AS ENUM ('success', 'denied', 'error');

-- Connector lifecycle status. Heartbeats flip a connector between active and
-- stale; deregister moves it to `deregistered`.
CREATE TYPE connector_status AS ENUM ('active', 'stale', 'deregistered');

-- ---------------------------------------------------------------------------
-- incident — first-class business entity (see data_model.md, Incident v1).
-- ---------------------------------------------------------------------------
CREATE TABLE incident (
    id                   UUID         PRIMARY KEY,
    tenant_id            UUID         NOT NULL,
    schema_version       INTEGER      NOT NULL,
    external_id          TEXT,
    title                TEXT         NOT NULL,
    description          TEXT,
    severity             incident_severity NOT NULL,
    status               incident_status   NOT NULL,
    assignee_id          UUID,
    source_connector_id  TEXT,
    -- attributes is the schema-versioned flex zone; validated against the
    -- JSON Schema in the schema registry before INSERT/UPDATE in domain.
    attributes           JSONB,
    tags                 TEXT[],
    created_at           TIMESTAMPTZ  NOT NULL,
    updated_at           TIMESTAMPTZ  NOT NULL,
    closed_at            TIMESTAMPTZ
);

-- Index for the default list view: tenant + status + recency.
CREATE INDEX incident_tenant_status_created_idx
    ON incident (tenant_id, status, created_at DESC);

-- Index for the activity feed: tenant + recency-by-update.
CREATE INDEX incident_tenant_updated_idx
    ON incident (tenant_id, updated_at DESC);

-- GIN index on tags for `tags @> ARRAY['ioc']`-style queries.
CREATE INDEX incident_tags_gin_idx ON incident USING GIN (tags);

-- Partial unique index supports idempotent re-ingest from a connector that
-- supplies an external_id. NULL external_id is allowed to be non-unique
-- because not every incident comes from an external system.
CREATE UNIQUE INDEX incident_tenant_connector_external_uq
    ON incident (tenant_id, source_connector_id, external_id)
    WHERE external_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- audit — append-only intent + authorization log.
-- ---------------------------------------------------------------------------
CREATE TABLE audit (
    id              UUID         PRIMARY KEY,
    tenant_id       UUID         NOT NULL,
    occurred_at     TIMESTAMPTZ  NOT NULL,
    -- actor is JSONB so we can carry { type, id } without a join table.
    actor           JSONB        NOT NULL,
    action          TEXT         NOT NULL,
    -- target is { type, id } of the affected entity; nullable for actions
    -- that have no target (e.g. login attempts, future).
    target          JSONB,
    result          audit_result NOT NULL,
    correlation_id  UUID,
    -- metadata holds request hash, source IP, etc. — never payload bodies.
    metadata        JSONB
);

CREATE INDEX audit_tenant_occurred_idx ON audit (tenant_id, occurred_at DESC);
CREATE INDEX audit_correlation_idx ON audit (correlation_id);

-- ---------------------------------------------------------------------------
-- outbox — durable event-emission staging.
--
-- Rows are inserted in the same transaction as the state change that produced
-- them. The relay (internal/outbox) selects rows WHERE published_at IS NULL,
-- publishes them to RabbitMQ, then sets published_at = now(). This gives us
-- at-least-once delivery semantics; consumers must be idempotent on event_id.
-- ---------------------------------------------------------------------------
CREATE TABLE outbox (
    id            BIGSERIAL    PRIMARY KEY,
    event_id      UUID         NOT NULL UNIQUE,
    event_type    TEXT         NOT NULL,
    tenant_id     UUID         NOT NULL,
    -- payload is the *full* event envelope, ready to publish. The relay does
    -- not assemble envelopes; the domain layer does.
    payload       JSONB        NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL,
    published_at  TIMESTAMPTZ
);

-- Partial index on unpublished rows so the relay's poll query is cheap even
-- as the outbox table grows.
CREATE INDEX outbox_unpublished_idx
    ON outbox (created_at)
    WHERE published_at IS NULL;

-- ---------------------------------------------------------------------------
-- connector — registry of connector identities (ADR-003).
-- ---------------------------------------------------------------------------
CREATE TABLE connector (
    id                       TEXT         PRIMARY KEY,
    tenant_id                UUID         NOT NULL,
    -- capabilities is { produces: [...], consumes: [...],
    --                   event_schema_versions: [...] } per ADR-003.
    capabilities             JSONB        NOT NULL,
    status                   connector_status NOT NULL,
    last_heartbeat_at        TIMESTAMPTZ,
    registered_at            TIMESTAMPTZ  NOT NULL,
    deregistered_at          TIMESTAMPTZ
);

CREATE INDEX connector_tenant_status_idx ON connector (tenant_id, status);
