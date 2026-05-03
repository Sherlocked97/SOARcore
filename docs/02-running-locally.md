# 02 — Running Locally

What you'll need installed:

- Docker + the `docker compose` plugin
- Go 1.23+ (the local installation; `go.mod` pins the floor)
- `make`, `curl`, `jq`, and the `psql` client (for the smoke test)

Everything else (Postgres, RabbitMQ) runs as containers.

## The five-line happy path

```bash
git clone <repo> && cd SOARcore
make up         # starts postgres, rabbitmq, core, reference-connector
make build      # builds the local Go binaries (incl. smoke-consumer)
make smoke      # runs scripts/smoke.sh against the running stack
make logs       # tail logs from every container (Ctrl-C to leave)
make down       # tear it all down, including volumes
```

If `make smoke` exits 0, the stack is working end to end.

## What just happened, step by step

`make up` runs `docker compose -f deploy/docker-compose.yml up -d --build`.
That stands up four containers:

| Service               | Image                                | Port  |
|-----------------------|--------------------------------------|-------|
| `postgres`            | `postgres:16-alpine`                 | 5432  |
| `rabbitmq`            | `rabbitmq:3.13-management-alpine`    | 5672 (AMQP), 15672 (UI) |
| `core`                | built from `deploy/Dockerfile.core`  | 8080  |
| `reference-connector` | built from `deploy/Dockerfile.connector` | —     |

The core container, on startup:

1. Loads config from env (see `internal/config/config.go`).
2. Waits for Postgres to accept connections.
3. Runs migrations (`migrations/0001_init.up.sql`, embedded into the binary).
4. Waits for RabbitMQ.
5. Starts the HTTP server on `:8080`.
6. Starts the outbox relay goroutine.

The reference connector waits for `core` to be healthy, then registers
itself, then subscribes to the `soar.events` exchange.

## A first request by hand

```bash
curl -s -X POST http://localhost:8080/v1/incidents \
  -H 'Content-Type: application/json' \
  -H 'X-Principal-Id: user:11111111-1111-1111-1111-111111111111' \
  -H 'X-Tenant-Id:    00000000-0000-0000-0000-000000000001' \
  -d '{
        "title": "first incident",
        "severity": "high",
        "tags": ["manual"]
      }' | jq .
```

You'll get a `201` and a JSON body with the new `id`. The reference
connector will pick it up within a second or two and add the
`enriched-by-reference-connector` tag — re-fetch with
`GET /v1/incidents/<id>` to see it.

## Useful side trips

- **Postgres**: `psql postgres://soar:soar@localhost:5432/soar`
- **RabbitMQ management UI**: <http://localhost:15672> (login `soar` / `soar`)
- **Logs**: `docker compose -f deploy/docker-compose.yml logs core -f`

## Troubleshooting

- **`make smoke` fails "core /healthz never returned 200"** — the core
  container never came up. Check `make logs` for the migration phase
  failing.
- **Reference connector keeps reconnecting** — RabbitMQ might still be
  starting up. The connector retries; give it a few seconds.
- **Port in use** — Postgres/RabbitMQ ports collide with another local
  install. Stop the local one or change the port in
  `deploy/docker-compose.yml`.

## Running without docker

You can also run the binaries directly:

```bash
go run ./cmd/core
# in another terminal, after Postgres + RabbitMQ are up locally:
go run ./cmd/reference-connector
```

Set `POSTGRES_DSN`, `AMQP_URL`, and `CORE_API_BASE` in the environment
to override the defaults. See `internal/config/config.go` for the full
list of variables.
