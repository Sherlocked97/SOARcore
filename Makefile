# SOARcore — local dev loop.
#
# `make help` for the short list. The Makefile is deliberately small —
# every target maps to one or two boring commands so a contributor can
# read this file and understand exactly what's run.

.DEFAULT_GOAL := help
.PHONY: help up down migrate smoke test lint build core connector smoke-consumer logs

COMPOSE := docker compose -f deploy/docker-compose.yml --project-name soarcore
BIN_DIR := bin

help: ## list available targets
	@awk 'BEGIN{FS=":.*##"; printf "Targets:\n"} /^[a-zA-Z_-]+:.*##/ {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## ----- containers ----------------------------------------------------------

up: ## start the full stack (postgres + rabbitmq + core + reference-connector)
	$(COMPOSE) up -d --build

down: ## stop the stack and drop volumes
	$(COMPOSE) down -v

logs: ## tail logs from every service
	$(COMPOSE) logs -f --tail=50

## ----- code ---------------------------------------------------------------

build: core connector smoke-consumer ## build all three Go binaries to ./bin

core: ## build cmd/core to ./bin/core
	mkdir -p $(BIN_DIR)
	go build -trimpath -o $(BIN_DIR)/core ./cmd/core

connector: ## build cmd/reference-connector to ./bin/reference-connector
	mkdir -p $(BIN_DIR)
	go build -trimpath -o $(BIN_DIR)/reference-connector ./cmd/reference-connector

smoke-consumer: ## build cmd/smoke-consumer to ./bin/smoke-consumer
	mkdir -p $(BIN_DIR)
	go build -trimpath -o $(BIN_DIR)/smoke-consumer ./cmd/smoke-consumer

test: ## run unit tests
	go test ./...

lint: ## run golangci-lint (skips if not installed)
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || \
	  echo "golangci-lint not installed — see https://golangci-lint.run/"

## ----- migrations ---------------------------------------------------------

migrate: ## apply pending migrations against a running compose stack
	$(COMPOSE) exec -T postgres psql -U soar -d soar -c "select 1" >/dev/null
	@echo "migrations are auto-applied at core startup; this target is a placeholder"

## ----- end-to-end ---------------------------------------------------------

smoke: smoke-consumer ## run scripts/smoke.sh against the running stack
	./scripts/smoke.sh
