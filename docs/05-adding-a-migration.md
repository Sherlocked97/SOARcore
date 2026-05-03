# 05 — Adding a Migration

We use [golang-migrate]. Migrations live in `migrations/` and are
embedded into the `cmd/core` binary at build time, so a release bundle
carries every schema state needed to bring an empty database up to
that release.

[golang-migrate]: https://github.com/golang-migrate/migrate

## File naming

```
NNNN_short_description.up.sql
NNNN_short_description.down.sql
```

- `NNNN` is a zero-padded 4-digit sequence number, monotonically
  increasing. `0001_init.up.sql` already exists; the next one is
  `0002_*`.
- Always ship a `.down.sql` even if it's just a comment explaining
  why a rollback isn't safe. golang-migrate enforces the file pair.

## Authoring rules

1. **Expand-then-contract.** A breaking change is two migrations
   minimum:
   - First migration: add the new column / new table / new index, but
     keep the old one. Application code reads both, writes new.
   - Deploy.
   - Second migration (next release): drop the old column. By this
     point no running version of the app reads it.

2. **Stamp `schema_version` on rows you write.** When the *shape* of
   `attributes` (the JSONB flex zone) changes, bump
   `incident.SchemaVersion` in `internal/domain/incident/incident.go`
   *and* register a new schema in
   `internal/schemaregistry/schemas/`. Keep both validators around;
   the registry serves both.

3. **No data backfills inside the migration file** unless they are
   small and idempotent. Large backfills go in a separate one-shot job
   so a long migration doesn't extend deploy windows.

4. **Index changes that lock a hot table** need `CONCURRENTLY` (and
   that means the migration can't be inside a transaction —
   golang-migrate supports this with a tagged comment, see its docs).

## Example: adding a `priority` column to `incident`

`migrations/0002_incident_priority.up.sql`:

```sql
ALTER TABLE incident
    ADD COLUMN priority INTEGER;

-- Index because the API will list by it.
CREATE INDEX incident_tenant_priority_idx
    ON incident (tenant_id, priority)
    WHERE priority IS NOT NULL;
```

`migrations/0002_incident_priority.down.sql`:

```sql
DROP INDEX IF EXISTS incident_tenant_priority_idx;
ALTER TABLE incident DROP COLUMN IF EXISTS priority;
```

Then:

1. Add `Priority *int` to `IncidentRow` in
   `internal/persistence/incidents.go` and update Insert/Update/Get
   SQL.
2. Add `Priority *int` to the domain `Incident` and the API
   request/response types.
3. If `priority` is part of `attributes` rather than its own column,
   instead update `internal/schemaregistry/schemas/incident_v1.json`
   *or* register a `incident_v2.json`, and bump `SchemaVersion`.

## Running migrations

`cmd/core` runs pending migrations on every startup, idempotently. For
local dev `make up` is enough. For prod-style deploys, you can run
migrations as a one-shot job: that surface isn't built yet — when it is,
it'll live in `cmd/migrate/` or similar, and this page will be updated.

## Resetting

```bash
make down   # drops the postgres volume too
make up
```

That gives you a fresh DB, migrations re-applied from scratch.
